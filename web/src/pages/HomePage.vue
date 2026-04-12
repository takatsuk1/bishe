<script setup lang="ts">
import { RouterLink } from 'vue-router'
import PageHeader from '../components/PageHeader.vue'

type NavItem = {
  label: string
  href: string
}

type HeroMetric = {
  label: string
  value: string
}

type CapabilityCard = {
  title: string
  description: string
  eyebrow: string
  points: string[]
}

type FlowStep = {
  step: string
  title: string
  description: string
}

type PreviewCard = {
  key: string
  title: string
  description: string
  tags: string[]
  accent: string
  items: string[]
}

const navItems: NavItem[] = [
  { label: '首页', href: '#top' },
  { label: '平台能力', href: '#capabilities' },
]

const legacyHeroMetrics: HeroMetric[] = [
  { label: '智能体模板', value: '24+' },
  { label: '可编排节点', value: '60+' },
  { label: '运行事件采集', value: '99.9%' },
]

void legacyHeroMetrics

const heroMetrics: HeroMetric[] = [
  { label: '核心模块已接入', value: '6 个' },
  { label: '典型业务链路', value: '4 类' },
  { label: '统一工作入口', value: '1 套' },
]

const capabilityCards: CapabilityCard[] = [
  {
    eyebrow: 'Conversational AI',
    title: '智能对话',
    description: '统一承载多角色问答、任务协作与上下文记忆，让访客快速进入平台能力体验。',
    points: ['多模型接入', '上下文会话', '多助手分工'],
  },
  {
    eyebrow: 'Workflow Studio',
    title: '工作流编排',
    description: '通过可视化节点连接、参数配置和发布机制，把复杂业务流程沉淀为可复用资产。',
    points: ['拖拽式节点画布', '参数映射', '发布与版本管理'],
  },
  {
    eyebrow: 'Observability',
    title: '运行监控',
    description: '从任务状态、执行链路到失败原因统一追踪，帮助团队稳定运营每一次调用。',
    points: ['任务追踪', '步骤回放', '异常定位'],
  },
  {
    eyebrow: 'Tool Hub',
    title: '工具中心',
    description: '集中管理平台工具、输入输出协议和能力说明，形成可组合的企业级工具底座。',
    points: ['工具目录', '参数规范', '测试调试'],
  },
]

const flowSteps: FlowStep[] = [
  {
    step: '01',
    title: '定义助手与角色',
    description: '从对话入口快速搭建场景角色，让平台先具备可展示、可咨询、可执行的智能体验。',
  },
  {
    step: '02',
    title: '编排业务流程',
    description: '把模型、工具和规则串联成工作流，沉淀标准化执行路径与交付逻辑。',
  },
  {
    step: '03',
    title: '接入工具能力',
    description: '统一注册工具与参数规范，让工作流和助手都能调用同一套能力中心。',
  },
  {
    step: '04',
    title: '上线并持续监控',
    description: '通过监控视图追踪运行质量，持续优化提示词、流程节点与工具成功率。',
  },
]

const previewCards: PreviewCard[] = [
  {
    key: 'dialogue',
    title: '对话页预览',
    description: '多助手会话与任务推进并行展示。',
    tags: ['会话管理', '多助手', '上下文记忆'],
    accent: 'green',
    items: ['访客咨询接待', '方案问答生成', '执行进度反馈'],
  },
  {
    key: 'workflow',
    title: '编排页预览',
    description: '节点画布、参数面板与测试能力集中呈现。',
    tags: ['节点编排', '参数映射', '发布版本'],
    accent: 'purple',
    items: ['触发节点', '条件路由', '工具执行'],
  },
  {
    key: 'monitor',
    title: '监控页预览',
    description: '运行状态、链路日志与失败重试一屏掌控。',
    tags: ['实时状态', '链路追踪', '异常分析'],
    accent: 'amber',
    items: ['运行队列', '步骤事件', '失败告警'],
  },
  {
    key: 'tools',
    title: '工具页预览',
    description: '工具清单、调用协议与调试入口统一管理。',
    tags: ['工具目录', '协议说明', '在线调试'],
    accent: 'cyan',
    items: ['HTTP 工具', '数据库工具', '内部服务代理'],
  },
]
</script>

