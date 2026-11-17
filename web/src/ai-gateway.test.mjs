import test from 'node:test'
import { gatewayCredentialQuery } from './ai-gateway.ts'

test('credential history keeps exact state/app filters and opaque cursor without exposing secrets',()=>{
  const cursor='signed-workspace+state%3Dexpired/app-a='
  const query=new URLSearchParams(gatewayCredentialQuery({state:'expired',applicationId:'app-a'},cursor))
  assert.equal(query.get('state'),'expired')
  assert.equal(query.get('applicationId'),'app-a')
  assert.equal(query.get('before'),cursor)
  assert.equal(query.has('token'),false)
  assert.equal(new URLSearchParams(gatewayCredentialQuery({state:'',applicationId:''})).has('before'),false)
  for(const state of ['enabled','revoked','*'])assert.throws(()=>gatewayCredentialQuery({state,applicationId:''}))
  assert.throws(()=>gatewayCredentialQuery({state:'active',applicationId:'app/*'}))
})
import assert from 'node:assert/strict'
import {gatewayIdentityDraft,gatewayIdentityPayload,gatewayPublicURLs,gatewayMicros,gatewayDollars,gatewayConfigurationPayload,gatewayAuditSummary,gatewayApplicationDraft,gatewayProviderDraft} from './ai-gateway.ts'

