<script setup lang="ts">
import { RouterLink } from 'vue-router'
import PageHeader from '../components/PageHeader.vue'

type CapabilityModule = {
  key: string
  title: string
  subtitle: string
  description: string
  route: string
  accent: 'green' | 'purple' | 'cyan' | 'amber'
  highlights: string[]
  preview: {
    layout: 'chat' | 'assistant' | 'workflow' | 'monitor' | 'tools' | 'account'
    stats: string[]
  }
}

const capabilityModules: CapabilityModule[] = [
  {
    key: 'dialogue',
    title: '对话控制台',
    subtitle: 'Conversation Console',
    description: '承载访客咨询、任务发起与多轮上下文对话，是平台体验最直接的统一入口。',
    route: '/dialogue',
    accent: 'green',
    highlights: ['会话管理', '消息流式响应', '任务进度可视化'],
    preview: {
      layout: 'chat',
      stats: ['多轮会话', '上下文连续', '执行反馈'],
    },
  },
  {
    key: 'assistants',
    title: '助手中心',
    subtitle: 'Assistant Hub',
    description: '集中展示不同助手角色与能力说明，支持从统一入口切换适合当前任务的智能助手。',
    route: '/assistants/host',
    accent: 'purple',
    highlights: ['角色切换', '能力说明', '统一助手入口'],
    preview: {
      layout: 'assistant',
      stats: ['主助手', '专业助手', '角色分工'],
    },
  },
  {
    key: 'workflow',
    title: '工作流编排',
    subtitle: 'Workflow Orchestration',
    description: '通过可视化节点画布、参数面板和测试能力，把复杂流程沉淀成可复用工作流。',
    route: '/orchestration',
    accent: 'cyan',
    highlights: ['节点连接', '参数配置', '发布管理'],
    preview: {
      layout: 'workflow',
      stats: ['节点画布', '参数映射', '测试发布'],
    },
  },
  {
    key: 'monitoring',
    title: '运行监控',
    subtitle: 'Runtime Monitoring',
    description: '聚合运行状态、执行链路和异常信息，让平台从可用走向可运营、可追踪。',
    route: '/monitoring',
    accent: 'amber',
    highlights: ['状态追踪', '链路回放', '异常排查'],
    preview: {
      layout: 'monitor',
      stats: ['运行队列', '步骤日志', '异常告警'],
    },
  },
  {
    key: 'tools',
    title: '工具中心',
    subtitle: 'Tool Center',
    description: '统一管理工具清单、调用协议和调试能力，为对话与工作流提供稳定的工具底座。',
    route: '/tools',
    accent: 'green',
    highlights: ['工具注册', '协议管理', '在线调试'],
    preview: {
      layout: 'tools',
      stats: ['工具目录', '输入输出', '调试入口'],
    },
  },
  {
    key: 'account',
    title: '账户中心',
    subtitle: 'Account Center',
    description: '提供个人资料维护、密码更新与访问状态承接，完善平台基础账户体验。',
    route: '/account',
    accent: 'purple',
    highlights: ['资料维护', '密码管理', '访问承接'],
    preview: {
      layout: 'account',
      stats: ['个人资料', '密码安全', '角色信息'],
    },
  },
]

const capabilitySummary = [
  { label: '已接入模块', value: '6' },
  { label: '统一体验入口', value: '1' },
  { label: '公共展示风格', value: 'Light Glass' },
]
</script>

