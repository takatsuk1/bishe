<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import PageContainer from '../components/PageContainer.vue'
import PageHeader from '../components/PageHeader.vue'
import { AGENTS } from '../lib/agents'
import { currentUser } from '../lib/authStore'
import { currentPrimaryRole, currentRoles } from '../lib/permission'

type QuickEntry = {
  title: string
  description: string
  route: string
  accent: 'green' | 'purple' | 'cyan' | 'amber'
  tags: string[]
}

type WorkspaceAssistantCard = {
  title: string
  agentId: string
  description: string
}

const quickEntries: QuickEntry[] = [
  {
    title: '对话控制台',
    description: '进入多轮对话与任务执行主入口，继续推进你的智能交互流程。',
    route: '/dialogue',
    accent: 'green',
    tags: ['会话入口', '任务推进'],
  },
  {
    title: '工作流编排',
    description: '查看和编辑可视化工作流，把平台能力沉淀为结构化流程。',
    route: '/orchestration',
    accent: 'purple',
    tags: ['节点画布', '流程配置'],
  },
  {
    title: '运行监控',
    description: '追踪任务状态、执行链路与异常信息，快速掌握平台运行情况。',
    route: '/monitoring',
    accent: 'amber',
    tags: ['状态监控', '链路回放'],
  },
  {
    title: '工具中心',
    description: '统一查看平台工具、调用协议与调试入口，管理可复用能力底座。',
    route: '/tools',
    accent: 'cyan',
    tags: ['工具目录', '在线调试'],
  },
]

const assistantCards = computed<WorkspaceAssistantCard[]>(() =>
  AGENTS.slice(0, 6).map((agent) => ({
    title: agent.label,
    agentId: agent.value,
    description: agent.description,
  })),
)

const welcomeName = computed(() => currentUser.value?.displayName || currentUser.value?.username || '用户')
const roleText = computed(() => (currentRoles.value.length > 0 ? currentRoles.value.join(' / ') : currentPrimaryRole.value))
</script>

<template>
  <PageContainer mode="wide">
  <main class="workspace-home">
    <section class="workspace-home__hero glass-card">
      <div class="workspace-home__hero-copy">
        <p class="workspace-home__eyebrow">Workspace Home</p>
        <h1>欢迎回来，{{ welcomeName }}</h1>
        <p class="workspace-home__desc">
          这里是登录后的工作台首页，用于统一分发对话、编排、监控、工具和助手入口，让你可以更快进入当前任务。
        </p>

        <PageHeader
          class="workspace-home__hero-header"
          eyebrow="Workspace Home"
          :title="`欢迎回来，${welcomeName}`"
          description="这里是登录后的统一工作台首页，用于分发对话、编排、监控、工具和助手入口，让你更快进入当前任务。"
          :surface="false"
        />

        <div class="workspace-home__meta">
          <article class="meta-card">
            <span>当前用户</span>
            <strong>{{ currentUser?.username || '--' }}</strong>
          </article>
          <article class="meta-card">
            <span>当前角色</span>
            <strong>{{ roleText }}</strong>
          </article>
        </div>
      </div>

      <div class="workspace-home__hero-panel">
        <div class="hero-panel">
          <div class="hero-panel__top">
            <span></span>
            <span></span>
            <span></span>
          </div>
          <div class="hero-panel__grid">
            <div v-for="entry in quickEntries" :key="entry.title" class="hero-panel__card" :class="`is-${entry.accent}`">
              <strong>{{ entry.title }}</strong>
              <span>{{ entry.tags[0] }}</span>
            </div>
          </div>
        </div>
      </div>
    </section>

    <section class="workspace-home__section">
      <div class="section-heading">
        <p class="workspace-home__eyebrow">Quick Access</p>
        <h2>从工作台首页快速进入核心模块</h2>
        <p>不承载复杂业务逻辑，只作为登录后的统一入口分发页。</p>
      </div>

      <div class="quick-entry-grid">
        <article
          v-for="entry in quickEntries"
          :key="entry.title"
          class="quick-entry-card glass-card"
          :class="`quick-entry-card--${entry.accent}`"
        >
          <div class="quick-entry-card__head">
            <h3>{{ entry.title }}</h3>
            <span>{{ entry.accent }}</span>
          </div>
          <p>{{ entry.description }}</p>
          <div class="quick-entry-card__tags">
            <span v-for="tag in entry.tags" :key="tag">{{ tag }}</span>
          </div>
          <RouterLink class="btn-primary quick-entry-card__action" :to="entry.route">进入模块</RouterLink>
        </article>
      </div>
    </section>

    <section class="workspace-home__section">
      <div class="section-heading">
        <p class="workspace-home__eyebrow">Assistant Overview</p>
        <h2>已有助手概览</h2>
        <p>展示当前项目中已接入的助手角色，方便从工作台统一理解能力分工。</p>
      </div>

      <div class="assistant-grid">
        <article v-for="assistant in assistantCards" :key="assistant.agentId" class="assistant-card glass-card">
          <p class="assistant-card__id">{{ assistant.agentId }}</p>
          <h3>{{ assistant.title }}</h3>
          <p>{{ assistant.description }}</p>
          <RouterLink class="assistant-card__link" :to="`/assistants/${encodeURIComponent(assistant.agentId)}`">
            打开助手
          </RouterLink>
        </article>
      </div>
    </section>

    <section class="workspace-home__section">
      <article class="platform-note glass-card">
        <p class="workspace-home__eyebrow">Platform Note</p>
        <h2>平台说明</h2>
        <p>
          该工作台首页聚合了对话控制台、工作流编排、运行监控、工具中心与助手体系，
          目的是让用户在进入系统后先看到能力分布与常用入口，再进入具体模块开展工作。
        </p>
      </article>
    </section>
  </main>
  </PageContainer>
