import test from 'node:test'
import assert from 'node:assert/strict'
import { runtimeEvidenceQuery } from './ai-security-runtime.ts'

test('runtime history preserves opaque cursors and exact applied filter scope',()=>{
  const cursor='signed.workspace-source%2F9+filter=output:deny'
  const query=new URLSearchParams(runtimeEvidenceQuery({sourceId:'17',kind:'output',outcome:'deny'},cursor))
  assert.equal(query.get('before'),cursor)
  assert.equal(query.get('sourceId'),'17')
  assert.equal(query.get('kind'),'output')
  assert.equal(query.get('outcome'),'deny')
  assert.equal(query.get('limit'),'100')
  assert.equal(new URLSearchParams(runtimeEvidenceQuery({sourceId:'',kind:'',outcome:''})).has('before'),false)
})
test('runtime query rejects unsafe source identifiers and unrecognized filters',()=>{
  for(const sourceId of ['0','-1','1.2','9 OR 1=1','9007199254740993'])assert.throws(()=>runtimeEvidenceQuery({sourceId,kind:'',outcome:''}))
  assert.throws(()=>runtimeEvidenceQuery({sourceId:'',kind:'unknown-protocol',outcome:''}))
  assert.throws(()=>runtimeEvidenceQuery({sourceId:'',kind:'',outcome:'success'}))
})
