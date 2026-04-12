<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import PageContainer from '../components/PageContainer.vue'
import PageHeader from '../components/PageHeader.vue'
import { AGENTS } from '../lib/agents'

type AssistantCenterCard = {
  title: string
  agentId: string
  description: string
  tags: string[]
}

const searchKeyword = ref('')

const assistantCards = computed<AssistantCenterCard[]>(() => [
  {
    title: '主控编排助手',
    agentId: 'host',
    description: '统一分发与编排任务到各个系统助手，适合作为平台的总入口与协同中枢。',
    tags: ['总控分发', '任务编排', '协同入口'],
  },
  {
    title: '深度检索助手',
    agentId: 'deepresearch',
    description: '面向复杂问题进行联网检索、信息整合与来源归纳，适合研究型任务。',
    tags: ['联网检索', '研究归纳', '来源整理'],
  },
  {
    title: '网页阅读助手',
    agentId: 'urlreader',
    description: '读取网页内容并快速形成结构化摘要，帮助用户高效吸收页面信息。',
    tags: ['网页解析', '摘要提炼', '结构化输出'],
  },
  {
    title: '出行助手',
    agentId: 'lbshelper',
    description: '处理路线规划、地点查询与出行建议，适合位置相关服务场景。',
    tags: ['路线规划', '地点服务', '出行建议'],
  },
  {
    title: '日程规划助手',
    agentId: 'schedulehelper',
    description: '帮助用户梳理任务优先级、时间安排和执行节奏，适合个人效率场景。',
    tags: ['日程安排', '优先级', '执行节奏'],
  },
  {
    title: '财务助手',
    agentId: 'financehelper',
    description: '支持记账、报表理解、财经信息辅助与理财建议，适合财务管理场景。',
    tags: ['记账分析', '财经信息', '理财辅助'],
  },
  {
    title: '简历优化助手',
    agentId: 'resumecustomizer',
    description: '结合岗位要求与已有简历内容生成更有针对性的优化建议和表达方案。',
    tags: ['简历定制', '岗位匹配', '表达优化'],
  },
  {
    title: '面试模拟助手',
    agentId: 'interviewsimulator',
    description: '基于简历与岗位背景进行结构化面试模拟，帮助用户提前准备问答。',
    tags: ['模拟面试', '问答训练', '结构化反馈'],
  },
  {
    title: '职场雷达助手',
    agentId: 'careerradar',
    description: '帮助识别岗位风险、推荐匹配岗位并提供求职判断支持。',
    tags: ['岗位匹配', '风险识别', '求职辅助'],
  },
])

const filteredAssistants = computed(() => {
  const keyword = searchKeyword.value.trim().toLowerCase()
  if (!keyword) {
    return assistantCards.value
  }
  return assistantCards.value.filter((item) => {
    return (
      item.title.toLowerCase().includes(keyword) ||
      item.agentId.toLowerCase().includes(keyword) ||
      item.description.toLowerCase().includes(keyword) ||
      item.tags.some((tag) => tag.toLowerCase().includes(keyword))
    )
  })
})

const assistantCount = computed(() => assistantCards.value.length)
const filteredCount = computed(() => filteredAssistants.value.length)
const assistantMap = computed(() => new Map(AGENTS.map((agent) => [agent.value, agent.description])))
</script>

