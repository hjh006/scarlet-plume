import test from 'node:test'
import assert from 'node:assert/strict'
import { mediaDeclaration, mediaVersionState, runtimeMediaEvidence } from './ai-security-media.ts'
const event={id:7,sourceId:3,catalogDigest:'a'.repeat(64),requestDigest:'b'.repeat(64),current:true,record:{decision:{catalogDigest:'a'.repeat(64),requestDigest:'b'.repeat(64),mediaDigests:['c'.repeat(64)],mediaInspectionVersion:'scarlet-media-ocr-asr-v2'}}}
test('media version labels preserve historical semantics without upgrading unknown reports',()=>{
  assert.match(mediaVersionState('scarlet-media-ocr-asr-v1'),/历史 v1/)
  assert.match(mediaVersionState('scarlet-media-ocr-asr-v2'),/最低有效片段/)
  assert.match(mediaVersionState('future'),/未知/)
  assert.match(mediaVersionState(['scarlet-media-ocr-asr-v2']),/未记录/)
  assert.deepEqual(mediaDeclaration({media:{inspectionVersion:'scarlet-media-ocr-asr-v1',allowedMimeTypes:['image/png',[]],maxAssetBytes:4096}}),{version:'scarlet-media-ocr-asr-v1',mimeTypes:['image/png'],maxAssetBytes:4096})
  assert.equal(mediaDeclaration({}),undefined)
})
test('media evidence must retain exact request and catalog bindings',()=>{
  assert.equal(runtimeMediaEvidence(event).bound,true)
  for(const patch of [{mediaDigests:[]},{mediaDigests:['not-a-digest']},{catalogDigest:'d'.repeat(64)},{requestDigest:'d'.repeat(64)},{mediaDigests:['c'.repeat(64),null]}])assert.equal(runtimeMediaEvidence({...event,record:{decision:{...event.record.decision,...patch}}}).bound,false)
  const old=runtimeMediaEvidence({...event,current:false,record:{decision:{...event.record.decision,mediaInspectionVersion:'scarlet-media-ocr-asr-v1'}}})
  assert.equal(old.version,'scarlet-media-ocr-asr-v1');assert.equal(old.bound,true)
  assert.equal(runtimeMediaEvidence({...event,record:{decision:{outcome:'allow'}}}),undefined)
  assert.equal(runtimeMediaEvidence({...event,record:{decision:{mediaInspectionVersion:'scarlet-media-ocr-asr-v2'}}}).bound,false)
})
