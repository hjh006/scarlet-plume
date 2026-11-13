import { isReferencePublication, type ClassificationRecord, type ClassificationStandards } from './classification-standards'

export interface ClassificationTaxonomy {
  id: number | string
  parentId?: number | string | null
  name: string
  path: string
  revision: number
  enabled: boolean
  description: string
}

export interface ClassificationCandidate {
  name: string; category: string; source: string; location?: string; observedAt: string
  runId?: string; scanId?: number; sourceId?: number; ruleKey?: string; ruleVersion?: number
  current?: boolean; method?: string; matchCount?: number; scannedCount?: number
}

export function classificationCandidateMethodLabel(method: string) {
  return ({ metadata: '元数据规则', source_aggregate: '源内格式聚合', bounded_customer_sample: '受限内容样本' } as Record<string, string>)[method] || method
}

export interface WorkbenchItem extends ClassificationRecord {
  candidates: ClassificationCandidate[]
  candidateCount: number
  suggestedCategory?: string
  suggestedBusinessLevel?: string
  standardDefinition?: import('./classification-standards').ClassificationStandard
  catalog?: {
    kind: string; source: string; sourceRef: string; system: string; database: string; schema: string
    table: string; field: string; current: boolean; observedAt: string; sourceId?: number; runId?: number
  }
  taxonomy?: { id: number | string; name: string; path: string; revision: number; signature?: string }
  taxonomyStale: boolean
  needsReview?: boolean
  publicationState: 'unpublished' | 'published' | 'changed'
}

export type WorkbenchFacet = { key: string; count: number }
export interface ClassificationWorkbenchPage {
  items: WorkbenchItem[]
  total: number
  page: number
  pageSize: number
  counts: { total: number; pending: number; reviewed: number; automatic: number; importantOrCore: number; changed: number; unpublished: number }
  facets: Record<'category' | 'businessLevel' | 'statutoryCategory' | 'source' | 'system' | 'kind', WorkbenchFacet[]>
  taxonomy: ClassificationTaxonomy[]
}

export interface ClassificationPublication {
  id: number | string
  title: string
  note: string
  status: 'submitted' | 'published' | 'rejected'
  creator: string
  createdBy: number
  createdAt: string
  reviewer?: string
  reviewedBy?: number
  reviewedAt?: string
  reviewNote?: string
  snapshotDigest: string
  itemCount: number
  items?: WorkbenchItem[]
}

export type ClassificationFilters = Record<'q' | 'source' | 'system' | 'kind' | 'category' | 'businessLevel' | 'statutoryCategory' | 'status' | 'taxonomyId', string>
export const emptyClassificationFilters = (): ClassificationFilters => ({ q: '', source: '', system: '', kind: '', category: '', businessLevel: '', statutoryCategory: '', status: '', taxonomyId: '' })
export const taxonomyPath = (item?: { path: string; name: string }) => item ? item.path || item.name : '未指定业务分类'
export const publicationStateLabels = { unpublished: '尚未发布', published: '与已发布版本一致', changed: '发布后已变化' }
export const publicationStatusLabels = { submitted: '待独立复核', published: '已发布', rejected: '已退回' }

export function availableClassificationStandards(catalog?: ClassificationStandards) {
  return catalog?.items.filter(item => item.status === 'effective' || isReferencePublication(item)) ?? []
}
