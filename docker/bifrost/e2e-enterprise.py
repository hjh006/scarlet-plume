#!/usr/bin/env python3
"""Disposable PG + controller + real pinned Bifrost + authorized source + MCP.
No paid model calls. SCARLET_DB_DSN must name an isolated disposable database.
"""
import datetime as dt
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
# This disposable fixture owns its random workspace. Remove task descendants
# before workspace credentials, after all gateway/controller processes stop.
HELPER=legacy.HELPER.replace(
    'if os.Args[1]=="cleanup"{if _,e=pool.Exec(ctx,`DELETE FROM plume.workspaces WHERE slug=$1`,os.Args[2]);e!=nil{panic(e)};return}',
    r'''if os.Args[1]=="cleanup"{
 if len(os.Args[2])!=21||os.Args[2][:11]!="enterprise-"{panic("disposable enterprise workspace required")}
 tx,e:=pool.Begin(ctx);if e!=nil{panic(e)};defer tx.Rollback(ctx)
 var id int64;if e=tx.QueryRow(ctx,`SELECT id FROM plume.workspaces WHERE slug=$1 FOR UPDATE`,os.Args[2]).Scan(&id);e!=nil{panic(e)}
 if _,e=tx.Exec(ctx,`DELETE FROM plume.ai_gateway_tasks WHERE workspace_id=$1`,id);e!=nil{panic(e)}
 if _,e=tx.Exec(ctx,`DELETE FROM plume.workspaces WHERE id=$1 AND slug=$2`,id,os.Args[2]);e!=nil{panic(e)}
 if e=tx.Commit(ctx);e!=nil{panic(e)};return
}''')
if HELPER==legacy.HELPER:raise RuntimeError('enterprise fixture cleanup helper not installed')

CONTENT='Synthetic source contact: person@example.com\nAuthorized for support only.'
CONTENT_SHA=hashlib.sha256(CONTENT.encode()).hexdigest()
WORKSPACE=''
SOURCE_TOKEN='synthetic-enterprise-source-token-00000001'

def timestamp(seconds=120):return (dt.datetime.now(dt.timezone.utc)+dt.timedelta(seconds=seconds)).isoformat().replace('+00:00','Z')

class Services(http.server.BaseHTTPRequestHandler):
    mode='safe';source_mode='safe';hits={'model':0,'tool':0,'source':0};saw_injected=False
    entered=threading.Event();resume=threading.Event()
    def log_message(self,*_):pass
    def send(self,status,value,extra=None):
        self.send_response(status);self.send_header('Content-Type','application/json')
        for key,val in (extra or {}).items():self.send_header(key,val)
        self.end_headers();self.wfile.write(json.dumps(value).encode())
    def do_POST(self):
        value=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        if self.path=='/source':
            type(self).hits['source']+=1
            assert self.headers.get('Authorization')=='Bearer '+SOURCE_TOKEN
            assert value['workspaceSlug']==WORKSPACE and value['applicationId']=='app' and value['principalId']=='alice'
            assert value['profile']=='scarlet-document-source-v1' and value['documentIds']==['doc']
            doc={'id':'doc','version':'v1','aclVersion':'acl1','content':CONTENT,'contentDigest':CONTENT_SHA,'authorized':self.source_mode!='revoked','authorizationExpiresAt':timestamp()}
            if self.source_mode=='changed':doc['content']='same version but replaced';doc['contentDigest']=hashlib.sha256(doc['content'].encode()).hexdigest()
            self.send(200,{'workspaceSlug':WORKSPACE,'profile':'scarlet-document-source-v1','applicationId':'app','principalId':'alice','documents':[doc]});return
        if self.path=='/tool':
            type(self).hits['tool']+=1
            assert value['method']=='tools/call' and value['params']['name']=='read' and value['params']['arguments']['ticketId']=='ticket-17'
            self.send(200,{'jsonrpc':'2.0','id':value['id'],'result':{'resultType':'complete','content':[{'type':'text','text':'approved tool answer'}]}},{'MCP-Protocol-Version':'2026-07-28'});return
        type(self).hits['model']+=1
        assert self.headers.get('Authorization')=='Bearer synthetic-enterprise-provider-key-0001'
        model=value['model'];assert model in ['approved-model','public-model']
        if self.mode=='pause':type(self).entered.set();type(self).resume.wait(10)
        for msg in value.get('messages',[]):
            if isinstance(msg.get('content'),str) and 'Untrusted source data;' in msg['content']:
                data=json.loads(msg['content'][msg['content'].index('['):]);assert data[0]['content']==CONTENT and data[0]['contentDigest']==CONTENT_SHA and data[0]['aclVersion']=='acl1';type(self).saw_injected=True
        if self.mode=='revoke-source':type(self).source_mode='revoked'
        if value.get('stream'):
            self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
            chunks=[{'content':'approved '},{'content':'answer'},{}]
            for index,delta in enumerate(chunks):
                chunk={'id':'enterprise-stream','object':'chat.completion.chunk','model':model,'choices':[{'index':0,'delta':delta,'finish_reason':'stop' if index==2 else None}]}
                self.wfile.write(('data: '+json.dumps(chunk)+'\n\n').encode())
            chunk=dict(chunk,choices=[],usage={'prompt_tokens':3,'completion_tokens':2,'total_tokens':5})
            self.wfile.write(('data: '+json.dumps(chunk)+'\n\ndata: [DONE]\n\n').encode());return
        self.send(200,{'object':'chat.completion','model':model,'choices':[{'index':0,'message':{'role':'assistant','content':'approved answer'},'finish_reason':'stop'}]})

