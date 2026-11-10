export interface ReportCounts {
  draft: number
  inReview: number
  needsWork: number
  verified: number
  total: number
  templated: number
  evidence: number
  remediations: number
  retests: number
  reviews: number
}

export interface ReportSource {
  jobId: number
  source: string
  runId: string
  observedAt: string
}

export interface ReportIdentification {
  tables: number
  columns: number
  sensitiveCandidates: number
  tablesWithCandidates: number
  publicTableSelect: number
  publicColumnSelect: number
}

export interface ReportArchive extends ReportSource {
  id: number
  generatedAt: string
  status: 'ready' | 'failed'
  code?: string
  counts?: ReportCounts
  identification?: ReportIdentification
}

export interface ReportArchivePage {
  items: ReportArchive[]
  retentionPerJob: number
}

export const archiveFailureLabels: Record<string, string> = {
  scope_too_large: '本次采集范围超出报告归档上限。',
  generation_failed: '本次报告生成失败。',
}
