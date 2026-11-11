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
