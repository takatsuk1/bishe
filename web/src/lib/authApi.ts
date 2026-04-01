import { clearAuthSession, currentUser, getAccessToken, getRefreshToken, setAuthSession, updateCurrentUser, type AuthUser } from './authStore'

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://127.0.0.1:11000'

interface TokenPair {
  accessToken: string
  refreshToken: string
  expiresIn: number
}

interface AuthResponse {
  user?: AuthUser
  tokens?: TokenPair
  roles?: string[]
  primaryRole?: string
  error?: string
}

interface AuthPayload {
  username: string
  password: string
  displayName?: string
}

async function postAuth(path: string, payload: object): Promise<AuthResponse> {
  const res = await fetch(`${API_BASE_URL}${path}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  })
  const data = (await res.json().catch(() => ({}))) as AuthResponse
  if (!res.ok) {
    throw new Error(data.error || `请求失败（${res.status}）`)
  }
  return data
}

function assertAuthResponse(data: AuthResponse): { user: AuthUser; tokens: TokenPair } {
  if (!data.user || !data.tokens) {
    throw new Error(data.error || '认证响应缺少用户或令牌')
  }
  return { user: parseAuthUser(data), tokens: data.tokens }
}

function parseAuthUser(data: AuthResponse): AuthUser {
  if (!data.user) {
    throw new Error(data.error || '认证响应缺少用户信息')
  }
  const roles = Array.isArray(data.roles) && data.roles.length > 0
    ? data.roles
    : Array.isArray(data.user.roles)
      ? data.user.roles
      : []
  return {
    ...data.user,
    roles,
    primaryRole: data.primaryRole || data.user.primaryRole || '',
  }
}

export async function register(payload: AuthPayload): Promise<AuthUser> {
  const data = await postAuth('/v1/auth/register', payload)
  const parsed = assertAuthResponse(data)
  setAuthSession(parsed.user, parsed.tokens.accessToken, parsed.tokens.refreshToken)
  return parsed.user
}

export async function login(payload: AuthPayload): Promise<AuthUser> {
  const data = await postAuth('/v1/auth/login', payload)
  const parsed = assertAuthResponse(data)
  setAuthSession(parsed.user, parsed.tokens.accessToken, parsed.tokens.refreshToken)
  return parsed.user
}

export async function refreshSession(): Promise<void> {
  const refreshToken = getRefreshToken()
  if (!refreshToken) {
    throw new Error('缺少刷新令牌')
  }
  const data = await postAuth('/v1/auth/refresh', { refreshToken })
  const parsed = assertAuthResponse(data)
  setAuthSession(parsed.user, parsed.tokens.accessToken, parsed.tokens.refreshToken)
}

export async function me(): Promise<AuthUser> {
  const accessToken = getAccessToken()
  if (!accessToken) {
    throw new Error('未登录')
  }
  const res = await fetch(`${API_BASE_URL}/v1/auth/me`, {
    headers: {
      Authorization: `Bearer ${accessToken}`,
    },
  })
  const data = (await res.json().catch(() => ({}))) as AuthResponse
  if (!res.ok || !data.user) {
    throw new Error(data.error || `请求失败（${res.status}）`)
  }
  return parseAuthUser(data)
}

export async function logout(): Promise<void> {
  const accessToken = getAccessToken()
  if (accessToken) {
    await fetch(`${API_BASE_URL}/v1/auth/logout`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${accessToken}`,
      },
    }).catch(() => undefined)
  }
  clearAuthSession()
}

export async function updateProfile(displayName: string): Promise<AuthUser> {
  const accessToken = getAccessToken()
  const res = await fetch(`${API_BASE_URL}/v1/auth/profile`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${accessToken}`,
    },
    body: JSON.stringify({ displayName }),
  })
  const data = (await res.json().catch(() => ({}))) as AuthResponse
  if (!res.ok || !data.user) {
    throw new Error(data.error || `请求失败（${res.status}）`)
  }
  const mergedUser: AuthUser = {
    ...data.user,
    roles: currentUser.value?.roles ?? [],
    primaryRole: currentUser.value?.primaryRole ?? '',
  }
  updateCurrentUser(mergedUser)
  return mergedUser
}

export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  const accessToken = getAccessToken()
  const res = await fetch(`${API_BASE_URL}/v1/auth/change-password`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${accessToken}`,
    },
    body: JSON.stringify({ currentPassword, newPassword }),
  })
  const data = (await res.json().catch(() => ({}))) as AuthResponse
  if (!res.ok) {
    throw new Error(data.error || `请求失败（${res.status}）`)
  }
}
