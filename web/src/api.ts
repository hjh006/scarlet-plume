export interface Workspace {
  id: number | string
  slug: string
  name: string
  role: string
}

export interface Session {
  username: string
  workspaces: Workspace[]
}

export interface Asset {
  id: number | string
  name: string
  system: string
  source: string
  sourceRef: string
  sourceProvider?: string
  ownerTeam: string
  category?: string
  businessLevel?: string
  statutoryCategory?: string
  classificationBasis?: string
  description?: string
  createdAt: string
}

export interface AssetPage {
  items: Asset[]
  nextCursor: number | null
}

export interface AssetDraft {
  name: string
  system: string
  ownerTeam: string
}

export const assetCategoryLabels: Record<string, string> = {
  business: '业务数据', personal: '个人信息', sensitive_personal: '敏感个人信息',
}

export const assetStatutoryCategoryLabels: Record<string,string> = {unknown:'待认定',general:'一般数据',important:'重要数据',core:'核心数据'}

export const assetBusinessLevelLabels: Record<string, string> = {
  internal: '内部', confidential: '机密',
}

export interface Integration {
  id: string
  label: string
  category: string
  capability: string
  status: 'available' | 'planned'
  docs: string
}

export interface ImportRun {
  id: number | string
  provider: string
  source: string
  batchId: string
  created: number
  existing: number
  replayed?: boolean
  createdAt: string
}

export interface ImportManifest {
  version: 1
  provider: 'postgresql' | 'openmetadata'
  source: string
  batchId: string
  assets: { sourceRef: string; name: string }[]
}

export interface Overview {
  workspace: Workspace
  generatedAt?: string
  counts: {
    assets: number
    integrations: number
    activities: number
    checks: number
    draft: number
    inReview: number
    needsWork: number
    verified: number
    activeSources?: number
    openRuntimeFindings?: number
    pendingClassification?: number
  }
  recentAssets: Asset[]
  recentImports: ImportRun[]
  recentFindings?: import('./runtime-findings').RuntimeFinding[]
  collectionSources?: {
    id: number
    name: string
    provider: string
    kind: 'automation' | 'runtime' | 'discovery' | 'operations'
    state: 'collecting' | 'queued' | 'paused' | 'enabled' | 'failed' | 'never_run' | 'observing'
    lastObservedAt?: string
    lastRunAt?: string
  }[]
}

export const sessionPath = '/api/v1/session'

export function workspacePath(slug: string) {
  return `/api/v1/workspaces/${encodeURIComponent(slug)}`
}

export class ApiError extends Error {
  readonly status: number
  readonly retryAfterSeconds?: number
  constructor(status: number, message: string, retryAfterSeconds?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.retryAfterSeconds = retryAfterSeconds
  }
}

export function retryAfterSeconds(value: string | null, now = Date.now()): number | undefined {
  if (!value || value.length > 128) return undefined
  const seconds = /^\d+$/.test(value.trim()) ? Number(value) : (Date.parse(value) - now) / 1000
  return Number.isFinite(seconds) && seconds >= 0 ? Math.min(86400, Math.ceil(seconds)) : undefined
}

export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  headers.set('Accept', 'application/json')
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')
  const deadline = AbortSignal.timeout(45000)
  const signal = options.signal ? AbortSignal.any([options.signal, deadline]) : deadline
  const response = await fetch(path, { ...options, headers, signal, credentials: 'same-origin' })

  if (response.status === 204) return undefined as T
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const message = typeof body?.error === 'string' ? body.error.slice(0, 300) : ''
    throw new ApiError(response.status, message, retryAfterSeconds(response.headers.get('Retry-After')))
  }
  if (body === null) throw new Error('服务返回的响应格式无效。')
  return body as T
}

export async function downloadFile(path: string, filename: string, signal: AbortSignal) {
  const response = await fetch(path, { signal: AbortSignal.any([signal, AbortSignal.timeout(120000)]), credentials: 'same-origin' })
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    throw new ApiError(response.status, typeof body?.error === 'string' ? body.error.slice(0, 300) : '', retryAfterSeconds(response.headers.get('Retry-After')))
  }
  const blob = await response.blob()
  if (signal.aborted) return
  const fileUrl = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = fileUrl
  link.download = filename
  document.body.append(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(fileUrl), 1000)
}

export function describeError(error: unknown) {
  if (error instanceof ApiError) {
    if (error.status === 401) return '登录已失效，请重新登录。'
    if (error.status === 403) return '当前账号无权执行此操作。'
    if (error.status === 404) return '找不到此工作区或记录。'
    if (error.status === 413) return '文件超过导入大小限制。'
    if (error.status === 429) return error.retryAfterSeconds !== undefined ? `请求过于频繁，请在 ${error.retryAfterSeconds} 秒后重试。` : '请求过于频繁，请稍后重试。'
    if (error.status === 409) return error.message || '记录已被更新。请保留草稿，核对最新版本后重试。'
    if (error.status >= 500) return '服务暂时无法完成请求，请稍后重试。'
    return error.message || '无法完成请求，请检查输入。'
  }
  if (error instanceof DOMException && error.name === 'TimeoutError') return '请求等待超时。若刚才执行了保存，请先刷新核对结果，避免重复提交。'
  return error instanceof TypeError ? '无法连接服务，请检查网络后重试。' : '无法完成请求，请稍后重试。'
}

export const roleLabels: Record<string, string> = {
  owner: '负责人',
  operator: '经办人',
  reviewer: '复核人',
  viewer: '只读成员',
}

const dateFormat = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai',
  dateStyle: 'medium',
  timeStyle: 'short',
  hour12: false,
})

export function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? '—' : dateFormat.format(date)
}

export function providerLabel(provider: string): string {
  if (provider.startsWith('discovery:')) return '数据发现 · ' + providerLabel(provider.slice('discovery:'.length))
  if (provider === 'discovery') return '多源数据发现'
  if (provider === 'mysql') return 'MySQL'
  if (provider === 'mariadb') return 'MariaDB'
  if (provider === 'mongodb') return 'MongoDB'
  if (provider === 'cmdb') return 'CMDB 导入'
  if (provider === 'security-events' || provider === 'security_events') return '安全事件集成'
  if (provider === 'postgres_control') return 'PostgreSQL 权限执行器'

  if (provider === 'postgresql') return 'PostgreSQL'
  if (provider === 'openmetadata') return 'OpenMetadata'
  if (provider === 'openapi') return 'OpenAPI'
  if (provider === 'runtime-api') return 'API 运行时'
  return provider
}

export function documentationUrl(value: string) {
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && !url.username && !url.password ? url.href : undefined
  } catch {
    return undefined
  }
}
