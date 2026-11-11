import type { Asset } from './api'

export const standardKindLabels = {
  national: '国家标准',
  industry: '行业标准',
  guideline: '规则与指南',
}

export const standardStatusLabels = {
  effective: '现行',
  published: '已发布',
  superseded: '已替代',
}

export interface ClassificationStandard {
  id: string
  code: string
  title: string
  industry: string
  kind: keyof typeof standardKindLabels
  status: keyof typeof standardStatusLabels
  publishedOn?: string
  effectiveOn?: string
  authority: string
  url: string
  scope: string
  levels: { code: string; label: string; statutoryCategory?: string }[]
  rules: { id: string; clause: string; title: string; criterion: string; evidence: string; levelCodes?: string[] }[]
}

export interface ClassificationStandards {
  items: ClassificationStandard[]
  defaultStandardId: string
  catalogAsOf?: string
}

export interface ClassificationStandardSnapshot {
  id: string
  code: string
  title: string
  industry: string
  kind: keyof typeof standardKindLabels
  url: string
  levelCode?: string
  levelLabel?: string
  ruleId?: string
  ruleClause?: string
  ruleTitle?: string
  ruleCriterion?: string
  ruleEvidence?: string
}

export interface ClassificationFact {
  kind: string
  column?: string
  category?: string
  source: string
  count?: number
  note?: string
}

export type ClassificationEvaluationStatus = 'detected' | 'review_required' | 'no_signal' | 'insufficient_data'

export interface ClassificationEvaluation {
  version: string
  standard: ClassificationStandard
  status: ClassificationEvaluationStatus
  autoApplicable: boolean
  category: string
  businessLevel: string
  levelCode?: string
  statutoryCategory?: string
  rules: {
    rule: ClassificationStandard['rules'][number]
    status: ClassificationEvaluationStatus
    facts: ClassificationFact[]
    missingConditions: string[]
  }[]
  facts: ClassificationFact[]
  missingConditions: string[]
}

export interface AutomaticClassification {
  jobId: number
  runId: string
  observedAt: string
  classificationRevision: number
  comparison: 'matched' | 'conflict' | 'unconfirmed' | 'insufficient'
  comparisonReasons: string[]
  stale?: boolean
  evaluation: ClassificationEvaluation
}

export interface ClassificationRecord {
  asset: Asset
  revision: number
  origin?: 'manual' | 'automatic'
  standard?: ClassificationStandardSnapshot
  automatic?: AutomaticClassification
}

export const classificationEvaluationLabels: Record<ClassificationEvaluationStatus, string> = {
  detected: '检出数据属性信号',
  review_required: '需要复核',
  no_signal: '本次未检出格式信号',
  insufficient_data: '采集事实不足',
}

export const classificationComparisonLabels = {
  matched: '与当前分类相容',
  conflict: '与当前分类存在差异',
  unconfirmed: '分类待确认',
  insufficient: '事实不足以比对',
}

export const classificationOriginLabels = {
  manual: '人工确认',
  automatic: '自动识别',
}

export function isReferencePublication(standard: ClassificationStandard) {
  return standard.status === 'published' && !standard.effectiveOn && !standard.levels?.length && !standard.rules?.length
}

export function standardsByIndustry(standards: ClassificationStandard[]) {
  const groups = new Map<string, ClassificationStandard[]>()
  for (const standard of standards) {
    const group = groups.get(standard.industry)
    if (group) group.push(standard)
    else groups.set(standard.industry, [standard])
  }
  return [...groups]
}
