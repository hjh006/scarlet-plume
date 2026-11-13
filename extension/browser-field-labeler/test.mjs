import test from 'node:test'
import assert from 'node:assert/strict'
import { collectCurrentPageFields, collectFromActiveTab, platformImportUrl } from './collect.js'
test('explicit active-tab action reads labels while never accessing input values', async () => {
  const element = (tag, attrs, text = '') => ({tagName:tag.toUpperCase(),getAttribute:key=>attrs[key]??null,getClientRects:()=>[{}],closest:()=>null,querySelector:()=>null,labels:[],textContent:text,get value(){throw new Error('value access forbidden')}})
  const fields=[element('input',{name:'phone','aria-label':'联系电话'}),element('input',{name:'password',type:'password'}),element('textarea',{name:'description'},'PRIVATE RAW CONTENT'),element('th',{},'经营收入'),element('input',{name:'api_token'})]
  globalThis.document={querySelectorAll:()=>fields}
  globalThis.location={origin:'https://synthetic.invalid',pathname:'/business',search:'?secret=excluded',hash:'#excluded'}
  const calls=[]
  const chrome={tabs:{query:async query=>{calls.push(query);return [{id:7,url:'https://synthetic.invalid/business'}]}},scripting:{executeScript:async action=>{calls.push(action);return [{result:action.func()}]}}}
  const payload=await collectFromActiveTab(chrome)
  assert.equal(payload.fields.length,3)
  assert.equal(payload.fields[2].label,'经营收入')
  assert.ok(!JSON.stringify(payload).includes('PRIVATE RAW CONTENT'))
  assert.ok(!JSON.stringify(payload).includes('password'))
  assert.deepEqual(calls[0],{active:true,currentWindow:true})
  assert.deepEqual(calls[1].target,{tabId:7})
  assert.equal(calls[1].func,collectCurrentPageFields)
})
test('handoff stays in hash, targets selected workspace and carries no session credential',()=>{
 const payload={version:1,page:{origin:'https://synthetic.invalid',path:'/business',title:''},fields:[{name:'收入',label:'营业收入'}]}
 const url=new URL(platformImportUrl('https://platform.invalid/#/workspace/overview',payload))
 assert.equal(url.search,'')
 assert.ok(url.hash.startsWith('#/workspace/capabilities?tab=browser&annotation='))
 const token=new URLSearchParams(url.hash.split('?')[1]).get('annotation')
 assert.deepEqual(JSON.parse(Buffer.from(token,'base64url').toString('utf8')),payload)
 assert.throws(()=>platformImportUrl('javascript:alert(1)',payload))
 assert.throws(()=>platformImportUrl('https://user:secret@platform.invalid/#/workspace/overview',payload))
})