const stored={revision:4,enabled:true,issuer:'https://idp.example/tenant',clientId:'client-a',clientSecretRef:'env:SCARLET_IDP_CLIENT_SECRET',scimEnabled:true,hasScimToken:true,defaultRole:'viewer',groupRoles:{team:'operator'},updatedAt:'2026-11-15T00:00:00Z'}
test('identity edit and save whitelist fields and preserve the original CAS revision',()=>{
  const draft=gatewayIdentityDraft({...stored,clientSecret:'must-never-copy',scimToken:'must-never-copy'})
  const result=gatewayIdentityPayload({...draft,hasScimToken:true,clientSecret:'must-never-copy'},[{externalId:'team',role:'operator'}])
  assert.equal(result.expectedRevision,4)
  assert.deepEqual(result.groupRoles,{team:'operator'})
  for(const key of ['revision','hasScimToken','clientSecret','scimToken','updatedAt'])assert.equal(Object.hasOwn(result,key),false)
  assert.equal(result.clientSecretRef,'env:SCARLET_IDP_CLIENT_SECRET')
})
test('identity input cannot grant owner or silently overwrite a group mapping',()=>{
  const draft=gatewayIdentityDraft(stored)
  assert.throws(()=>gatewayIdentityPayload({...draft,defaultRole:'owner'},[]))
  assert.throws(()=>gatewayIdentityPayload(draft,[{externalId:'team',role:'owner'}]))
  assert.throws(()=>gatewayIdentityPayload(draft,[{externalId:'team',role:'viewer'},{externalId:' team ',role:'operator'}]))
  const result=gatewayIdentityPayload(draft,[{externalId:'__proto__',role:'viewer'}])
  assert.equal(Object.hasOwn(result.groupRoles,'__proto__'),true)
  assert.equal({}.polluted,undefined)
})
test('OIDC secrets must be references and issuer cannot embed credentials or alternate redirect data',()=>{
  const draft=gatewayIdentityDraft(stored)
  for(const issuer of ['http://idp.example','https://user:password@idp.example','https://idp.example?redirect=https://other.example','https://idp.example#other'])assert.throws(()=>gatewayIdentityPayload({...draft,issuer},[]))
  for(const clientSecretRef of ['actual-secret','env:OTHER_SECRET','env:SCARLET_IDP_'])assert.throws(()=>gatewayIdentityPayload({...draft,clientSecretRef},[]))
  assert.doesNotThrow(()=>gatewayIdentityPayload({...draft,enabled:false,scimEnabled:false,issuer:'',clientId:'',clientSecretRef:''},[]))
  assert.throws(()=>gatewayIdentityPayload({...draft,enabled:false,scimEnabled:true,issuer:'',clientId:'',clientSecretRef:''},[]))
})
test('enterprise login and SCIM addresses stay on the current origin and encode workspace scope',()=>{
  const addresses=gatewayPublicURLs('https://console.example/','customer/a')
  assert.equal(addresses.login,'https://console.example/api/v1/identity/customer%2Fa/oidc/start')
  assert.equal(addresses.callback,'https://console.example/api/v1/identity/customer%2Fa/oidc/callback')
  assert.equal(addresses.scim,'https://console.example/scim/v2/customer%2Fa')
})
const app={id:'app-a',name:'Application A',enabled:true,allowRag:true,principalId:'principal-a',modelIdsText:'model-a',toolIdsText:'tool-a',budgetUSD:'12.345678',requestsPerMinute:120,tokensPerMinute:10000}
const provider={id:'route-a',name:'Route A',provider:'openai',modelId:'model-a',upstreamModel:'approved',region:'private-a',responseFormat:'openai_chat',endpointRef:'env:SCARLET_GATEWAY_ENDPOINT',secretRef:'env:SCARLET_GATEWAY_KEY',enabled:true,inputUSDPerMillion:'1.000001',outputUSDPerMillion:'2'}
const digest='a'.repeat(64)
test('budget amounts convert exact decimal USD to microdollars without rounding or scientific notation',()=>{
  for(const value of ['0','0.000001','12.345678','9000000000'])assert.equal(gatewayMicros(gatewayDollars(gatewayMicros(value))),gatewayMicros(value))
  assert.equal(gatewayMicros('12.345678'),12345678)
  assert.equal(gatewayDollars(1000001),'1.000001')
  for(const value of ['','-1','1e6','NaN','1.0000001','9007199254.740992'])assert.throws(()=>gatewayMicros(value))
})
test('governance save retains the original revision, catalog pin and full binding while omitting unexpected secrets',()=>{
  const result=gatewayConfigurationPayload(7,true,[{...app,token:'never-send'}],[{...provider,apiKey:'never-send'}],digest)
  assert.equal(result.expectedRevision,7)
  assert.equal(result.catalogDigest,digest)
  assert.equal(result.applications[0].allowRag,true)
  assert.deepEqual(result.applications[0].modelIds,['model-a'])
  assert.equal(result.providers[0].region,'private-a')
  assert.equal(result.providers[0].responseFormat,'openai_chat')
  assert.equal(JSON.stringify(result).includes('never-send'),false)
  assert.equal(Object.hasOwn(result,'revision'),false)
})
test('zero budgets and request or token rates remain explicit rather than silently omitted',()=>{
  const result=gatewayConfigurationPayload(7,true,[{...app,budgetUSD:'0',requestsPerMinute:0,tokensPerMinute:0}],[provider],digest)
  assert.equal(result.applications[0].budgetMicros,0)
  assert.equal(result.applications[0].requestsPerMinute,0)
  assert.equal(result.applications[0].tokensPerMinute,0)
})
test('requiring trusted task scope remains explicit in application configuration and draft round trips',()=>{
  const value=gatewayConfigurationPayload(7,true,[{...app,requireTask:true}],[provider],digest)
  assert.equal(value.applications[0].requireTask,true)
  assert.equal(gatewayApplicationDraft(value.applications[0]).requireTask,true)
  assert.equal(gatewayConfigurationPayload(7,true,[{...app,requireTask:false}],[provider],digest).applications[0].requireTask,false)
  assert.throws(()=>gatewayConfigurationPayload(7,true,[{...app,requireTask:'true'}],[provider],digest))
})
test('a governed model has one enabled provider and explicit application scopes',()=>{
  assert.throws(()=>gatewayConfigurationPayload(7,true,[app],[provider,{...provider,id:'route-b'}],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[{...app,modelIdsText:'model-other'}],[provider],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[{...app,modelIdsText:'*'}],[provider],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[app,{...app}],[provider],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[{...app,requestsPerMinute:0.5}],[provider],digest))
})
test('gateway configuration requires a real catalog digest, strict output profile and bounded environment references',()=>{
  for(const catalogDigest of ['', 'B'.repeat(64),'a'.repeat(63)])assert.throws(()=>gatewayConfigurationPayload(7,true,[app],[provider],catalogDigest))
  for(const override of [{secretRef:'actual-secret'},{secretRef:'env:OTHER_KEY'},{endpointRef:'https://untrusted.example'},{responseFormat:'json'},{region:''}])assert.throws(()=>gatewayConfigurationPayload(7,true,[app],[{...provider,...override}],digest))
})
test('runtime authorization IDs stay within the engine contract while provider and upstream names may contain slash',()=>{
  for(const override of [{id:'app/slash'},{principalId:'user/slash'},{id:'x'.repeat(81)},{modelIdsText:'model/slash'},{toolIdsText:'tool/slash'}])assert.throws(()=>gatewayConfigurationPayload(7,true,[{...app,...override}],[provider],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[app],[{...provider,modelId:'model/slash'}],digest))
  const value=gatewayConfigurationPayload(7,true,[app],[{...provider,id:'provider/route',upstreamModel:'organization/model'}],digest)
  assert.equal(value.providers[0].id,'provider/route')
  assert.equal(value.providers[0].upstreamModel,'organization/model')
})
test('loading and editing amounts preserves exact saved units and authorized route data',()=>{
  const saved=gatewayConfigurationPayload(7,true,[app],[provider],digest)
  const edited=gatewayConfigurationPayload(7,true,saved.applications.map(gatewayApplicationDraft),saved.providers.map(gatewayProviderDraft),digest)
  assert.deepEqual(edited,saved)
})
test('audit display includes whitelisted decision evidence and never renders arbitrary payload secrets',()=>{
  const summary=gatewayAuditSummary({payload:{applicationId:'app-a',requestDigest:'digest-a',secret:'do-not-render',token:'do-not-render',rawBody:{password:'do-not-render'}}})
  assert.match(summary,/app-a/)
  assert.match(summary,/digest-a/)
  assert.equal(summary.includes('do-not-render'),false)
})

