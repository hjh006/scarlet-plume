export interface LifecycleField { location: string; name: string; category: string }

export interface LifecycleOperation {
  paths: string[]
  auth: string[]
  deprecated: boolean
  deprecationMixed?: boolean
  sensitiveFields: LifecycleField[]
  assetIds: number[]
  checkIds: number[]
}

export interface LifecycleSource {
  id: number; name: string; kind: 'gateway' | 'otlp'; lastSeen: string
  requests: number; responses4xx: number; responses5xx: number; revokedAt?: string
}

export const lifecycleStateLabels = {
  registered: '已登记 · 有观测',
  unobserved: '已登记 · 未观测',
  observed: '仅运行时观测',
  retired_candidate: '契约移除 · 下线待核实',
}

export const lifecycleChangeLabels = {
  added: '新增接口', removed: '移除接口', auth_changed: '鉴权声明变化',
  deprecation_changed: '弃用声明变化', sensitive_fields_changed: '敏感字段声明变化',
}

export const lifecycleAuthLabels: Record<string, string> = {
  required: '声明需鉴权', anonymous: '声明允许匿名', unspecified: '未声明', invalid: '声明待核对',
}

export const lifecycleLocationLabels: Record<string, string> = {
  parameter: '参数', request: '请求', response: '响应',
}

export const lifecycleCategoryLabels: Record<string, string> = {
  email: '邮箱', phone: '电话号码', national_id: '身份标识', financial_account: '金融账号',
  credential: '凭据', personal_name: '姓名', address: '地址', ip_address: 'IP 地址',
}

export interface LifecycleItem extends LifecycleOperation {
  service: string; method: string; route: string; routeKey: string
  state: keyof typeof lifecycleStateLabels; registered: boolean; observed: boolean
  scanId?: number; previousScanId?: number; lastSeen?: string; sources: LifecycleSource[]
}

export interface LifecycleChange {
  method: string; route: string; routeKey: string
  kinds: (keyof typeof lifecycleChangeLabels)[]
  before?: LifecycleOperation; after?: LifecycleOperation
}

export interface LifecycleComparison {
  service: string; scanId: number; createdAt: string; previousScanId?: number
  previousCreatedAt?: string; initial: boolean; changes: LifecycleChange[]
}

export interface ApiLifecycle {
  generatedAt: string; items: LifecycleItem[]; comparisons: LifecycleComparison[]
  services: string[]
  sources: { id: number; name: string; service: string; kind: 'gateway' | 'otlp'; revokedAt?: string }[]
  limit: number
}
