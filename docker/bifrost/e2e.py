#!/usr/bin/env python3
"""Real pinned Bifrost OSS + Scarlet process + synthetic HTTP provider, no paid calls."""
import datetime as dt
import hashlib
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
IMAGE = json.loads((ROOT / 'integrations/bifrost/version.json').read_text())['image']
TOKEN = 'synthetic-scarlet-gateway-client-token-0000001'
PROVIDER_TOKEN = 'synthetic-provider-key-000000001'

def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

def write(path, value):
    path.write_text(json.dumps(value))
    path.chmod(0o600)

def request(url, value=None, headers=None):
    data = None if value is None else json.dumps(value).encode()
    h = {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + TOKEN}
    h.update(headers or {})
    req = urllib.request.Request(url, data=data, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

def wait_port(number, process=None):
    until = time.monotonic() + 40
    while time.monotonic() < until:
        if process is not None and process.poll() is not None:
            raise RuntimeError('Scarlet gateway exited before readiness')
        try:
            with socket.create_connection(('127.0.0.1', number), timeout=.5):
                return
        except OSError:
            time.sleep(.2)
    raise RuntimeError('bounded readiness deadline exceeded')

def wait_http(number):
    until = time.monotonic() + 90
    while time.monotonic() < until:
        try:
            with urllib.request.urlopen(f'http://127.0.0.1:{number}/health',timeout=2) as r:
                return
        except urllib.error.HTTPError as error:
            if error.code == 404:
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(.3)
    raise RuntimeError('Bifrost HTTP initialization deadline exceeded')

class Provider(http.server.BaseHTTPRequestHandler):
    mode = 'safe'
    hits = 0
    escaped = 0
    lock = threading.Lock()
    def log_message(self, *_):
        pass
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        with self.lock:
            type(self).hits += 1
            mode = self.mode
        if self.path == '/escaped':
            type(self).escaped += 1
        assert body['model'] == 'approved-model', body
        assert self.headers.get('Authorization') == 'Bearer ' + PROVIDER_TOKEN
        assert not self.headers.get('x-bf-direct-key')
        if mode in ('error', 'unauthorized'):
            self.send_response(500 if mode == 'error' else 401)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"error":{"message":"synthetic refusal"}}')
            return
        if mode == 'redirect':
            self.send_response(302)
            self.send_header('Location', '/escaped')
            self.end_headers()
            return
        content = 'person@example.com' if mode == 'pii' else 'approved synthetic answer'
        if body.get('stream'):
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            for part in (content[:8], content[8:]):
                chunk = {'id':'synthetic-stream','object':'chat.completion.chunk','created':1,'model':'approved-model','choices':[{'index':0,'delta':{'content':part},'finish_reason':None}]}
                self.wfile.write(('data: '+json.dumps(chunk)+'\n\n').encode())
            chunk['choices'] = [{'index':0,'delta':{},'finish_reason':'stop'}]
            self.wfile.write(('data: '+json.dumps(chunk)+'\n\n').encode())
            chunk['choices'] = []
            chunk['usage'] = {'prompt_tokens':2,'completion_tokens':2,'total_tokens':4}
            self.wfile.write(('data: '+json.dumps(chunk)+'\n\ndata: [DONE]\n\n').encode())
            return
        value = {'id': 'synthetic-provider-id', 'object': 'chat.completion', 'created': 1,
                 'model': 'unexpected-model' if mode == 'wrong-model' else 'approved-model',
                 'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': content}, 'finish_reason': 'stop'}],
                 'usage': {'prompt_tokens': 9, 'completion_tokens': 4, 'total_tokens': 13}}
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps(value).encode())

