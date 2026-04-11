<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import { logout } from './lib/authApi'
import { currentUser, isAuthenticated } from './lib/authStore'
import { canManageUsers, currentPrimaryRole } from './lib/permission'
import { AGENTS } from './lib/agents'
import { listAgents } from './lib/orchestratorApi'
import { getGlobalSelectedAgent, setGlobalSelectedAgent } from './lib/agentSelection'
import type { AgentModel } from './types/chat'

const navCollapsed = ref(false)

const userLabel = computed(() => currentUser.value?.displayName || currentUser.value?.username || '访客')
const roleLabel = computed(() => currentPrimaryRole.value)

const availableAgents = ref<{ label: string; value: AgentModel; description: string }[]>([...AGENTS])
const selectedAgent = ref<AgentModel>(getGlobalSelectedAgent() || AGENTS[0]?.value || 'host')

async function handleLogout(): Promise<void> {
  await logout()
  window.location.href = '/login'
}

function toggleNav(): void {
  navCollapsed.value = !navCollapsed.value
}

function onSelectGlobalAgent(agent: AgentModel): void {
  selectedAgent.value = agent
  setGlobalSelectedAgent(agent)
}

async function loadAvailableAgents(): Promise<void> {
  try {
    const agents = await listAgents()
    if (agents.length > 0) {
      availableAgents.value = agents.map((agent) => ({
        label: agent.name || agent.id,
        value: agent.id,
        description: agent.description || '',
      }))
    } else {
      availableAgents.value = [...AGENTS]
    }
  } catch {
    availableAgents.value = [...AGENTS]
  }

  if (!availableAgents.value.some((agent) => agent.value === selectedAgent.value)) {
    selectedAgent.value = availableAgents.value[0]?.value || 'host'
    setGlobalSelectedAgent(selectedAgent.value)
  }
}

onMounted(() => {
  if (isAuthenticated.value) {
    void loadAvailableAgents()
  }
})
</script>

<template>
  <div class="app-shell" :class="{ 'nav-collapsed': navCollapsed }">
    <aside class="app-nav">
      <button class="nav-toggle" type="button" @click="toggleNav">
        {{ navCollapsed ? '展开' : '收起' }}
      </button>

      <div class="nav-head">
        <p class="nav-eyebrow">智能助手工作台</p>
        <h1 class="nav-title">mmmanus</h1>
      </div>

      <nav class="nav-links">
        <template v-if="isAuthenticated">
          <div class="nav-chat-group">
            <RouterLink to="/" class="nav-link">聊天</RouterLink>
            <ul class="nav-chat-subtitles">
              <li v-for="agent in availableAgents" :key="agent.value">
                <button
                  type="button"
                  class="nav-chat-subitem"
                  :class="{ active: selectedAgent === agent.value }"
                  @click="onSelectGlobalAgent(agent.value)"
                >
                  {{ agent.label }}
                </button>
              </li>
            </ul>
          </div>
          <RouterLink to="/workflow" class="nav-link">编排</RouterLink>
          <RouterLink to="/monitor" class="nav-link">监控</RouterLink>
          <RouterLink to="/tools" class="nav-link">工具</RouterLink>
          <RouterLink v-if="canManageUsers()" to="/admin/users" class="nav-link">用户管理</RouterLink>
          <RouterLink to="/profile" class="nav-link">账号</RouterLink>
        </template>
        <template v-else>
          <RouterLink to="/login" class="nav-link">登录</RouterLink>
          <RouterLink to="/register" class="nav-link">注册</RouterLink>
        </template>
      </nav>

      <div class="nav-foot" v-if="isAuthenticated">
        <p class="nav-user">{{ userLabel }}</p>
        <p class="nav-user">角色：{{ roleLabel }}</p>
        <button type="button" class="nav-logout" @click="handleLogout">退出登录</button>
      </div>
    </aside>

    <main class="app-main">
      <RouterView v-slot="{ Component, route: viewRoute }">
        <Transition name="tab-fade" mode="out-in">
          <component v-if="Component" :is="Component" :key="viewRoute.fullPath" />
        </Transition>
      </RouterView>
    </main>
  </div>
</template>
