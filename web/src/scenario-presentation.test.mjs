import assert from 'node:assert/strict'
import test from 'node:test'
import { scenarioPresentations, defaultScenarioTasks, scenarioStages } from './scenario-presentation.ts'
import { scenarios } from './scenario-navigation.ts'

test('every existing scenario task and default has an explicit presentation', () => {
  for (const scene of scenarios) {
    assert.ok(scene.tasks.some(task => task.id === defaultScenarioTasks[scene.id]), scene.id)
    for (const task of scene.tasks) assert.ok(scenarioPresentations[`${scene.id}/${task.id}`], `${scene.id}/${task.id}`)
  }
})

test('no run creates no source, execution or retest completion', () => {
  assert.ok(scenarioStages().every(stage => !stage.reached))
})

test('approved actions and execution receipts cannot establish retest or independent review', () => {
  const run = { id: 5, evidence: [{ id: 'e1' }], actions: [{ status: 'approved' }], retestState: 'same_source_observation' }
  let stages = scenarioStages(run)
  assert.equal(stages[3].reached, false)
  assert.equal(stages[4].reached, false)
  assert.equal(stages[5].reached, false)
  run.actions[0].status = 'applied'
  stages = scenarioStages(run)
  assert.equal(stages[3].reached, true)
  assert.equal(stages[4].reached, false)
  assert.equal(stages[5].reached, false)
})

test('new source adoption remains separate from independent review', () => {
  const stages = scenarioStages({ id: 6, retestState: 'new_source_observation', actions: [] })
  assert.equal(stages[4].reached, true)
  assert.equal(stages[5].reached, false)
})
