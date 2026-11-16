import test from 'node:test'
import assert from 'node:assert/strict'
import { workloadDraft, workloadExchangeExample, workloadPayload, workloadPublicKeys } from './ai-gateway-workloads.ts'
import { gatewayCredentialActive } from './ai-gateway.ts'

const key={kty:'OKP',kid:'runtime-key-v1',use:'sig',alg:'EdDSA',crv:'Ed25519',x:'A'.repeat(43)}
function validDraft(){return {...workloadDraft(),name:'业务工作负载',applicationId:'app-a',issuer:'https://identity.example/issuer',audience:'urn:gateway',subject:'spiffe://example/workload-a',jwksJSON:JSON.stringify({keys:[key]})}}
test('workload binding imports public keys only and never accepts secret or remote key material',()=>{
  assert.deepEqual(workloadPublicKeys(JSON.stringify({keys:[key]})),{keys:[key]})
  for(const extra of [{d:'private-material'},{k:'symmetric-material'},{jku:'https://remote.example/keys'},{x5u:'https://remote.example/cert'},{key_ops:['sign']},{kid:'*'}])assert.throws(()=>workloadPublicKeys(JSON.stringify({keys:[{...key,...extra}]})))
  assert.throws(()=>workloadPublicKeys(JSON.stringify({keys:[key,key]})))
  assert.throws(()=>workloadPublicKeys(JSON.stringify({keys:[{...key,alg:'none'}]})))
  assert.throws(()=>workloadPublicKeys(JSON.stringify({keys:[]})))
  assert.throws(()=>workloadPublicKeys(JSON.stringify({keys:[key],privateKeys:[]})))
})
test('workload create and update preserve strict identity binding and original revision',()=>{
  const draft=validDraft()
  const created=workloadPayload({...draft,token:'must-not-copy',jwt:'must-not-copy'})
  assert.equal(created.issuer,draft.issuer)
  assert.equal(created.subject,draft.subject)
  assert.equal('token' in created,false)
  assert.equal('jwt' in created,false)
  assert.equal('enabled' in created,false)
  assert.equal('expectedRevision' in created,false)
  const updated=workloadPayload({...draft,id:12,revision:7,enabled:false})
  assert.equal(updated.expectedRevision,7)
  assert.equal(updated.enabled,false)
  assert.throws(()=>workloadPayload({...draft,id:12,revision:0}))
})
test('workload limits use UTF-8 bytes and reject unsupported issuer or TTL',()=>{
  const draft=validDraft()
  for(const issuer of ['http://identity.example','https://user:pass@identity.example','https://identity.example?target=all','https://identity.example/#x'])assert.throws(()=>workloadPayload({...draft,issuer}))
  for(const maxTtlSeconds of [0,301,1.5])assert.throws(()=>workloadPayload({...draft,maxTtlSeconds}))
  assert.throws(()=>workloadPayload({...draft,name:'字'.repeat(43)}))
  assert.throws(()=>workloadPayload({...draft,subject:'字'.repeat(86)}))
  assert.throws(()=>workloadPayload({...draft,audience:'urn:gateway\nadmin'}))
})
test('short-lived credentials must still be active at the exact expiry boundary',()=>{
  const now=Date.parse('2026-11-16T01:00:00Z'),credential={enabled:true}
  assert.equal(gatewayCredentialActive(credential,now),true)
  assert.equal(gatewayCredentialActive({...credential,expiresAt:'2026-11-16T01:00:01Z'},now),true)
  assert.equal(gatewayCredentialActive({...credential,expiresAt:'2026-11-16T01:00:00Z'},now),false)
  assert.equal(gatewayCredentialActive({...credential,expiresAt:'invalid'},now),false)
  assert.equal(gatewayCredentialActive({...credential,enabled:false},now),false)
  assert.equal(gatewayCredentialActive({...credential,revokedAt:'2026-11-16T00:59:59Z'},now),false)
})
test('machine exchange example contains placeholders and one exact scoped binding selector',()=>{
  const example=workloadExchangeExample('space / a',12,'https://gateway.example')
  assert.ok(example.includes('/workspaces/space%20%2F%20a/ai-gateway/workload-token'))
  assert.ok(example.includes('"bindingId": 12'))
  assert.ok(example.includes('<工作负载以受信任私钥签发的短期 JWT>'))
})
