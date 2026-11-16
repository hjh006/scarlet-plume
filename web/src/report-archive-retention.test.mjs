import test from 'node:test'
import assert from 'node:assert/strict'
import { archiveActions, archiveRetentionPayload, verifyArchiveResponse } from './report-archive-retention.ts'

const now=Date.parse('2026-11-16T00:00:00Z')
function archive(retention={}){return {id:7,retention:{revision:4,state:'review_required',legalHold:false,contributors:[],...retention}}}
test('unknown-purpose archives stay protected until a real independent rotation decision',()=>{
  assert.deepEqual(archiveActions(archive(),'viewer',now),[])
  assert.deepEqual(archiveActions(archive(),'operator',now),['hold','extend'])
  assert.deepEqual(archiveActions(archive(),'reviewer',now),['allow_rotation'])
  const protectedArchive=archive({legalHold:true,retainUntil:'2020-01-01T00:00:00Z'})
  assert.deepEqual(archiveActions(protectedArchive,'reviewer',now),['release_hold'])
  assert.throws(()=>archiveRetentionPayload(protectedArchive,{action:'allow_rotation',basis:'已核对',retainUntil:'',evidenceId:'12'},now),/保护未解除/)
  assert.deepEqual(archiveActions(archive({retainUntil:'2027-01-01T00:00:00Z'}),'reviewer',now),[])
})
test('retention extensions preserve CAS and never shorten existing protection',()=>{
  const record=archive({retainUntil:'2026-12-01T00:00:00Z'})
  const payload=archiveRetentionPayload(record,{action:'extend',basis:'实际合同留存义务',retainUntil:'2027-01-01T00:00:00+08:00',evidenceId:'13'},now)
  assert.equal(payload.revision,4)
  assert.equal(payload.retainUntil,'2027-01-01T00:00:00+08:00')
  assert.equal('evidenceId' in payload,false)
  for(const retainUntil of ['2026-11-17T00:00:00Z','2026-12-01T00:00:00Z','2027-01-01T00:00'])assert.throws(()=>archiveRetentionPayload(record,{action:'extend',basis:'依据',retainUntil,evidenceId:''},now))
})
test('release and rotation require real evidence identifiers without inventing delivery confirmation',()=>{
  const draft={action:'release_hold',basis:'独立核对保全解除依据',retainUntil:'',evidenceId:'19'}
  assert.deepEqual(archiveRetentionPayload(archive({legalHold:true}),draft,now),{revision:4,action:'release_hold',basis:draft.basis,evidenceId:19})
  for(const evidenceId of ['','0','-1','1.1','9007199254740993'])assert.throws(()=>archiveRetentionPayload(archive(),{...draft,evidenceId},now))
})
test('downloaded bytes must match actual SHA-256 before a file save is started',async()=>{
  const bytes=new TextEncoder().encode('archive fixture').buffer
  const digest=Buffer.from(await crypto.subtle.digest('SHA-256',bytes)).toString('base64')
  assert.equal(await verifyArchiveResponse(bytes,'sha-256=:'+digest+':'),true)
  assert.equal(await verifyArchiveResponse(bytes,null),false)
  await assert.rejects(()=>verifyArchiveResponse(new TextEncoder().encode('changed archive').buffer,'sha-256=:'+digest+':'),/不匹配/)
  await assert.rejects(()=>verifyArchiveResponse(bytes,'sha-256=unsupported'),/格式未受支持/)
})