const cachePolicy={enabled:true,ttlSeconds:600,maxEntries:250,maxBytes:1048576,keyRef:'env:SCARLET_GATEWAY_CACHE_KEY',keyVersion:'v1'}
const backup={...provider,id:'route-b',provider:'other-provider',modelId:'model-b',region:'private-b',fallbackModelIds:[]}
test('cache policy saves only bounded encrypted application settings and preserves exact revision',()=>{
  const saved=gatewayConfigurationPayload(19,true,[{...app,cachePolicy:{...cachePolicy,encryptionKey:'never-send',runtimeReady:true}}],[provider],digest)
  assert.equal(saved.expectedRevision,19)
  assert.deepEqual(saved.applications[0].cachePolicy,cachePolicy)
  assert.equal(JSON.stringify(saved).includes('never-send'),false)
  assert.equal(Object.hasOwn(saved.applications[0].cachePolicy,'runtimeReady'),false)
  for(const revision of [-1,0.5,NaN,Infinity,Number.MAX_SAFE_INTEGER+1])assert.throws(()=>gatewayConfigurationPayload(revision,true,[app],[provider],digest))
  for(const override of [{ttlSeconds:0},{ttlSeconds:86401},{ttlSeconds:1.5},{maxEntries:10001},{maxBytes:16777217},{keyRef:'plaintext'},{keyRef:'env:OTHER_CACHE_KEY'},{keyVersion:''}])assert.throws(()=>gatewayConfigurationPayload(19,true,[{...app,cachePolicy:{...cachePolicy,...override}}],[provider],digest))
})
test('cache defaults stay disabled and disabled saved settings survive editing without claiming readiness',()=>{
  const saved=gatewayConfigurationPayload(2,true,[app],[provider],digest)
  assert.equal(saved.applications[0].cachePolicy.enabled,false)
  const disabled={...cachePolicy,enabled:false}
  const configured=gatewayConfigurationPayload(2,true,[{...app,cachePolicy:disabled}],[provider],digest)
  const replay=gatewayConfigurationPayload(2,true,configured.applications.map(gatewayApplicationDraft),configured.providers.map(gatewayProviderDraft),digest)
  assert.deepEqual(replay,configured)
})
test('ordered fallback destinations require independent routes and explicit application authorization',()=>{
  const fallback={...provider,fallbackModelIds:['model-b']}
  const scopedApp={...app,modelIdsText:'model-a,model-b'}
  const saved=gatewayConfigurationPayload(7,true,[scopedApp],[fallback,backup],digest)
  assert.deepEqual(saved.providers[0].fallbackModelIds,['model-b'])
  assert.equal(saved.providers[1].provider,'other-provider')
  assert.throws(()=>gatewayConfigurationPayload(7,true,[app],[fallback,backup],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[scopedApp],[fallback,{...backup,enabled:false}],digest))
  for(const fallbackModelIds of [['model-a'],['model-b','model-b'],['*'],['absent'],['a','b','c','d']])assert.throws(()=>gatewayConfigurationPayload(7,true,[scopedApp],[{...provider,fallbackModelIds},backup],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[scopedApp],[fallback,{...backup,fallbackModelIds:['model-a']}],digest))
})
test('fallback ordering round trips without copying arbitrary route fields',()=>{
  const third={...backup,id:'route-c',modelId:'model-c'}
  const first={...provider,fallbackModelIds:['model-c','model-b'],endpointURL:'never-send',skipApproval:true}
  const saved=gatewayConfigurationPayload(7,true,[{...app,modelIdsText:'model-a,model-b,model-c'}],[first,backup,third],digest)
  assert.deepEqual(saved.providers[0].fallbackModelIds,['model-c','model-b'])
  assert.equal(JSON.stringify(saved).includes('never-send'),false)
  assert.equal(Object.hasOwn(saved.providers[0],'skipApproval'),false)
  const replay=gatewayConfigurationPayload(7,true,saved.applications.map(gatewayApplicationDraft),saved.providers.map(gatewayProviderDraft),digest)
  assert.deepEqual(replay,saved)
})
test('attempt and cache audit summaries expose only decision metadata',()=>{
  const summary=gatewayAuditSummary({payload:{providerId:'route-b',parentRequestId:'parent-1',rootModelId:'model-a',attempt:1,reservationId:'reservation-1',cacheHit:true,rawPrompt:'never-render',encryptedBody:'never-render'}})
  for(const value of ['route-b','parent-1','model-a','attempt: 1','reservation-1','cacheHit: true'])assert.match(summary,new RegExp(value))
  assert.equal(summary.includes('never-render'),false)
})
test('task audit summaries show trusted scope bindings without rendering unrecognized source or prompt payloads',()=>{
  const summary=gatewayAuditSummary({payload:{taskId:'task-a',rootTaskId:'root-a',scopeDigest:'a'.repeat(64),audit:{decision:{taskId:'task-a',taskPurpose:'support-answer',taskScopeDigest:'b'.repeat(64),sourceBody:'never-render'},execution:{outcome:'succeeded',rawOutput:'never-render'}}}})
  for(const value of ['task-a','root-a','support-answer','a'.repeat(64),'b'.repeat(64)])assert.equal(summary.includes(value),true)
  assert.equal(summary.includes('never-render'),false)
})

