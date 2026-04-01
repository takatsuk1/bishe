import type { AgentModel } from '../types/chat'

export const DEFAULT_AGENT: AgentModel = 'host'

export const AGENTS: { label: string; value: AgentModel; description: string }[] = [
  { label: 'Host 编排', value: 'host', description: '负责分发/委派任务到各个专用 agent。' },
  { label: 'DeepResearch 深度检索', value: 'deepresearch', description: '进行多轮检索并综合输出（含引用）。' },
  { label: 'URL Reader 网页阅读', value: 'urlreader', description: '读取网页内容并生成摘要/回答（含引用）。' },
  { label: 'LBS Helper 出行助手', value: 'lbshelper', description: '面向出行/路线/地点相关问题的检索与规划。' },
  { label: 'Schedule Helper 日程规划', value: 'schedulehelper', description: '输出任务优先级和时间安排建议。' },
  { label: 'Finance Helper 财务优化', value: 'financehelper', description: '输出消费分析和节流方案。' },
  { label: 'Memo Reminder 备忘录', value: 'memoreminder', description: '结构化保存用户提醒并定时弹窗通知。' },
]

export function getAgentDescription(agentId: string): string {
  return AGENTS.find((agent) => agent.value === agentId)?.description ?? '用户发布的自定义 Agent。'
}
