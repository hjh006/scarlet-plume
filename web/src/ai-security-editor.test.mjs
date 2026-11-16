import test from 'node:test'
import assert from 'node:assert/strict'
import { aiConfigHasDuplicateIds, aiFieldValue, aiList, aiRecordDraft, buildAIRequest, buildAIWireRequest, validateAIModelPayload, validateAIToolProtocol, validateAIToolRequest, validateAIAuthorizationExpiry, validateAIConfigurationExtensions, aiMediaDraft, validateAIMedia, aiMaxModelWireBytes, validateAIDataUse, validateAIDataHandling } from './ai-security-editor.ts'

test('explicit ACL lists preserve identifiers and never infer wildcard permissions',()=>{
  assert.deepEqual(aiList('sales-a, sales-b，sales-a\nfinance'),['sales-a','sales-b','finance'])
  assert.deepEqual(aiList('  '),[])
  assert.deepEqual(aiFieldValue({key:'subjects',label:'主体',type:'list'},''),[])
})
test('new records start with explicit empty scopes and disabled toggles',()=>{
  assert.deepEqual(aiRecordDraft({key:'documents',label:'资料',description:'',fields:[{key:'id',label:'标识'},{key:'subjects',label:'ACL',type:'list'},{key:'active',label:'启用',type:'checkbox'}]}),{id:'',subjects:[],active:false})
})
test('optional extension fields stay absent until supplied, and clearing a lease sends null',()=>{
  const fields=[{key:'id',label:'标识'},{key:'authorizationExpiresAt',label:'租约',type:'rfc3339',optional:true},{key:'definitionDigests',label:'定义摘要',type:'json',optional:true},{key:'requireAuthorizationLeases',label:'强制租约',type:'checkbox',optional:true}]
  assert.deepEqual(aiRecordDraft({key:'principals',label:'主体',description:'',fields}),{id:''})
  assert.equal(aiFieldValue(fields[1],''),null)
  assert.equal(aiFieldValue(fields[1],' 2026-11-30T23:59:59+08:00 '),'2026-11-30T23:59:59+08:00')
})
test('authorization leases require real RFC3339 dates and accept expired leases for deny verification',()=>{
  for(const value of [undefined,null,'2026-11-30T23:59:59+08:00','2024-02-29T01:02:03.123456789Z','2020-01-01T00:00:00Z']) assert.doesNotThrow(()=>validateAIAuthorizationExpiry(value))
  for(const value of ['',123,'2026-11-30T23:59','2026-02-29T00:00:00Z','2026-11-31T00:00:00Z','2026-11-30T24:00:00Z','2026-11-30T23:59:59+25:00','0001-01-01T00:00:00Z','0001-01-01T01:00:00+01:00']) assert.throws(()=>validateAIAuthorizationExpiry(value))
})
test('parameter constraints require JSON objects',()=>{
  assert.deepEqual(aiFieldValue({key:'params',label:'参数',type:'json'},'{"orderId":"A1"}'),{orderId:'A1'})
  for(const value of ['null','[]','"allow-all"']) assert.throws(()=>aiFieldValue({key:'params',label:'参数',type:'json'},value))
})
test('regex and exact targets preserve commas and significant spaces',()=>{
  assert.deepEqual(aiFieldValue({key:'patterns',label:'规则',type:'lines'},'[A-Z]{2,4}\nkey,value\n  exact target  '),['[A-Z]{2,4}','key,value','  exact target  '])
  assert.deepEqual(aiRecordDraft({key:'policy',label:'策略',description:'',fields:[{key:'patterns',label:'规则',type:'lines'}]}),{patterns:[]})
})
test('configuration validation reports duplicate identity records before saving',()=>{
  const sections=[{key:'principals',label:'主体',description:'',fields:[]}]
  assert.deepEqual(aiConfigHasDuplicateIds({principals:[{id:'employee-a'},{id:'employee-a'}]},sections),['主体存在重复标识：employee-a'])
  assert.deepEqual(aiConfigHasDuplicateIds({principals:[{id:'employee-a'},{id:'employee-b'}]},sections),[])
})

