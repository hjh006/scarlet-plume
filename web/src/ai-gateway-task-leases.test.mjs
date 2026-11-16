import test from 'node:test'
import assert from 'node:assert/strict'
import {ApiError} from './api.ts'
import {bindingProposalPayload,bindingReviewPayload,leaseExpectations,leaseFailure,machineRenewalExample,taskLeaseProtocol} from './ai-gateway-task-leases.ts'

const now=Date.parse('2026-11-16T08:00:00Z')
const node=(taskId,credentialId)=>({taskId,taskRevision:3,scopeDigest:'b'.repeat(64),taskStatus:'active',taskExpiresAt:new Date(now+3600000).toISOString(),applicationId:'app',principalId:'principal',originalCredentialId:credentialId-1,lease:{credentialId,revision:4,mode:'renewal',updatedAt:new Date(now).toISOString()},credential:{id:credentialId,enabled:true},bindingStatus:'current',blockers:[]})
const view={taskId:'leaf',rootTaskId:'root',leaseChainDigest:'a'.repeat(64),leaseProtocolVersion:taskLeaseProtocol,observedAt:new Date(now).toISOString(),chain:[node('root',11),node('leaf',12)],canPropose:true,blockers:[],boundary:''}

test('controlled rebind freezes exact full-chain CAS without rewriting task scope, original credential or budget',()=>{
  const payload=bindingProposalPayload(view,{leaf:20},'Planned credential rotation','operation-1','',now)
  assert.deepEqual(payload.expectedChain,[{taskId:'root',taskRevision:3,leaseRevision:4,credentialId:11},{taskId:'leaf',taskRevision:3,leaseRevision:4,credentialId:12}])
  assert.deepEqual(payload.changes,[{taskId:'leaf',newCredentialId:20}])
  assert.deepEqual(bindingProposalPayload(view,{root:20},'Ancestor rotation','operation-root','',now).changes,[{taskId:'root',newCredentialId:20}])
  for (const key of ['scopeDigest','budgetMicros','expiresAt','originalCredentialId','token']) assert.equal(key in payload,false)
  assert.equal(view.chain[1].lease.credentialId,12)
  for (const targets of [{},{other:20},{leaf:12},{leaf:-1},{leaf:1.5}]) assert.throws(()=>bindingProposalPayload(view,targets,'reason','operation-1','',now))
  assert.throws(()=>bindingProposalPayload({...view,canPropose:false},{leaf:20},'reason','operation-1','',now))
})
test('lease expectations reject incomplete, reordered, duplicate or incompatible ancestor chains',()=>{
  for (const change of [{leaseProtocolVersion:'legacy'},{leaseChainDigest:''},{chain:[]},{chain:[...view.chain].reverse()},{chain:[node('root',11),node('root',11),node('leaf',12)]},{chain:[node('root',11),{...node('leaf',12),lease:{credentialId:12,revision:0}}]}]) assert.throws(()=>leaseExpectations({...view,...change}))
})
test('proposal and independent review deadlines cannot silently extend task or credential authorization',()=>{
  assert.throws(()=>bindingProposalPayload(view,{leaf:20},'reason','operation-1',new Date(now).toISOString(),now))
  assert.throws(()=>bindingProposalPayload(view,{leaf:20},'reason','operation-1',new Date(now+900001).toISOString(),now))
  const proposal={status:'proposed',canReview:true,revision:7,expiresAt:new Date(now+60000).toISOString(),changes:[{taskId:'leaf',newCredentialId:20}],expectedChain:leaseExpectations(view),credentialPlan:[{taskId:'leaf',source:{id:12},target:{id:20}}]}
  assert.deepEqual(bindingReviewPayload(proposal,'approve','Independent review',now),{expectedRevision:7,decision:'approve',note:'Independent review'})
  for(const change of [{canReview:false},{status:'applied'},{expiresAt:new Date(now).toISOString()},{revision:0}]) assert.throws(()=>bindingReviewPayload({...proposal,...change},'approve','reason',now))
  assert.throws(()=>bindingReviewPayload({...proposal,credentialPlan:[]},'approve','reason',now))
  assert.throws(()=>bindingReviewPayload({...proposal,credentialPlan:[{taskId:'leaf',source:{id:11},target:{id:20}}]},'approve','reason',now))
})
test('lease failures expose bounded known recovery facts and never render arbitrary server fields',()=>{
  const error=new ApiError(409,'Check pending work',undefined,{code:'inflight_unresolved',blockers:[{message:'Unknown result',stepId:'step-1',reservedMicros:0,token:'never-render'},null],recovery:['Check original audit',null],token:'never-render'})
  const result=leaseFailure(error)
  assert.equal(result.code,'inflight_unresolved')
  assert.equal(result.blockers[0].stepId,'step-1')
  assert.equal(result.blockers[0].reservedMicros,0)
  assert.equal('token' in result.blockers[0],false)
  assert.deepEqual(result.recovery,['Check original audit'])
})
test('machine renewal example contains placeholders and exact scoped path without browser secrets',()=>{
  const value=machineRenewalExample('space / a',view,'https://gateway.example')
  assert.ok(value.includes('/workspaces/space%20%2F%20a/ai-gateway/tasks/leaf/credential-renewal'))
  assert.ok(value.includes('expectedLeaseChainDigest'))
  assert.ok(value.includes('Cookie: 不发送'))
  assert.ok(value.includes('\"leaseRevision\": 4'))
  assert.ok(value.includes('\"credentialId\": 12'))
})
