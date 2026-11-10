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
  counts: {
    assets: number
    integrations: number
    activities: number
    checks: number
    draft: number
    inReview: number
    needsWork: number
    verified: number
  }
  recentAssets: Asset[]
  recentImports: ImportRun[]
}

export const sessionPath = '/api/v1/session'

export function workspacePath(slug: string) {
  return `/api/v1/workspaces/${encodeURIComponent(slug)}`
}

export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message)
  }
}

export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  headers.set('Accept', 'application/json')
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')
  const response = await fetch(path, { ...options, headers, credentials: 'same-origin' })

  if (response.status === 204) return undefined as T
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const message = typeof body?.error === 'string' ? body.error.slice(0, 300) : ''
    throw new ApiError(response.status, message)
  }
  if (body === null) throw new Error('服务返回的响应格式无效。')
  return body as T
}

export async function downloadFile(path: string, filename: string, signal: AbortSignal) {
  const response = await fetch(path, { signal, credentials: 'same-origin' })
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    throw new ApiError(response.status, typeof body?.error === 'string' ? body.error.slice(0, 300) : '')
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
    if (error.status >= 500) return '服务暂时无法完成请求，请稍后重试。'
    return error.message || '无法完成请求，请检查输入。'
  }
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

export function providerLabel(provider: string) {
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
