#!/usr/bin/env python3
"""Disposable DB/controller + two real pinned Bifrost processes + synthetic providers.
Exercises strict-hold SSE, scoped encrypted cache and independently governed failover.
No paid model calls. Requires SCARLET_DB_DSN, bootstrap values are ephemeral.
"""
import base64
import hashlib
import http.cookiejar
import http.server
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import threading
import urllib.error
import urllib.request
import uuid

from e2e import ROOT, IMAGE, port, write, wait_port, wait_http
spec=importlib.util.spec_from_file_location('controller_e2e',Path(__file__).with_name('e2e-controller.py'))
legacy=importlib.util.module_from_spec(spec);spec.loader.exec_module(legacy)

class Provider(http.server.BaseHTTPRequestHandler):
    mode='transient'
    hits={'primary':0,'secondary':0}
    def log_message(self,*_):pass
    def do_POST(self):
        body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        route='secondary' if self.path.startswith('/secondary/') else 'primary'
        type(self).hits[route]+=1
        expected=route+'-model'
        assert body['model']==expected,(route,body)
        assert self.headers.get('Authorization')=='Bearer synthetic-'+route+'-credential-00000001'
        assert not self.headers.get('x-bf-cache-key') and not self.headers.get('x-bf-direct-key')
        if route=='primary' and self.mode=='transient':
            self.send_response(503);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{"error":{"message":"synthetic unavailable"}}');return
        self.send_response(200)
        if body.get('stream'):
            assert body.get('stream_options',{}).get('include_usage') is True
            self.send_header('Content-Type','text/event-stream');self.end_headers()
            parts=['person@','example.com'] if self.mode=='pii' else ['approved ','synthetic answer']
            for text in parts:
                value={'id':'synthetic-sse','object':'chat.completion.chunk','created':1,'model':expected,'choices':[{'index':0,'delta':{'content':text},'finish_reason':None}]}
                self.wfile.write(('data: '+json.dumps(value)+'\n\n').encode());self.wfile.flush()
            value={'id':'synthetic-sse','object':'chat.completion.chunk','created':1,'model':expected,'choices':[{'index':0,'delta':{},'finish_reason':'stop'}]}
            self.wfile.write(('data: '+json.dumps(value)+'\n\n').encode())
            value=dict(value,choices=[],usage={'prompt_tokens':4,'completion_tokens':3,'total_tokens':7})
            self.wfile.write(('data: '+json.dumps(value)+'\n\ndata: [DONE]\n\n').encode());self.wfile.flush()
        else:
            self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(json.dumps({'object':'chat.completion','model':expected,'choices':[{'index':0,'message':{'role':'assistant','content':'approved synthetic answer'},'finish_reason':'stop'}]}).encode())