const draft={requestId:'request-fixed-00001',kind:'tool',applicationId:'app-a',principalId:'principal-a',documentIds:['doc-a'],modelId:'model-a',toolId:'tool-a',action:'read',target:'orders',argumentsJSON:'{"orderId":"A1"}',input:'synthetic-input',output:'synthetic-output',approvalId:'approval-a',issuedAt:'2026-11-14T01:00:00Z',expiresAt:'2026-11-14T01:05:00Z'}
test('preview request carries only the engine contract and preserves approval-bound request references',()=>{
  const actual=buildAIRequest({...draft,categories:['fake-public'],allowedPrincipalIds:['*']})
  assert.deepEqual(actual,{requestId:'request-fixed-00001',kind:'tool',applicationId:'app-a',principalId:'principal-a',toolId:'tool-a',action:'read',target:'orders',arguments:{orderId:'A1'},approvalId:'approval-a',issuedAt:'2026-11-14T01:00:00Z',expiresAt:'2026-11-14T01:05:00Z'})
  assert.deepEqual(buildAIRequest(draft),actual)
})
test('tool preview arguments accept string values and reject nested or typed values',()=>{
  for(const argumentsJSON of ['[]','null','{"amount":100}','{"all":true}','{"orderId":{"id":"A1"}}']) assert.throws(()=>buildAIRequest({...draft,argumentsJSON}))
})
test('exact tool targets preserve significant spaces and commas in runtime request templates',()=>{
  const target='  record,one  '
  assert.equal(buildAIRequest({...draft,target}).target,target)
  assert.equal(buildAIWireRequest({...draft,target}).target,target)
})
test('RAG and model previews retain the actual document references without caller-provided ACL metadata',()=>{
  const rag=buildAIRequest({...draft,kind:'rag'})
  assert.deepEqual(rag.documentIds,['doc-a'])
  assert.equal('input' in rag,false)
  const model=buildAIRequest({...draft,kind:'model'})
  assert.equal(model.modelId,'model-a')
  assert.equal(model.input,'synthetic-input')
  assert.equal(model.output,'synthetic-output')
  assert.equal('arguments' in model,false)
})
test('customer runtime template omits caller identity and synthetic output',()=>{
  const model=buildAIWireRequest({...draft,kind:'model'})
  assert.equal('applicationId' in model,false)
  assert.equal('principalId' in model,false)
  assert.equal('output' in model,false)
  assert.equal(model.modelId,'model-a')
  assert.equal(model.input,'synthetic-input')
})
test('task template carries only an explicit task selector, not self-reported purpose or source taint',()=>{
  const input={...draft,taskId:'task-synthetic',purpose:'forged',observedCategories:['public'],taskStepId:'self-asserted'}
  for(const value of [buildAIRequest(input),buildAIWireRequest(input)]){
    assert.equal(value.taskId,'task-synthetic')
    for(const key of ['purpose','observedCategories','taskStepId'])assert.equal(key in value,false)
  }
  assert.equal('taskId' in buildAIWireRequest({...draft,taskId:''}),false)
  assert.throws(()=>buildAIWireRequest({...draft,taskId:'task id with spaces'}))
})
test('model runtime template verifies the actual model and bounded text adapter',()=>{
  assert.doesNotThrow(()=>validateAIModelPayload('{"model":"approved","messages":[{"role":"user","content":"synthetic"}],"stream":false,"max_tokens":100}', 'approved'))
  assert.doesNotThrow(()=>validateAIModelPayload('{"prompt":"synthetic"}', 'approved'))
  for(const payload of ['{"model":"other","prompt":"synthetic"}','{"prompt":"synthetic","deployment":"other"}','{"model":"approved"}','{"prompt":"text","input":"other"}','{"messages":[{"role":"tool","content":"test"}]}','{"messages":[{"role":"user","content":[{"type":"image_url"}]}]}']) assert.throws(()=>validateAIModelPayload(payload,'approved'))
})
test('MCP tools require a reserved trusted target parameter without changing legacy JSON tools',()=>{
  for(const tool of [{},{protocol:''},{protocol:'json'},{protocol:'mcp_http',targetArgument:'resourceId',allowedArguments:{orderId:['A1']},requiredArguments:['orderId']}]) assert.doesNotThrow(()=>validateAIToolProtocol(tool))
  for(const tool of [{protocol:'mcp'},{protocol:'mcp_http'},{protocol:'mcp_http',targetArgument:'resource id'},{protocol:'mcp_http',targetArgument:'x'.repeat(81)},{protocol:'json',targetArgument:'resourceId'},{protocol:'mcp_http',targetArgument:'resourceId',allowedArguments:{resourceId:['other']}},{protocol:'mcp_http',targetArgument:'resourceId',requiredArguments:['resourceId']}]) assert.throws(()=>validateAIToolProtocol(tool))
})
test('MCP runtime templates reject target overrides and keep protocol mapping out of caller payloads',()=>{
  const tool={protocol:'mcp_http',targetArgument:'resourceId',allowedArguments:{orderId:['A1']}}
  const wire=buildAIWireRequest({...draft,protocol:'mcp_http',targetArgument:'callerResource',runtimeProtocolVersion:'forged',inspectionVersion:'forged',authorizationExpiresAt:'2099-01-01T00:00:00Z',responseFormat:'json',definitionDigests:{read:'a'.repeat(64)}})
  assert.doesNotThrow(()=>validateAIToolRequest(tool,wire.arguments))
  assert.throws(()=>validateAIToolRequest(tool,{...wire.arguments,resourceId:'other'}))
  assert.doesNotThrow(()=>validateAIToolRequest({protocol:'json'},{resourceId:'ordinary-parameter'}))
  assert.equal('protocol' in wire,false)
  assert.equal('targetArgument' in wire,false)
  for(const key of ['runtimeProtocolVersion','inspectionVersion','authorizationExpiresAt','responseFormat','definitionDigests']) assert.equal(key in wire,false)
  assert.deepEqual(wire.arguments,{orderId:'A1'})
  assert.equal(wire.target,'orders')
})
test('MCP definition pins cover exactly the allowed actions and use the fixed protocol version',()=>{
  const tool={protocol:'mcp_http',targetArgument:'resourceId',actions:['read','update']}
  for(const definitions of [undefined,null,{}, {read:'a'.repeat(64),update:'b'.repeat(64)}]) assert.doesNotThrow(()=>validateAIToolProtocol({...tool,protocolVersion:'2026-07-28',definitionDigests:definitions}))
  for(const definitions of [[],{read:'a'.repeat(64)},{read:'a'.repeat(64),other:'b'.repeat(64)},{read:'A'.repeat(64),update:'b'.repeat(64)},{read:10,update:'b'.repeat(64)}]) assert.throws(()=>validateAIToolProtocol({...tool,definitionDigests:definitions}))
  assert.throws(()=>validateAIToolProtocol({...tool,protocolVersion:'2025-03-26'}))
  assert.throws(()=>validateAIToolProtocol({protocol:'json',protocolVersion:'2026-07-28'}))
  assert.throws(()=>validateAIToolProtocol({protocol:'json',definitionDigests:{read:'a'.repeat(64)}}))
})
test('configuration extension validation preserves legacy JSON and supports strict response and version pins',()=>{
  const legacy={principals:[{id:'principal-a'}],documents:[{id:'doc-a'}],models:[{id:'model-a'}],tools:[{id:'tool-a'}],policy:{}}
  const serialized=JSON.stringify(legacy)
  validateAIConfigurationExtensions(legacy)
  assert.equal(JSON.stringify(legacy),serialized)
  const pinned={...legacy,principals:[{authorizationExpiresAt:'2020-01-01T00:00:00Z'}],models:[{responseFormat:'openai_chat'}],policy:{requiredRuntimeProtocolVersion:'scarlet-ai-runtime-v2',requiredInspectionVersion:'datasafety-four-format-v1',requireAuthorizationLeases:true}}
  assert.doesNotThrow(()=>validateAIConfigurationExtensions(pinned))
  for(const configuration of [{...pinned,models:[{responseFormat:'arbitrary'}]},{...pinned,policy:{requiredRuntimeProtocolVersion:'forged'}},{...pinned,policy:{requiredInspectionVersion:'forged'}},{...pinned,policy:{requireAuthorizationLeases:'true'}},{...pinned,documents:[{authorizationExpiresAt:'no-time-zone'}]}]) assert.throws(()=>validateAIConfigurationExtensions(configuration))
})

