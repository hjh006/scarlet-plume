import test from 'node:test'
import assert from 'node:assert/strict'
import { aiHistoryQuery, aiHistoryRecordId } from './ai-security-history.ts'

test('four history contracts preserve opaque scoped cursors and only supported filter fields',()=>{
  const filters={kind:'tool',outcome:'deny',status:'approved'},cursor='opaque-workspace/filter+cursor='
  const decisions=new URLSearchParams(aiHistoryQuery('decisions',filters,cursor))
  assert.equal(decisions.get('before'),cursor)
  assert.equal(decisions.get('kind'),'tool')
  assert.equal(decisions.get('outcome'),'deny')
  assert.equal(decisions.has('status'),false)
  const approvals=new URLSearchParams(aiHistoryQuery('approvals',filters))
  assert.equal(approvals.get('status'),'approved')
  assert.equal(approvals.has('kind'),false)
  assert.equal(approvals.has('outcome'),false)
  assert.equal(approvals.has('before'),false)
  assert.deepEqual([...new URLSearchParams(aiHistoryQuery('regression-packs',filters)).keys()],['limit'])
})
test('history filters keep stored statuses distinct from derived expiry or missing metadata',()=>{
  for(const outcome of ['succeeded','unknown','blocked'])assert.throws(()=>aiHistoryQuery('decisions',{kind:'',outcome,status:''}))
  assert.throws(()=>aiHistoryQuery('decisions',{kind:'output',outcome:'',status:''}))
  for(const status of ['expired','consumed','pending'])assert.throws(()=>aiHistoryQuery('approvals',{kind:'',outcome:'',status}))
  assert.throws(()=>aiHistoryQuery('evaluations',{kind:'',outcome:'',status:'approved'}))
  assert.equal(new URLSearchParams(aiHistoryQuery('evaluations',{kind:'',outcome:'',status:'failed'})).get('status'),'failed')
})
test('direct historical IDs are exact safe positive numbers before scope-bound GETs',()=>{
  assert.equal(aiHistoryRecordId('42'),'42')
  assert.equal(aiHistoryRecordId(42),'42')
  for(const value of ['',0,-1,1.5,'01','2/../../configuration','9007199254740993'])assert.throws(()=>aiHistoryRecordId(value))
})