def main():
    assert os.getenv('SCARLET_DB_DSN'),'private disposable SCARLET_DB_DSN required'
    workspace='gateway-v3-'+uuid.uuid4().hex[:10]
    controller_port,gateway_port,gateway2_port,provider_port=port(),port(),port(),port()
    bf_ports=[port(),port()]
    names=['scarlet-v3-'+uuid.uuid4().hex[:10] for _ in range(2)]
    provider=http.server.ThreadingHTTPServer(('0.0.0.0',provider_port),Provider)
    threading.Thread(target=provider.serve_forever,daemon=True).start()
    processes=[];logs=[];created=False
    control=f'http://127.0.0.1:{controller_port}'
    with tempfile.TemporaryDirectory(prefix='scarlet-v3-e2e-') as directory,tempfile.TemporaryDirectory(prefix='.gateway-v3-e2e-',dir=ROOT/'backend') as helper_dir:
        tmp=Path(directory);helper_path=Path(helper_dir);(helper_path/'main.go').write_text(legacy.HELPER)
        node_token=secrets.token_urlsafe(48)
        env=dict(os.environ,SCARLET_ORIGIN=control,SCARLET_GATEWAY_WORKSPACE=workspace,SCARLET_GATEWAY_CONTROL_TOKEN=node_token,SCARLET_GATEWAY_CONTROL_TOKENS='',SCARLET_BOOTSTRAP_WORKSPACE=workspace,SCARLET_BOOTSTRAP_WORKSPACE_NAME='Disposable V3 E2E',SCARLET_BOOTSTRAP_USER=workspace+'@example.invalid',SCARLET_BOOTSTRAP_PASSWORD=secrets.token_urlsafe(32),SCARLET_GATEWAY_CACHE_KEY=base64.b64encode(secrets.token_bytes(32)).decode())
        def helper(operation,*values):return subprocess.check_output([str(tmp/'helper'),operation,*map(str,values)],env=env).decode()
        def launch(command,label,process_env=env):
            log=(tmp/(label+'.log')).open('w');logs.append(log);p=subprocess.Popen(command,env=process_env,stdout=log,stderr=log);processes.append(p);return p
        opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        def call(path,value=None,method=None,token=None,gateway=False,node_port=None,nonce=None):
            headers={'Content-Type':'application/json','Origin':control}
            if token:headers['Authorization']='Bearer '+token
            if nonce:headers['Idempotency-Key']=nonce
            request=urllib.request.Request((f'http://127.0.0.1:{node_port or gateway_port}' if gateway else control)+path,data=None if value is None else json.dumps(value).encode(),headers=headers,method=method)
            try:
                with opener.open(request,timeout=30) as response:
                    raw=response.read().decode();return response.status,raw,dict(response.headers)
            except urllib.error.HTTPError as error:return error.code,error.read().decode(),dict(error.headers)
        base='/api/v1/workspaces/'+workspace+'/ai-gateway'
        try:
            for target,package in [('server','./cmd/server'),('ai-gateway','./cmd/ai-gateway')]:subprocess.run(['go','-C',str(ROOT/'backend'),'build','-o',str(tmp/target),package],check=True)
            subprocess.run(['go','-C',str(ROOT/'backend'),'build','-o',str(tmp/'helper'),'./'+helper_path.name],check=True)
            helper('check',workspace)
            subprocess.run([str(tmp/'server'),'-mode','bootstrap','-allow-insecure-loopback'],env=env,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);created=True
            p=launch([str(tmp/'server'),'-addr',f'127.0.0.1:{controller_port}','-allow-insecure-loopback'],'controller');wait_port(controller_port,p)
            status,_,_=call('/api/v1/session',{'username':env['SCARLET_BOOTSTRAP_USER'],'password':env['SCARLET_BOOTSTRAP_PASSWORD']});assert status==204,('login',status)
            models=[];profiles=[];configured=[];targets={}
            for i,route in enumerate(['primary','secondary']):
                model_id='enterprise' if i==0 else 'backup'
                endpoint='env:SCARLET_GATEWAY_'+route.upper()+'_ENDPOINT'
                keyenv='SCARLET_GATEWAY_'+route.upper()+'_KEY'
                profile={'provider':route,'region':'CN','endpointRef':endpoint,'baseUrl':f'http://host.docker.internal:{provider_port}/{route}/v1','apiKeyEnv':keyenv,'models':[route+'-model'],'port':bf_ports[i]}
                profiles.append(profile);models.append({'id':model_id,'name':route,'enabled':True,'provider':route,'endpointRef':route,'region':'CN','upstreamModel':route+'-model','responseFormat':'openai_chat','allowedApplicationIds':['app'],'allowedInputCategories':[],'allowedOutputCategories':[]})
                configured.append({'id':route,'name':route,'provider':route,'modelId':model_id,'upstreamModel':route+'-model','endpointRef':endpoint,'secretRef':'env:'+keyenv,'region':'CN','responseFormat':'openai_chat','enabled':True,'fallbackModelIds':['backup'] if i==0 else [],'inputMicrosPerMillion':1000000,'outputMicrosPerMillion':1000000})
                targets[route]={'url':f'http://127.0.0.1:{bf_ports[i]}/openai/v1/chat/completions'}
                env[endpoint[4:]]=profile['baseUrl'];env[keyenv]='synthetic-'+route+'-credential-00000001'
            catalog={'version':'v3-e2e','applications':[{'id':'app','name':'app','enabled':True,'principalIds':['alice'],'documentIds':[],'modelIds':['enterprise','backup'],'toolIds':[]}],'principals':[{'id':'alice','name':'alice','enabled':True,'applicationIds':['app']}],'documents':[],'models':models,'tools':[],'approvals':[],'policy':{'version':'v3-policy','maxRequestAgeSeconds':300,'maxInputBytes':65536,'maxOutputBytes':65536,'allowedInputCategories':[],'allowedOutputCategories':[],'allowedModelRegions':['CN'],'blockedInputPatterns':[],'blockedOutputPatterns':[]}}
            write(tmp/'catalog.json',catalog);write(tmp/'profiles.json',profiles);write(tmp/'bindings.json',{'credentials':[],'targets':targets,'tools':[]})
            for i,profile in enumerate(profiles):
                data=tmp/('bifrost'+str(i));data.mkdir(mode=0o700);write(tmp/('profile'+str(i)+'.json'),profile)
                catalog_digest=helper('assets',tmp/('profile'+str(i)+'.json'),tmp/'catalog.json',data/'config.json')
                subprocess.run(['docker','run','-d','--name',names[i],'--user',f'{os.getuid()}:{os.getgid()}','-p',f'127.0.0.1:{bf_ports[i]}:8080','-v',f'{data}:/app/data:rw','-e',profile['apiKeyEnv']+'='+env[profile['apiKeyEnv']],'-e','HTTP_PROXY=','-e','HTTPS_PROXY=','-e','ALL_PROXY=','-e','NO_PROXY=*','--entrypoint','/app/main',IMAGE,'-host','0.0.0.0','-port','8080','-app-dir','/app/data'],check=True,stdout=subprocess.DEVNULL);wait_http(bf_ports[i])
            app={'id':'app','name':'V3 app','enabled':True,'principalId':'alice','allowRag':False,'modelIds':['enterprise','backup'],'toolIds':[],'budgetMicros':100000000,'requestsPerMinute':100,'tokensPerMinute':1000000,'cachePolicy':{'enabled':True,'ttlSeconds':60,'maxEntries':100,'maxBytes':1048576,'keyRef':'env:SCARLET_GATEWAY_CACHE_KEY','keyVersion':'v1'}}
            config={'expectedRevision':0,'enabled':True,'catalogDigest':catalog_digest,'applications':[app],'providers':configured}
            status,out,_=call(base+'/config',config,'PUT');assert status==200,('config',status,out);saved=json.loads(out)
            status,out,_=call(base+'/credentials',{'applicationId':'app','name':'v3 token'});assert status==201,('credential',status,out);credential=json.loads(out);token=credential['token']
            (tmp/'node.token').write_text(node_token);(tmp/'node.token').chmod(0o600)
            p=launch([str(tmp/'ai-gateway'),'-local','-listen',f'127.0.0.1:{gateway_port}','-catalog',str(tmp/'catalog.json'),'-bindings',str(tmp/'bindings.json'),'-bifrost-profile',str(tmp/'profiles.json'),'-audit',str(tmp/'audit.jsonl'),'-journal',str(tmp/'journal.jsonl'),'-control-url',control,'-control-token-file',str(tmp/'node.token'),'-workspace',workspace,'-node-id','v3-node'],'gateway');wait_port(gateway_port,p)
            p=launch([str(tmp/'ai-gateway'),'-local','-listen',f'127.0.0.1:{gateway2_port}','-catalog',str(tmp/'catalog.json'),'-bindings',str(tmp/'bindings.json'),'-bifrost-profile',str(tmp/'profiles.json'),'-audit',str(tmp/'audit2.jsonl'),'-journal',str(tmp/'journal2.jsonl'),'-control-url',control,'-control-token-file',str(tmp/'node.token'),'-workspace',workspace,'-node-id','v3-node2'],'gateway2');wait_port(gateway2_port,p)
            chat={'model':'enterprise','messages':[{'role':'user','content':'joint synthetic safe text'}],'max_tokens':20,'stream':True,'stream_options':{'include_usage':True}}
            results=[]
            def check(label,body,statuses,calls,node_port=None,nonce=None):
                before=dict(Provider.hits);status,out,headers=call('/v1/chat/completions',body,token=token,gateway=True,node_port=node_port,nonce=nonce)
                assert status in statuses,(label,status,out)
                delta={k:Provider.hits[k]-before[k] for k in before};assert delta==calls,(label,delta,calls)
                assert 'person@example.com' not in out,(label,'DLP leak')
                results.append({'case':label,'status':status,'providerCalls':delta,'cache':headers.get('X-Scarlet-Cache')})
                return out,headers
            out,_=check('real-two-provider-sse-fallback',chat,{200},{'primary':1,'secondary':1});assert 'data: [DONE]' in out and 'approved synthetic answer' in out
            out,headers=check('fresh-scoped-cache-hit',dict(chat,model='backup'),{200},{'primary':0,'secondary':0},nonce='cross-node-cache-replay-0001');assert headers.get('X-Scarlet-Cache')=='hit' and 'data: [DONE]' in out
            out,_=check('cross-node-cache-nonce-replay-blocked',dict(chat,model='backup'),{403,409,503},{'primary':0,'secondary':0},node_port=gateway2_port,nonce='cross-node-cache-replay-0001');assert 'data: [DONE]' not in out
            check('input-dlp-before-either-provider',dict(chat,messages=[{'role':'user','content':'person@example.com'}]),{403},{'primary':0,'secondary':0})
            Provider.mode='pii';check('cross-chunk-output-dlp-no-fallback',dict(chat,messages=[{'role':'user','content':'different synthetic input'}]),{403},{'primary':1,'secondary':0})
            Provider.mode='transient'
            for suffix in ('second','third'):check('failed-primary-'+suffix,dict(chat,messages=[{'role':'user','content':'new synthetic '+suffix}]),{200},{'primary':1,'secondary':1})
            check('shared-circuit-open-skips-primary',dict(chat,messages=[{'role':'user','content':'new synthetic fourth'}]),{200},{'primary':0,'secondary':1})
            status,out,_=call(base+'/reservations?limit=200');assert status==200;reservations=json.loads(out)['items'];assert any(r['state']=='unknown' for r in reservations) and any(r['state']=='settled' for r in reservations)
            status,out,_=call(base+'/audit?limit=500');assert status==200;audit=json.loads(out);assert audit['valid'];assert any(row['action']=='gateway.cache_hit' for row in audit['items']),[row['action'] for row in audit['items']]
            status,_,_=call(base+'/credentials/'+str(credential['credential']['id']),method='DELETE');assert status==204
            check('revoked-cache-credential-fails-closed',dict(chat,model='backup'),{401},{'primary':0,'secondary':0})
            print(json.dumps({'image':IMAGE,'tests':results,'reservations':len(reservations),'verifiedAudit':audit['valid']},indent=2))
        except Exception:
            if 'token' in locals():
                scope={'workspaceSlug':workspace,'tokenHash':hashlib.sha256(token.encode()).hexdigest(),'applicationId':'app','principalId':'alice','modelId':'enterprise','catalogDigest':catalog_digest,'inspectionVersion':'datasafety-four-format-v1','inputHash':'a'*64,'documentHash':'b'*64,'policyHash':catalog_digest}
                try:
                    st,diagnostic,_=call('/api/v1/internal/ai-gateway/cache/get',scope,token=node_token);print('cache metadata diagnostic',st,diagnostic,'configuredCatalog',catalog_digest)
                except Exception:pass
            for log in logs:log.flush()
            for label in ('controller','gateway','gateway2'):
                p=tmp/(label+'.log')
                if p.exists():print(label,p.read_text()[-3000:])
            if (tmp/'audit.jsonl').exists():
                rows=[json.loads(x) for x in (tmp/'audit.jsonl').read_text().splitlines()];print('runtime metadata',json.dumps([{'stage':x['stage'],'code':x['decision']['code'],'execution':x['execution']} for x in rows[-12:]]))
            for name in names:
                result=subprocess.run(['docker','logs','--tail','10',name],capture_output=True,text=True);print('bifrost',result.stdout[-2000:],result.stderr[-2000:])
            raise
        finally:
            for p in reversed(processes):
                p.terminate()
                try:p.wait(timeout=15)
                except subprocess.TimeoutExpired:p.kill();p.wait()
            for log in logs:log.close()
            for name in names:subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            if created:helper('cleanup',workspace)
            provider.shutdown()
if __name__=='__main__':main()