const media={allowedMimeTypes:['image/png','image/jpeg','audio/wav'],maxAssetBytes:4194304,maxTotalBytes:8388608,maxPixels:16000000,inspectionVersion:'scarlet-media-ocr-asr-v1'}
test('media configuration keeps legacy text scope optional and limits real supported scan formats',()=>{
  for(const value of [undefined,null,media])assert.doesNotThrow(()=>validateAIMedia(value))
  for(const override of [{allowedMimeTypes:[]},{allowedMimeTypes:['video/mp4']},{allowedMimeTypes:['image/png','image/png']},{maxAssetBytes:4194305},{maxTotalBytes:8388609},{maxPixels:16000001},{maxPixels:1.5},{inspectionVersion:'ready-because-enabled'},{scannerReady:true}])assert.throws(()=>validateAIMedia({...media,...override}))
  assert.throws(()=>validateAIConfigurationExtensions({models:[{media:{...media,maxAssetBytes:0}}]}))
  assert.doesNotThrow(()=>validateAIConfigurationExtensions({models:[{media}],policy:{allowedInputCategories:['image','audio','unclassified_media']}}))
})
test('media form drafts whitelist policy fields rather than copying scanner status or caller assertions',()=>{
  assert.deepEqual(aiMediaDraft({...media,scannerReady:true,rawImage:'never-copy',categories:['public']}),media)
  assert.deepEqual(aiRecordDraft({key:'models',label:'模型',description:'',fields:[{key:'id',label:'ID'},{key:'media',label:'媒体',type:'media',optional:true}]}),{id:''})
})

