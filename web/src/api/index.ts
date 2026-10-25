// REST API 封装（docs/06 §1：统一错误格式 / 分页格式）
export class ApiError extends Error {
  code: string
  status: number
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(`/api/v1${path}`, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  })
  if (resp.status === 401) {
    localStorage.removeItem('herd_auth_ok')
    if (!location.pathname.startsWith('/login')) {
      location.href = '/login'
    }
    throw new ApiError(401, 'unauthorized', '请先登录')
  }
  const data = await resp.json().catch(() => ({}))
  if (!resp.ok) {
    const err = (data as any)?.error
    throw new ApiError(resp.status, err?.code ?? 'unknown', err?.message ?? `HTTP ${resp.status}`)
  }
  return data as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body ?? {}),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body ?? {}),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body ?? {}),
  delete: <T>(path: string) => request<T>('DELETE', path),
}

// ---------- 领域类型（对应后端响应） ----------

export interface Page<T> {
  data: T[]
  page: number
  page_size: number
  total: number
}

export interface Node {
  id: string
  name: string
  hostname: string
  agent_version: string
  proto_version: string
  os_name: string
  os_version: string
  os_arch: string
  status: string
  enabled: boolean
  last_seen: number
  tags: string[]
  capabilities: string[]
  created_at: number
}

export interface Metric {
  node_id: string
  cpu: number
  mem_used: number
  mem_total: number
  disk_used: number
  disk_total: number
  net_rx: number
  net_tx: number
  load1: number
}

export interface AppRow {
  id: number
  node_id: string
  node_name: string
  probe_name: string
  app_name: string
  app_type: string
  version: string
  status: string
  detail: Record<string, any>
  detected_at: number
}

export interface Command {
  command_id: string
  node_id: string
  type: string
  payload: any
  status: string
  stdout?: string
  stderr?: string
  truncated: boolean
  exit_code?: number
  stages?: { name: string; status: string; detail?: string }[]
  error?: string
  timeout_s: number
  created_by: string
  created_at: number
  finished_at?: number
}

export interface ConfigTarget {
  id: number
  node_id: string
  app_type: string
  target_path: string
  created_at: number
  current_version_id: number
  last_apply_status: string
}

export interface ConfigVersion {
  id: number
  target_id: number
  content: string
  checksum: string
  status: string
  command_id: string
  apply_result: any
  created_by: string
  created_at: number
}

export interface AuditLog {
  id: number
  username: string
  action: string
  target: string
  detail: any
  ip: string
  created_at: number
}

export interface RegistrationToken {
  id: number
  note: string
  status: string
  node_id?: string
  expires_at: number
  created_at: number
}

// ---------- 常用时间/文本格式化 ----------

export function fmtTime(ts?: number): string {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false })
}

export function fmtAgo(ts?: number): string {
  if (!ts) return '从未'
  const diff = Math.floor(Date.now() / 1000) - ts
  if (diff < 60) return `${diff} 秒前`
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`
  return `${Math.floor(diff / 86400)} 天前`
}
