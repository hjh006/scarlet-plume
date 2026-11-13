import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { AssessmentFact } from './assessments'
import type { WorkbenchItem } from './classification-workbench'

export interface ScenarioContext { assetId: string; activityId: string; scene: string; task: string }
type ContextUpdate = Partial<ScenarioContext>
export const scenarioScopeEvent = 'scarlet-scenario-scope'
const positiveID = (value: string | null | undefined) => value && /^[1-9]\d*$/.test(value) && Number.isSafeInteger(Number(value)) ? value : ''

export function readScenarioContext(slug: string): ScenarioContext {
  const [path, rawQuery = ''] = window.location.hash.split('?')
  const parts = (path ?? '').split('/')
  let currentSlug = ''
  try { currentSlug = decodeURIComponent(parts[1] ?? '') } catch { /* Invalid route has no usable scope. */ }
  if (currentSlug !== slug) return { assetId: '', activityId: '', scene: '', task: '' }
  const query = new URLSearchParams(rawQuery)
  return { assetId: positiveID(query.get('assetId')), activityId: positiveID(query.get('activityId')), scene: parts[2] === 'scenarios' ? parts[3] ?? '' : query.get('scene') ?? '', task: parts[2] === 'scenarios' ? parts[4] ?? '' : query.get('task') ?? '' }
}

export function scenarioContextHref(slug: string, page: string, update: ContextUpdate = {}, context = readScenarioContext(slug)) {
  const [path = '', rawQuery = ''] = page.split('?')
  const query = new URLSearchParams(rawQuery)
  const next = { ...context, ...update }
  const explicitAsset = 'assetId' in update || query.has('assetId')
  const explicitActivity = 'activityId' in update || query.has('activityId')
  next.assetId = 'assetId' in update ? update.assetId ?? '' : query.has('assetId') ? query.get('assetId') ?? '' : context.assetId
  next.activityId = 'activityId' in update ? update.activityId ?? '' : query.has('activityId') ? query.get('activityId') ?? '' : context.activityId
  if (explicitAsset && next.assetId && next.assetId !== context.assetId && !explicitActivity) next.activityId = ''
  if (explicitActivity && next.activityId && next.activityId !== context.activityId && !explicitAsset) next.assetId = ''
  for (const key of ['assetId', 'activityId'] as const) {
    const value = positiveID(next[key])
    if (value) query.set(key, value)
    else query.delete(key)
  }
  if (path.startsWith('scenarios/')) { query.delete('scene'); query.delete('task') }
  else {
    if ('scene' in update) {
      if (update.scene) query.set('scene', update.scene)
      else query.delete('scene')
    } else if (context.scene && !query.has('scene')) query.set('scene', context.scene)
    if ('task' in update) {
      if (update.task) query.set('task', update.task)
      else query.delete('task')
    } else if (context.task && !query.has('task') && query.get('scene') === context.scene) query.set('task', context.task)
  }
  return `#/${encodeURIComponent(slug)}/${path}${query.size ? '?' + query : ''}`
}

export function useScenarioContext(slug: string) {
  const context = ref(readScenarioContext(slug))
  const refresh = () => { context.value = readScenarioContext(slug) }
  function setScope(update: Pick<ScenarioContext, 'assetId' | 'activityId'>) {
    const url = new URL(window.location.href)
    const [path = '', rawQuery = ''] = url.hash.split('?')
    const query = new URLSearchParams(rawQuery)
    for (const key of ['assetId', 'activityId'] as const) {
      const value = positiveID(update[key])
      if (value) query.set(key, value)
      else query.delete(key)
    }
    url.hash = path + (query.size ? '?' + query : '')
    window.history.replaceState(window.history.state, '', url)
    window.dispatchEvent(new Event(scenarioScopeEvent))
  }
  onMounted(() => { window.addEventListener('hashchange', refresh); window.addEventListener(scenarioScopeEvent, refresh) })
  onBeforeUnmount(() => { window.removeEventListener('hashchange', refresh); window.removeEventListener(scenarioScopeEvent, refresh) })
  const href = (page: string, update: ContextUpdate = {}) => scenarioContextHref(slug, page, update, context.value)
  return { context, setScope, href }
}

export interface ScenarioMaterial {
  key: string; title: string; status: 'auto_filled' | 'needs_confirmation' | 'missing' | 'not_applicable'
  value?: string; factKeys: string[]; records: string[]; guidance: string; action: { page: string; label: string }
}
export interface ScenarioTaskData {
  assetId?: number; activityId?: number; scenario?: string; task?: string
  facts: AssessmentFact[]; sources: { id: string; name: string; kind: string; status: string; lastObservedAt?: string }[]; gaps: string[]
  classification?: { item: WorkbenchItem; manuallyConfirmed: boolean; usable: boolean; trust: 'reviewed' | 'legacy_declared' | 'candidate' | 'unconfirmed'; publication?: { id: number; title: string; snapshotDigest: string; reviewedAt?: string; assetRevision: number; current: boolean }; warnings: string[] }
  materials: ScenarioMaterial[]
  materialCounts: { autoFilled: number; needsConfirmation: number; missing: number; notApplicable: number }
}