<template>
  <main class="capability-page">
    <section class="capability-shell">
      <header class="landing-nav glass-card">
        <RouterLink class="brand-mark" to="/">
          <span class="brand-mark__icon">多智</span>
          <span class="brand-mark__text">
            <strong>多智能体助手平台</strong>
            <small>AI Platform</small>
          </span>
        </RouterLink>

        <nav class="landing-nav__links">
          <RouterLink to="/">首页</RouterLink>
          <RouterLink class="is-active" to="/capabilities">平台能力</RouterLink>
        </nav>

        <div class="landing-nav__actions">
          <RouterLink class="ghost-btn" to="/login">登录</RouterLink>
          <RouterLink class="btn-primary hero-cta" to="/login">立即体验</RouterLink>
        </div>
      </header>

      <section class="capability-hero glass-card">
        <div class="capability-hero__copy">
          <p class="section-eyebrow">Platform Capabilities</p>
          <h1>聚焦现有模块，把内部系统能力清晰地展示给外部访客。</h1>
          <p class="capability-hero__desc">
            这一页不新增任何功能，只把你当前已经具备的对话、助手、编排、监控、工具与账户能力，
            以统一的商业化官网语言重新组织与呈现。
          </p>

          <PageHeader
            class="capability-hero__header"
            eyebrow="Platform Capabilities"
            title="聚焦现有模块，把内部系统能力清晰地展示给外部访客"
            description="这一页不新增任何功能，只把当前已经具备的对话、助手、编排、监控、工具与账户能力，以统一的商业化平台语言重新组织与呈现。"
            :surface="false"
          />

          <div class="capability-hero__actions">
            <RouterLink class="btn-primary hero-cta" to="/login">登录体验后台</RouterLink>
            <RouterLink class="ghost-btn ghost-btn--strong" to="/">返回官网首页</RouterLink>
          </div>

          <div class="summary-grid">
            <article v-for="item in capabilitySummary" :key="item.label" class="summary-card">
              <strong>{{ item.value }}</strong>
              <span>{{ item.label }}</span>
            </article>
          </div>
        </div>

        <div class="capability-hero__panel">
          <div class="hero-overview">
            <div class="hero-overview__top">
              <span></span>
              <span></span>
              <span></span>
            </div>
            <div class="hero-overview__body">
              <article
                v-for="item in capabilityModules"
                :key="item.key"
                class="hero-overview__module"
                :class="`hero-overview__module--${item.accent}`"
              >
                <strong>{{ item.title }}</strong>
                <small>{{ item.subtitle }}</small>
              </article>
            </div>
          </div>
        </div>
      </section>

      <section class="section-heading">
        <p class="section-eyebrow">Capability Matrix</p>
        <h2>六个现有模块，组成平台对外展示的完整能力矩阵</h2>
        <p>每个能力卡都只映射当前系统中的真实页面，用预览占位与简述帮助访客快速理解模块职责。</p>
      </section>

      <section class="module-grid">
        <article
          v-for="module in capabilityModules"
          :key="module.key"
          class="module-card glass-card"
          :class="`module-card--${module.accent}`"
        >
          <div class="module-card__header">
            <div>
              <p class="module-card__subtitle">{{ module.subtitle }}</p>
              <h3>{{ module.title }}</h3>
            </div>
            <span class="module-card__badge">{{ module.key }}</span>
          </div>

          <p class="module-card__desc">{{ module.description }}</p>

          <div class="module-preview" :class="`module-preview--${module.preview.layout}`">
            <div class="module-preview__chrome">
              <span></span>
              <span></span>
              <span></span>
            </div>

            <div class="module-preview__body">
              <template v-if="module.preview.layout === 'chat'">
                <div class="preview-chat__sidebar">
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
                <div class="preview-chat__content">
                  <div class="preview-bubble preview-bubble--user"></div>
                  <div class="preview-bubble preview-bubble--assistant"></div>
                  <div class="preview-row">
                    <span></span>
                    <span></span>
                    <span></span>
                  </div>
                </div>
              </template>

              <template v-else-if="module.preview.layout === 'assistant'">
                <div class="preview-list">
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
                <div class="preview-stack">
                  <span></span>
                  <span></span>
                </div>
              </template>

              <template v-else-if="module.preview.layout === 'workflow'">
                <div class="preview-nodes">
                  <span></span>
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
                <div class="preview-links">
                  <span></span>
                  <span></span>
                </div>
              </template>

              <template v-else-if="module.preview.layout === 'monitor'">
                <div class="preview-metrics">
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
                <div class="preview-log">
                  <span></span>
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
              </template>

              <template v-else-if="module.preview.layout === 'tools'">
                <div class="preview-tools">
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
                <div class="preview-form">
                  <span></span>
                  <span></span>
                </div>
              </template>

              <template v-else>
                <div class="preview-account">
                  <span></span>
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
              </template>
            </div>
          </div>

          <div class="module-card__footer">
            <div class="module-card__highlights">
              <span v-for="highlight in module.highlights" :key="highlight">{{ highlight }}</span>
            </div>
            <RouterLink class="ghost-btn ghost-btn--small" :to="module.route">进入模块</RouterLink>
          </div>
        </article>
      </section>

      <footer class="landing-footer glass-card">
        <div>
          <strong>模块展示页</strong>
          <p>适合作为官网二级页，用于说明平台当前已经具备的页面与能力，而不是描绘未来功能。</p>
        </div>
        <div class="landing-footer__links">
          <RouterLink to="/">首页</RouterLink>
          <RouterLink to="/capabilities">平台能力</RouterLink>
          <RouterLink to="/login">登录后台</RouterLink>
        </div>
      </footer>
    </section>
  </main>
