import { computed, ref } from 'vue'

export interface AuthUser {
  userId: string
  username: string
  displayName?: string
  roles?: string[]
  primaryRole?: string
}

const ACCESS_TOKEN_KEY = 'mmmanus.auth.accessToken.v1'
const REFRESH_TOKEN_KEY = 'mmmanus.auth.refreshToken.v1'
const USER_KEY = 'mmmanus.auth.user.v1'

const accessTokenRef = ref('')
const refreshTokenRef = ref('')
const userRef = ref<AuthUser | null>(null)
const initializedRef = ref(false)

export const currentUser = computed(() => userRef.value)
export const isAuthenticated = computed(() => Boolean(accessTokenRef.value && userRef.value))

function normalizeRoles(roles?: string[]): string[] {
  if (!Array.isArray(roles)) {
    return []
  }
  const seen = new Set<string>()
  const normalized: string[] = []
  roles.forEach((role) => {
    const code = String(role || '').trim().toLowerCase()
    if (!code || seen.has(code)) {
      return
    }
    seen.add(code)
    normalized.push(code)
  })
  return normalized
}

function pickPrimaryRole(roles: string[]): string {
  if (roles.length === 0) {
    return ''
  }
  const priority: Record<string, number> = { admin: 1, operator: 2, user: 3, viewer: 4 }
  let best = roles[0]
  let score = Number.MAX_SAFE_INTEGER
  roles.forEach((role) => {
    const next = priority[role] ?? 50
    if (next < score) {
      best = role
      score = next
    }
  })
  return best
}

function normalizeAuthUser(user: AuthUser): AuthUser {
  const roles = normalizeRoles(user.roles)
  const primaryRole = String(user.primaryRole || '').trim().toLowerCase() || pickPrimaryRole(roles)
  return {
    ...user,
    roles,
    primaryRole,
  }
}

export function initAuthState(): void {
  if (initializedRef.value) {
    return
  }
  initializedRef.value = true
  if (typeof window === 'undefined') {
    return
  }

  accessTokenRef.value = window.localStorage.getItem(ACCESS_TOKEN_KEY) ?? ''
  refreshTokenRef.value = window.localStorage.getItem(REFRESH_TOKEN_KEY) ?? ''
  const rawUser = window.localStorage.getItem(USER_KEY)
  if (rawUser) {
    try {
      userRef.value = normalizeAuthUser(JSON.parse(rawUser) as AuthUser)
    } catch {
      userRef.value = null
    }
  }
}

export function getAccessToken(): string {
  return accessTokenRef.value
}

export function getRefreshToken(): string {
  return refreshTokenRef.value
}

export function setAuthSession(user: AuthUser, accessToken: string, refreshToken: string): void {
  const normalizedUser = normalizeAuthUser(user)
  userRef.value = normalizedUser
  accessTokenRef.value = accessToken
  refreshTokenRef.value = refreshToken

  if (typeof window === 'undefined') {
    return
  }
  window.localStorage.setItem(ACCESS_TOKEN_KEY, accessToken)
  window.localStorage.setItem(REFRESH_TOKEN_KEY, refreshToken)
  window.localStorage.setItem(USER_KEY, JSON.stringify(normalizedUser))
}

export function clearAuthSession(): void {
  userRef.value = null
  accessTokenRef.value = ''
  refreshTokenRef.value = ''

  if (typeof window === 'undefined') {
    return
  }
  window.localStorage.removeItem(ACCESS_TOKEN_KEY)
  window.localStorage.removeItem(REFRESH_TOKEN_KEY)
  window.localStorage.removeItem(USER_KEY)
}

export function updateCurrentUser(user: AuthUser): void {
  const normalizedUser = normalizeAuthUser(user)
  userRef.value = normalizedUser
  if (typeof window === 'undefined') {
    return
  }
  window.localStorage.setItem(USER_KEY, JSON.stringify(normalizedUser))
}