<template>
  <PageContainer mode="wide">
  <main class="assistant-center">
    <section class="assistant-center__hero module-section">
      <div class="assistant-center__hero-copy">
        <p class="assistant-center__eyebrow">Assistant Center</p>
        <h1>助手中心</h1>
        <p class="assistant-center__desc">
          统一展示当前平台已有的所有助手入口，以资源中心的方式帮助用户快速理解不同助手的职责与适用场景。
        </p>
      </div>

      <PageHeader
        class="assistant-center__hero-header"
        eyebrow="Assistant Center"
        title="助手中心"
        description="统一展示当前平台已有的全部助手入口，以平台资源中心的方式帮助用户快速理解不同助手的职责与适用场景。"
        :surface="false"
      />

      <div class="assistant-center__hero-stats">
        <article class="assistant-center__stat-card">
          <span>助手总数</span>
          <strong>{{ assistantCount }}</strong>
        </article>
        <article class="assistant-center__stat-card">
          <span>当前结果</span>
          <strong>{{ filteredCount }}</strong>
        </article>
      </div>
    </section>

    <section class="assistant-center__toolbar module-section">
      <div class="assistant-center__search">
        <label for="assistant-search">搜索助手</label>
        <input
          id="assistant-search"
          v-model="searchKeyword"
          type="search"
          placeholder="按助手名称、说明或标签搜索"
        />
      </div>
      <p class="assistant-center__toolbar-tip">点击卡片即可进入对应助手的对话控制台。</p>
    </section>

    <section class="assistant-center__grid">
      <article v-for="assistant in filteredAssistants" :key="assistant.agentId" class="assistant-center__card module-section">
        <div class="assistant-center__card-head">
          <div>
            <p class="assistant-center__card-id">{{ assistant.agentId }}</p>
            <h2>{{ assistant.title }}</h2>
          </div>
          <span class="assistant-center__card-badge">Assistant</span>
        </div>

        <p class="assistant-center__card-desc">
          {{ assistant.description || assistantMap.get(assistant.agentId) }}
        </p>

        <div class="assistant-center__card-preview">
          <div class="assistant-center__preview-top">
            <span></span>
            <span></span>
            <span></span>
          </div>
          <div class="assistant-center__preview-body">
            <aside class="assistant-center__preview-side">
              <span></span>
              <span></span>
              <span></span>
            </aside>
            <div class="assistant-center__preview-main">
              <div class="assistant-center__preview-hero"></div>
              <div class="assistant-center__preview-grid">
                <span></span>
                <span></span>
                <span></span>
              </div>
            </div>
          </div>
        </div>

        <div class="assistant-center__tags">
          <span v-for="tag in assistant.tags" :key="tag">{{ tag }}</span>
        </div>

        <RouterLink class="btn-primary assistant-center__action" :to="`/assistants/${encodeURIComponent(assistant.agentId)}`">
          进入助手
        </RouterLink>
      </article>
    </section>
  </main>
  </PageContainer>
</template>

<style scoped>
.assistant-center {
  width: 100%;
  margin: 0;
  display: grid;
  gap: 14px;
  animation: rise 0.26s ease;
}

.assistant-center__hero,
.assistant-center__toolbar,
.assistant-center__card {
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82)),
    var(--bg-panel);
}

.assistant-center__hero {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 280px;
  gap: 18px;
  align-items: center;
  background:
    radial-gradient(circle at 10% 12%, rgba(182, 225, 207, 0.2), transparent 28%),
    radial-gradient(circle at 92% 16%, rgba(220, 203, 244, 0.18), transparent 30%),
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82));
}

.assistant-center__hero-copy,
.assistant-center__hero-stats,
.assistant-center__search,
.assistant-center__card {
  display: grid;
  gap: 12px;
}

.assistant-center__hero-copy > .assistant-center__eyebrow,
.assistant-center__hero-copy > h1,
.assistant-center__hero-copy > .assistant-center__desc {
  display: none;
}

.assistant-center__hero-copy {
  display: none;
}

.assistant-center__hero-header {
  max-width: 700px;
  grid-column: 1 / 2;
  align-self: center;
}

.assistant-center__hero-header :deep(.page-header__title) {
  font-size: clamp(32px, 3.6vw, 42px);
}

.assistant-center__eyebrow {
  margin: 0;
  font-size: 11px;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: #5f8a78;
  font-weight: 700;
}

.assistant-center__hero h1,
.assistant-center__card h2 {
  margin: 0;
  font-family: var(--font-display);
  letter-spacing: -0.02em;
}