</template>

<style scoped>
.capability-page {
  min-height: 100vh;
  padding: 24px;
}

.capability-shell {
  width: min(1280px, 100%);
  margin: 0 auto;
  display: grid;
  gap: 24px;
}

.glass-card {
  border: 1px solid rgba(221, 225, 230, 0.92);
  border-radius: 28px;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82)),
    rgba(255, 255, 255, 0.76);
  box-shadow: 0 16px 38px rgba(48, 56, 68, 0.08);
  backdrop-filter: blur(16px);
}

.landing-nav,
.landing-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 16px 20px;
}

.landing-nav {
  position: sticky;
  top: 18px;
  z-index: 10;
}

.brand-mark {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  text-decoration: none;
}

.brand-mark__icon {
  width: 44px;
  height: 44px;
  border-radius: 14px;
  display: grid;
  place-items: center;
  font-family: var(--font-display);
  font-weight: 800;
  letter-spacing: -0.04em;
  background: linear-gradient(135deg, rgba(204, 232, 220, 0.95), rgba(226, 216, 246, 0.9));
  color: #234338;
}

.brand-mark__text {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.brand-mark__text strong {
  font-family: var(--font-display);
  font-size: 18px;
}

.brand-mark__text small,
.module-card__subtitle,
.module-card__desc,
.landing-footer p,
.section-heading p,
.capability-hero__desc {
  color: var(--text-muted);
}

.landing-nav__links,
.landing-nav__actions,
.capability-hero__actions,
.landing-footer__links,
.module-card__highlights,
.module-card__footer {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.landing-nav__links a,
.landing-footer__links a {
  text-decoration: none;
  color: var(--text-main);
  font-weight: 600;
}

.landing-nav__links .is-active {
  color: #335248;
}

.ghost-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 42px;
  padding: 0 16px;
  border-radius: 999px;
  border: 1px solid var(--line);
  background: rgba(255, 255, 255, 0.78);
  color: var(--text-main);
  text-decoration: none;
  font-weight: 700;
  transition: transform 0.2s ease, box-shadow 0.2s ease, background 0.2s ease;
}

.ghost-btn:hover,
.module-card:hover {
  transform: translateY(-1px);
}

.ghost-btn:hover {
  box-shadow: var(--shadow-soft);
  background: rgba(255, 255, 255, 0.96);
}

.ghost-btn--strong {
  background: rgba(255, 255, 255, 0.92);
}

.ghost-btn--small {
  min-height: 40px;
}

.hero-cta {
  min-height: 48px;
  padding-inline: 20px;
  border-radius: 999px;
  text-decoration: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}

.capability-hero {
  padding: 28px;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 380px;
  gap: 24px;
  align-items: center;
}

.capability-hero__copy,
.section-heading {
  display: grid;
  gap: 12px;
}

.capability-hero__copy > .section-eyebrow,
.capability-hero__copy > h1,
.capability-hero__copy > .capability-hero__desc {
  display: none;
}

.capability-hero__header {
  max-width: 720px;
}

.capability-hero__header :deep(.page-header__title) {
  max-width: 11em;
  font-size: clamp(34px, 4vw, 50px);
}

.section-eyebrow {
  margin: 0;
  color: #5f8a78;
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.14em;
  font-weight: 700;
}

.capability-hero h1,
.section-heading h2,
.module-card h3 {
  margin: 0;
  font-family: var(--font-display);
  letter-spacing: -0.03em;
}

.capability-hero h1 {
  font-size: clamp(38px, 5vw, 60px);
  line-height: 1.04;
}

.capability-hero__desc,
.section-heading p,
.module-card__desc,
.landing-footer p {
  margin: 0;
  line-height: 1.7;
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  margin-top: 8px;
}

.summary-card {
  border-radius: 20px;
  border: 1px solid rgba(221, 225, 230, 0.9);
  background: rgba(255, 255, 255, 0.68);
  padding: 16px;
}

.summary-card strong {
  display: block;
  font-family: var(--font-display);
  font-size: 24px;
  margin-bottom: 6px;
}

.summary-card span {
  color: var(--text-muted);
  font-size: 13px;
}

.capability-hero__panel {
  min-height: 420px;
}

.hero-overview {
  height: 100%;
  border-radius: 26px;
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: linear-gradient(180deg, rgba(250, 251, 252, 0.94), rgba(245, 247, 249, 0.9));
  padding: 16px;
  display: grid;
  grid-template-rows: auto 1fr;
  gap: 14px;
}

.hero-overview__top {
  display: flex;
  gap: 8px;
}

.hero-overview__top span,
.module-preview__chrome span {
  width: 10px;
  height: 10px;
  border-radius: 999px;
  background: rgba(165, 172, 183, 0.42);
}

.hero-overview__body {
  display: grid;
  gap: 12px;
}

.hero-overview__module {
  border-radius: 20px;
  padding: 16px;
  border: 1px solid rgba(221, 225, 230, 0.9);
  background: rgba(255, 255, 255, 0.88);
}

.hero-overview__module strong {
  display: block;
  font-size: 16px;
  margin-bottom: 4px;
}

.hero-overview__module small {
  color: var(--text-muted);
}

.hero-overview__module--green {
  background: linear-gradient(135deg, rgba(230, 245, 238, 0.95), rgba(255, 255, 255, 0.88));
}

.hero-overview__module--purple {
  background: linear-gradient(135deg, rgba(239, 233, 250, 0.96), rgba(255, 255, 255, 0.88));
}

.hero-overview__module--cyan {
  background: linear-gradient(135deg, rgba(228, 244, 247, 0.96), rgba(255, 255, 255, 0.88));
}

.hero-overview__module--amber {
  background: linear-gradient(135deg, rgba(251, 242, 224, 0.96), rgba(255, 255, 255, 0.88));
}

.module-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 18px;
}