</template>

<style scoped>
.workspace-home {
  width: 100%;
  margin: 0;
  display: grid;
  gap: 16px;
  animation: rise 0.26s ease;
}

.glass-card {
  border: 1px solid var(--line);
  border-radius: 24px;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82)),
    var(--bg-panel);
  box-shadow: var(--shadow-soft);
  backdrop-filter: blur(14px);
}

.workspace-home__hero {
  padding: 22px;
  display: grid;
  grid-template-columns: minmax(0, 1.12fr) minmax(320px, 0.88fr);
  gap: 18px;
  align-items: center;
  background:
    radial-gradient(circle at 10% 12%, rgba(182, 225, 207, 0.22), transparent 30%),
    radial-gradient(circle at 90% 14%, rgba(220, 203, 244, 0.22), transparent 32%),
    var(--bg-panel);
}

.workspace-home__hero-copy,
.section-heading {
  display: grid;
  gap: 10px;
}

.workspace-home__hero-copy > .workspace-home__eyebrow,
.workspace-home__hero-copy > h1,
.workspace-home__hero-copy > .workspace-home__desc {
  display: none;
}

.workspace-home__hero-header {
  max-width: 720px;
}

.workspace-home__hero-header :deep(.page-header__title) {
  font-size: clamp(34px, 4vw, 48px);
  line-height: 1.06;
}

.workspace-home__eyebrow {
  margin: 0;
  font-size: 11px;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: #5f8a78;
  font-weight: 700;
}

.workspace-home__hero-copy h1,
.section-heading h2,
.quick-entry-card h3,
.assistant-card h3,
.platform-note h2 {
  margin: 0;
  font-family: var(--font-display);
  letter-spacing: -0.02em;
}

.workspace-home__hero-copy h1 {
  font-size: clamp(34px, 4vw, 48px);
  line-height: 1.06;
}

.workspace-home__desc,
.section-heading p,
.quick-entry-card p,
.assistant-card p,
.platform-note p {
  margin: 0;
  color: var(--text-muted);
  line-height: 1.7;
}

.workspace-home__meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  margin-top: 6px;
}