def main():
    global WORKSPACE
    assert os.getenv('SCARLET_DB_DSN'),'private disposable SCARLET_DB_DSN required'
    WORKSPACE='enterprise-'+uuid.uuid4().hex[:10]
    cp,gp,bp,sp=port(),port(),port(),port();control=f'http://127.0.0.1:{cp}'
    server=http.server.ThreadingHTTPServer(('0.0.0.0',sp),Services);threading.Thread(target=server.serve_forever,daemon=True).start()
    bf_name='scarlet-enterprise-'+uuid.uuid4().hex[:10];processes=[];logs=[];created=False;results=[]
    with tempfile.TemporaryDirectory(prefix='scarlet-enterprise-e2e-') as directory,tempfile.TemporaryDirectory(prefix='.gateway-enterprise-e2e-',dir=ROOT/'backend') as helper_dir:
        tmp=Path(directory);hp=Path(helper_dir);(hp/'main.go').write_text(HELPER)
        node=secrets.token_urlsafe(48)
        env=dict(os.environ,SCARLET_ORIGIN=control,SCARLET_GATEWAY_WORKSPACE=WORKSPACE,SCARLET_GATEWAY_CONTROL_TOKEN=node,SCARLET_GATEWAY_CONTROL_TOKENS='',SCARLET_BOOTSTRAP_WORKSPACE=WORKSPACE,SCARLET_BOOTSTRAP_WORKSPACE_NAME='Disposable Enterprise Test',SCARLET_BOOTSTRAP_USER=WORKSPACE+'@example.invalid',SCARLET_BOOTSTRAP_PASSWORD=secrets.token_urlsafe(32),SCARLET_GATEWAY_PROVIDER_KEY='synthetic-enterprise-provider-key-0001',SCARLET_GATEWAY_ENDPOINT=f'http://host.docker.internal:{sp}/v1',SCARLET_GATEWAY_CACHE_KEY=__import__('base64').b64encode(secrets.token_bytes(32)).decode())
        def helper(op,*args):return subprocess.check_output([str(tmp/'helper'),op,*map(str,args)],env=env).decode()
        def launch(cmd,label):
            log=(tmp/(label+'.log')).open('w');logs.append(log);p=subprocess.Popen(cmd,env=env,stdout=log,stderr=log);processes.append(p);return p
        opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        def call(path,value=None,method=None,token=None,task=None,gateway=False):
            headers={'Content-Type':'application/json'}
            if not gateway:headers['Origin']=control
            if token:headers['Authorization']='Bearer '+token
            if task:headers['X-Scarlet-Task-Id']=task
            request=urllib.request.Request((f'http://127.0.0.1:{gp}' if gateway else control)+path,data=None if value is None else json.dumps(value).encode(),headers=headers,method=method)
            try:
                with opener.open(request,timeout=30) as response:return response.status,response.read().decode(),dict(response.headers)
            except urllib.error.HTTPError as e:return e.code,e.read().decode(),dict(e.headers)
        base='/api/v1/workspaces/'+WORKSPACE+'/ai-gateway'
        try:
            for target,pkg in [('server','./cmd/server'),('gateway','./cmd/ai-gateway'),('helper','./'+hp.name)]:subprocess.run(['go','-C',str(ROOT/'backend'),'build','-o',str(tmp/target),pkg],check=True)
            helper('check',WORKSPACE);subprocess.run([str(tmp/'server'),'-mode','bootstrap','-allow-insecure-loopback'],env=env,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);created=True
            p=launch([str(tmp/'server'),'-addr',f'127.0.0.1:{cp}','-allow-insecure-loopback'],'controller');wait_port(cp,p)
            assert call('/api/v1/session',{'username':env['SCARLET_BOOTSTRAP_USER'],'password':env['SCARLET_BOOTSTRAP_PASSWORD']})[0]==204
            handling={'retentionHours':0,'trainingUse':'prohibited','evidenceRef':'synthetic-reviewed','authorizationExpiresAt':timestamp(3600)}
            models=[{'id':mid,'name':mid,'enabled':True,'provider':'openai','endpointRef':'model','region':'CN','upstreamModel':upstream,'responseFormat':'openai_chat','allowedApplicationIds':['app','cache-app'],'allowedInputCategories':['email'] if mid=='enterprise' else [],'allowedOutputCategories':['email'] if mid=='enterprise' else [],'handling':handling} for mid,upstream in [('enterprise','approved-model'),('public','public-model')]]
            catalog={'version':'enterprise-e2e','applications':[{'id':'app','name':'source app','enabled':True,'requireTask':False,'principalIds':['alice'],'documentIds':['doc','doc-open'],'modelIds':['enterprise','public'],'toolIds':['ticket']},{'id':'cache-app','name':'cache app','enabled':True,'requireTask':True,'principalIds':['cache-user'],'documentIds':[],'modelIds':['enterprise'],'toolIds':[]}],'principals':[{'id':'alice','enabled':True,'applicationIds':['app']},{'id':'cache-user','enabled':True,'applicationIds':['cache-app']}],'documents':[{'id':'doc','enabled':True,'version':'v1','aclVersion':'acl1','knowledgeBaseId':'kb','sourceRef':'private-doc','integrityDigest':CONTENT_SHA,'allowedPrincipalIds':['alice'],'categories':[],'usePolicy':{'allowedPurposes':['support'],'maxRetentionHours':0,'allowTraining':False}},{'id':'doc-open','enabled':True,'version':'v1','aclVersion':'acl1','knowledgeBaseId':'kb','sourceRef':'private-open-doc','integrityDigest':hashlib.sha256(b'Open synthetic source').hexdigest(),'allowedPrincipalIds':['alice'],'categories':[]}],'models':models,'tools':[{'id':'ticket','name':'synthetic read','enabled':True,'endpointRef':'tool','protocol':'mcp_http','protocolVersion':'2026-07-28','targetArgument':'ticketId','allowedApplicationIds':['app'],'allowedPrincipalIds':['alice'],'actions':['read'],'targets':['ticket-17','ticket-99'],'allowedArguments':{},'requiredArguments':[],'highImpact':False,'allowedInputCategories':['email'],'handling':dict(handling,retentionHours=1)}],'approvals':[],'policy':{'version':'enterprise-policy','maxRequestAgeSeconds':300,'maxInputBytes':65536,'maxOutputBytes':65536,'allowedInputCategories':['email'],'allowedOutputCategories':['email'],'allowedModelRegions':['CN'],'blockedInputPatterns':[],'blockedOutputPatterns':[],'requirePinnedSourceContent':True,'requireDataHandling':True}}
            profile={'provider':'openai','region':'CN','endpointRef':'env:SCARLET_GATEWAY_ENDPOINT','baseUrl':env['SCARLET_GATEWAY_ENDPOINT'],'apiKeyEnv':'SCARLET_GATEWAY_PROVIDER_KEY','models':['approved-model','public-model'],'port':bp}
            write(tmp/'catalog.json',catalog);write(tmp/'profile.json',profile);write(tmp/'bindings.json',{'credentials':[],'targets':{'model':{'url':f'http://127.0.0.1:{bp}/openai/v1/chat/completions'},'tool':{'url':f'http://127.0.0.1:{sp}/tool'}},'tools':[{'name':'read-ticket','toolId':'ticket','action':'read'}]})
            data=tmp/'bifrost';data.mkdir(mode=0o700);digest=helper('assets',tmp/'profile.json',tmp/'catalog.json',data/'config.json')
            subprocess.run(['docker','run','-d','--name',bf_name,'--user',f'{os.getuid()}:{os.getgid()}','-p',f'127.0.0.1:{bp}:8080','-v',f'{data}:/app/data:rw','-e','SCARLET_GATEWAY_PROVIDER_KEY='+env['SCARLET_GATEWAY_PROVIDER_KEY'],'-e','HTTP_PROXY=','-e','HTTPS_PROXY=','-e','ALL_PROXY=','-e','NO_PROXY=*','--entrypoint','/app/main',IMAGE,'-host','0.0.0.0','-port','8080','-app-dir','/app/data'],stdout=subprocess.DEVNULL,check=True);wait_http(bp)
            cache={'enabled':True,'ttlSeconds':60,'maxEntries':100,'maxBytes':1048576,'keyRef':'env:SCARLET_GATEWAY_CACHE_KEY','keyVersion':'v1'}
            apps=[{'id':aid,'name':aid,'enabled':True,'requireTask':True,'principalId':principal,'allowRag':aid=='app','modelIds':['enterprise','public'] if aid=='app' else ['enterprise'],'toolIds':['ticket'] if aid=='app' else [],'budgetMicros':100000000,'requestsPerMinute':100,'tokensPerMinute':1000000,'cachePolicy':cache} for aid,principal in [('app','alice'),('cache-app','cache-user')]]
            providers=[{'id':mid,'name':mid,'provider':'openai','modelId':mid,'upstreamModel':up,'endpointRef':'env:SCARLET_GATEWAY_ENDPOINT','secretRef':'env:SCARLET_GATEWAY_PROVIDER_KEY','region':'CN','responseFormat':'openai_chat','enabled':True,'fallbackModelIds':[],'inputMicrosPerMillion':1000000,'outputMicrosPerMillion':1000000} for mid,up in [('enterprise','approved-model'),('public','public-model')]]
            status,out,_=call(base+'/config',{'expectedRevision':0,'enabled':True,'catalogDigest':digest,'applications':apps,'providers':providers},'PUT');assert status==200,('config',status,out)
            credentials={}
            for app in apps:
                status,out,_=call(base+'/credentials',{'applicationId':app['id'],'name':'enterprise synthetic'});assert status==201,(status,out);credentials[app['id']]=json.loads(out)
            for name,value in [('node.token',node),('source.token',SOURCE_TOKEN)]:p=tmp/name;p.write_text(value);p.chmod(0o600)
            p=launch([str(tmp/'gateway'),'-local','-health-listen',f'127.0.0.1:{port()}','-listen',f'127.0.0.1:{gp}','-catalog',str(tmp/'catalog.json'),'-bindings',str(tmp/'bindings.json'),'-bifrost-profile',str(tmp/'profile.json'),'-audit',str(tmp/'audit.jsonl'),'-journal',str(tmp/'journal.jsonl'),'-control-url',control,'-control-token-file',str(tmp/'node.token'),'-workspace',WORKSPACE,'-node-id','enterprise-node','-document-source-url',f'http://127.0.0.1:{sp}/source','-document-source-token-file',str(tmp/'source.token')],'gateway');wait_port(gp,p)
            def new_task(app='app',parent=None,max_steps=10,purpose='support'):
                cred=credentials[app];value={'applicationId':app,'principalId':'alice' if app=='app' else 'cache-user','credentialId':cred['credential']['id'],'catalogDigest':digest,'purpose':purpose,'modelIds':['enterprise','public'] if app=='app' else ['enterprise'],'toolIds':['ticket'] if app=='app' else [],'toolScopes':[{'toolId':'ticket','actions':['read'],'targets':['ticket-17']}] if app=='app' else [],'documentIds':['doc'] if app=='app' else [],'allowedCategories':['email'],'maxSteps':max_steps,'budgetMicros':10000000,'maxDelegationDepth':0 if parent else 1,'expiresAt':timestamp(1800)}
                if parent:
                    st,parent_detail,_=call(base+'/tasks/'+parent);assert st==200
                    parent_detail=json.loads(parent_detail)['task'];value['parentTaskId']=parent;value['expiresAt']=parent_detail['expiresAt'];value['budgetMicros']=100000
                status,out,_=call(base+'/tasks',value);return status,json.loads(out)
            status,task=new_task();assert status==201,('task',status,task);task_id=task['id'];token=credentials['app']['token']
            def wire(kind='model',model='enterprise',docs=None):return {'requestId':'enterprise-step-'+uuid.uuid4().hex[:16],'kind':kind,'modelId':model if kind=='model' else '', 'documentIds':docs or [],'input':json.dumps({'messages':[{'role':'user','content':'synthetic request'}],'stream':False}) if kind=='model' else '', 'issuedAt':timestamp(-1),'expiresAt':timestamp(60)}
            def check(label,path,body,allowed,model_delta=0,tool_delta=0,task=task_id,cred=token):
                before=dict(Services.hits);status,out,headers=call(path,body,token=cred,task=task,gateway=True);assert status in allowed,(label,status,out[:1500]);assert Services.hits['model']-before['model']==model_delta and Services.hits['tool']-before['tool']==tool_delta,(label,Services.hits,before);results.append({'case':label,'status':status,'providerCalls':model_delta,'toolCalls':tool_delta});return out,headers
            check('required-task-before-provider','/v1/chat/completions',{'model':'enterprise','messages':[{'role':'user','content':'safe'}]},{403},task=None)
            before_source=Services.hits['source'];check('central-only-task-required-before-source','/v1/execute',wire(kind='rag',docs=['doc-open']),{403},task=None);assert Services.hits['source']==before_source,'source was read before central task requirement'
            out,_=check('authorized-source-injected-real-bifrost','/v1/execute',wire(docs=['doc']),{200},1);assert Services.saw_injected
            status,detail,_=call(base+'/tasks/'+task_id);assert status==200;detail=json.loads(detail);assert detail['task']['sources'] and 'email' in detail['task']['observedCategories']
            check('task-source-taint-cannot-be-omitted','/v1/execute',wire(model='public'),{403})
            tool=wire(kind='tool');tool.update(toolId='ticket',action='read',target='ticket-17',arguments={});check('model-source-use-policy-follows-tool','/v1/execute',tool,{403})
            Services.mode='revoke-source';check('source-revoked-during-provider-output-suppressed','/v1/execute',wire(docs=['doc']),{403},1);Services.mode='safe';Services.source_mode='safe'
            status,other=new_task();assert status==201
            tool=wire(kind='tool');tool.update(toolId='ticket',action='read',target='ticket-17',arguments={});check('task-approved-real-mcp-action','/v1/execute',tool,{200},tool_delta=1,task=other['id'])
            tool=wire(kind='tool');tool.update(toolId='ticket',action='read',target='ticket-99',arguments={});check('task-target-scope-before-mcp','/v1/execute',tool,{403},task=other['id'])
            status,cache_task=new_task('cache-app',max_steps=3);assert status==201;cache_id=cache_task['id'];cache_token=credentials['cache-app']['token']
            status,child=new_task('cache-app',parent=cache_id,max_steps=1);assert status==201,('child',status,child)
            chat={'model':'enterprise','messages':[{'role':'user','content':'same task scoped cached text'}],'stream':True,'stream_options':{'include_usage':True}}
            out,_=check('task-real-sse-checked','/v1/chat/completions',chat,{200},1,task=cache_id,cred=cache_token);assert '[DONE]' in out
            out,headers=check('task-cache-hit-consumes-step','/v1/chat/completions',chat,{200},task=cache_id,cred=cache_token);assert headers.get('X-Scarlet-Cache')=='hit'
            check('delegated-step-approved-cache-isolated','/v1/chat/completions',chat,{200},1,task=child['id'],cred=cache_token)
            check('task-step-limit-cannot-use-cache','/v1/chat/completions',chat,{403},task=cache_id,cred=cache_token)
            check('task-cannot-cross-credential-app','/v1/chat/completions',chat,{403},task=task_id,cred=cache_token)
            check('delegation-cannot-reset-parent-steps','/v1/chat/completions',chat,{403},task=child['id'],cred=cache_token)
            status,detail,_=call(base+'/tasks/'+cache_id);assert status==200;detail=json.loads(detail)
            status,out,_=call(base+'/tasks/'+cache_id+'/status',{'expectedRevision':detail['task']['revision'],'status':'cancelled','reason':'synthetic emergency stop'});assert status==200,(status,out)
            check('parent-cancel-blocks-descendant-before-provider','/v1/chat/completions',chat,{403},task=child['id'],cred=cache_token)
            status,kill_task=new_task('cache-app',max_steps=2);assert status==201
            Services.mode='pause';Services.entered.clear();Services.resume.clear();reply={}
            before=Services.hits['model']
            def active_call():reply['result']=call('/v1/chat/completions',{k:v for k,v in dict(chat,stream=False).items() if k!='stream_options'},token=cache_token,task=kill_task['id'],gateway=True)
            worker=threading.Thread(target=active_call);worker.start();assert Services.entered.wait(5),'provider did not start'
            status,detail,_=call(base+'/tasks/'+kill_task['id']);assert status==200;detail=json.loads(detail)
            status,out,_=call(base+'/tasks/'+kill_task['id']+'/status',{'expectedRevision':detail['task']['revision'],'status':'cancelled','reason':'synthetic active stop'});assert status==200,(status,out)
            worker.join(4);stopped=not worker.is_alive();Services.resume.set();worker.join(15);Services.mode='safe'
            assert stopped,'active task did not cancel gateway wait within bounded polling interval'
            status,out,_=reply['result'];assert status>=400 and 'approved answer' not in out and Services.hits['model']-before==1,(status,out)
            results.append({'case':'active-task-kill-suppresses-provider-output','status':status,'providerCalls':1,'toolCalls':0})
            status,detail,_=call(base+'/tasks/'+kill_task['id']);assert status==200;assert json.loads(detail)['task']['reservedMicros']>0,'unknown provider occupancy was incorrectly refunded'
            status,out,_=call(base+'/audit?limit=500');assert status==200;audit=json.loads(out);assert audit['valid'];assert any(item['action']=='gateway.task_step_completed' for item in audit['items'])
            rows=[json.loads(row) for row in (tmp/'audit.jsonl').read_text().splitlines()];assert all(CONTENT not in json.dumps(row) for row in rows)
            report={'image':IMAGE,'tests':results,'sourceReads':Services.hits['source'],'sourceBodyInjected':Services.saw_injected,'runtimeAuditRecords':len(rows),'verifiedAudit':audit['valid'],'sourceProfile':'scarlet-document-source-v1','billing':'local synthetic model/MCP/source; no paid calls'}
            report_path=ROOT/'artifacts/gateway-enterprise-e2e-2026-11-16.json'
            report_path.parent.mkdir(parents=True,exist_ok=True)
            report_path.write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report,indent=2))
        except Exception:
            for log in logs:log.flush()
            for label in ['gateway','controller']:
                f=tmp/(label+'.log')
                if f.exists():print(label,f.read_text()[-1800:])
            if (tmp/'audit.jsonl').exists():
                rows=[json.loads(row) for row in (tmp/'audit.jsonl').read_text().splitlines()];print('runtime decisions',[(x['stage'],x['decision']['code'],x['execution']['outcome']) for x in rows[-12:]])
            raise
        finally:
            for p in reversed(processes):
                p.terminate()
                try:p.wait(timeout=15)
                except subprocess.TimeoutExpired:p.kill();p.wait()
            for log in logs:log.close()
            subprocess.run(['docker','rm','-f',bf_name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            if created:helper('cleanup',WORKSPACE)
            server.shutdown()

if __name__=='__main__':main()