.module-card {
  padding: 22px;
  display: grid;
  gap: 16px;
  transition: transform 0.2s ease, box-shadow 0.2s ease;
}

.module-card:hover {
  box-shadow: 0 20px 44px rgba(48, 56, 68, 0.11);
}

.module-card__header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
}

.module-card__subtitle {
  margin: 0 0 6px;
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.14em;
  font-weight: 700;
}

.module-card__badge,
.module-card__highlights span {
  border-radius: 999px;
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: rgba(255, 255, 255, 0.8);
  padding: 7px 10px;
  font-size: 12px;
  color: #4a5b6a;
}

.module-preview {
  border-radius: 24px;
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: linear-gradient(180deg, rgba(250, 251, 252, 0.96), rgba(245, 247, 249, 0.92));
  padding: 14px;
  min-height: 260px;
  display: grid;
  grid-template-rows: auto 1fr;
  gap: 14px;
}

.module-card--green .module-preview,
.module-card--green .module-card__badge {
  background-color: rgba(230, 245, 238, 0.6);
}

.module-card--purple .module-preview,
.module-card--purple .module-card__badge {
  background-color: rgba(239, 233, 250, 0.62);
}

.module-card--cyan .module-preview,
.module-card--cyan .module-card__badge {
  background-color: rgba(228, 244, 247, 0.62);
}

