import { createRouter, createWebHistory } from 'vue-router'

import ChatPage from './pages/ChatPage.vue'
import WorkflowPage from './pages/WorkflowPage.vue'
import ToolPage from './pages/ToolPage.vue'
import MonitorPage from './pages/MonitorPage.vue'
import LoginPage from './pages/LoginPage.vue'
import RegisterPage from './pages/RegisterPage.vue'
import ProfilePage from './pages/ProfilePage.vue'
import AdminUsersPage from './pages/AdminUsersPage.vue'
import { me } from './lib/authApi'
import { hasPermission, type AppPermission } from './lib/permission'
import { currentUser, initAuthState, isAuthenticated, updateCurrentUser } from './lib/authStore'

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
    { path: '/', name: 'chat', component: ChatPage },
    { path: '/workflow', name: 'workflow', component: WorkflowPage },
    { path: '/monitor', name: 'monitor', component: MonitorPage },
    { path: '/tools', name: 'tools', component: ToolPage },
    { path: '/profile', name: 'profile', component: ProfilePage },
    {
      path: '/admin/users',
      name: 'admin-users',
      component: AdminUsersPage,
      meta: {
        requiredPermission: 'user.manage.all',
      },
    },
    { path: '/login', name: 'login', component: LoginPage, meta: { public: true } },
    { path: '/register', name: 'register', component: RegisterPage, meta: { public: true } },
  ],
})

router.beforeEach(async (to) => {
  initAuthState()
  if (to.meta.public) {
    if (isAuthenticated.value && (to.path === '/login' || to.path === '/register')) {
      return '/'
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
      path: '/profile',
      query: {
        denied: '1',
        from: to.fullPath,
      },
    }
  }

  return true
})