<template>
  <main id="top" class="landing-page">
    <section class="landing-shell">
      <header class="landing-nav glass-card">
        <RouterLink class="brand-mark" to="/">
          <span class="brand-mark__icon">多智</span>
          <span class="brand-mark__text">
            <strong>多智能体助手平台</strong>
            <small>AI Platform</small>
          </span>
        </RouterLink>

        <nav class="landing-nav__links">
          <a v-for="item in navItems" :key="item.label" :href="item.href">{{ item.label }}</a>
        </nav>

        <div class="landing-nav__actions">
          <RouterLink class="ghost-btn" to="/login">登录</RouterLink>
          <RouterLink class="btn-primary hero-cta" to="/login">立即体验</RouterLink>
        </div>
      </header>

      <section class="hero-section">
        <div class="hero-copy">
          <p class="section-eyebrow">Commercial AI Workspace</p>
          <h1>把内部智能系统，升级为可交付、可运营、可展示的商业化平台原型。</h1>
          <p class="hero-subtitle">
            以统一对话入口、可视化工作流、全链路监控和工具中心为核心，快速构建面向客户与团队的 AI 平台首页。
          </p>

          <PageHeader
            class="hero-copy__header"
            eyebrow="Commercial AI Workspace"
            title="把内部智能系统升级为可交付、可运营、可展示的商业化平台原型"
            description="以统一对话入口、可视化工作流、全链路监控和工具中心为核心，快速搭建面向客户与团队协作的 AI 平台官网首页。"
            :surface="false"
          />

          <div class="hero-actions">
            <RouterLink class="btn-primary hero-cta" to="/login">立即体验</RouterLink>
            <a class="ghost-btn ghost-btn--strong" href="#preview">查看平台预览</a>
          </div>

          <div class="hero-metrics">
            <article v-for="metric in heroMetrics" :key="metric.label" class="metric-card glass-card">
              <strong>{{ metric.value }}</strong>
              <span>{{ metric.label }}</span>
            </article>
          </div>
        </div>

        <div class="hero-visual">
          <article class="hero-window hero-window--main glass-card">
            <div class="hero-window__top">
              <span class="window-pill"></span>
              <span class="window-pill"></span>
              <span class="window-pill"></span>
            </div>
            <div class="hero-window__content">
              <div class="hero-dialogue">
                <div class="hero-dialogue__sidebar">
                  <span class="hero-chip hero-chip--soft">新建会话</span>
                  <span class="hero-line"></span>
                  <span class="hero-line hero-line--short"></span>
                  <span class="hero-line"></span>
                </div>
                <div class="hero-dialogue__main">
                  <div class="hero-bubble hero-bubble--user">请帮我生成一套企业知识助手上线方案</div>
                  <div class="hero-bubble hero-bubble--ai">
                    已基于工具中心和工作流编排生成平台方案，并同步创建监控看板。
                  </div>
                  <div class="hero-grid">
                    <span>助手路由</span>
                    <span>工具调用</span>
                    <span>执行回放</span>
                    <span>发布状态</span>
                  </div>
                </div>
              </div>
            </div>
          </article>

          <article class="hero-window hero-window--floating glass-card">
            <div class="preview-mini preview-mini--workflow">
              <div class="preview-mini__header">
                <strong>Workflow</strong>
                <span>v2.1</span>
              </div>
              <div class="preview-nodes">
                <span></span>
                <span></span>
                <span></span>
                <span></span>
              </div>
            </div>
          </article>

          <article class="hero-window hero-window--floating hero-window--bottom glass-card">
            <div class="preview-mini preview-mini--monitor">
              <div class="preview-mini__header">
                <strong>Monitor</strong>
                <span>Live</span>
              </div>
              <div class="preview-bars">
                <span></span>
                <span></span>
                <span></span>
              </div>
            </div>
          </article>
        </div>
      </section>

      <section id="capabilities" class="content-section">
        <div class="section-heading">
          <p class="section-eyebrow">Platform Capabilities</p>
          <h2>四类核心能力，构成商业化 AI 平台的完整展示面</h2>
          <p>保持统一的浅色玻璃卡片语言，让访客快速理解平台价值，也方便后续接入真实能力。</p>
        </div>

        <div class="capability-grid">
          <article v-for="card in capabilityCards" :key="card.title" class="capability-card glass-card">
            <p class="capability-card__eyebrow">{{ card.eyebrow }}</p>
            <h3>{{ card.title }}</h3>
            <p class="capability-card__desc">{{ card.description }}</p>
            <ul class="capability-card__points">
              <li v-for="point in card.points" :key="point">{{ point }}</li>
            </ul>
          </article>
        </div>
      </section>

      <section class="content-section">
        <div class="section-heading">
          <p class="section-eyebrow">Platform Workflow</p>
          <h2>从搭建到上线，四步完成平台化闭环</h2>
          <p>首页不仅承担品牌展示，也能直接解释你的系统如何落地、如何运转、如何持续优化。</p>
        </div>

        <div class="flow-grid">
          <article v-for="step in flowSteps" :key="step.step" class="flow-card glass-card">
            <span class="flow-card__step">{{ step.step }}</span>
            <h3>{{ step.title }}</h3>
            <p>{{ step.description }}</p>
          </article>
        </div>
      </section>

      <section id="preview" class="content-section">
        <div class="section-heading">
          <p class="section-eyebrow">Preview Gallery</p>
          <h2>关键页面缩略预览，一屏感知平台全貌</h2>
          <p>对话页、编排页、监控页、工具页采用统一预览卡结构，方便后续替换为真实截图或动态数据。</p>
        </div>

        <div class="preview-grid">
          <article
            v-for="card in previewCards"
            :key="card.key"
            class="preview-card glass-card"
            :class="`preview-card--${card.accent}`"
          >
            <div class="preview-card__top">
              <div>
                <h3>{{ card.title }}</h3>
                <p>{{ card.description }}</p>
              </div>
              <span class="preview-card__badge">{{ card.key }}</span>
            </div>

            <div class="preview-canvas">
              <div class="preview-canvas__sidebar">
                <span></span>
                <span></span>
                <span></span>
              </div>
              <div class="preview-canvas__panel">
                <div class="preview-canvas__toolbar"></div>
                <div class="preview-canvas__body">
                  <span v-for="item in card.items" :key="item">{{ item }}</span>
                </div>
              </div>
            </div>

            <div class="preview-card__tags">
              <span v-for="tag in card.tags" :key="tag">{{ tag }}</span>
            </div>
          </article>
        </div>
      </section>

      <footer class="landing-footer glass-card">
        <div>
          <strong>多智能体助手平台</strong>
          <p>从内部系统走向平台化产品，用统一首页承接品牌、能力与体验入口。</p>
        </div>
        <div class="landing-footer__links">
          <a href="#top">返回顶部</a>
          <a href="#capabilities">平台能力</a>
          <RouterLink to="/login">登录后台</RouterLink>
        </div>
      </footer>
    </section>
  </main>
