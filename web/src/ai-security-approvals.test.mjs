import test from 'node:test'
import assert from 'node:assert/strict'
import { aiApprovalId, aiApprovalReviewNote, buildAIApprovalReviewPlan, sameAIApprovalBinding } from './ai-security-approvals.ts'

const now=Date.parse('2026-11-17T01:00:00Z')
const record={id:1,configurationRevision:4,status:'proposed',requestDigest:'a'.repeat(64),catalogDigest:'b'.repeat(64),requestId:'bounded-request',issuedAt:'2026-11-17T00:59:00Z',expiresAt:'2026-11-17T01:04:00Z',applicationId:'app',principalId:'principal',toolId:'crm',action:'update',target:'  approved,target  ',arguments:{record:'exact,value',reason:'  keep spaces  '}}
const intent={id:'1',decision:'approved',note:'  独立核对具体动作  '}
test('approval plans freeze the exact request without changing arguments or targets',()=>{
  const input=structuredClone(record),plan=buildAIApprovalReviewPlan(input,intent,4,now)
  input.arguments.record='changed';input.target='changed'
  assert.equal(plan.record.arguments.record,'exact,value');assert.equal(plan.record.target,'  approved,target  ')
  assert.equal(plan.note,'独立核对具体动作');assert.equal(plan.decision,'approved')
})
test('approval review refuses stale, expired, malformed and mismatched records',()=>{
  for(const patch of [{id:2},{status:'approved'},{configurationRevision:3},{expiresAt:'invalid'},{expiresAt:9999},{expiresAt:'2026-11-17T01:00:00Z'},{issuedAt:'invalid'},{requestDigest:''},{catalogDigest:'x'.repeat(64)},{applicationId:''},{arguments:[]},{arguments:{record:1}},{taskId:0},{taskId:'task',leaseChainDigest:'c'.repeat(64)},{taskId:'task',leaseChainDigest:'c'.repeat(64),leaseProtocolVersion:'future-protocol'}]) assert.throws(()=>buildAIApprovalReviewPlan({...record,...patch},intent,4,now))
  assert.throws(()=>buildAIApprovalReviewPlan(record,intent,5,now));assert.throws(()=>buildAIApprovalReviewPlan(record,{...intent,decision:'allow'},4,now))
  assert.doesNotThrow(()=>buildAIApprovalReviewPlan({...record,taskId:'task',leaseChainDigest:'c'.repeat(64),leaseProtocolVersion:'scarlet-task-lease-v1'},intent,4,now))
  assert.doesNotThrow(()=>buildAIApprovalReviewPlan({...record,arguments:null},intent,4,now))
})
test('recovery compares all immutable binding facts and preserves parameter semantics',()=>{
  assert.equal(sameAIApprovalBinding(record,{...record,status:'approved',arguments:{reason:'  keep spaces  ',record:'exact,value'}}),true)
  for(const patch of [{target:'approved,target'},{requestId:'another'},{requestDigest:'c'.repeat(64)},{expiresAt:'2026-11-17T01:05:00Z'},{arguments:{...record.arguments,record:'another'}},{taskId:'new-task'},{leaseChainDigest:'c'.repeat(64)}]) assert.equal(sameAIApprovalBinding(record,{...record,...patch}),false)
})
test('review identities and UTF-8 note limits reject coercion and ambiguous input',()=>{
  for(const id of [0,-1,1.5,NaN,true,'01','1/../2','9223372036854775808',Number.MAX_SAFE_INTEGER+1]) assert.throws(()=>aiApprovalId(id))
  assert.equal(aiApprovalId('9223372036854775807'),'9223372036854775807')
  assert.equal(aiApprovalReviewNote('字'.repeat(333)).length,333)
  for(const note of ['   ','字'.repeat(334)]) assert.throws(()=>aiApprovalReviewNote(note))
})
