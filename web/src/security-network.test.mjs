import test from 'node:test'
import assert from 'node:assert/strict'
import {networkHistoryQuery,networkRecordId,networkScopeIds} from './security-network.ts'

test('network scope accepts only exact positive safe record identifiers',()=>{
  assert.deepEqual(networkScopeIds('12, 34，12\n56'),[12,34,56])
  assert.deepEqual(networkScopeIds(''),[])
  for (const value of ['0','-1','1e2','1.2','9007199254740992','1&bindingId=2','01']) assert.throws(()=>networkRecordId(value))
})
test('network history keeps filter scopes separate and encodes opaque cursors',()=>{
  const filters={q:'区域 A / 核对 &确认',status:'pending_review',bindingId:'272'}
  const bindings=new URL(networkHistoryQuery('network-bindings',filters,'a+b/='),'https://example.test')
  assert.equal(bindings.searchParams.get('q'),filters.q)
  assert.equal(bindings.searchParams.get('cursor'),'a+b/=')
  assert.equal(bindings.searchParams.has('bindingId'),false)
  const changes=new URL(networkHistoryQuery('network-changes',filters),'https://example.test')
  assert.equal(changes.searchParams.get('bindingId'),'272')
  assert.equal(changes.searchParams.get('status'),'pending_review')
  assert.equal(changes.searchParams.has('q'),false)
  assert.equal(changes.searchParams.has('cursor'),false)
  assert.equal(networkHistoryQuery('snapshots',filters),'/snapshots?limit=50')
  assert.throws(()=>networkHistoryQuery('network-changes',{...filters,status:'all'}))
  assert.throws(()=>networkHistoryQuery('network-changes',{...filters,bindingId:'1e2'}))
})
test('snapshot time windows retain instants and reject reversed or invalid ranges',()=>{
  const query = new URL(networkHistoryQuery('snapshots',{q:'',status:'',bindingId:'',createdFrom:'2026-11-16T08:00:00+08:00',createdTo:'2026-11-17T08:00:00+08:00'}),'https://example.test')
  assert.equal(query.searchParams.get('createdFrom'),'2026-11-16T00:00:00.000Z')
  assert.equal(query.searchParams.get('createdTo'),'2026-11-17T00:00:00.000Z')
  assert.throws(()=>networkHistoryQuery('snapshots',{q:'',status:'',bindingId:'',createdFrom:'invalid'}))
  assert.throws(()=>networkHistoryQuery('snapshots',{q:'',status:'',bindingId:'',createdFrom:'2026-11-17T00:00:00Z',createdTo:'2026-11-16T00:00:00Z'}))
})
