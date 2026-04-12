import { createRouter, createWebHistory } from 'vue-router'

import PublicLayout from './layouts/PublicLayout.vue'
import WorkspaceLayout from './layouts/WorkspaceLayout.vue'
import HomePage from './pages/HomePage.vue'
import PlatformCapabilitiesPage from './pages/PlatformCapabilitiesPage.vue'
import LoginPage from './pages/LoginPage.vue'
import WorkspaceHomePage from './pages/WorkspaceHomePage.vue'
import ChatPage from './pages/ChatPage.vue'
import AssistantsCenterPage from './pages/AssistantsCenterPage.vue'
import WorkflowPage from './pages/WorkflowPage.vue'
import MonitorPage from './pages/MonitorPage.vue'
import ToolPage from './pages/ToolPage.vue'
import ProfilePage from './pages/ProfilePage.vue'
import AdminUsersPage from './pages/AdminUsersPage.vue'
import { me } from './lib/authApi'
import { hasPermission, type AppPermission } from './lib/permission'
import { currentUser, initAuthState, isAuthenticated, updateCurrentUser } from './lib/authStore'
import { setGlobalSelectedAgent } from './lib/agentSelection'
import type { AgentModel } from './types/chat'

let profileLoadedOnce = false

async function ensureProfileLoaded(): Promise<void> {
  if (!isAuthenticated.value || profileLoadedOnce) {
    return
  }
  const roles = currentUser.value?.roles ?? []
  if (roles.length > 0) {
    profileLoadedOnce = true
    return
  }
  const user = await me()
  updateCurrentUser(user)
  profileLoadedOnce = true
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      component: PublicLayout,
      meta: { public: true, zone: 'public' },
      children: [
        { path: '', name: 'public-home', component: HomePage },
        { path: 'capabilities', name: 'public-capabilities', component: PlatformCapabilitiesPage },
        { path: 'login', name: 'public-login', component: LoginPage },
      ],
    },
    {
      path: '/workspace',
      component: WorkspaceLayout,
      meta: { zone: 'workspace' },
      children: [
        { path: '', name: 'workspace-home', component: WorkspaceHomePage },
        { path: 'dialogue', name: 'workspace-dialogue', component: ChatPage },
        { path: 'assistants', name: 'workspace-assistants', component: AssistantsCenterPage },
        {
          path: 'assistants/:agentId',
          name: 'workspace-assistant-entry',
          redirect: (to) => {
            const agentId = String(to.params.agentId || '').trim()
            if (agentId) {
              setGlobalSelectedAgent(agentId as AgentModel)
            }
            return '/workspace/dialogue'
          },
        },
        { path: 'orchestration', name: 'workspace-orchestration', component: WorkflowPage },
        { path: 'monitoring', name: 'workspace-monitoring', component: MonitorPage },
        { path: 'tools', name: 'workspace-tools', component: ToolPage },
        { path: 'account', name: 'workspace-account', component: ProfilePage },
        {
          path: 'admin/users',
          name: 'workspace-admin-users',
          component: AdminUsersPage,
          meta: { requiredPermission: 'user.manage.all' },
        },
      ],
    },
    { path: '/dialogue', redirect: '/workspace/dialogue' },
    { path: '/assistants', redirect: '/workspace/assistants' },
    {
      path: '/assistants/:agentId',
      redirect: (to) => `/workspace/assistants/${encodeURIComponent(String(to.params.agentId || ''))}`,
    },
    { path: '/orchestration', redirect: '/workspace/orchestration' },
    { path: '/monitoring', redirect: '/workspace/monitoring' },
    { path: '/tools', redirect: '/workspace/tools' },
    { path: '/account', redirect: '/workspace/account' },
    { path: '/admin/users', redirect: '/workspace/admin/users' },
    { path: '/workflow', redirect: '/workspace/orchestration' },
    { path: '/monitor', redirect: '/workspace/monitoring' },
    { path: '/profile', redirect: '/workspace/account' },
    { path: '/register', redirect: '/login' },
  ],
})

router.beforeEach(async (to) => {
  initAuthState()
  const isPublicRoute = to.matched.some((record) => record.meta.public === true)

  if (isPublicRoute) {
    if (isAuthenticated.value && to.name === 'public-login') {
      return '/workspace'
    }
    return true
  }

  if (!isAuthenticated.value) {
    profileLoadedOnce = false
    return {
      path: '/login',
      query: {
        redirect: to.fullPath,
      },
    }
  }

  try {
    await ensureProfileLoaded()
  } catch {
    // keep compatibility when /me is temporarily unavailable
  }

  const requiredPermission = String(to.meta.requiredPermission || '').trim() as AppPermission | ''
  if (requiredPermission && !hasPermission(requiredPermission)) {
    return {
      path: '/workspace/account',
      query: {
        denied: '1',
        from: to.fullPath,
      },
    }
  }

  return true
})