</template>

<style scoped>
.landing-page {
  min-height: 100vh;
  padding: 24px;
}

.landing-shell {
  width: min(1280px, 100%);
  margin: 0 auto;
  display: grid;
  gap: 28px;
}

.glass-card {
  position: relative;
  border: 1px solid rgba(221, 225, 230, 0.92);
  border-radius: 28px;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.9), rgba(255, 255, 255, 0.82)),
    rgba(255, 255, 255, 0.76);
  box-shadow: 0 16px 38px rgba(48, 56, 68, 0.08);
  backdrop-filter: blur(16px);
}

.landing-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 16px 20px;
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

.brand-mark__text small {
  color: var(--text-muted);
  font-size: 12px;
}

.landing-nav__links,
.landing-nav__actions,
.hero-actions,
.landing-footer__links,
.preview-card__tags {
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

.ghost-btn:hover {
  transform: translateY(-1px);
  box-shadow: var(--shadow-soft);
  background: rgba(255, 255, 255, 0.96);
}

.ghost-btn--strong {
  background: rgba(255, 255, 255, 0.92);
}

.hero-section {
  display: grid;
  grid-template-columns: minmax(0, 1.02fr) minmax(0, 0.98fr);
  gap: 26px;
  align-items: center;
  padding: 8px 2px 6px;
}

.hero-copy {
  display: grid;
  gap: 18px;
}

.hero-copy > .section-eyebrow,
.hero-copy > h1,
.hero-copy > .hero-subtitle {
  display: none;
}

.hero-copy__header {
  max-width: 720px;
}

.hero-copy__header :deep(.page-header__title) {
  max-width: 10.5em;
  font-size: clamp(42px, 5vw, 68px);
  line-height: 1.03;
}

.hero-copy__header :deep(.page-header__desc) {
  max-width: 58ch;
  font-size: 15px;
}

.section-eyebrow {
  margin: 0;
  color: #5f8a78;
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.14em;
  font-weight: 700;
}

.hero-copy h1,
.section-heading h2 {
  margin: 0;
  font-family: var(--font-display);
  letter-spacing: -0.03em;
}

.hero-copy h1 {
  font-size: clamp(42px, 5vw, 68px);
  line-height: 1.03;
  max-width: 10.5em;
}

.hero-subtitle,
.section-heading p,
.capability-card__desc,
.flow-card p,
.preview-card__top p,
.landing-footer p {
  margin: 0;
  color: var(--text-muted);
  line-height: 1.7;
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

.hero-metrics {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
}

.metric-card {
  padding: 16px 18px;
}

.metric-card strong {
  display: block;
  font-family: var(--font-display);
  font-size: 28px;
  margin-bottom: 6px;
}

.metric-card span {
  color: var(--text-muted);
  font-size: 13px;
}

.hero-visual {
  position: relative;
  min-height: 560px;
  padding: 22px 10px 18px 24px;
}

.hero-window {
  overflow: hidden;
}

.hero-window--main {
  min-height: 520px;
  padding: 18px;
}

.hero-window--floating {
  position: absolute;
  right: -6px;
  top: 24px;
  width: 220px;
  padding: 14px;
}

.hero-window--bottom {
  top: auto;
  bottom: 8px;
  right: 28px;
  width: 240px;
}

.hero-window__top {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
}

.window-pill {
  width: 10px;
  height: 10px;
  border-radius: 999px;
  background: rgba(165, 172, 183, 0.42);
}

.hero-window__content,
.hero-dialogue,
.hero-dialogue__main {
  height: 100%;
}

.hero-dialogue {
  display: grid;
  grid-template-columns: 124px minmax(0, 1fr);
  gap: 14px;
}

.hero-dialogue__sidebar,
.preview-canvas__sidebar {
  border-radius: 22px;
  background: linear-gradient(180deg, rgba(248, 249, 251, 0.94), rgba(241, 244, 247, 0.92));
  border: 1px solid rgba(221, 225, 230, 0.95);
}

.hero-dialogue__sidebar {
  padding: 16px 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.hero-chip {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 36px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 700;
}

.hero-chip--soft {
  background: linear-gradient(120deg, rgba(204, 232, 220, 0.72), rgba(226, 216, 246, 0.66));
  color: #335248;
}

.hero-line {
  display: block;
  height: 12px;
  border-radius: 999px;
  background: rgba(204, 210, 220, 0.72);
}

.hero-line--short {
  width: 70%;
}

.hero-dialogue__main {
  border-radius: 26px;
  border: 1px solid rgba(221, 225, 230, 0.9);
  background: linear-gradient(180deg, rgba(255, 255, 255, 0.96), rgba(248, 249, 251, 0.92));
  padding: 18px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.hero-bubble {
  max-width: 88%;
  padding: 14px 16px;
  border-radius: 20px;
  line-height: 1.55;
  font-size: 14px;
}

.hero-bubble--user {
  align-self: flex-end;
  background: rgba(242, 247, 244, 0.98);
  border: 1px solid #d8e8df;
}

.hero-bubble--ai {
  align-self: flex-start;
  background: linear-gradient(132deg, rgba(204, 232, 220, 0.46), rgba(226, 216, 246, 0.42));
  border: 1px solid rgba(204, 210, 220, 0.74);
}

.hero-grid {
  margin-top: auto;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.hero-grid span,
.preview-canvas__body span {
  border-radius: 16px;
  border: 1px solid rgba(221, 225, 230, 0.95);
  background: rgba(255, 255, 255, 0.84);
  padding: 12px 14px;
  font-size: 13px;
  font-weight: 700;
}

.preview-mini {
  display: grid;
  gap: 14px;
}

.preview-mini__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.preview-mini__header strong {
  font-family: var(--font-display);
  font-size: 16px;
}

.preview-mini__header span {
  color: var(--text-muted);
  font-size: 12px;
}

.preview-nodes,
.preview-bars {
  display: grid;
  gap: 10px;
}

.preview-nodes span,
.preview-bars span {
  display: block;
  border-radius: 12px;
  background: linear-gradient(120deg, rgba(204, 232, 220, 0.64), rgba(226, 216, 246, 0.54));
}

.preview-nodes span {
  height: 44px;
}

.preview-bars span {
  height: 14px;
}

.preview-bars span:nth-child(2) {
  width: 82%;
}

.preview-bars span:nth-child(3) {
  width: 64%;
}

.content-section {
  display: grid;
  gap: 18px;
}

.section-heading {
  display: grid;
  gap: 10px;
  width: min(760px, 100%);
}

.section-heading h2 {
  font-size: clamp(30px, 3vw, 44px);
  line-height: 1.12;
}

.capability-grid,
.flow-grid,
.preview-grid {
  display: grid;
  gap: 16px;
}

.capability-grid {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.flow-grid {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.preview-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.capability-card,
.flow-card,
.preview-card,
.landing-footer {
  padding: 22px;
}

.capability-card {
  display: grid;
  gap: 12px;
}

.capability-card__eyebrow {
  margin: 0;
  font-size: 12px;
  color: #678977;
  text-transform: uppercase;
  letter-spacing: 0.12em;
  font-weight: 700;
}

.capability-card h3,
.flow-card h3,
.preview-card h3 {
  margin: 0;
  font-family: var(--font-display);
  font-size: 24px;
  line-height: 1.15;
}

.capability-card__points {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.capability-card__points li,
.preview-card__tags span,
.preview-card__badge {
  padding: 7px 10px;
  border-radius: 999px;
  background: rgba(247, 248, 250, 0.94);
  border: 1px solid rgba(221, 225, 230, 0.94);
  font-size: 12px;
  color: #4a5b6a;
}

.flow-card {
  display: grid;
  gap: 12px;
}

.flow-card__step {
  width: fit-content;
  padding: 8px 11px;
  border-radius: 999px;
  background: linear-gradient(120deg, rgba(204, 232, 220, 0.66), rgba(226, 216, 246, 0.58));
  font-family: var(--font-display);
  font-size: 14px;
  font-weight: 700;
}

.preview-card {
  display: grid;
  gap: 18px;
}

.preview-card__top {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
}

.preview-canvas {
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr);
  gap: 12px;
  min-height: 240px;
}

.preview-canvas__sidebar {
  padding: 14px 12px;
  display: grid;
  align-content: start;
  gap: 10px;
}

.preview-canvas__sidebar span,
.preview-canvas__toolbar {
  display: block;
  border-radius: 12px;
  background: rgba(201, 208, 219, 0.62);
}

.preview-canvas__sidebar span {
  height: 16px;
}

.preview-canvas__panel {
  border-radius: 22px;
  border: 1px solid rgba(221, 225, 230, 0.94);
  background: rgba(255, 255, 255, 0.86);
  padding: 14px;
  display: grid;
  grid-template-rows: 18px 1fr;
  gap: 12px;
}

.preview-canvas__body {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  align-content: start;
  gap: 10px;
}

.preview-card--green .preview-canvas__body span:nth-child(odd),
.preview-card--green .preview-card__badge {
  background: rgba(222, 243, 234, 0.96);
}

.preview-card--purple .preview-canvas__body span:nth-child(odd),
.preview-card--purple .preview-card__badge {
  background: rgba(235, 229, 250, 0.96);
}

.preview-card--amber .preview-canvas__body span:nth-child(odd),
.preview-card--amber .preview-card__badge {
  background: rgba(250, 238, 214, 0.98);
}

.preview-card--cyan .preview-canvas__body span:nth-child(odd),
.preview-card--cyan .preview-card__badge {
  background: rgba(222, 241, 246, 0.98);
}

.landing-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 8px;
}

@media (max-width: 1180px) {
  .hero-section,
  .capability-grid,
  .flow-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .hero-copy h1 {
    max-width: none;
  }
}

@media (max-width: 900px) {
  .landing-page {
    padding: 16px;
  }

  .landing-nav,
  .landing-footer,
  .hero-section,
  .preview-grid,
  .hero-metrics,
  .capability-grid,
  .flow-grid {
    grid-template-columns: 1fr;
  }

  .landing-nav,
  .landing-footer {
    position: static;
    flex-direction: column;
    align-items: stretch;
  }

  .landing-nav__links,
  .landing-nav__actions {
    justify-content: space-between;
  }

  .hero-visual {
    min-height: 520px;
    padding-inline: 0;
  }

  .hero-window--floating {
    position: absolute;
    right: 0;
  }
}

@media (max-width: 640px) {
  .hero-copy h1 {
    font-size: 38px;
  }

  .hero-dialogue,
  .preview-canvas {
    grid-template-columns: 1fr;
  }

  .hero-dialogue__sidebar,
  .preview-canvas__sidebar {
    min-height: auto;
  }

  .hero-window--main,
  .capability-card,
  .flow-card,
  .preview-card,
  .landing-footer {
    padding: 18px;
  }

  .hero-window--floating,
  .hero-window--bottom {
    position: static;
    width: 100%;
    margin-top: 12px;
  }

  .hero-visual {
    display: grid;
    gap: 12px;
    min-height: auto;
  }
}
</style>
