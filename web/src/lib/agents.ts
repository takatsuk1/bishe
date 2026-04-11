import type { AgentModel } from '../types/chat'

export const DEFAULT_AGENT: AgentModel = 'host'

export const AGENTS: { label: string; value: AgentModel; description: string }[] = [
  { label: '主控编排助手', value: 'host', description: '统一分发与编排任务到各个系统助手。' },
  { label: '深度检索助手', value: 'deepresearch', description: '进行联网检索并整理带来源的研究结论。' },
  { label: '网页阅读助手', value: 'urlreader', description: '读取网页内容并生成结构化摘要。' },
  { label: '出行助手', value: 'lbshelper', description: '处理路线规划与地点相关问题。' },
  { label: '日程规划助手', value: 'schedulehelper', description: '给出任务优先级与时间安排建议。' },
  { label: '财务助手', value: 'financehelper', description: '支持记账、报表、财经信息与理财建议。' },
  { label: '简历优化助手', value: 'resumecustomizer', description: '结合岗位与简历内容生成定制化简历。' },
  { label: '面试模拟助手', value: 'interviewsimulator', description: '基于简历进行结构化模拟面试。' },
  { label: '职场雷达助手', value: 'careerradar', description: '推荐匹配岗位并识别高风险岗位描述。' },
]

export function getAgentDescription(agentId: string): string {
  return AGENTS.find((agent) => agent.value === agentId)?.description ?? '用户发布的自定义 Agent。'
}
