import test from 'node:test'
import assert from 'node:assert/strict'
import {taskDraft,taskCreatePayload,taskStatusPayload,taskAvailableBudget,taskHeaderTemplate} from './ai-gateway-tasks.ts'

const now=Date.parse('2026-11-16T08:00:00+08:00')
const config={revision:2,enabled:true,catalogDigest:'a'.repeat(64),applications:[{id:'app',name:'app',enabled:true,principalId:'alice',allowRag:true,modelIds:['model'],toolIds:['tool']}],providers:[]}
const credentials=[{id:1,applicationId:'app',enabled:true},{id:2,applicationId:'app',enabled:true},{id:3,applicationId:'other',enabled:true}]
const root={...taskDraft(undefined,now),applicationId:'app',credentialId:1,purpose:'support-answer',modelIds:['model'],toolIds:['tool'],toolScopes:[{toolId:'tool',actions:['read','close'],targets:['record,one','record-two']}],documentIds:['doc'],allowedCategories:['confidential'],budgetUSD:'1.000001',maxSteps:20,maxDelegationDepth:2}
const parent={...taskCreatePayload(root,config,credentials,undefined,now),id:'task-parent',rootTaskId:'task-parent',depth:0,status:'active',revision:4,scopeDigest:'b'.repeat(64),stepsUsed:3,reservedMicros:100000,spentMicros:100000,observedCategories:[],sources:[],createdAt:new Date(now).toISOString(),updatedAt:new Date(now).toISOString()}

test('task creation binds saved app identity/catalog and enabled credential while omitting self-reported source facts',()=>{
  const value=taskCreatePayload({...root,principalId:'forged',catalogDigest:'c'.repeat(64),sources:[{content:'never-send'}],observedCategories:['public']},config,credentials,undefined,now)
  assert.equal(value.principalId,'alice');assert.equal(value.catalogDigest,config.catalogDigest);assert.equal(value.budgetMicros,1000001)
  for(const key of ['sources','observedCategories','stepId','taskStepId'])assert.equal(key in value,false)
  for(const credentialId of [0,3,1.5])assert.throws(()=>taskCreatePayload({...root,credentialId},config,credentials,undefined,now))
  assert.throws(()=>taskCreatePayload(root,{...config,enabled:false},credentials,undefined,now))
})
test('task scopes and duration keep strict explicit bounds without expanding application authorization',()=>{
  for(const override of [{modelIds:['other']},{toolIds:['*']},{documentIds:['doc','doc']},{allowedCategories:['x'.repeat(81)]},{maxSteps:0},{maxSteps:1000001},{maxDelegationDepth:5},{maxDelegationDepth:0.5},{purpose:'Free form task body'},{expiresAt:new Date(now).toISOString()},{expiresAt:new Date(now+7*86400000+1).toISOString()},{budgetUSD:'9000000000.000001'}])assert.throws(()=>taskCreatePayload({...root,...override},config,credentials,undefined,now))
  assert.doesNotThrow(()=>taskCreatePayload({...root,budgetUSD:'0',modelIds:[],toolIds:[],toolScopes:[],documentIds:[],allowedCategories:[]},config,credentials,undefined,now))
})
test('delegation narrows parent scope and checks remaining tree budget, deadline and steps including outstanding reservations',()=>{
  const child=taskDraft(parent,now)
  child.credentialId=2;child.budgetUSD='0.8'
  const payload=taskCreatePayload(child,config,credentials,parent,now)
  assert.equal(payload.parentTaskId,parent.id);assert.equal(payload.credentialId,2);assert.equal(taskAvailableBudget(parent),800001)
  assert.equal(payload.maxDelegationDepth,1)
  for(const override of [{purpose:'other-purpose'},{documentIds:['other-doc']},{allowedCategories:['personal']},{maxSteps:18},{budgetUSD:'0.800002'},{maxDelegationDepth:2},{expiresAt:new Date(Date.parse(parent.expiresAt)+1).toISOString()}])assert.throws(()=>taskCreatePayload({...child,...override},config,credentials,parent,now))
  assert.throws(()=>taskCreatePayload(child,config,credentials,{...parent,status:'cancelled'},now))
  assert.throws(()=>taskCreatePayload(child,config,credentials,undefined,now))
})
test('task state actions retain server CAS and explain cancellation without inventing rollback or client delivery',()=>{
  assert.deepEqual(taskStatusPayload(parent,'cancelled','Stop future work'),{expectedRevision:4,status:'cancelled',reason:'Stop future work'})
  for(const status of ['active','expired','rolled_back'])assert.throws(()=>taskStatusPayload(parent,status,'reason'))
  for(const reason of ['','x'.repeat(241),'one\ntwo'])assert.throws(()=>taskStatusPayload(parent,'completed',reason))
  assert.throws(()=>taskStatusPayload({...parent,status:'completed'},'cancelled','reason'))
  const template=taskHeaderTemplate(parent)
  assert.match(template,/X-Scarlet-Task-Id: task-parent/);assert.equal(template.includes('Bearer'),false);assert.equal(template.includes('purpose'),false)
})
test('tool task scopes bind exact actions and targets rather than permitting all actions on a tool ID',()=>{
  for(const toolScopes of [[],[{toolId:'other',actions:['read'],targets:['record,one']}],[{toolId:'tool',actions:[],targets:['record,one']}],[{toolId:'tool',actions:['read'],targets:['*']}],[{toolId:'tool',actions:['read'],targets:['record*']}],[{toolId:'tool',actions:['read'],targets:['record,one','record,one']}]])assert.throws(()=>taskCreatePayload({...root,toolScopes},config,credentials,undefined,now))
  const child=taskDraft(parent,now)
  child.toolScopes=[{toolId:'tool',actions:['read'],targets:['record,one']}]
  assert.equal(taskCreatePayload(child,config,credentials,parent,now).toolScopes[0].targets[0],'record,one')
  for(const scope of [{toolId:'tool',actions:['write'],targets:['record,one']},{toolId:'tool',actions:['read'],targets:['record-three']}])assert.throws(()=>taskCreatePayload({...child,toolScopes:[scope]},config,credentials,parent,now))
})
test('delegation narrows direct document read rights while preserving inherited source context and taint',()=>{
  const tainted={...parent,observedCategories:['confidential'],sources:[{documentId:'doc',version:'v1',aclVersion:'acl-v1',contentDigest:'c'.repeat(64)}]}
  const child=taskDraft(tainted,now)
  assert.doesNotThrow(()=>taskCreatePayload(child,config,credentials,tainted,now))
  assert.doesNotThrow(()=>taskCreatePayload({...child,documentIds:[]},config,credentials,tainted,now))
  assert.throws(()=>taskCreatePayload({...child,allowedCategories:[]},config,credentials,tainted,now))
})
