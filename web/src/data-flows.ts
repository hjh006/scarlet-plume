export interface DataFlowFilter {
  service?: string
  category?: string
  subjectRef?: string
  traceRef?: string
  focus?: string
}
export interface DataFlowEvidence {
  details?: Record<string, unknown>
  kind: string
  source: string
  sourceId?: number
  record: string | number
  observedAt?: string
  requests?: number
  traceRef?: string
  spanRef?: string
  warnings?: string[]
}
export interface DataFlowNode {
  id: string
  kind: 'application' | 'api' | 'table' | 'database_type' | 'subject'
  label: string
  service?: string
  assetId?: number
  statutoryCategory?: string
  categories: string[]
  evidence: DataFlowEvidence[]
}
export interface DataFlowEdge {
  id: string
  from: string
  to: string
  relation: 'service_call' | 'catalog' | 'declared' | 'observed' | 'database_type' | 'database_target' | 'subject_access'
  categories: string[]
  evidence: DataFlowEvidence[]
}
export interface DataFlowSummary {
  nodes: number
  edges: number
  applications: number
  apis: number
  tables: number
  subjects: number
  tracedDatabaseTargets: number
}
export interface DataFlowGraph {
  scope: DataFlowFilter
  generatedAt: string
  windowStart: string
  retentionDays: number
  nodes: DataFlowNode[]
  edges: DataFlowEdge[]
  warnings: string[]
  summary: DataFlowSummary
  snapshotStatus?: { status: string; lastAttemptAt: string; error?: string }
}
export interface DataFlowSnapshotSummary {
  id: number
  createdAt: string
  origin: string
  scope: DataFlowFilter
  digest: string
  summary: DataFlowSummary
  warnings: string[]
}
export interface DataFlowSnapshot extends DataFlowSnapshotSummary { graph: DataFlowGraph }
export interface DataFlowComparison {
  from: DataFlowSnapshotSummary
  to: DataFlowSnapshotSummary
  addedNodes: DataFlowNode[]
  removedNodes: DataFlowNode[]
  changedNodes: DataFlowNode[]
  addedEdges: DataFlowEdge[]
  removedEdges: DataFlowEdge[]
  changedEdges: DataFlowEdge[]
}
export interface DataFlowRequest {
  id: number
  sourceId: number
  source: string
  service: string
  kind: string
  observedAt: string
  method: string
  route: string
  status: number
  traceRef?: string
  spanRef?: string
  parentSpanRef?: string
  serviceCalls?: { targetService: string; spanRef: string }[]
  subjectRef?: string
  databaseSystems?: string[]
  databaseTargets?: { system: string; source: string; database: string; schema: string; table: string; operation: string }[]
  dataFields?: { name: string; location: string; category: string; count: number; observed: number; method: string }[]
  captureWarnings?: string[]
}
export interface DataFlowInvestigation {
  items: DataFlowRequest[]
  limit: number
  truncated: boolean
  retentionDays: number
}
export const dataFlowNodeLabels: Record<DataFlowNode['kind'], string> = {
  application: '业务应用', api: '接口', table: '数据库表', database_type: '数据库类型', subject: '脱敏主体',
}
export const dataFlowRelationLabels: Record<DataFlowEdge['relation'], string> = {
  service_call: '跨服务父子Span验证', catalog: '目录结构', declared: '契约声明', observed: '运行时观测', database_type: '数据库类型', database_target: '数据库目标定位', subject_access: '脱敏主体访问',
}
export const dataFlowEvidenceLabels: Record<string, string> = {
  catalog_discovery: '多源目录与分类证据',
  postgresql: 'PostgreSQL 元数据', openapi: 'OpenAPI 契约', gateway: '独立 Agent / 网关观测', otlp: '应用探针 · OTLP',
  catalog: '资产目录', declared: '契约声明', observed: '运行时观测', runtime: '运行时观测',
}
export function dataFlowQuery(filter: DataFlowFilter) {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(filter)) if (value?.trim()) query.set(key, value.trim())
  return query.toString() ? '?' + query : ''
}
import { postgresProfileWarning } from './postgresql'


const dataFlowWarningLabels: Record<string, string> = {
  scope_is_connected_sources: '仅覆盖已接入且可见的来源',
  runtime_window_7_days: '运行时关联只覆盖最近 7 天',
  database_family_is_not_table_lineage: '数据库类型级关联不代表已定位具体表',
  unmatched_service_call: '部分跨服务调用缺少另一端有效父子Span证据，候选保留在请求调查中',
  service_links_window_2_minutes: '跨服务关系要求两端授权来源、服务别名及父子Span匹配，观测时间相差不超过2分钟',
  invalid_service_call: '部分跨服务调用引用未通过校验',
  unmatched_database_target: '部分数据库目标未匹配唯一资产，未生成表级关系',
  json_size_limit: 'JSON 超出采集大小上限',
  json_depth_limit: 'JSON 超出采集深度上限',
  json_field_limit: '部分字段超出采集数量上限',
  json_dynamic_keys: '动态字段名未纳入采集',
  json_invalid: '内容不是受支持的 JSON 格式',
  json_read_error: '部分内容未能读取',
  json_encoding_unsupported: '内容编码尚不支持识别',
  invalid_trace_context: 'Trace 上下文未通过校验',
  invalid_data_fields: '部分字段元数据未通过校验',
  invalid_database_target: '部分数据库目标未通过校验',
  capture_item_limit: '部分元数据超出本轮采集数量范围',
  graph_limit: '关系规模超出留存上限，请缩小筛选范围后重试',
  capture_failed: '最近一次快照留存失败，可刷新后重试',
  capture_busy: '另一快照正在留存，本次采集已保留，可稍后重试快照',
}
export function dataFlowWarning(code: string) {
  return dataFlowWarningLabels[code] || postgresProfileWarning(code)
}
