import { computed } from 'vue'
import { currentUser, type AuthUser } from './authStore'

export type AppRole = 'viewer' | 'user' | 'operator' | 'admin'
export type AppPermission =
  | 'workflow.read.own'
  | 'workflow.manage.own'
  | 'tool.read.own'
  | 'tool.manage.own'
  | 'tool.system.manage'
  | 'monitor.read.own'
  | 'monitor.read.all'
  | 'monitor.alert.manage'
  | 'user.manage.all'

const rolePriority: Record<AppRole, number> = {
  admin: 1,
  operator: 2,
  user: 3,
  viewer: 4,
}

const rolePermissions: Record<AppRole, AppPermission[]> = {
  viewer: ['workflow.read.own', 'tool.read.own', 'monitor.read.own'],
  user: ['workflow.read.own', 'workflow.manage.own', 'tool.read.own', 'tool.manage.own', 'monitor.read.own'],
  operator: [
    'workflow.read.own',
    'workflow.manage.own',
    'tool.read.own',
    'tool.manage.own',
    'monitor.read.own',
    'monitor.read.all',
    'monitor.alert.manage',
  ],
  admin: [
    'workflow.read.own',
    'workflow.manage.own',
    'tool.read.own',
    'tool.manage.own',
    'tool.system.manage',
    'monitor.read.own',
    'monitor.read.all',
    'monitor.alert.manage',
    'user.manage.all',
  ],
}

function normalizeRoles(input?: string[]): AppRole[] {
  const out: AppRole[] = []
  const seen = new Set<AppRole>()
  ;(input || []).forEach((value) => {
    const role = String(value || '').trim().toLowerCase() as AppRole
    if (!role || !(role in rolePriority) || seen.has(role)) {
      return
    }
    seen.add(role)
    out.push(role)
  })
  if (out.length === 0) {
    return ['viewer']
  }
  return out.sort((a, b) => rolePriority[a] - rolePriority[b])
}

export function getUserRoles(user?: AuthUser | null): AppRole[] {
  return normalizeRoles(user?.roles)
}

export function getPrimaryRole(user?: AuthUser | null): AppRole {
  const fromField = String(user?.primaryRole || '').trim().toLowerCase() as AppRole
  if (fromField && fromField in rolePriority) {
    return fromField
  }
  return getUserRoles(user)[0]
}

export function hasRole(role: AppRole, user?: AuthUser | null): boolean {
  return getUserRoles(user ?? currentUser.value).includes(role)
}

export function hasPermission(permission: AppPermission, user?: AuthUser | null): boolean {
  const roles = getUserRoles(user ?? currentUser.value)
  return roles.some((role) => rolePermissions[role].includes(permission))
}

export function isViewer(user?: AuthUser | null): boolean {
  return getPrimaryRole(user ?? currentUser.value) === 'viewer'
}

export function canManageUsers(user?: AuthUser | null): boolean {
  return hasPermission('user.manage.all', user)
}

export function canReadAllMonitor(user?: AuthUser | null): boolean {
  return hasPermission('monitor.read.all', user)
}

export function canManageMonitorAlerts(user?: AuthUser | null): boolean {
  return hasPermission('monitor.alert.manage', user)
}

export function canManageOwnWorkflow(user?: AuthUser | null): boolean {
  return hasPermission('workflow.manage.own', user)
}

export function canManageOwnTools(user?: AuthUser | null): boolean {
  return hasPermission('tool.manage.own', user)
}

export function canManageSystemTools(user?: AuthUser | null): boolean {
  return hasPermission('tool.system.manage', user)
}

export const currentRoles = computed(() => getUserRoles(currentUser.value))
export const currentPrimaryRole = computed(() => getPrimaryRole(currentUser.value))
