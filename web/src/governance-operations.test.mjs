import test from 'node:test'
import assert from 'node:assert/strict'
import { governanceActions, governanceCanEdit, governanceDraft, governanceIDs, governancePayload, governanceSourceURL } from './governance-operations.ts'

function vendorDraft(){const draft=governanceDraft('vendor');Object.assign(draft,{title:'供应商范围复核',owner:'运营团队',checkIds:'12',evidenceIds:'23',regulationIds:['cn-network-data-2025'],applicability:'unknown',applicabilityReason:'实际处理关系待核定'});Object.assign(draft.details,{name:'供应商引用',processingScope:'已确认处理范围'});return draft}
test('governance draft preserves unknown applicability and never fabricates evidence or receipt',()=>{
  const draft=governanceDraft('rights_request')
  assert.equal(draft.applicability,'unknown')
  assert.equal(draft.details.subjectRef,'')
  assert.equal(draft.details.receivedAt,'')
  assert.deepEqual(draft.regulationIds,[])
  assert.deepEqual(draft.receipts,[])
  assert.equal(draft.evidenceIds,'')
})
test('governance payload binds receipts to selected real evidence and strips another kind fields',()=>{
  const draft=vendorDraft()
  draft.details.subjectRef='must-not-cross-kind'
  draft.receipts=[{purpose:'contract',evidenceId:23,system:'  vendor-system '}]
  const payload=governancePayload(draft)
  assert.equal('subjectRef' in payload.details,false)
  assert.equal(payload.receipts[0].system,'vendor-system')
  draft.receipts[0].evidenceId=24
  assert.throws(()=>governancePayload(draft),/已关联的真实证据/)
  draft.receipts=[{purpose:'initial_report',evidenceId:23}]
  assert.throws(()=>governancePayload(draft),/已关联的真实证据/)
})
test('rights records reject raw identity and timezone-free or impossible timestamps',()=>{
  const draft=governanceDraft('rights_request')
  Object.assign(draft.details,{subjectRef:'a'.repeat(64),receivedAt:'2026-11-16T09:30:00+08:00'})
  assert.equal(governancePayload(draft).details.receivedAt,'2026-11-16T09:30:00+08:00')
  draft.details.subjectRef='person@example.com'
  assert.throws(()=>governancePayload(draft),/SHA-256/)
  draft.details.subjectRef='a'.repeat(64)
  for(const value of ['2026-11-16T09:30','2026-02-30T09:30:00+08:00']){draft.details.receivedAt=value;assert.throws(()=>governancePayload(draft))}
})
test('numeric scale zero stays explicit while absent scale remains absent',()=>{
  const draft=governanceDraft('audit');draft.details.scope='实际审计范围'
  assert.equal('processorPeople' in governancePayload(draft).details,false)
  draft.details.processorPeople='0'
  assert.equal(governancePayload(draft).details.processorPeople,0)
  assert.equal(governancePayload(draft).details.minorData,false)
})
test('role affordances preserve independent review and immutable terminal states',()=>{
  const record={kind:'consent',status:'in_review'}
  assert.deepEqual(governanceActions(record,'operator'),[])
  assert.deepEqual(governanceActions(record,'reviewer'),['approve','request_changes'])
  assert.equal(governanceCanEdit(record,'owner'),false)
  record.status='completed'
  assert.deepEqual(governanceActions(record,'operator'),['withdraw'])
  assert.equal(governanceCanEdit(record,'owner'),false)
  record.status='withdrawal_pending'
  assert.equal(governanceCanEdit(record,'operator'),true)
  assert.deepEqual(governanceActions(record,'viewer'),[])
})
test('reference parsing is bounded and external sources must use HTTPS',()=>{
  assert.deepEqual(governanceIDs('12, 12，13\n14'),[12,13,14])
  for(const value of ['0','-1','1.5','9007199254740993',Array.from({length:51},(_,index)=>index+1).join(',')])assert.throws(()=>governanceIDs(value))
  assert.equal(governanceSourceURL('javascript:alert(1)'),undefined)
  assert.equal(governanceSourceURL('http://example.com'),undefined)
  assert.equal(governanceSourceURL('https://example.com/rule'),'https://example.com/rule')
})