.meta-card {
  border: 1px solid var(--line);
  border-radius: 18px;
  background: rgba(255, 255, 255, 0.74);
  padding: 14px 16px;
  display: grid;
  gap: 6px;
}

.meta-card span {
  color: var(--text-muted);
  font-size: 12px;
}

.meta-card strong {
  font-size: 18px;
}

.hero-panel {
  border: 1px solid var(--line);
  border-radius: 24px;
  background: rgba(255, 255, 255, 0.84);
  padding: 16px;
  display: grid;
  gap: 14px;
}

.hero-panel__top {
  display: flex;
  gap: 8px;
}

.hero-panel__top span {
  width: 10px;
  height: 10px;
  border-radius: 999px;
  background: rgba(165, 172, 183, 0.42);
}

.hero-panel__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.hero-panel__card {
  min-height: 112px;
  border-radius: 20px;
  border: 1px solid rgba(221, 225, 230, 0.94);
  padding: 16px;
  display: grid;
  align-content: space-between;
}

.hero-panel__card strong {
  font-size: 16px;
}

.hero-panel__card span {
  color: #4d5a67;
  font-size: 12px;
  font-weight: 700;
}

.hero-panel__card.is-green {
  background: linear-gradient(135deg, rgba(230, 245, 238, 0.95), rgba(255, 255, 255, 0.9));
}

.hero-panel__card.is-purple {
  background: linear-gradient(135deg, rgba(239, 233, 250, 0.96), rgba(255, 255, 255, 0.9));
}

.hero-panel__card.is-cyan {
  background: linear-gradient(135deg, rgba(228, 244, 247, 0.96), rgba(255, 255, 255, 0.9));
}

.hero-panel__card.is-amber {
  background: linear-gradient(135deg, rgba(251, 242, 224, 0.96), rgba(255, 255, 255, 0.9));
}

.workspace-home__section {
  display: grid;
  gap: 14px;
}

.quick-entry-grid,
.assistant-grid {
  display: grid;
  gap: 14px;
}

.quick-entry-grid {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.assistant-grid {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.quick-entry-card,
.assistant-card,
.platform-note {
  padding: 18px;
}

.quick-entry-card {
  display: grid;
  gap: 12px;
}

.quick-entry-card__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.quick-entry-card__head span,
.quick-entry-card__tags span,
.assistant-card__id {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: fit-content;
  min-height: 28px;
  padding: 0 10px;
  border-radius: 999px;
  border: 1px solid rgba(221, 225, 230, 0.94);
  background: rgba(255, 255, 255, 0.78);
  color: #4a5b6a;
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
}

.quick-entry-card__tags {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.quick-entry-card__action {
  width: fit-content;
  text-decoration: none;
}

.quick-entry-card--green {
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82)),
    linear-gradient(135deg, rgba(230, 245, 238, 0.82), rgba(255, 255, 255, 0.9));
}

.quick-entry-card--purple {
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82)),
    linear-gradient(135deg, rgba(239, 233, 250, 0.84), rgba(255, 255, 255, 0.9));
}

.quick-entry-card--amber {
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82)),
    linear-gradient(135deg, rgba(251, 242, 224, 0.84), rgba(255, 255, 255, 0.9));
}

.quick-entry-card--cyan {
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82)),
    linear-gradient(135deg, rgba(228, 244, 247, 0.84), rgba(255, 255, 255, 0.9));
}

.assistant-card {
  display: grid;
  gap: 10px;
}

.assistant-card__link {
  color: #0f5f4a;
  font-weight: 700;
  text-decoration: none;
}

.assistant-card__link:hover {
  text-decoration: underline;
}

.platform-note {
  display: grid;
  gap: 10px;
}

@media (max-width: 1180px) {
  .workspace-home__hero,
  .quick-entry-grid,
  .assistant-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 840px) {
  .workspace-home__hero,
  .quick-entry-grid,
  .assistant-grid,
  .workspace-home__meta,
  .hero-panel__grid {
    grid-template-columns: 1fr;
  }
}
</style>
