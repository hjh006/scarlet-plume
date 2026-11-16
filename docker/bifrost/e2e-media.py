#!/usr/bin/env python3
"""Real pinned Bifrost media interoperability with actual OCR/ASR, no paid calls.
Usage: python3 e2e-media.py --tesseract /absolute/tesseract --whisper /absolute/whisper-cli
 --whisper-model /private/ggml-tiny.en.bin --whisper-model-sha256 SHA256 --wav /private/synthetic.wav
The WAV fixture must contain the synthetic word 'confidential' and at least one
recognized speech token below 90% confidence for the v2 uncertainty denial case.
"""
import argparse
import base64
import datetime as dt
import hashlib
import http.server
import json
import os
import subprocess
import tempfile
import threading
import uuid
from pathlib import Path
from e2e import ROOT,IMAGE,TOKEN,PROVIDER_TOKEN,port,write,request,wait_port,wait_http

class Provider(http.server.BaseHTTPRequestHandler):
    mode='text';hits=0;seen={'image':0,'audio':0};image='';audio=''
    def log_message(self,*_):pass
    def do_POST(self):
        body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        assert body['model']=='approved-model'
        assert self.headers.get('Authorization')=='Bearer '+PROVIDER_TOKEN
        type(self).hits+=1
        for message in body.get('messages',[]):
            if isinstance(message.get('content'),list):
                for part in message['content']:
                    if part['type']=='image_url':
                        data=base64.b64decode(part['image_url']['url'].split(',',1)[1],validate=True);assert data.startswith(b'\x89PNG\r\n\x1a\n');type(self).seen['image']+=1
                    if part['type']=='input_audio':
                        data=base64.b64decode(part['input_audio']['data'],validate=True);assert data[:4]==b'RIFF' and data[8:12]==b'WAVE';type(self).seen['audio']+=1
        message={'role':'assistant','content':'approved synthetic answer'}
        if self.mode=='empty-null':message['content']=None
        if self.mode=='empty-array':message['content']=[]
        if self.mode=='image':message['content']=[{'type':'text','text':'approved image'},{'type':'image_url','image_url':{'url':'data:image/png;base64,'+self.image}}]
        if self.mode=='audio':
            assert body.get('modalities')==['text','audio'] and body.get('audio')=={'voice':'alloy','format':'wav'},'public facade audio request options did not reach provider'
            message['audio']={'id':'synthetic-audio','data':self.audio,'expires_at':2000000000,'transcript':'synthetic provider transcript'}
        value={'id':'synthetic-provider','object':'chat.completion','created':1,'model':'approved-model','choices':[{'index':0,'message':message,'finish_reason':'stop'}],'usage':{'prompt_tokens':4,'completion_tokens':3,'total_tokens':7}}
        self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(json.dumps(value).encode())

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--tesseract',required=True);parser.add_argument('--whisper',required=True);parser.add_argument('--whisper-model',required=True);parser.add_argument('--whisper-model-sha256',required=True);parser.add_argument('--wav',required=True);args=parser.parse_args()
    image=(ROOT/'backend/pkg/aisecurity/testdata/media-email.png').read_bytes();wav=Path(args.wav).read_bytes()
    Provider.image=base64.b64encode(image).decode();Provider.audio=base64.b64encode(wav).decode()
    bf_port,gateway_port,provider_port=port(),port(),port();name='scarlet-media-'+uuid.uuid4().hex[:10]
    provider=http.server.ThreadingHTTPServer(('0.0.0.0',provider_port),Provider);threading.Thread(target=provider.serve_forever,daemon=True).start()
    process=None;results=[]
    with tempfile.TemporaryDirectory(prefix='scarlet-media-e2e-') as directory:
        tmp=Path(directory);data=tmp/'bifrost';data.mkdir(mode=0o700)
        profile={'provider':'openai','region':'CN','endpointRef':'','baseUrl':f'http://host.docker.internal:{provider_port}/v1','apiKeyEnv':'SCARLET_GATEWAY_PROVIDER_KEY','models':['approved-model'],'port':bf_port}
        write(tmp/'profile.json',profile)
        helper=tmp/'config.go';helper.write_text('package main\nimport("os";"encoding/json";"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity")\nfunc main(){raw,_:=os.ReadFile(os.Args[1]);var p aisecurity.BifrostProfile;json.Unmarshal(raw,&p);out,err:=aisecurity.BifrostConfig(p,true);if err!=nil{panic(err)};os.Stdout.Write(out)}')
        (data/'config.json').write_bytes(subprocess.check_output(['go','-C',str(ROOT/'backend'),'run',str(helper),str(tmp/'profile.json')]));(data/'config.json').chmod(0o600)
        allowed=['image','audio','email','unclassified_media']
        catalog={'version':'media-e2e-v1','applications':[{'id':'app','name':'app','enabled':True,'principalIds':['alice'],'documentIds':[],'modelIds':['enterprise'],'toolIds':[]}],'principals':[{'id':'alice','name':'alice','enabled':True,'applicationIds':['app']}],'documents':[],'models':[{'id':'enterprise','name':'media','enabled':True,'provider':'openai','endpointRef':'model','region':'CN','upstreamModel':'approved-model','responseFormat':'openai_chat','allowedApplicationIds':['app'],'allowedInputCategories':list(allowed),'allowedOutputCategories':list(allowed),'media':{'allowedMimeTypes':['image/png','audio/wav'],'maxAssetBytes':1048576,'maxTotalBytes':2097152,'maxPixels':2000000,'inspectionVersion':'scarlet-media-ocr-asr-v2'}}],'tools':[],'approvals':[],'policy':{'version':'media-policy-v1','maxRequestAgeSeconds':300,'maxInputBytes':1048576,'maxOutputBytes':1048576,'allowedInputCategories':list(allowed),'allowedOutputCategories':list(allowed),'allowedModelRegions':['CN'],'blockedInputPatterns':[],'blockedOutputPatterns':[]}}
        write(tmp/'catalog.json',catalog);write(tmp/'bindings.json',{'credentials':[{'tokenHash':hashlib.sha256(TOKEN.encode()).hexdigest(),'applicationId':'app','principalId':'alice'}],'targets':{'model':{'url':f'http://127.0.0.1:{bf_port}/openai/v1/chat/completions'}},'tools':[]})
        subprocess.run(['go','-C',str(ROOT/'backend'),'build','-o',str(tmp/'ai-gateway'),'./cmd/ai-gateway'],check=True)
        log=(tmp/'gateway.log').open('w')
        try:
            subprocess.run(['docker','run','-d','--name',name,'--user',f'{os.getuid()}:{os.getgid()}','-p',f'127.0.0.1:{bf_port}:8080','-v',f'{data}:/app/data:rw','-e','SCARLET_GATEWAY_PROVIDER_KEY='+PROVIDER_TOKEN,'-e','HTTP_PROXY=','-e','HTTPS_PROXY=','-e','ALL_PROXY=','-e','NO_PROXY=*','--entrypoint','/app/main',IMAGE,'-host','0.0.0.0','-port','8080','-app-dir','/app/data'],check=True,stdout=subprocess.DEVNULL);wait_http(bf_port)
            process=subprocess.Popen([str(tmp/'ai-gateway'),'-local','-listen',f'127.0.0.1:{gateway_port}','-catalog',str(tmp/'catalog.json'),'-bindings',str(tmp/'bindings.json'),'-bifrost-profile',str(tmp/'profile.json'),'-audit',str(tmp/'audit.jsonl'),'-journal',str(tmp/'journal.jsonl'),'-media-tesseract',args.tesseract,'-media-whisper',args.whisper,'-media-whisper-model',args.whisper_model,'-media-whisper-model-sha256',args.whisper_model_sha256],env=dict(os.environ,SCARLET_GATEWAY_PROVIDER_KEY=PROVIDER_TOKEN),stdout=log,stderr=log);wait_port(gateway_port,process)
            url=f'http://127.0.0.1:{gateway_port}/v1/chat/completions'
            text={'model':'enterprise','messages':[{'role':'user','content':'inspect synthetic material'}]}
            image_request=dict(text,messages=[{'role':'user','content':[{'type':'text','text':'inspect synthetic image'},{'type':'image_url','image_url':{'url':'data:image/png;base64,'+Provider.image}}]}])
            audio_request=dict(text,messages=[{'role':'user','content':[{'type':'text','text':'inspect synthetic audio'},{'type':'input_audio','input_audio':{'format':'wav','data':Provider.audio}}]}])
            def check(label,body,statuses,delta,endpoint=None):
                before=Provider.hits;status,out=request(url if endpoint is None else endpoint,body);assert status in statuses,(label,status,out[:300]);assert Provider.hits-before==delta,(label,Provider.hits-before);results.append({'case':label,'status':status,'providerCalls':delta});return out
            check('real-ocr-approved-png-through-bifrost',image_request,{200},1);assert Provider.seen['image']==1
            voice_payload={'messages':image_request['messages'],'modalities':['text','audio'],'audio':{'voice':'13800138000','format':'wav'}}
            now=dt.datetime.now(dt.timezone.utc)
            wire={'requestId':uuid.uuid4().hex,'kind':'model','modelId':'enterprise','input':json.dumps(voice_payload),'issuedAt':now.isoformat().replace('+00:00','Z'),'expiresAt':(now+dt.timedelta(seconds=60)).isoformat().replace('+00:00','Z')}
            check('media-audio-voice-pii-projection-blocked',wire,{403},0,endpoint=f'http://127.0.0.1:{gateway_port}/v1/execute')
            check('facade-media-audio-voice-pii-blocked',dict(voice_payload,model='enterprise'),{403},0)
            check('real-asr-wav-through-bifrost',audio_request,{200},1);assert Provider.seen['audio']==1
            for scope in (catalog['policy'],catalog['models'][0]):
                for key in ('allowedInputCategories','allowedOutputCategories'):scope[key]=[x for x in scope[key] if x!='unclassified_media']
            write(tmp/'catalog.json',catalog)
            check('asr-lowest-token-confidence-not-averaged-away',audio_request,{403},0)
            for scope in (catalog['policy'],catalog['models'][0]):
                for key in ('allowedInputCategories','allowedOutputCategories'):scope[key]=list(allowed)
            write(tmp/'catalog.json',catalog)
            Provider.mode='image';out=check('provider-png-output-checked-standard-content',text,{200},1);message=json.loads(out)['choices'][0]['message'];assert isinstance(message['content'],list) and any(p['type']=='image_url' for p in message['content'])
            Provider.mode='audio';audio_output_request=dict(text,modalities=['text','audio'],audio={'voice':'alloy','format':'wav'});out=check('provider-wav-output-checked-standard-audio',audio_output_request,{200},1);message=json.loads(out)['choices'][0]['message'];assert message.get('audio',{}).get('data') and base64.b64decode(message['audio']['data'])[:4]==b'RIFF'
            for mode in ('empty-null','empty-array'):
                Provider.mode=mode
                check('provider-'+mode+'-output-rejected',text,{422},1)
            for scope in (catalog['policy'],catalog['models'][0]):
                for key in ('allowedInputCategories','allowedOutputCategories'):scope[key]=[x for x in scope[key] if x!='email']
            write(tmp/'catalog.json',catalog)
            Provider.mode='text';check('ocr-pii-input-blocked-before-bifrost',image_request,{403},0)
            Provider.mode='image';check('ocr-pii-provider-output-suppressed',text,{403},1)
            Provider.mode='text';catalog['policy']['blockedInputPatterns']=['(?i)confidential'];write(tmp/'catalog.json',catalog);check('asr-projection-input-pattern-before-bifrost',audio_request,{403},0)
            rows=[json.loads(line) for line in (tmp/'audit.jsonl').read_text().splitlines()];assert any(row['decision'].get('mediaInspectionVersion')=='scarlet-media-ocr-asr-v2' for row in rows);assert all(Provider.image not in json.dumps(row) and Provider.audio not in json.dumps(row) for row in rows)
            print(json.dumps({'image':IMAGE,'tests':results,'mediaSeenByRealProvider':Provider.seen,'auditEvents':len(rows)},indent=2))
        except Exception:
            log.flush();print('gateway metadata log', (tmp/'gateway.log').read_text()[-3000:])
            if (tmp/'audit.jsonl').exists():
                rows=[json.loads(x) for x in (tmp/'audit.jsonl').read_text().splitlines()];print('runtime metadata',json.dumps([{'stage':r['stage'],'code':r['decision']['code'],'categories':r['decision']['categories']} for r in rows[-9:]]))
            raise
        finally:
            if process is not None:
                process.terminate()
                try:process.wait(timeout=20)
                except subprocess.TimeoutExpired:process.kill();process.wait()
            log.close();subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);provider.shutdown()
if __name__=='__main__':main()
