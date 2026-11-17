import test from 'node:test'
import assert from 'node:assert/strict'
import {policyReadback} from './runtime-policies.ts'
test('unknown policy saves stay locked on failed or unrelated reads and only a fresh rule page clears them',()=>{
 const pending=new Set(['sensitive_response','destination_scope'])
 assert.deepEqual([...policyReadback(pending)], [...pending])
 assert.deepEqual([...policyReadback(pending,{items:[{rule:'missing_auth'}]})],[...pending])
 assert.deepEqual([...policyReadback(pending,{items:[{rule:'sensitive_response',revision:3}]})],['destination_scope'])
 assert.deepEqual([...pending],['sensitive_response','destination_scope'])
})