.module-card--amber .module-preview,
.module-card--amber .module-card__badge {
  background-color: rgba(251, 242, 224, 0.62);
}

.module-preview__chrome {
  display: flex;
  gap: 8px;
}

.module-preview__body {
  display: grid;
  gap: 12px;
}

.preview-chat__sidebar,
.preview-list,
.preview-stack,
.preview-nodes,
.preview-metrics,
.preview-tools,
.preview-account,
.preview-log,
.preview-form,
.preview-links,
.preview-row,
.preview-chat__content {
  display: grid;
  gap: 10px;
}

.preview-chat__sidebar,
.preview-list,
.preview-stack,
.preview-nodes span,
.preview-metrics span,
.preview-tools span,
.preview-account span,
.preview-log span,
.preview-form span,
.preview-links span,
.preview-row span {
  border-radius: 14px;
  background: rgba(197, 205, 216, 0.58);
}

.preview-chat__body,
.preview-chat__content {
  min-height: 0;
}

.module-preview--chat .module-preview__body {
  grid-template-columns: 96px minmax(0, 1fr);
}

.preview-chat__sidebar {
  padding: 10px;
  align-content: start;
}

.preview-chat__sidebar span {
  height: 18px;
}

.preview-chat__content {
  align-content: start;
}

.preview-bubble {
  border-radius: 18px;
  height: 44px;
}

.preview-bubble--user {
  margin-left: auto;
  width: 72%;
  background: rgba(216, 237, 227, 0.9);
}

.preview-bubble--assistant {
  width: 86%;
  background: rgba(231, 226, 246, 0.92);
}

.preview-row {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.preview-row span {
  height: 54px;
}

.module-preview--assistant .module-preview__body,
.module-preview--tools .module-preview__body {
  grid-template-columns: 1fr 1fr;
}

.preview-list span,
.preview-stack span,
.preview-tools span,
.preview-form span {
  height: 48px;
}

.module-preview--workflow .module-preview__body {
  grid-template-columns: 1fr 100px;
}

.preview-nodes {
  grid-template-columns: repeat(2, minmax(0, 1fr));
  align-content: start;
}

.preview-nodes span {
  height: 64px;
}

.preview-links span {
  height: 12px;
}

.module-preview--monitor .module-preview__body {
  grid-template-rows: auto 1fr;
}

.preview-metrics {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.preview-metrics span {
  height: 56px;
}

.preview-log span {
  height: 18px;
}

.module-preview--account .module-preview__body {
  align-content: start;
}

.preview-account span {
  height: 44px;
}

.module-card__footer {
  justify-content: space-between;
}

.landing-footer p {
  margin-top: 6px;
}

@media (max-width: 1080px) {
  .capability-hero,
  .module-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 720px) {
  .capability-page {
    padding: 16px;
  }

  .landing-nav,
  .landing-footer {
    position: static;
    flex-direction: column;
    align-items: stretch;
  }

  .summary-grid,
  .module-preview--chat .module-preview__body,
  .module-preview--assistant .module-preview__body,
  .module-preview--workflow .module-preview__body,
  .module-preview--tools .module-preview__body {
    grid-template-columns: 1fr;
  }
}
</style>
