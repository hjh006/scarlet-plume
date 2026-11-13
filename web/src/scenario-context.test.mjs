import assert from 'node:assert/strict'
import test from 'node:test'
import { readScenarioContext, scenarioContextHref } from './scenario-context.ts'

function mockLocation(t, hash) {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location: { hash } },
  })
  t.after(() => {
    if (original) Object.defineProperty(globalThis, 'window', original)
    else delete globalThis.window
  })
}

function query(href) {
  return new URLSearchParams(href.split('?')[1] ?? '')
}

const currentScope = '#/workspace/assessments?assetId=1&activityId=10&scene=personal&task=pia'

test('explicit destination scope takes precedence over the previous asset and activity', t => {
  mockLocation(t, currentScope)
  const destination = query(scenarioContextHref('workspace', 'assessments?assetId=2&activityId=20'))
  assert.equal(destination.get('assetId'), '2')
  assert.equal(destination.get('activityId'), '20')

  const updated = query(scenarioContextHref('workspace', 'assessments?assetId=2&activityId=20', { assetId: '3', activityId: '30' }))
  assert.equal(updated.get('assetId'), '3')
  assert.equal(updated.get('activityId'), '30')
})

test('changing one member of the scope clears its inherited counterpart', t => {
  mockLocation(t, currentScope)
  const newAsset = query(scenarioContextHref('workspace', 'activities/new?assetId=2'))
  assert.equal(newAsset.get('assetId'), '2')
  assert.equal(newAsset.has('activityId'), false)

  const newActivity = query(scenarioContextHref('workspace', 'checks/new', { activityId: '20' }))
  assert.equal(newActivity.get('activityId'), '20')
  assert.equal(newActivity.has('assetId'), false)
})

test('clearing an activity for a new record keeps the selected asset', t => {
  mockLocation(t, '#/workspace/assessments?assetId=63&activityId=1&scene=personal&task=pia')
  const newActivity = query(scenarioContextHref('workspace', 'activities/new', { activityId: '' }))
  assert.equal(newActivity.get('assetId'), '63')
  assert.equal(newActivity.has('activityId'), false)

  const activityOnly = query(scenarioContextHref('workspace', 'checks/new', { assetId: '' }))
  assert.equal(activityOnly.has('assetId'), false)
  assert.equal(activityOnly.get('activityId'), '1')
})

test('another workspace receives none of the current workspace scope', t => {
  mockLocation(t, currentScope)
  assert.deepEqual(readScenarioContext('another-workspace'), { assetId: '', activityId: '', scene: '', task: '' })
  assert.equal(scenarioContextHref('another-workspace', 'assessments'), '#/another-workspace/assessments')
})

test('switching scenes drops the previous scene task while preserving the selected scope', t => {
  mockLocation(t, currentScope)
  for (const href of [
    scenarioContextHref('workspace', 'assessments?scene=crossborder'),
    scenarioContextHref('workspace', 'assessments', { scene: 'crossborder' }),
  ]) {
    const destination = query(href)
    assert.equal(destination.get('scene'), 'crossborder')
    assert.equal(destination.has('task'), false)
    assert.equal(destination.get('assetId'), '1')
    assert.equal(destination.get('activityId'), '10')
  }

  const explicit = query(scenarioContextHref('workspace', 'assessments?scene=personal&task=pia', { scene: 'crossborder', task: 'assessment' }))
  assert.equal(explicit.get('scene'), 'crossborder')
  assert.equal(explicit.get('task'), 'assessment')
})

test('a scenario task path defines its own context without stale query task values', t => {
  mockLocation(t, currentScope)
  const href = scenarioContextHref('workspace', 'scenarios/crossborder/assessment?scene=personal&task=pia')
  assert.equal(query(href).has('scene'), false)
  assert.equal(query(href).has('task'), false)
  window.location.hash = href
  assert.deepEqual(readScenarioContext('workspace'), { assetId: '1', activityId: '10', scene: 'crossborder', task: 'assessment' })
})

test('navigation within one scene retains the valid task and scope', t => {
  mockLocation(t, currentScope)
  const destination = query(scenarioContextHref('workspace', 'activities'))
  assert.equal(destination.get('assetId'), '1')
  assert.equal(destination.get('activityId'), '10')
  assert.equal(destination.get('scene'), 'personal')
  assert.equal(destination.get('task'), 'pia')
})