test('stream templates use strict hold protocol without allowing caller routing or projection assertions',()=>{
  assert.doesNotThrow(()=>validateAIModelPayload('{"prompt":"synthetic","stream":true,"stream_options":{"include_usage":true}}','approved'))
  for(const payload of [{prompt:'safe',stream:'true'},{prompt:'safe',stream:false,stream_options:{include_usage:true}},{prompt:'safe',stream:true,stream_options:{raw_chunks:true}},{prompt:'safe',projection:'trusted'},{prompt:'safe',scanResult:{complete:true}}])assert.throws(()=>validateAIModelPayload(JSON.stringify(payload),'approved'))
})
test('media templates require saved model scope and bounded inline data without accepting remote URLs',()=>{
  const image={messages:[{role:'user',content:[{type:'image_url',image_url:{url:'data:image/png;base64,AA=='}}]}]}
  const audio={messages:[{role:'user',content:[{type:'input_audio',input_audio:{format:'wav',data:'AA=='}}]}]}
  for(const payload of [image,audio])assert.doesNotThrow(()=>validateAIModelPayload(JSON.stringify(payload),'approved',media))
  assert.throws(()=>validateAIModelPayload(JSON.stringify(image),'approved'))
  assert.throws(()=>validateAIModelPayload(JSON.stringify(audio),'approved',{...media,allowedMimeTypes:['image/png']}))
  for(const part of [{type:'image_url',image_url:{url:'https://remote.invalid/image.png'}},{type:'image_url',image_url:{url:'data:image/png;base64,AA=='},projection:'safe'},{type:'input_audio',input_audio:{format:'mp3',data:'AA=='}},{type:'input_audio',input_audio:{format:'wav',data:'not-base64'}}])assert.throws(()=>validateAIModelPayload(JSON.stringify({messages:[{role:'user',content:[part]}]}),'approved',media))
  const oversize={...media,maxAssetBytes:1,maxTotalBytes:1}
  assert.throws(()=>validateAIModelPayload(JSON.stringify({messages:[{role:'user',content:[{type:'input_audio',input_audio:{format:'wav',data:'AAAA'}}]}]}),'approved',oversize))
  assert.throws(()=>validateAIMedia({...media,maxTotalBytes:1}))
})
test('audio output templates remain non-streaming and require explicit trusted WAV profile',()=>{
  const payload={prompt:'synthetic',modalities:['text','audio'],audio:{voice:'alloy',format:'wav'}}
  assert.doesNotThrow(()=>validateAIModelPayload(JSON.stringify(payload),'approved',media))
  for(const override of [{stream:true},{audio:{voice:'alloy',format:'mp3'}},{audio:{voice:'alloy',format:'wav',scanResult:'safe'}}])assert.throws(()=>validateAIModelPayload(JSON.stringify({...payload,...override}),'approved',media))
})

