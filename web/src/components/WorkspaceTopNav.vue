<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { logout } from '../lib/authApi'
import { currentUser } from '../lib/authStore'
import { canManageUsers, currentPrimaryRole } from '../lib/permission'

type WorkspaceNavItem = {
  label: string
  to: string
}

const router = useRouter()

const navItems: WorkspaceNavItem[] = [
  { label: '工作台', to: '/workspace' },
  { label: '对话', to: '/workspace/dialogue' },
  { label: '助手中心', to: '/workspace/assistants' },
  { label: '工作流编排', to: '/workspace/orchestration' },
  { label: '运行监控', to: '/workspace/monitoring' },
  { label: '工具中心', to: '/workspace/tools' },
  { label: '账户中心', to: '/workspace/account' },
]

const userLabel = computed(() => currentUser.value?.displayName || currentUser.value?.username || '访客')
const roleLabel = computed(() => currentPrimaryRole.value)

async function handleLogout(): Promise<void> {
  await logout()
  await router.replace('/login')
}
</script>

<template>
  <header class="workspace-top-nav">
    <div class="workspace-top-nav__inner">
      <RouterLink class="workspace-top-nav__brand" to="/workspace">
        <strong>多智能体助手平台</strong>
        <span>AI Workspace</span>
      </RouterLink>

      <nav class="workspace-top-nav__links">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="workspace-top-nav__link"
        >
          {{ item.label }}
        </RouterLink>
      </nav>

      <div class="workspace-top-nav__user">
        <RouterLink
          v-if="canManageUsers()"
          to="/workspace/admin/users"
          class="workspace-top-nav__utility"
        >
          用户管理
        </RouterLink>
        <div class="workspace-top-nav__user-meta">
          <strong>{{ userLabel }}</strong>
          <span>{{ roleLabel }}</span>
        </div>
        <button type="button" class="workspace-top-nav__logout" @click="handleLogout">退出</button>
      </div>
    </div>
  </header>
</template>

<style scoped>
.workspace-top-nav {
  position: sticky;
  top: 0;
  z-index: 30;
  padding: 14px var(--workspace-shell-gutter, 24px) 0;
}

.workspace-top-nav__inner {
  width: 100%;
  margin: 0 auto;
  border: 1px solid var(--line);
  border-radius: 22px;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.95), rgba(255, 255, 255, 0.9)),
    var(--bg-panel);
  backdrop-filter: blur(14px);
  box-shadow: var(--shadow-soft);
  padding: 10px 14px;
  display: grid;
  grid-template-columns: auto minmax(0, 1.25fr) auto;
  gap: 14px;
  align-items: center;
}

.workspace-top-nav__brand {
  display: grid;
  text-decoration: none;
  gap: 2px;
}

.workspace-top-nav__brand strong {
  font-family: var(--font-display);
  font-size: 24px;
  line-height: 1;
  letter-spacing: -0.02em;
}

.workspace-top-nav__brand span {
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.12em;
  color: #5f8a78;
  font-weight: 700;
}

.workspace-top-nav__links {
  display: grid;
  grid-template-columns: repeat(7, minmax(0, 1fr));
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.workspace-top-nav__link,
.workspace-top-nav__utility {
  border: 1px solid transparent;
  border-radius: 999px;
  padding: 11px 8px;
  text-decoration: none;
  font-size: 16px;
  font-weight: 700;
  text-align: center;
  white-space: nowrap;
  color: var(--text-muted);
  transition: background 0.2s ease, color 0.2s ease, border-color 0.2s ease, transform 0.2s ease;
}

.workspace-top-nav__link:hover,
.workspace-top-nav__utility:hover {
  transform: translateY(-1px);
  background: rgba(255, 255, 255, 0.72);
  border-color: rgba(207, 213, 220, 0.8);
  color: var(--text-main);
}

.workspace-top-nav__link.router-link-active,
.workspace-top-nav__utility.router-link-active {
  background: linear-gradient(120deg, rgba(204, 232, 220, 0.62), rgba(226, 216, 246, 0.56));
  border-color: #c9d3dc;
  color: var(--text-main);
}

.workspace-top-nav__user {
  display: flex;
  align-items: center;
  gap: 8px;
}

.workspace-top-nav__user-meta {
  display: grid;
  gap: 2px;
  padding: 0 2px;
}

.workspace-top-nav__user-meta strong {
  font-size: 15px;
  line-height: 1.2;
}

.workspace-top-nav__user-meta span {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.2;
}

.workspace-top-nav__logout {
  border: 1px solid #ead8dd;
  border-radius: 999px;
  background: rgba(255, 240, 240, 0.9);
  color: var(--danger-text);
  padding: 9px 14px;
  font-weight: 700;
  cursor: pointer;
}

@media (max-width: 1180px) {
  .workspace-top-nav__inner {
    grid-template-columns: 1fr;
    justify-items: stretch;
  }

  .workspace-top-nav__links {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }

  .workspace-top-nav__user {
    justify-content: space-between;
    flex-wrap: wrap;
  }
}

@media (max-width: 720px) {
  .workspace-top-nav {
    padding: 12px var(--workspace-shell-gutter, 12px) 0;
  }

  .workspace-top-nav__inner {
    padding: 12px;
  }

  .workspace-top-nav__links {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .workspace-top-nav__link,
  .workspace-top-nav__utility {
    padding: 9px 10px;
    font-size: 14px;
  }
}
</style>