.assistant-center__hero h1 {
  font-size: clamp(34px, 4vw, 48px);
  line-height: 1.06;
}

.assistant-center__desc,
.assistant-center__toolbar-tip,
.assistant-center__card-desc {
  margin: 0;
  color: var(--text-muted);
  line-height: 1.7;
}

.assistant-center__hero-stats {
  grid-template-columns: 1fr;
}

.assistant-center__stat-card {
  border: 1px solid var(--line);
  border-radius: 18px;
  background: rgba(255, 255, 255, 0.78);
  padding: 14px 16px;
  display: grid;
  gap: 8px;
}

.assistant-center__stat-card span {
  color: var(--text-muted);
  font-size: 12px;
}

.assistant-center__stat-card strong {
  font-size: 28px;
  line-height: 1.1;
}

.assistant-center__toolbar {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.assistant-center__search {
  min-width: min(440px, 100%);
}

.assistant-center__search label {
  font-size: 13px;
  font-weight: 700;
}

.assistant-center__search input {
  min-height: 48px;
  border: 1px solid #d7dde3;
  border-radius: 14px;
  padding: 0 14px;
  background: rgba(251, 252, 253, 0.92);
  transition: border-color 0.2s ease, box-shadow 0.2s ease, background 0.2s ease;
}

.assistant-center__search input:focus {
  outline: none;
  border-color: #b8c0f0;
  box-shadow: 0 0 0 4px rgba(184, 192, 240, 0.16);
  background: #fff;
}

.assistant-center__grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 14px;
}

.assistant-center__card {
  padding: 18px;
  border-radius: 20px;
  box-shadow: var(--shadow-soft);
}

.assistant-center__card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.assistant-center__card-id,
.assistant-center__card-badge,
.assistant-center__tags span {
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
}

.assistant-center__card-preview {
  border: 1px solid var(--line);
  border-radius: 18px;
  background: linear-gradient(180deg, rgba(250, 251, 252, 0.96), rgba(245, 247, 249, 0.92));
  padding: 14px;
  display: grid;
  gap: 12px;
  min-height: 220px;
}

.assistant-center__preview-top {
  display: flex;
  gap: 8px;
}

.assistant-center__preview-top span {
  width: 10px;
  height: 10px;
  border-radius: 999px;
  background: rgba(165, 172, 183, 0.42);
}

.assistant-center__preview-body {
  display: grid;
  grid-template-columns: 84px minmax(0, 1fr);
  gap: 12px;
  min-height: 0;
}

.assistant-center__preview-side,
.assistant-center__preview-main,
.assistant-center__preview-grid {
  display: grid;
  gap: 10px;
}

.assistant-center__preview-side {
  align-content: start;
  border-radius: 16px;
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: rgba(255, 255, 255, 0.88);
  padding: 10px;
}

.assistant-center__preview-side span,
.assistant-center__preview-grid span {
  border-radius: 12px;
  background: rgba(197, 205, 216, 0.58);
}

.assistant-center__preview-side span {
  height: 16px;
}

.assistant-center__preview-main {
  grid-template-rows: 110px 1fr;
}

.assistant-center__preview-hero {
  border-radius: 16px;
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: linear-gradient(135deg, rgba(230, 245, 238, 0.94), rgba(239, 233, 250, 0.9));
}

.assistant-center__preview-grid {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.assistant-center__preview-grid span {
  min-height: 48px;
}

.assistant-center__tags {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.assistant-center__action {
  width: fit-content;
  text-decoration: none;
}

@media (max-width: 1180px) {
  .assistant-center__grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 820px) {
  .assistant-center__hero,
  .assistant-center__grid,
  .assistant-center__preview-body,
  .assistant-center__preview-grid {
    grid-template-columns: 1fr;
  }

  .assistant-center__toolbar {
    align-items: stretch;
  }

  .assistant-center__search {
    min-width: 100%;
  }
}
</style>
