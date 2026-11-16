#!/usr/bin/env python3
"""Real PostgreSQL control plane -> Scarlet nodes -> pinned Bifrost -> synthetic provider.
Requires SCARLET_DB_DSN and bootstrap owner/password in the environment. No paid API calls.
"""
import argparse
import http.cookiejar
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

from e2e import ROOT, IMAGE, Provider as BaseProvider, port, wait_port, write

class Provider(BaseProvider):
    pause_next = False
    entered = threading.Event()
    resume = threading.Event()
    def do_POST(self):
        if type(self).pause_next:
            type(self).pause_next = False
            type(self).entered.set()
            if not type(self).resume.wait(15):
                self.send_error(503)
                return
        super().do_POST()

HELPER = r'''package main
import("context";"encoding/json";"fmt";"os";"time";"github.com/hjh006/scarlet-plume/backend/internal/store";"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity";"github.com/jackc/pgx/v5/pgxpool")
func main(){
 if os.Args[1]=="assets"{raw,_:=os.ReadFile(os.Args[2]);var p aisecurity.BifrostProfile;if json.Unmarshal(raw,&p)!=nil{panic("profile")};out,e:=aisecurity.BifrostConfig(p,true);if e!=nil{panic(e)};if os.WriteFile(os.Args[4],out,0600)!=nil{panic("config")};raw,_=os.ReadFile(os.Args[3]);var c aisecurity.Config;if json.Unmarshal(raw,&c)!=nil{panic("catalog")};fmt.Print(aisecurity.ConfigDigest(c));return}
 ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel();pool,e:=pgxpool.New(ctx,os.Getenv("SCARLET_DB_DSN"));if e!=nil{panic("DB config")};defer pool.Close();repo:=store.New(pool);if e=repo.Migrate(ctx);e!=nil{panic(e)}
 if os.Args[1]=="check"{var exists bool;if e=pool.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM plume.workspaces WHERE slug=$1)`,os.Args[2]).Scan(&exists);e!=nil||exists{panic("test workspace already exists or DB unavailable")};return}
 if os.Args[1]=="cleanup"{if _,e=pool.Exec(ctx,`DELETE FROM plume.workspaces WHERE slug=$1`,os.Args[2]);e!=nil{panic(e)};return}
}
'''


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--control-port', type=int, default=8898)
    parser.add_argument('--workspace', default='gateway-e2e')
    args = parser.parse_args()
    assert all(os.environ.get(k) for k in ('SCARLET_DB_DSN', 'SCARLET_BOOTSTRAP_USER', 'SCARLET_BOOTSTRAP_PASSWORD')), 'private DB and bootstrap environment required'
    provider_port, bf_port, first_port, second_port = port(), port(), port(), port()
    name = 'scarlet-controller-e2e-' + uuid.uuid4().hex[:10]
    control = f'http://127.0.0.1:{args.control_port}'
    node_token = secrets.token_urlsafe(48)
    processes, logs, results = [], [], []
    import http.server
    provider = http.server.ThreadingHTTPServer(('0.0.0.0', provider_port), Provider)
    Provider.mode, Provider.hits, Provider.escaped = 'safe', 0, 0
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    created = False
    with tempfile.TemporaryDirectory(prefix='scarlet-controller-e2e-') as directory, tempfile.TemporaryDirectory(prefix='.gateway-controller-e2e-', dir=ROOT/'backend') as helper_dir:
        tmp, helper_path = Path(directory), Path(helper_dir)
        (helper_path/'main.go').write_text(HELPER)
        env = dict(os.environ, SCARLET_ORIGIN=control, SCARLET_GATEWAY_WORKSPACE=args.workspace,
                   SCARLET_GATEWAY_CONTROL_TOKEN=node_token, SCARLET_GATEWAY_CONTROL_TOKENS='',
                   SCARLET_BOOTSTRAP_WORKSPACE=args.workspace, SCARLET_BOOTSTRAP_WORKSPACE_NAME='Disposable controller E2E')
        def helper(operation, *values):
            return subprocess.check_output([str(tmp/'helper'), operation, *map(str, values)], env=env).decode()
        def launch(command, label, process_env=env):
            log = (tmp/(label+'.log')).open('w')
            logs.append(log)
            process = subprocess.Popen(command, env=process_env, stdout=log, stderr=log)
            processes.append(process)
            return process
        cookie_jar = http.cookiejar.CookieJar()
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookie_jar))
        def call(path, value=None, method=None, auth=None, gateway=None, extra=None):
            data = None if value is None else json.dumps(value).encode()
            headers = {'Content-Type':'application/json'}
            if gateway is None:
                headers['Origin'] = control
            if auth is not None:
                headers['Authorization'] = 'Bearer '+auth
            headers.update(extra or {})
            req = urllib.request.Request((control if gateway is None else gateway)+path, data=data, headers=headers, method=method)
            try:
                with opener.open(req, timeout=25) as response:
                    raw = response.read()
                    return response.status, json.loads(raw) if raw else None
            except urllib.error.HTTPError as error:
                raw = error.read()
                return error.code, json.loads(raw) if raw else None
        base = '/api/v1/workspaces/'+args.workspace+'/ai-gateway'
        try:
            for target, package in [('server','./cmd/server'), ('ai-gateway','./cmd/ai-gateway'), ('export','./cmd/gateway-audit-export')]:
                subprocess.run(['go','-C',str(ROOT/'backend'),'build','-o',str(tmp/target),package],check=True)
            subprocess.run(['go','-C',str(ROOT/'backend'),'build','-o',str(tmp/'helper'),'./'+helper_path.name],check=True)
            helper('check', args.workspace)
            subprocess.run([str(tmp/'server'),'-mode','bootstrap','-allow-insecure-loopback'],env=env,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            created = True
            controller = launch([str(tmp/'server'),'-addr',f'127.0.0.1:{args.control_port}','-allow-insecure-loopback'],'controller')
            wait_port(args.control_port, controller)
            status, _ = call('/api/v1/session', {'username':env['SCARLET_BOOTSTRAP_USER'],'password':env['SCARLET_BOOTSTRAP_PASSWORD']})
            assert status == 204, ('owner login', status)
            data = tmp/'bifrost';data.mkdir(mode=0o700)
            endpoint_ref = 'env:SCARLET_GATEWAY_E2E_ENDPOINT'
            profile = {'provider':'openai','region':'CN','endpointRef':endpoint_ref,
                       'baseUrl':f'http://host.docker.internal:{provider_port}/v1','apiKeyEnv':'SCARLET_GATEWAY_PROVIDER_KEY','models':['approved-model'],'port':bf_port}
            catalog = {'version':'controller-e2e-v1','applications':[{'id':'app','name':'E2E app','enabled':True,'principalIds':['alice'],'documentIds':[],'modelIds':['enterprise'],'toolIds':[]}],
                       'principals':[{'id':'alice','name':'E2E user','enabled':True,'applicationIds':['app']}],'documents':[],
                       'models':[{'id':'enterprise','name':'Fixed model','enabled':True,'provider':'openai','endpointRef':'model','region':'CN','upstreamModel':'approved-model','responseFormat':'openai_chat','allowedApplicationIds':['app'],'allowedInputCategories':[],'allowedOutputCategories':[]}],
                       'tools':[],'approvals':[],'policy':{'version':'controller-policy-v1','maxRequestAgeSeconds':300,'maxInputBytes':65536,'maxOutputBytes':65536,'allowedInputCategories':[],'allowedOutputCategories':[],'allowedModelRegions':['CN'],'blockedInputPatterns':[],'blockedOutputPatterns':[]}}
            write(tmp/'profile.json',profile);write(tmp/'catalog.json',catalog)
            digest = helper('assets',tmp/'profile.json',tmp/'catalog.json',data/'config.json')
            application = {'id':'app','name':'Dashboard issued E2E app','enabled':True,'principalId':'alice','allowRag':False,'modelIds':['enterprise'],'toolIds':[],'budgetMicros':100_000_000,'requestsPerMinute':100,'tokensPerMinute':1_000_000}
            configured_provider = {'id':'fixed','name':'Fixed e2e provider','provider':'openai','modelId':'enterprise','upstreamModel':'approved-model','endpointRef':endpoint_ref,'secretRef':'env:SCARLET_GATEWAY_PROVIDER_KEY','region':'CN','responseFormat':'openai_chat','enabled':True,'inputMicrosPerMillion':1_000_000,'outputMicrosPerMillion':1_000_000}
            config = {'expectedRevision':0,'enabled':True,'catalogDigest':digest,'applications':[application],'providers':[configured_provider]}
            status, saved = call(base+'/config',config,'PUT');assert status==200, ('configure',status,saved)
            status, credential = call(base+'/credentials',{'applicationId':'app','name':'Dashboard one-time token'});assert status==201, ('issue credential',status)
            client_token = credential['token']
            write(tmp/'bindings.json',{'credentials':[],'targets':{'model':{'url':f'http://127.0.0.1:{bf_port}/openai/v1/chat/completions'}},'tools':[]})
            (tmp/'node.token').write_text(node_token);(tmp/'node.token').chmod(0o600)
            subprocess.run(['docker','run','-d','--name',name,'--user',f'{os.getuid()}:{os.getgid()}', '-p',f'127.0.0.1:{bf_port}:8080','-v',f'{data}:/app/data:rw','-e','SCARLET_GATEWAY_PROVIDER_KEY=synthetic-provider-key-000000001','-e','HTTP_PROXY=','-e','HTTPS_PROXY=','-e','ALL_PROXY=','-e','NO_PROXY=*','--entrypoint','/app/main',IMAGE,'-host','0.0.0.0','-port','8080','-app-dir','/app/data'],check=True,stdout=subprocess.DEVNULL)
            wait_port(bf_port)
            import time
            until=time.monotonic()+30
            while True:
                try:
                    with urllib.request.urlopen(f'http://127.0.0.1:{bf_port}/health',timeout=1) as ready:
                        if ready.status==200:break
                except Exception:
                    if time.monotonic()>until:raise RuntimeError('Bifrost health deadline exceeded')
                    time.sleep(.2)
            node_env = dict(env,SCARLET_GATEWAY_PROVIDER_KEY='synthetic-provider-key-000000001',SCARLET_GATEWAY_E2E_ENDPOINT=profile['baseUrl'])
            for label, number in [('node1',first_port),('node2',second_port)]:
                node = launch([str(tmp/'ai-gateway'),'-local','-listen',f'127.0.0.1:{number}','-catalog',str(tmp/'catalog.json'),'-bindings',str(tmp/'bindings.json'),'-bifrost-profile',str(tmp/'profile.json'),'-audit',str(tmp/(label+'.audit.jsonl')),'-journal',str(tmp/(label+'.journal.jsonl')),'-control-url',control,'-control-token-file',str(tmp/'node.token'),'-workspace',args.workspace,'-node-id',label],label,node_env)
                wait_port(number,node)
            until=time.monotonic()+3
            while True:
                status,overview=call(base);assert status==200
                live={node['id']:node['status'] for node in overview['nodes']}
                if live.get('node1')=='healthy' and live.get('node2')=='healthy':
                    results.append({'case':'two-live-node-heartbeats','healthyNodes':2})
                    break
                assert time.monotonic()<until,('two healthy node heartbeats not received',live)
                time.sleep(.1)
            chat = {'model':'enterprise','messages':[{'role':'user','content':'synthetic safe text'}],'max_tokens':20}
            def stats():
                status, overview = call(base);assert status==200
                status, reservations = call(base+'/reservations?limit=200');assert status==200
                return overview['usage'][0],reservations['items']
            def check(label,body,want,delta,node=first_port,extra=None):
                before=Provider.hits
                status,out=call('/v1/chat/completions',body,auth=client_token,gateway=f'http://127.0.0.1:{node}',extra=extra)
                assert status in want,(label,status,out)
                assert Provider.hits-before==delta,(label,'upstream count',Provider.hits-before)
                assert 'person@example.com' not in json.dumps(out),(label,'DLP output leaked')
                results.append({'case':label,'status':status,'providerCalls':delta})
            check('dashboard-token-real-controller-success',chat,{200},1)
            usage, reservations=stats();assert usage['spentMicros']>0 and usage['reservedMicros']==0 and len(reservations)==1 and reservations[0]['state']=='settled'
            replay_key={'Idempotency-Key':'controller-cross-node-replay-0001'}
            check('cross-node-first-execution',chat,{200},1,extra=replay_key)
            check('cross-node-replay-never-provider',chat,{403,409},0,node=second_port,extra=replay_key)
            before_usage,before_reservations=stats()
            check('input-dlp-before-controller-reservation',dict(chat,messages=[{'role':'user','content':'person@example.com'}]),{403},0)
            usage,reservations=stats();assert len(reservations)==len(before_reservations) and usage==before_usage
            status,denied_audit=call(base+'/audit?limit=500');assert status==200 and denied_audit['valid']
            denied_entries=[entry for entry in denied_audit['items'] if entry['action']=='gateway.call_denied' and entry['payload']['audit']['decision']['code']=='input_category_denied']
            assert denied_entries and denied_entries[-1]['payload']['verified'] is True and denied_entries[-1]['payload']['billingEffect']=='none'
            assert denied_entries[-1]['payload']['audit']['execution']=={'attempted':False,'outcome':'not_attempted'}
            results.append({'case':'input-dlp-visible-in-central-hash-chain','verified':True,'billingEffect':'none'})

            Provider.mode='pii';check('output-dlp-keeps-unknown-reservation',chat,{403},1)
            usage,reservations=stats();assert usage['reservedMicros']>0 and sum(x['state']=='unknown' for x in reservations)==1
            Provider.mode='error';check('upstream-error-keeps-unknown-reservation',chat,{502},1)
            usage,reservations=stats();assert sum(x['state']=='unknown' for x in reservations)==2
            # Revoke only the model scope while identity remains valid, after the real provider receives the request.
            Provider.mode='safe';Provider.entered.clear();Provider.resume.clear();Provider.pause_next=True
            reply=[];before_hits=Provider.hits
            pending=threading.Thread(target=lambda:reply.append(call('/v1/chat/completions',chat,auth=client_token,gateway=f'http://127.0.0.1:{first_port}')))
            pending.start();assert Provider.entered.wait(10),'provider request did not reach the pause gate'
            application['modelIds']=[];config['expectedRevision']=saved['revision']
            status,saved=call(base+'/config',config,'PUT');assert status==200
            Provider.resume.set();pending.join(20)
            assert reply and reply[0][0] in (403,422),("scope revocation during execution",reply)
            assert Provider.hits-before_hits==1
            usage,reservations=stats();assert sum(x['state']=='unknown' for x in reservations)==3
            results.append({'case':'inflight-model-scope-revocation-suppresses-output','status':reply[0][0],'providerCalls':1})
            application['modelIds']=['enterprise'];config['expectedRevision']=saved['revision']
            status,saved=call(base+'/config',config,'PUT');assert status==200
            # A static local provider/profile mismatch denies before provider and releases its proven-not-attempted quote.
            Provider.mode='safe';config['expectedRevision']=saved['revision'];configured_provider['region']='US'
            status,saved=call(base+'/config',config,'PUT');assert status==200
            prior_reserved=usage['reservedMicros'];check('controller-profile-mismatch-fails-closed',chat,{403},0)
            usage,_=stats();assert usage['reservedMicros']==prior_reserved
            status,_=call(base+'/credentials/'+str(credential['credential']['id']),method='DELETE');assert status==204
            check('revoked-dashboard-token-rejected-by-live-resolver',chat,{401},0)
            export_env=dict(env,SCARLET_GATEWAY_CONTROL_TOKEN=node_token)
            archive=tmp/'export.jsonl'
            for _ in range(2):
                subprocess.run([str(tmp/'export'),'-local','-platform',control,'-workspace',args.workspace,'-output',str(archive)],env=export_env,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            exported=[json.loads(line) for line in archive.read_text().splitlines()]
            status,audit=call(base+'/audit?limit=500');assert status==200 and audit['valid'] and len(exported)==audit['headSequence']
            assert all('person@example.com' not in json.dumps(row) for row in exported)
            assert len({row['sequence'] for row in exported})==len(exported)
            results.append({'case':'verified-audit-cli-repeat-no-duplicates','events':len(exported)})
            print(json.dumps({'image':IMAGE,'tests':results,'spentQuoteMicros':usage['spentMicros'],'unknownReservedMicros':usage['reservedMicros'],'providerCalls':Provider.hits},indent=2))
        except Exception:
            for log in logs:log.flush()
            for label in ('controller','node1','node2'):
                path=tmp/(label+'.log')
                if path.exists():print(label+' log: '+path.read_text()[-2000:])
                audit_path=tmp/(label+'.audit.jsonl')
                if audit_path.exists():
                    events=[json.loads(line) for line in audit_path.read_text().splitlines()]
                    print(label+' execution evidence: '+json.dumps([{'stage':e['stage'],'code':e['decision']['code'],'outcome':e['execution']} for e in events[-8:]]))
            print('synthetic provider calls='+str(Provider.hits))
            diagnostic=subprocess.run(['docker','logs','--tail','25',name],capture_output=True,text=True)
            print('Bifrost diagnostic: '+(diagnostic.stdout+diagnostic.stderr)[-5000:])
            try:
                probe=urllib.request.Request(f'http://127.0.0.1:{bf_port}/openai/v1/chat/completions',data=json.dumps({'model':'approved-model','messages':[{'role':'user','content':'synthetic network diagnostic'}]}).encode(),headers={'Content-Type':'application/json'})
                with urllib.request.urlopen(probe,timeout=10) as response:
                    body=json.loads(response.read())
                    print('synthetic Bifrost diagnostic shape='+json.dumps({'status':response.status,'topKeys':sorted(body),'model':body.get('model'),'choiceKeys':sorted(body.get('choices',[{}])[0]),'messageKeys':sorted(body.get('choices',[{}])[0].get('message',{})),'usageKeys':sorted(body.get('usage',{}))}))
            except urllib.error.HTTPError as error:
                print('synthetic Bifrost diagnostic: '+error.read().decode()[:2000])
            except Exception as error:print('synthetic Bifrost network error type='+type(error).__name__)
            if created:
                try:
                    _,res=call(base+'/reservations?limit=200')
                    print('reservation states='+json.dumps([{'state':x['state'],'reservedMicros':x['reservedMicros']} for x in res['items']]))
                except Exception:pass
            raise
        finally:
            Provider.resume.set()
            if created:
                try:call('/api/v1/session',method='DELETE')
                except Exception:pass
            for process in reversed(processes):
                process.terminate()
                try:process.wait(timeout=15)
                except subprocess.TimeoutExpired:process.kill();process.wait()
            for log in logs:log.close()
            subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            if created:helper('cleanup',args.workspace)
            provider.shutdown()

if __name__=='__main__':main()
