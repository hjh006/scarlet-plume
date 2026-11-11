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

export interface ReportDataFlows {
  snapshotId: number
  capturedAt: string
  summary: { nodes: number; edges: number; applications: number; apis: number; tables: number; subjects: number }
  warnings: string[]
  basis: string
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

export interface ReportCatalogDiscovery {
  sources: {id:number;source:string;provider:string;system:string;revision:number;runId?:number;status:string;observedAt?:string;current:boolean}[]
  assessments: {checkId:number;evidenceId:number;kind:string;current:boolean;assessment:import('./discovery').CatalogClassificationAssessment}[]
  tables:number;fields:number;ruleHits:number;truncated:boolean;basis:string;warnings:string[]
}

export interface ReportOperations {
  capturedAt: string; scope: string
  controls: import('./security-operations').ControlAction[]
  securityEvents: import('./security-operations').OperationsSecurityEvent[]
}

export interface ReportAssessmentReferences {items:{id:number;kind:string;title:string;status:string;activityId:number;templateRevisionId:number;revision:number;updatedAt:string;stale:boolean;reassessmentPending:boolean}[];truncated:boolean;basis:string}