test('media wire sizes accept the 16 MiB engine boundary while rejecting larger or noninteger limits',()=>{
  assert.equal(aiMaxModelWireBytes,16777216)
  assert.doesNotThrow(()=>validateAIConfigurationExtensions({policy:{maxInputBytes:aiMaxModelWireBytes,maxOutputBytes:aiMaxModelWireBytes}}))
  for(const value of [0,-1,0.5,aiMaxModelWireBytes+1,NaN,Infinity,'16777216',null])for(const key of ['maxInputBytes','maxOutputBytes'])assert.throws(()=>validateAIConfigurationExtensions({policy:{[key]:value}}))
  assert.doesNotThrow(()=>validateAIConfigurationExtensions({policy:{maxInputBytes:1,maxOutputBytes:1048576},models:[{media}]}))
  assert.throws(()=>validateAIMedia({...media,maxAssetBytes:aiMaxModelWireBytes}))
  assert.throws(()=>validateAIMedia({...media,maxTotalBytes:aiMaxModelWireBytes}))
})

test('source use limits distinguish zero retention and empty purpose scope from absent legacy policy',()=>{
  const policy={allowedPurposes:['support-answer'],maxRetentionHours:0,allowTraining:false}
  for(const value of [undefined,null,policy,{...policy,allowedPurposes:[]}])assert.doesNotThrow(()=>validateAIDataUse(value))
  for(const override of [{allowedPurposes:['*']},{allowedPurposes:['support-answer','support-answer']},{maxRetentionHours:-1},{maxRetentionHours:8761},{maxRetentionHours:0.5},{allowTraining:'false'},{trustedBecauseClientSaysSo:true}])assert.throws(()=>validateAIDataUse({...policy,...override}))
  assert.equal(aiFieldValue({key:'usePolicy',label:'用途',type:'json',optional:true},''),null)
  assert.equal(aiFieldValue({key:'handling',label:'约定',type:'json',optional:true},'null'),null)
})
test('destination handling requires bounded approved declarations and real expiry without accepting client evidence assertions',()=>{
  const handling={retentionHours:0,trainingUse:'prohibited',evidenceRef:'contract-v1',authorizationExpiresAt:'2020-01-01T00:00:00Z'}
  for(const value of [undefined,null,handling])assert.doesNotThrow(()=>validateAIDataHandling(value))
  for(const override of [{retentionHours:8761},{retentionHours:0.5},{trainingUse:'unknown'},{evidenceRef:''},{authorizationExpiresAt:undefined},{authorizationExpiresAt:'2026-02-29T00:00:00Z'},{independentlyVerified:true}])assert.throws(()=>validateAIDataHandling({...handling,...override}))
  assert.doesNotThrow(()=>validateAIConfigurationExtensions({documents:[{usePolicy:{allowedPurposes:[],maxRetentionHours:0,allowTraining:false}}],models:[{handling}],tools:[{handling}],applications:[{requireTask:true}],policy:{requirePinnedSourceContent:true,requireDataHandling:true}}))
  for(const key of ['requirePinnedSourceContent','requireDataHandling'])assert.throws(()=>validateAIConfigurationExtensions({policy:{[key]:'true'}}))
  assert.throws(()=>validateAIConfigurationExtensions({applications:[{requireTask:'true'}]}))
})