test('settlement audit summary includes only whitelisted nested execution and quote evidence',()=>{
  const summary=gatewayAuditSummary({payload:{applicationId:'app-a',chargedQuoteMicros:200,audit:{requestId:'request-a',stage:'execution',input:'never-render',execution:{outcome:'failed',httpStatus:503,outputReleased:false,output:'never-render'},decision:{outcome:'allow',code:'model_allowed',media:'never-render'}}}})
  for(const value of ['chargedQuoteMicros: 200','request-a','httpStatus: 503','outputReleased: false','model_allowed'])assert.equal(summary.includes(value),true)
  assert.equal(summary.includes('never-render'),false)
})

test('fallback chains remain bounded even when every destination is explicitly authorized',()=>{
  const make=(index,last)=>({...provider,id:'route-'+index,modelId:'model-'+index,fallbackModelIds:index<last?['model-'+(index+1)]:[]})
  assert.doesNotThrow(()=>gatewayConfigurationPayload(7,true,[{...app,modelIdsText:'model-0,model-1,model-2,model-3'}],Array.from({length:4},(_,index)=>make(index,3)),digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[{...app,modelIdsText:'model-0,model-1,model-2,model-3,model-4'}],Array.from({length:5},(_,index)=>make(index,4)),digest))
})

test('front-end configuration bounds match the control-plane catalog and provider identifiers',()=>{
  assert.throws(()=>gatewayConfigurationPayload(7,true,[{...app,toolIdsText:Array.from({length:201},(_,i)=>'tool-'+i).join(',')}],[provider],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,[app],[{...provider,provider:'provider/untrusted'}],digest))
  assert.throws(()=>gatewayConfigurationPayload(7,true,Array.from({length:201},(_,i)=>({...app,id:'app-'+i})),[provider],digest))
})

test('lost credential responses require original-app metadata and never interpret a missing known key as revoked',async()=>{
 const {gatewayCredentialCanPrepare}=await import('./ai-gateway.ts')
 const recovery={applicationId:'app-a',read:true,issuedId:12},filters={applicationId:'app-a',state:''}
 assert.equal(gatewayCredentialCanPrepare({...recovery,read:false},{items:[]},filters),false)
 assert.equal(gatewayCredentialCanPrepare(recovery,{items:[]},filters),false)
 assert.equal(gatewayCredentialCanPrepare(recovery,{items:[{id:12,applicationId:'app-a',enabled:true}]},filters),false)
 assert.equal(gatewayCredentialCanPrepare(recovery,{items:[{id:12,applicationId:'app-a',enabled:false}]},filters),true)
 assert.equal(gatewayCredentialCanPrepare({applicationId:'app-a',read:true},{items:[]},{applicationId:'other',state:''}),false)
 assert.equal(gatewayCredentialCanPrepare({applicationId:'app-a',read:true},{items:[]},{applicationId:'app-a',state:'expired'}),false)
})
