import test from 'node:test'
import assert from 'node:assert/strict'
import {sameDataScope,checkedDataCase,dataCaseCreatePayload} from './data-security.ts'
const scope={sourceId:7,from:'2026-11-17T08:00:00+08:00',to:'2026-11-17T09:00:00+08:00',subjectRef:'a'.repeat(64),channel:'ai_prompt'}
test('event snapshots compare the applied source, time and exact actor/device/channel scope',()=>{
  assert.equal(sameDataScope({...scope,sourceId:0},{...scope,sourceId:undefined}),true)
  assert.equal(sameDataScope(scope,{...scope,from:'2026-11-17T00:00:00Z',to:'2026-11-17T01:00:00Z'}),true)
  for(const change of [{sourceId:8},{from:'invalid'},{to:'2026-11-17T02:00:00Z'},{subjectRef:'b'.repeat(64)},{deviceRef:'c'.repeat(64)},{channel:'mail'}])assert.equal(sameDataScope(scope,{...scope,...change}),false)
})
test('post-error readback accepts only the original frozen case while allowing new notes',()=>{
  const original={id:12,scope,snapshotDigest:'d'.repeat(64),notes:[]}
  assert.equal(checkedDataCase(12,{...original,notes:[{id:1}]},original).notes.length,1)
  for(const received of [{...original,id:13},{...original,snapshotDigest:'e'.repeat(64)},{...original,scope:{...scope,sourceId:8}}])assert.throws(()=>checkedDataCase(12,received,original))
  assert.throws(()=>checkedDataCase(12,original,{...original,id:13}))
})

test('case creation requires a read event in the displayed scope and sends only the scope, never event bodies',()=>{
 const window={scope:{...scope,token:'never-copy'},items:[{id:1,rawBody:'never-copy'}],total:1,truncated:false}
 assert.throws(()=>dataCaseCreatePayload('Investigation',{...window,items:[]},scope),/至少需要一条已读取事件/)
 assert.throws(()=>dataCaseCreatePayload('Investigation',window,{...scope,sourceId:9}),/筛选范围已变化/)
 const payload=dataCaseCreatePayload(' Investigation ',window,scope)
 assert.equal(payload.title,'Investigation');assert.deepEqual(payload.scope,{...scope,deviceRef:undefined})
 assert.equal(JSON.stringify(payload).includes('never-copy'),false)
 assert.equal('items' in payload,false);assert.equal('total' in payload,false)
})