def main():
    bf_port, gateway_port, provider_port = port(), port(), port()
    name = 'scarlet-bifrost-e2e-' + uuid.uuid4().hex[:10]
    provider = http.server.ThreadingHTTPServer(('0.0.0.0', provider_port), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    process = None
    results = []
    with tempfile.TemporaryDirectory(prefix='scarlet-bifrost-e2e-') as directory:
        tmp = Path(directory)
        data = tmp / 'bifrost'
        data.mkdir(mode=0o700)
        profile = {'provider': 'openai', 'region': 'CN', 'endpointRef': '',
                   'baseUrl': f'http://host.docker.internal:{provider_port}/v1',
                   'apiKeyEnv': 'SCARLET_GATEWAY_PROVIDER_KEY', 'models': ['approved-model'], 'port': bf_port}
        write(tmp/'profile.json', profile)
        helper = tmp/'config.go'
        helper.write_text('package main\nimport("os";"encoding/json";"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity")\nfunc main(){raw,_:=os.ReadFile(os.Args[1]);var p aisecurity.BifrostProfile;json.Unmarshal(raw,&p);out,err:=aisecurity.BifrostConfig(p,true);if err!=nil{panic(err)};os.Stdout.Write(out)}')
        config = subprocess.check_output(['go', '-C', str(ROOT/'backend'), 'run', str(helper), str(tmp/'profile.json')])
        (data/'config.json').write_bytes(config)
        (data/'config.json').chmod(0o600)
        catalog = {'version': 'gateway-e2e-v1', 'applications': [{'id':'app','name':'Synthetic app','enabled':True,'principalIds':['alice'],'documentIds':[],'modelIds':['enterprise'],'toolIds':[]}],
                   'principals':[{'id':'alice','name':'Synthetic user','enabled':True,'applicationIds':['app']}], 'documents':[],
                   'models':[{'id':'enterprise','name':'Approved model','enabled':True,'provider':'openai','endpointRef':'model','region':'CN','upstreamModel':'approved-model','responseFormat':'openai_chat','allowedApplicationIds':['app'],'allowedInputCategories':[],'allowedOutputCategories':[]}],
                   'tools':[], 'approvals':[], 'policy':{'version':'policy-e2e-v1','maxRequestAgeSeconds':300,'maxInputBytes':65536,'maxOutputBytes':65536,'allowedInputCategories':[],'allowedOutputCategories':[],'allowedModelRegions':['CN'],'blockedInputPatterns':[],'blockedOutputPatterns':[]}}
        write(tmp/'catalog.json', catalog)
        write(tmp/'bindings.json', {'credentials':[{'tokenHash':hashlib.sha256(TOKEN.encode()).hexdigest(),'applicationId':'app','principalId':'alice'}], 'targets':{'model':{'url':f'http://127.0.0.1:{bf_port}/openai/v1/chat/completions'}}, 'tools':[]})
        subprocess.run(['go','-C',str(ROOT/'backend'),'build','-o',str(tmp/'ai-gateway'),'./cmd/ai-gateway'],check=True)
        log = (tmp/'gateway.log').open('w')
        try:
            subprocess.run(['docker','run','-d','--name',name,'--user',f'{os.getuid()}:{os.getgid()}',
                            '-p',f'127.0.0.1:{bf_port}:8080','-v',f'{data}:/app/data:rw',
                            '-e','SCARLET_GATEWAY_PROVIDER_KEY='+PROVIDER_TOKEN,
                            '-e','HTTP_PROXY=','-e','HTTPS_PROXY=','-e','ALL_PROXY=','-e','NO_PROXY=*',
                            '--entrypoint','/app/main',IMAGE,'-host','0.0.0.0','-port','8080','-app-dir','/app/data'],check=True,stdout=subprocess.DEVNULL)
            wait_http(bf_port)
            status,probe=request(f'http://127.0.0.1:{bf_port}/openai/v1/chat/completions',{'model':'approved-model','messages':[{'role':'user','content':'synthetic warmup'}],'stream':False})
            assert status==200,('direct synthetic Bifrost interoperability',status,probe)
            print('synthetic Bifrost envelope keys',list(json.loads(probe)), 'reported model',json.loads(probe).get('model'))
            Provider.hits=0
            env = dict(os.environ, SCARLET_GATEWAY_PROVIDER_KEY=PROVIDER_TOKEN)
            process = subprocess.Popen([str(tmp/'ai-gateway'),'-local','-health-listen',f'127.0.0.1:{port()}','-listen',f'127.0.0.1:{gateway_port}',
                                        '-catalog',str(tmp/'catalog.json'),'-bindings',str(tmp/'bindings.json'),
                                        '-bifrost-profile',str(tmp/'profile.json'),'-audit',str(tmp/'audit.jsonl'),
                                        '-journal',str(tmp/'replay.jsonl')],env=env,stdout=log,stderr=log)
            wait_port(gateway_port, process)
            url = f'http://127.0.0.1:{gateway_port}/v1/chat/completions'
            valid = {'model':'enterprise','messages':[{'role':'user','content':'synthetic text'}],'max_tokens':20}
            def check(label, body, expected, delta=0, headers=None):
                before = Provider.hits
                status, output = request(url, body, headers)
                assert status in expected, (label,status,output)
                assert Provider.hits-before == delta, (label,'upstream count',Provider.hits-before)
                assert 'person@example.com' not in output, (label,'blocked output leaked')
                results.append({'case':label,'status':status,'providerCalls':delta})
                return output
            check('safe-through-real-bifrost',valid,{200},1)
            check('same-prompt-not-cached',valid,{200},1,{'x-bf-cache-key':'attempted-cache','x-bf-direct-key':'attacker-key'})
            check('input-dlp-before-provider',dict(valid,messages=[{'role':'user','content':'person@example.com'}]),{403})
            streamed=check('strict-hold-sse-through-real-bifrost',dict(valid,stream=True),{200},1)
            assert 'data: [DONE]' in streamed
            check('fallback-refused',dict(valid,fallbacks=['other']),{400})
            check('uninspected-multimodal-refused',dict(valid,messages=[{'role':'user','content':[{'type':'image_url','image_url':'https://invalid.example'}]}]),{400,403,422})
            key = {'Idempotency-Key':'synthetic-replay-12345678'}
            check('first-nonce',valid,{200},1,key)
            check('replay-never-provider',valid,{409},0,key)
            for mode in ('error','unauthorized','redirect','pii','wrong-model'):
                Provider.mode=mode
                check(mode,valid,{403,422,502},1)
            assert Provider.escaped==0, 'provider redirect followed'
            events=[json.loads(x) for x in (tmp/'audit.jsonl').read_text().splitlines()]
            assert any(x['execution']['outcome']=='blocked_output' for x in events)
            assert all('person@example.com' not in json.dumps(x) for x in events)
            print(json.dumps({'image':IMAGE,'sourceCommit':'7cb75fb2f8276ca48d0792c857fecb67b8879e2f','tests':results,'auditEvents':len(events)},indent=2))
        except Exception:
            log.flush()
            print((tmp/'gateway.log').read_text()[-3000:])
            logs=subprocess.run(['docker','logs','--tail','30',name],capture_output=True,text=True)
            print((logs.stdout+logs.stderr)[-4000:])
            print('synthetic provider calls',Provider.hits)
            if (tmp/'audit.jsonl').exists():
                print((tmp/'audit.jsonl').read_text()[-2500:])
            raise
        finally:
            if process is not None:
                process.terminate()
                try: process.wait(timeout=20)
                except subprocess.TimeoutExpired: process.kill();process.wait()
            log.close()
            subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            provider.shutdown()

if __name__=='__main__':
    main()
