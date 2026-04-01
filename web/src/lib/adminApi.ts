import { getAccessToken } from './authStore'
import { refreshSession } from './authApi'

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://127.0.0.1:11000'
const ADMIN_API_BASE_URL = import.meta.env.VITE_ADMIN_API_BASE_URL ?? 'http://127.0.0.1:8080'

function candidateBaseURLs(): string[] {
  const values = [
    ADMIN_API_BASE_URL,
    API_BASE_URL,
    'http://127.0.0.1:8080',
    'http://127.0.0.1:11000',
  ]
  const out: string[] = []
  const seen = new Set<string>()
  values.forEach((value) => {
    const item = String(value || '').trim()
    if (!item || seen.has(item)) {
      return
    }
    seen.add(item)
    out.push(item)
  })
  return out
}

async function requestJson<T>(
  path: string,
  init?: RequestInit,
  retry = true,
): Promise<T> {
  const bases = candidateBaseURLs()
  let lastError: unknown = null

  for (const baseURL of bases) {
    try {
      const token = getAccessToken()
      const res = await fetch(`${baseURL}${path}`, {
        ...init,
        headers: {
          'Content-Type': 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
          ...(init?.headers ?? {}),
        },
      })

      if (res.status === 401 && retry) {
        try {
          await refreshSession()
          return requestJson<T>(path, init, false)
        } catch {
          // continue and surface original unauthorized response below
        }
      }

      if (!res.ok) {
        const text = await res.text().catch(() => '')
        lastError = new Error(text || `请求失败（${res.status}）`)
        // 404 is commonly caused by hitting openai_connector port without admin routes.
        if (res.status === 404) {
          continue
        }
        throw lastError as Error
      }

      return (await res.json()) as T
    } catch (err) {
      lastError = err
      continue
    }
  }

  if (lastError instanceof Error) {
    throw lastError
  }
  throw new Error('请求失败（admin API 不可达）')
}

export interface AdminUserItem {
  userId: string
  username: string
  displayName: string
  status: number
  roles: string[]
  primaryRole: string
}

export async function listAdminUsers(): Promise<AdminUserItem[]> {
  const data = await requestJson<{ items?: AdminUserItem[] }>('/v1/admin/users')
  return Array.isArray(data.items) ? data.items : []
}

export async function updateAdminUserStatus(userId: string, enabled: boolean): Promise<void> {
  await requestJson(`/v1/admin/users/${encodeURIComponent(userId)}/status`, {
    method: 'PUT',
    body: JSON.stringify({ enabled }),
  })
}

export async function updateAdminUserRoles(userId: string, roles: string[]): Promise<void> {
  await requestJson(`/v1/admin/users/${encodeURIComponent(userId)}/roles`, {
    method: 'PUT',
    body: JSON.stringify({ roles }),
  })
}
