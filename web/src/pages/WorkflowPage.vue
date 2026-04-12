<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  deleteWorkflow,
  getAgentWorkflows,
  getWorkflow,
  listAgents,
  listWorkflows,
  putWorkflow,
  saveWorkflowToDB,
} from '../lib/orchestratorApi'
import {
  testWorkflow,
  createUserAgent,
  publishUserAgent,
  stopUserAgent,
  restartUserAgent,
  listUserAgents,
  type ExecutionResult,
  type UserAgent,
} from '../lib/userAgentApi'
import {
  listTools,
  type ToolInfo,
} from '../lib/orchestratorApi'
import type {
  AgentInfo,
  AgentWorkflowDetail,
  DraftWorkflowSummary,
  EdgeDefinition,
  LoopConfig,
  NodeDefinition,
  NodeType,
  WorkflowDefinition,
  WorkflowSummary,
} from '../types/workflow'
import { canManageOwnWorkflow, currentPrimaryRole } from '../lib/permission'
import PageContainer from '../components/PageContainer.vue'
import PageHeader from '../components/PageHeader.vue'
import ModuleSectionCard from '../components/ModuleSectionCard.vue'

type PendingLink =
  | { fromNodeId: string; kind: 'next' }
  | { fromNodeId: string; kind: 'condition'; branch: 'true' | 'false' }
  | { fromNodeId: string; kind: 'loop'; branch: 'body' | 'loop' | 'break' | 'exit' }

const EXECUTABLE_NODE_TYPES: NodeType[] = ['chat_model', 'tool']

function isExecutableNodeType(type: NodeType): boolean {
  return EXECUTABLE_NODE_TYPES.includes(type)
}

function hasSingleOutput(type: NodeType): boolean {
  return type === 'start' || type === 'end' || isExecutableNodeType(type)
}

const savedWorkflows = ref<WorkflowSummary[]>([])
const draftWorkflows = ref<DraftWorkflowSummary[]>([])
const agents = ref<AgentInfo[]>([])
const agentWorkflows = ref<AgentWorkflowDetail[]>([])
const tools = ref<ToolInfo[]>([])

const activeWorkflowId = ref('')
const workflow = ref<WorkflowDefinition | null>(null)
const updatedAt = ref('')
const isDraft = ref(false)
const ttlMinutes = ref(0)

const selectedNodeId = ref<string>('')
const selectedEdgeIndex = ref<number | null>(null)
const pendingLink = ref<PendingLink | null>(null)
const inspectorCollapsed = ref(false)
const workflowCanvasRef = ref<HTMLDivElement | null>(null)

// Agent测试和发布相关状态
const showAgentTester = ref(false)
const testInput = ref('{\n  "query": "测试输入"\n}')
const testResult = ref<ExecutionResult | null>(null)
const testing = ref(false)
const publishing = ref(false)
const currentAgent = ref<UserAgent | null>(null)
const userAgents = ref<UserAgent[]>([])
const agentTestError = ref('')
const pageError = ref('')

const selectedNode = computed(() => {
  if (!workflow.value || !selectedNodeId.value) {
    return null
  }
  return workflow.value.nodes.find((n) => n.id === selectedNodeId.value) ?? null
})

const readOnlyMode = computed(() => !canManageOwnWorkflow())
const roleLabel = computed(() => currentPrimaryRole.value)

const startNodeId = computed(() => workflow.value?.startNodeId ?? '')
const currentWorkflowDisplayName = computed(() => workflow.value?.name?.trim() || workflow.value?.id || '未命名工作流')
const currentWorkflowSavedAt = computed(() => {
  if (isDraft.value) {
    return ttlMinutes.value > 0 ? `草稿剩余 ${ttlMinutes.value} 分钟` : '草稿未保存到正式列表'
  }
  if (updatedAt.value) {
    return `已保存：${new Date(updatedAt.value).toLocaleString()}`
  }
  return '尚未保存'
})

function getDelegateTarget(node: NodeDefinition): string {
  return node.metadata?.['set.agent_name'] ?? ''
}

function getDelegateText(node: NodeDefinition): string {
  return node.metadata?.['set.text'] ?? ''
}

function setDelegateTarget(nodeId: string, target: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const metadata: Record<string, string> = { ...(n.metadata ?? {}) }
    metadata['set.agent_name'] = target
    return { ...n, metadata }
  })
  saveDraft()
}

function setDelegateText(nodeId: string, text: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const metadata: Record<string, string> = { ...(n.metadata ?? {}) }
    metadata['set.text'] = text
    return { ...n, metadata }
  })
  saveDraft()
}

function uuid(): string {
  return crypto.randomUUID()
}

function normalizeNodeDefaults(node: NodeDefinition): NodeDefinition {
  const config = (node.config ?? {}) as Record<string, unknown>
  const cfgInputSource = typeof config['input_source'] === 'string' ? String(config['input_source']) : ''
  const normalizedInputSource = node.type === 'condition' || node.type === 'loop'
    ? 'previous'
    : (node.inputSource || cfgInputSource || 'previous')
  const normalized: NodeDefinition = {
    ...node,
    inputType: node.inputType || 'string',
    outputType: node.outputType || 'string',
    inputSource: normalizedInputSource as 'previous' | 'history',
  }

  if (normalized.type === 'tool') {
    normalized.agentId = undefined
    normalized.taskType = undefined
  }

  return normalized
}

function toBackendNodeDefinition(node: NodeDefinition): NodeDefinition {
  const next: NodeDefinition = {
    ...node,
    config: { ...(node.config ?? {}) },
  }

  if (next.type === 'condition') {
    delete (next.config as Record<string, unknown>)['left_type']
    delete (next.config as Record<string, unknown>)['left_value']
  }

  if ((next.type === 'condition' || next.type === 'loop') && next.config?.['input_source']) {
    delete (next.config as Record<string, unknown>)['input_source']
  }

  if ((next.type === 'condition' || next.type === 'loop') && next.inputSource) {
    delete (next as Partial<NodeDefinition>).inputSource
  }

  if (next.type !== 'condition' && next.type !== 'loop' && next.inputSource && !next.config?.['input_source']) {
    ;(next.config as Record<string, unknown>)['input_source'] = next.inputSource
  }

  if (next.type === 'chat_model') {
    if (next.outputType && !next.config?.['output_type']) {
      ;(next.config as Record<string, unknown>)['output_type'] = next.outputType
    }
    if (next.inputType && !next.config?.['input_type']) {
      ;(next.config as Record<string, unknown>)['input_type'] = next.inputType
    }
  }

  if (next.config && Object.keys(next.config).length === 0) {
    delete next.config
  }

  return next
}

function toBackendWorkflowDefinition(def: WorkflowDefinition): WorkflowDefinition {
  return {
    ...def,
    nodes: def.nodes.map((n) => toBackendNodeDefinition(n)),
  }
}

function getUiAgent(node: NodeDefinition): string {
  return node.metadata?.['ui.agent'] ?? ''
}

function getUiLabel(node: NodeDefinition): string {
  return node.metadata?.['ui.label'] ?? ''
}

function setUiAgent(nodeId: string, agent: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const metadata: Record<string, string> = { ...(n.metadata ?? {}) }
    metadata['ui.agent'] = agent
    return { ...n, metadata }
  })
  saveDraft()
}

function setUiLabel(nodeId: string, label: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const metadata: Record<string, string> = { ...(n.metadata ?? {}) }
    metadata['ui.label'] = label
    return { ...n, metadata }
  })
  saveDraft()
}

function getNodeConfig(node: NodeDefinition, key: string): string {
  const config = node.config as Record<string, unknown> | undefined
  return (config?.[key] as string) ?? ''
}

function getNodeConfigRecord(node: NodeDefinition): Record<string, unknown> {
  return { ...(node.config as Record<string, unknown> ?? {}) }
}

function getConditionConfig(node: NodeDefinition): Record<string, string> {
  const cfg = getNodeConfigRecord(node)
  const normalizeConditionType = (raw: unknown, fallback: 'string' | 'bool' = 'string'): 'string' | 'bool' => {
    const v = String(raw ?? '').toLowerCase().trim()
    if (v === 'bool') return 'bool'
    if (v === 'string' || v === 'const' || v === 'path') return 'string'
    return fallback
  }
  return {
    left_type: 'string',
    left_value: '__previous_output__',
    operator: String(cfg['operator'] ?? 'eq'),
    right_type: normalizeConditionType(cfg['right_type'], 'string'),
    right_value: String(cfg['right_value'] ?? ''),
  }
}

function getNodeInputSource(node: NodeDefinition): 'previous' | 'history' {
  if (node.type === 'condition' || node.type === 'loop') {
    return 'previous'
  }
  const fromNode = String(node.inputSource ?? '').trim().toLowerCase()
  if (fromNode === 'history') {
    return 'history'
  }
  const fromConfig = String(getNodeConfigRecord(node)['input_source'] ?? '').trim().toLowerCase()
  if (fromConfig === 'history') {
    return 'history'
  }
  return 'previous'
}

function setNodeInputSource(nodeId: string, source: 'previous' | 'history'): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    if (n.type === 'condition' || n.type === 'loop') {
      return { ...n, inputSource: 'previous' }
    }
    const config = getNodeConfigRecord(n)
    config['input_source'] = source
    return { ...n, inputSource: source, config }
  })
  saveDraft()
}

function setConditionConfig(nodeId: string, key: string, value: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const config = getNodeConfigRecord(n)
    config[key] = value
    return { ...n, config }
  })
  saveDraft()
}

function setNodeConfig(nodeId: string, key: string, value: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const config: Record<string, unknown> = { ...(n.config as Record<string, unknown> ?? {}) }
    config[key] = value
    return { ...n, config }
  })
  saveDraft()
}

function setToolNameForNode(nodeId: string, toolName: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }

    const config: Record<string, unknown> = getNodeConfigRecord(n)
    config['tool_name'] = toolName

    const validParams = new Set(getToolParameters(toolName).map((p) => p.name))
    const inputMapping = { ...((config['input_mapping'] as Record<string, string> | undefined) ?? {}) }
    const staticParams = { ...((config['params'] as Record<string, string> | undefined) ?? {}) }

    Object.keys(inputMapping).forEach((k) => {
      if (!validParams.has(k)) {
        delete inputMapping[k]
      }
    })
    Object.keys(staticParams).forEach((k) => {
      if (!validParams.has(k)) {
        delete staticParams[k]
      }
    })

    config['input_mapping'] = inputMapping
    config['params'] = staticParams

    return {
      ...n,
      config,
      inputType: getToolInputType(toolName),
      outputType: getToolOutputType(toolName),
    }
  })
  saveDraft()
}

function getSelectedToolInfo(toolName: string): ToolInfo | undefined {
  return tools.value.find(t => t.name === toolName)
}

function getToolParameters(toolName: string): ToolInfo['parameters'] {
  const tool = getSelectedToolInfo(toolName)
  return tool?.parameters ?? []
}

function getToolInputType(toolName: string): string {
  const tool = getSelectedToolInfo(toolName)
  if (!tool) return 'string'
  if (tool.parameters && tool.parameters.length > 0) {
    return tool.parameters[0].type || 'string'
  }
  return 'string'
}

function getToolOutputType(toolName: string): string {
  const tool = getSelectedToolInfo(toolName)
  if (!tool) return 'string'
  if (tool.outputParameters && tool.outputParameters.length > 0) {
    return tool.outputParameters[0].type || 'string'
  }
  return 'string'
}

function getToolInputMapping(node: NodeDefinition): Record<string, string> {
  const config = getNodeConfigRecord(node)
  return { ...((config['input_mapping'] as Record<string, string> | undefined) ?? {}) }
}

function getToolStaticParams(node: NodeDefinition): Record<string, string> {
  const config = getNodeConfigRecord(node)
  return { ...((config['params'] as Record<string, string> | undefined) ?? {}) }
}

function getMappedSourceNode(node: NodeDefinition, paramName: string): string {
  return getToolInputMapping(node)[paramName] ?? ''
}

function isToolParamMapped(node: NodeDefinition, paramName: string): boolean {
  return getMappedSourceNode(node, paramName) !== ''
}

function getToolParamValue(node: NodeDefinition, paramName: string): string {
  return getToolStaticParams(node)[paramName] ?? ''
}

function setToolParamValue(nodeId: string, paramName: string, value: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const config = getNodeConfigRecord(n)
    const params = getToolStaticParams(n)
    params[paramName] = value
    config['params'] = params
    return { ...n, config }
  })
  saveDraft()
}

function clearToolParamMapping(nodeId: string, paramName: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const config = getNodeConfigRecord(n)
    const inputMapping = getToolInputMapping(n)
    delete inputMapping[paramName]
    config['input_mapping'] = inputMapping
    return { ...n, config }
  })
  saveDraft()
}

function defaultLoopConfig(): LoopConfig {
  const wf = workflow.value
  const target = wf?.startNodeId || wf?.nodes[0]?.id || ''
  return {
    maxIterations: 1,
    continueTo: target,
    exitTo: target,
  }
}

function setSelectedNodeType(nextType: NodeType): void {
  const node = selectedNode.value
  if (!node) {
    return
  }

  node.type = nextType
  node.inputType = node.inputType || 'string'
  node.outputType = node.outputType || 'string'
  if (nextType === 'start') {
    node.agentId = undefined
    node.taskType = undefined
    node.condition = undefined
    node.loopConfig = undefined
    node.metadata = {
      ...(node.metadata ?? {}),
      next_to: node.metadata?.next_to ?? '',
      'ui.agent': node.metadata?.['ui.agent'] ?? 'orchestrator',
      'ui.label': node.metadata?.['ui.label'] ?? '开始',
    }
    saveDraft()
    return
  }

  if (nextType === 'end') {
    node.agentId = undefined
    node.taskType = undefined
    node.condition = undefined
    node.loopConfig = undefined
    node.metadata = {
      ...(node.metadata ?? {}),
      next_to: node.metadata?.next_to ?? '',
      'ui.agent': node.metadata?.['ui.agent'] ?? 'orchestrator',
      'ui.label': node.metadata?.['ui.label'] ?? '结束',
    }
    saveDraft()
    return
  }

  if (isExecutableNodeType(nextType)) {
    if (nextType === 'tool') {
      node.agentId = undefined
      node.taskType = undefined
    } else {
      node.agentId = node.agentId || 'host'
      node.taskType =
        node.taskType ||
        nextType
    }
    node.condition = undefined
    node.loopConfig = undefined
    node.metadata = {
      ...(node.metadata ?? {}),
      next_to: node.metadata?.next_to ?? '',
      'ui.agent': node.metadata?.['ui.agent'] ?? 'custom',
      'ui.label':
        node.metadata?.['ui.label'] ??
        (nextType === 'chat_model'
          ? '模型推理'
          : nextType === 'tool'
            ? '工具调用'
              : '新步骤'),
    }
    if (nextType === 'tool') {
      const toolName = (node.config as Record<string, unknown> | undefined)?.['tool_name'] as string | undefined
      node.inputType = getToolInputType(toolName ?? '')
      node.outputType = getToolOutputType(toolName ?? '')
    }
    saveDraft()
    return
  }

  if (nextType === 'condition') {
    node.agentId = undefined
    node.taskType = undefined
    node.condition = undefined
    node.config = {
      ...getNodeConfigRecord(node),
      operator: getConditionConfig(node).operator,
      right_type: getConditionConfig(node).right_type,
      right_value: getConditionConfig(node).right_value,
    }
    node.loopConfig = undefined
    node.metadata = {
      ...(node.metadata ?? {}),
      true_to: node.metadata?.true_to ?? '',
      false_to: node.metadata?.false_to ?? '',
      'ui.agent': node.metadata?.['ui.agent'] ?? 'custom',
      'ui.label': node.metadata?.['ui.label'] ?? '条件判断',
    }
    saveDraft()
    return
  }

  // loop
  node.agentId = undefined
  node.taskType = undefined
  node.condition = undefined
  node.loopConfig = node.loopConfig ?? defaultLoopConfig()
  node.metadata = {
    ...(node.metadata ?? {}),
    'ui.agent': node.metadata?.['ui.agent'] ?? 'custom',
    'ui.label': node.metadata?.['ui.label'] ?? '循环',
  }

  // keep a single visual edge for break path
  setLoopExitTo(node.id, node.loopConfig.exitTo)
  saveDraft()
}

function setLoopMaxIterations(nodeId: string, raw: number): void {
  const wf = ensureWorkflow()
  const maxIterations = Math.min(10, raw > 0 ? Math.floor(raw) : 1)
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const loopConfig = n.loopConfig ?? defaultLoopConfig()
    return { ...n, loopConfig: { ...loopConfig, maxIterations } }
  })
  saveDraft()
}

function setLoopExitTo(nodeId: string, exitTo: string): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const loopConfig = n.loopConfig ?? defaultLoopConfig()
    return { ...n, loopConfig: { ...loopConfig, exitTo } }
  })

  wf.edges = wf.edges.filter((e) => !(e.from === nodeId && e.label === 'exit'))
  if (exitTo) {
    wf.edges = [...wf.edges, { from: nodeId, to: exitTo, label: 'exit' }]
  }
  saveDraft()
}

function ensureWorkflow(): WorkflowDefinition {
  if (!workflow.value) {
    throw new Error('未加载工作流')
  }
  return workflow.value
}

function clampNumber(n: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, n))
}

function getNodePos(node: NodeDefinition): { x: number; y: number } {
  const xRaw = node.metadata?.['ui.x']
  const yRaw = node.metadata?.['ui.y']
  const x = xRaw ? Number.parseFloat(xRaw) : 80
  const y = yRaw ? Number.parseFloat(yRaw) : 80
  return {
    x: Number.isFinite(x) ? x : 80,
    y: Number.isFinite(y) ? y : 80,
  }
}

function setNodePos(nodeId: string, x: number, y: number): void {
  const wf = ensureWorkflow()
  wf.nodes = wf.nodes.map((n) => {
    if (n.id !== nodeId) {
      return n
    }
    const metadata: Record<string, string> = { ...(n.metadata ?? {}) }
    metadata['ui.x'] = String(x)
    metadata['ui.y'] = String(y)
    return { ...n, metadata }
  })
  saveDraft()
}

function buildExampleWorkflowDefinition(id: string): WorkflowDefinition {
  const nodes: NodeDefinition[] = []
  const edges: EdgeDefinition[] = []

  // Internal-flow example: nodes are internal steps of each agent.
  // Each executable node is annotated by metadata.ui.agent/ui.label.
  // Condition nodes are used to show control-flow inside the agent.
  const addStepAt = (
    x: number,
    y: number,
    id: string,
    agent: string,
    label: string,
    type: NodeType = 'chat_model',
    agentId?: string,
    taskType?: string,
  ): string => {
    nodes.push({
      id,
      type,
      inputType: 'string',
      outputType: 'string',
      agentId: agentId ?? (type === 'tool' || type === 'start' || type === 'end' || type === 'condition' || type === 'loop' ? undefined : 'host'),
      taskType: taskType ?? (type === 'start' || type === 'end' || type === 'condition' || type === 'loop' ? undefined : type),
      metadata: {
        'ui.x': String(x),
        'ui.y': String(y),
        'ui.agent': agent,
        'ui.label': label,
      },
    })
    return id
  }

  const addCondAt = (x: number, y: number, id: string, agent: string, label: string, condition: string): string => {
    nodes.push({
      id,
      type: 'condition',
      inputType: 'string',
      outputType: 'string',
      condition,
      metadata: {
        'ui.x': String(x),
        'ui.y': String(y),
        'ui.agent': agent,
        'ui.label': label,
        true_to: '',
        false_to: '',
      },
    })
    return id
  }

  // host internal flow (routing)
  {
    const baseY = 120
    const agent = 'host'
    const s0 = addStepAt(20, baseY, 'flow_start', 'orchestrator', '开始', 'start')
    const n0 = addStepAt(140, baseY, 'host_agent_info', agent, '获取可调用助手', 'tool', 'host', 'agent_info_fetch')
    const n1 = addStepAt(380, baseY, 'host_validate', agent, '路由决策(chat_model)', 'chat_model', 'host', 'chat_model_decide')
    const c1 = addCondAt(660, baseY, 'host_is_direct', agent, '是否直接回答', 'chat_model.response == "false"')
    const r2 = addStepAt(960, 60, 'host_delegate', agent, '调用目标 agent', 'tool', 'host', 'call_agent')
    const d1 = addStepAt(960, 180, 'host_direct', agent, '直接回答', 'chat_model', 'host', 'chat_model_direct')
    const e0 = addStepAt(1240, baseY, 'flow_end', 'orchestrator', '结束', 'end')

    const delegateNode = nodes.find((n) => n.id === r2)
    if (delegateNode) {
      delegateNode.metadata = {
        ...(delegateNode.metadata ?? {}),
        'set.agent_name': 'deepresearch',
        'set.text': '请继续分析并返回结论',
      }
    }

    edges.push({ from: s0, to: n0 })
    edges.push({ from: n0, to: n1 })
    edges.push({ from: n1, to: c1 })
    edges.push({ from: c1, to: d1, label: 'true' })
    edges.push({ from: c1, to: r2, label: 'false' })
    edges.push({ from: r2, to: e0 })
    edges.push({ from: d1, to: e0 })

    // deterministic routing for conditions
    const setCondTargets = (nodeId: string, t: string, f: string) => {
      const node = nodes.find((n) => n.id === nodeId)
      if (!node) {
        return
      }
      const md: Record<string, string> = { ...(node.metadata ?? {}) }
      md['true_to'] = t
      md['false_to'] = f
      node.metadata = md
    }
    setCondTargets('host_is_direct', 'host_direct', 'host_delegate')
  }

  // deepresearch internal flow
  {
    const baseY = 340
    const agent = 'deepresearch'
    const n1 = addStepAt(120, baseY, 'deep_validate', agent, '校验输入query')
    const n2 = addStepAt(420, baseY, 'deep_tavily', agent, 'Tavily 检索/收集证据', 'tool')
    const n3 = addStepAt(720, baseY, 'deep_synthesize', agent, '整理答案(含引用)')
    edges.push({ from: n1, to: n2 })
    edges.push({ from: n2, to: n3 })
  }

  // urlreader internal flow
  {
    const baseY = 560
    const agent = 'urlreader'
    const n1 = addStepAt(120, baseY, 'url_validate', agent, '校验输入/提取URL')
    const n2 = addStepAt(420, baseY, 'url_jina', agent, 'Jina Reader 读取网页')
    const n3 = addStepAt(720, baseY, 'url_question', agent, '构造问题/证据seed')
    const n4 = addStepAt(1020, baseY, 'url_research', agent, 'Runner.Run 生成回答(含引用)')
    edges.push({ from: n1, to: n2 })
    edges.push({ from: n2, to: n3 })
    edges.push({ from: n3, to: n4 })
  }

  // lbshelper internal flow
  {
    const baseY = 780
    const agent = 'lbshelper'
    const n1 = addStepAt(120, baseY, 'lbs_validate', agent, '校验输入/提取需求')
    const n2 = addStepAt(420, baseY, 'lbs_search', agent, 'Tavily 检索目的地信息')
    const n3 = addStepAt(720, baseY, 'lbs_plan', agent, '规划行程/整理回答')
    edges.push({ from: n1, to: n2 })
    edges.push({ from: n2, to: n3 })
  }

  const startNodeId = nodes[0]?.id ?? ''

  return {
    id,
    name: '示例：单-Agent内部节点编排',
    description: '自动生成：把每个 agent 内部的步骤作为节点展示（ui.agent/ui.label）',
    startNodeId,
    nodes,
    edges,
  }
}

async function createExampleWorkflow(): Promise<void> {
  try {
    const response = await getAgentWorkflows()
    agentWorkflows.value = response.agents

    if (response.agents.length === 0) {
      return
    }

    const allIds = [...savedWorkflows.value, ...draftWorkflows.value].map((w) => w.id)
    const existing = new Set(allIds)
    const createdIds: string[] = []

    for (const agent of response.agents) {
      const baseId = `example_${agent.id}`
      const id = existing.has(baseId) ? `${baseId}_${Math.random().toString(16).slice(2, 6)}` : baseId
      existing.add(id)

      const def: WorkflowDefinition = {
        id,
        name: `示例：${agent.name} 编排`,
        description: agent.description,
        startNodeId: agent.executionOrder.startNodeId,
        nodes: agent.nodes,
        edges: agent.edges,
      }

      await putWorkflow(id, def)
      createdIds.push(id)
    }

    await refreshWorkflows()
    if (createdIds.length > 0) {
      await loadWorkflowById(createdIds[0])
    }
  } catch (err) {
    console.error('Failed to create example workflow:', err)
    const baseId = 'example'
    const allIds = [...savedWorkflows.value, ...draftWorkflows.value].map((w) => w.id)
    const existing = new Set(allIds)
    const id = existing.has(baseId) ? `${baseId}_${Math.random().toString(16).slice(2, 6)}` : baseId
    const def = buildExampleWorkflowDefinition(id)
    await putWorkflow(id, def)
    await refreshWorkflows()
    await loadWorkflowById(id)
  }
}

function inferEdgeLabel(edge: EdgeDefinition, wf: WorkflowDefinition): string {
  const from = wf.nodes.find((n) => n.id === edge.from)
  if (!from || from.type !== 'condition') {
    return edge.label ?? ''
  }
  const trueTo = from.metadata?.['true_to']
  const falseTo = from.metadata?.['false_to']
  if (trueTo && trueTo === edge.to) {
    return 'true'
  }
  if (falseTo && falseTo === edge.to) {
    return 'false'
  }
  return edge.label ?? ''
}

async function refreshAgents(): Promise<void> {
  try {
    agents.value = await listAgents()
  } catch {
    agents.value = []
  }
}

async function refreshTools(): Promise<void> {
  try {
    tools.value = await listTools()
  } catch {
    tools.value = []
  }
}

// Agent测试和发布功能
async function refreshUserAgents(): Promise<void> {
  try {
    userAgents.value = await listUserAgents()
  } catch {
    userAgents.value = []
  }
}

function openAgentTester(): void {
  console.log('[WorkflowPage] openAgentTester called')
  showAgentTester.value = true
  testResult.value = null
  agentTestError.value = ''
  testInput.value = '{\n  "query": "测试输入"\n}'
}

function closeAgentTester(): void {
  showAgentTester.value = false
}

async function handleTestAgent(): Promise<void> {
  console.log('[WorkflowPage] handleTestAgent called')
  if (!workflow.value) {
    agentTestError.value = '请先创建工作流'
    return
  }

  testing.value = true
  agentTestError.value = ''
  testResult.value = null

  try {
    let input: Record<string, unknown> = {}
    if (testInput.value.trim()) {
      input = JSON.parse(testInput.value)
    }

    console.log('[WorkflowPage] testing workflow:', workflow.value.id, 'input:', input)
    
    const result = await testWorkflow({
      workflowDef: toBackendWorkflowDefinition(workflow.value),
      input,
    })
    
    console.log('[WorkflowPage] test result:', result)
    testResult.value = result
  } catch (e) {
    console.error('[WorkflowPage] test error:', e)
    agentTestError.value = e instanceof Error ? e.message : '测试失败'
  } finally {
    testing.value = false
  }
}

async function handlePublishAgent(): Promise<void> {
  console.log('[WorkflowPage] handlePublishAgent called')
  if (!workflow.value) {
    agentTestError.value = '请先创建工作流'
    return
  }

  publishing.value = true
  agentTestError.value = ''

  try {
    // 1. 先保存工作流
    const workflowId = workflow.value.id || `workflow_${Date.now()}`
    await saveWorkflowToDB(workflowId, toBackendWorkflowDefinition({
      id: workflowId,
      name: workflow.value.name || '未命名工作流',
      description: workflow.value.description || '',
      startNodeId: workflow.value.startNodeId || '',
      nodes: workflow.value.nodes,
      edges: workflow.value.edges,
    }))
    console.log('[WorkflowPage] workflow saved:', workflowId)

    // 2. 创建或更新Agent
    const agentId = workflowId
    let agent = userAgents.value.find(a => a.workflowId === workflowId)
    
    if (!agent) {
      agent = await createUserAgent({
        agentId,
        name: workflow.value.name || '未命名Agent',
        description: workflow.value.description || '',
        workflowId,
      })
      console.log('[WorkflowPage] agent created:', agent)
    }

    // 3. 发布Agent
    currentAgent.value = await publishUserAgent(agentId)
    console.log('[WorkflowPage] agent published:', currentAgent.value)

    // 4. 刷新Agent列表
    await refreshUserAgents()
  } catch (e) {
    console.error('[WorkflowPage] publish error:', e)
    agentTestError.value = e instanceof Error ? e.message : '发布失败'
  } finally {
    publishing.value = false
  }
}

async function handleStopAgent(): Promise<void> {
  if (!currentAgent.value) return
  
  try {
    await stopUserAgent(currentAgent.value.agentId)
    currentAgent.value = null
    await refreshUserAgents()
  } catch (e) {
    agentTestError.value = e instanceof Error ? e.message : '停止失败'
  }
}

async function handleRestartAgent(): Promise<void> {
  if (!currentAgent.value) return
  
  try {
    await restartUserAgent(currentAgent.value.agentId)
    await refreshUserAgents()
  } catch (e) {
    agentTestError.value = e instanceof Error ? e.message : '重启失败'
  }
}

async function refreshWorkflows(): Promise<void> {
  try {
    const res = await listWorkflows()
    savedWorkflows.value = res.saved
    draftWorkflows.value = res.drafts
    pageError.value = ''
  } catch (err) {
    savedWorkflows.value = []
    draftWorkflows.value = []
    handlePageError(err, '加载工作流失败')
  }
}

async function loadWorkflowById(id: string): Promise<void> {
  try {
    const res = await getWorkflow(id)
    activeWorkflowId.value = id
    workflow.value = {
      ...res.definition,
      nodes: res.definition.nodes.map(normalizeNodeDefaults),
    }
    updatedAt.value = res.updatedAt
    isDraft.value = res.isDraft ?? false
    ttlMinutes.value = res.ttlMinutes ?? 0
    selectedNodeId.value = ''
    selectedEdgeIndex.value = null
    pendingLink.value = null
    pageError.value = ''
  } catch (err) {
    handlePageError(err, '加载工作流详情失败')
  }
}

function isAuthError(err: unknown): boolean {
  const msg = err instanceof Error ? err.message : String(err)
  const lower = msg.toLowerCase()
  return lower.includes('authentication required') || lower.includes('unauthorized') || lower.includes('401')
}

function handlePageError(err: unknown, fallback: string): void {
  if (isAuthError(err)) {
    pageError.value = '登录状态失效或无权限访问工作流，请重新登录后重试。'
    return
  }
  pageError.value = err instanceof Error ? err.message : fallback
}

async function createWorkflow(): Promise<void> {
  const id = window.prompt('工作流 ID', `wf_${Math.random().toString(16).slice(2, 8)}`)
  if (!id) {
    return
  }
  const name = window.prompt('工作流名称', id) ?? id

  const startNode: NodeDefinition = {
    id: 'N1',
    type: 'start',
    inputType: 'string',
    outputType: 'string',
    metadata: {
      'ui.x': '120',
      'ui.y': '120',
      'ui.agent': 'orchestrator',
      'ui.label': '开始',
      next_to: '',
    },
  }

  const def: WorkflowDefinition = {
    id,
    name,
    description: '',
    startNodeId: startNode.id,
    nodes: [startNode],
    edges: [],
  }

  await putWorkflow(id, def)
  await refreshWorkflows()
  await loadWorkflowById(id)
}

async function saveWorkflow(): Promise<void> {
  const wf = ensureWorkflow()
  const res = await saveWorkflowToDB(wf.id, toBackendWorkflowDefinition(wf))
  updatedAt.value = res.updatedAt
  isDraft.value = false
  ttlMinutes.value = 0
  await refreshWorkflows()
}

async function removeWorkflow(): Promise<void> {
  const wf = ensureWorkflow()
  const ok = window.confirm(`确认删除工作流 “${wf.id}” 吗？`)
  if (!ok) {
    return
  }
  await deleteWorkflow(wf.id)
  workflow.value = null
  activeWorkflowId.value = ''
  selectedNodeId.value = ''
  selectedEdgeIndex.value = null
  pendingLink.value = null
  await refreshWorkflows()
}

function addNode(type: NodeType): void {
  const wf = ensureWorkflow()

  if (type === 'loop' && wf.nodes.length === 0) {
    window.alert('请先创建至少一个节点，再添加循环节点（循环需要配置 continue/exit 目标）。')
    return
  }

  const id = window.prompt('节点 ID', `N_${uuid().slice(0, 6)}`)
  if (!id) {
    return
  }
  if (wf.nodes.some((n) => n.id === id)) {
    window.alert('节点 ID 已存在。')
    return
  }

  const baseX = 160 + wf.nodes.length * 40
  const baseY = 160 + wf.nodes.length * 30

  const defaultLoopTarget = wf.startNodeId || wf.nodes[0]?.id || ''

  const newNode: NodeDefinition = {
    id,
    type,
    inputType: 'string',
    outputType: 'string',
    inputSource: 'previous',
    agentId: isExecutableNodeType(type) ? (type === 'tool' ? undefined : 'host') : undefined,
    taskType: isExecutableNodeType(type) ? (type === 'tool' ? undefined : type) : undefined,
    condition: undefined,
    preInput: undefined,
    config: type === 'condition'
      ? {
          operator: 'eq',
          right_type: 'string',
          right_value: '',
        }
      : type === 'chat_model' || type === 'tool'
        ? { input_source: 'previous' }
        : undefined,
    loopConfig:
      type === 'loop'
        ? {
            maxIterations: 1,
            continueTo: defaultLoopTarget,
            exitTo: defaultLoopTarget,
          }
        : undefined,
    metadata: {
      'ui.x': String(baseX),
      'ui.y': String(baseY),
      ...(type === 'start'
        ? { next_to: '', 'ui.agent': 'orchestrator', 'ui.label': '开始' }
        : type === 'end'
          ? { next_to: '', 'ui.agent': 'orchestrator', 'ui.label': '结束' }
        : isExecutableNodeType(type)
          ? {
              next_to: '',
              'ui.agent': 'custom',
              'ui.label':
                type === 'chat_model'
                  ? '模型推理'
                  : type === 'tool'
                    ? '工具调用'
                      : '新步骤',
            }
        : type === 'condition'
          ? { true_to: '', false_to: '', 'ui.agent': 'custom', 'ui.label': '条件判断' }
          : { 'ui.agent': 'custom', 'ui.label': '循环' }),
    },
  }

  if (type === 'loop' && defaultLoopTarget) {
    wf.edges = wf.edges.filter((e) => e.from !== id)
    wf.edges = [...wf.edges, { from: id, to: defaultLoopTarget, label: 'exit' }]
  }

  wf.nodes = [...wf.nodes, newNode]
  if (!wf.startNodeId) {
    wf.startNodeId = id
  }
  selectedNodeId.value = id

  saveDraft()
}

function removeNode(nodeId: string): void {
  const wf = ensureWorkflow()
  const ok = window.confirm(`确认删除节点 “${nodeId}” 吗？`)
  if (!ok) {
    return
  }

  wf.nodes = wf.nodes.filter((n) => n.id !== nodeId)

  wf.nodes = wf.nodes.map((n) => {
    if (n.type !== 'tool') {
      return n
    }
    const config = getNodeConfigRecord(n)
    const inputMapping = getToolInputMapping(n)
    let changed = false
    Object.keys(inputMapping).forEach((k) => {
      if (inputMapping[k] === nodeId) {
        delete inputMapping[k]
        changed = true
      }
    })
    if (!changed) {
      return n
    }
    config['input_mapping'] = inputMapping
    return { ...n, config }
  })

  wf.edges = wf.edges.filter((e) => e.from !== nodeId && e.to !== nodeId)
  if (wf.startNodeId === nodeId) {
    wf.startNodeId = wf.nodes[0]?.id ?? ''
  }
  if (selectedNodeId.value === nodeId) {
    selectedNodeId.value = ''
  }
  selectedEdgeIndex.value = null
  pendingLink.value = null
  saveDraft()
}

function selectEdge(edgeIndex: number): void {
  selectedNodeId.value = ''
  selectedEdgeIndex.value = edgeIndex
}

function deleteEdge(edgeIndex: number): void {
  const wf = ensureWorkflow()
  const edge = wf.edges[edgeIndex]
  if (!edge) {
    selectedEdgeIndex.value = null
    return
  }

  wf.edges = wf.edges.filter((_, idx) => idx !== edgeIndex)

  const fromNode = wf.nodes.find((n) => n.id === edge.from)
  if (fromNode) {
    const metadata: Record<string, string> = { ...(fromNode.metadata ?? {}) }

    if (hasSingleOutput(fromNode.type) && !edge.label && metadata['next_to'] === edge.to) {
      metadata['next_to'] = ''
    }

    if (fromNode.type === 'condition') {
      if (metadata['true_to'] === edge.to) {
        metadata['true_to'] = ''
      }
      if (metadata['false_to'] === edge.to) {
        metadata['false_to'] = ''
      }
    }

    if (fromNode.type === 'loop' && (edge.label === 'exit' || edge.label === 'break') && fromNode.loopConfig) {
      fromNode.loopConfig = { ...fromNode.loopConfig, exitTo: '' }
    }

    fromNode.metadata = metadata
  }

  selectedEdgeIndex.value = null
  saveDraft()
}

let saveDraftTimer: ReturnType<typeof setTimeout> | null = null

function saveDraft(): void {
  if (saveDraftTimer) {
    clearTimeout(saveDraftTimer)
  }
  saveDraftTimer = setTimeout(() => {
    const wf = workflow.value
    if (wf) {
      putWorkflow(wf.id, toBackendWorkflowDefinition(wf)).catch((err) => {
        console.error('Failed to save draft:', err)
      })
    }
    saveDraftTimer = null
  }, 300)
}

function setStartNode(nodeId: string): void {
  const wf = ensureWorkflow()
  wf.startNodeId = nodeId
  saveDraft()
}

function clientToCanvasPoint(clientX: number, clientY: number): { x: number; y: number } {
  const canvas = workflowCanvasRef.value
  if (!canvas) {
    return { x: clientX, y: clientY }
  }
  const rect = canvas.getBoundingClientRect()
  return {
    x: clientX - rect.left + canvas.scrollLeft,
    y: clientY - rect.top + canvas.scrollTop,
  }
}

function getOutputAnchor(nodeId: string, branch?: 'true' | 'false' | 'body' | 'loop' | 'break' | 'exit'): { x: number; y: number } {
  const wf = workflow.value
  if (!wf) {
    return { x: 0, y: 0 }
  }
  const node = wf.nodes.find((n) => n.id === nodeId)
  if (!node) {
    return { x: 0, y: 0 }
  }
  const pos = getNodePos(node)
  const baseY = pos.y + getNodeHeight(node) / 2
  const branchOffset =
    branch === 'true' || branch === 'body'
      ? -20
      : branch === 'false' || branch === 'loop'
        ? -6
        : branch === 'break'
          ? 8
          : branch === 'exit'
            ? 20
            : 0
  return {
    x: pos.x + nodeBox.width,
    y: baseY + branchOffset,
  }
}

function beginLinkDrag(
  event: MouseEvent,
  fromNodeId: string,
  kind: PendingLink['kind'],
  branch?: 'true' | 'false' | 'body' | 'loop' | 'break' | 'exit',
): void {
  if (event.button !== 0) {
    return
  }
  event.preventDefault()
  selectedEdgeIndex.value = null

  if (kind === 'condition' && (branch === 'true' || branch === 'false')) {
    pendingLink.value = { fromNodeId, kind: 'condition', branch }
  } else if (kind === 'loop' && (branch === 'body' || branch === 'loop' || branch === 'break' || branch === 'exit')) {
    pendingLink.value = { fromNodeId, kind: 'loop', branch }
  } else {
    pendingLink.value = { fromNodeId, kind: 'next' }
  }

  const start = getOutputAnchor(fromNodeId, branch)
  const current = clientToCanvasPoint(event.clientX, event.clientY)
  linkDragState.value = {
    startX: start.x,
    startY: start.y,
    currentX: current.x,
    currentY: current.y,
    didDrop: false,
  }
}

function startOrFinishLinkFromOutputHandle(
  event: MouseEvent,
  nodeId: string,
  kind: PendingLink['kind'],
  branch?: 'true' | 'false' | 'body' | 'loop' | 'break' | 'exit',
): void {
  if (linkDragState.value && pendingLink.value && pendingLink.value.fromNodeId !== nodeId) {
    event.preventDefault()
    finishLinkDragOnInput(nodeId)
    return
  }
  beginLinkDrag(event, nodeId, kind, branch)
}

function finishLinkDragOnInput(toNodeId: string, targetParam?: string): void {
  if (!linkDragState.value) {
    return
  }
  linkDragState.value.didDrop = true
  completeLink(toNodeId, targetParam)
  linkDragState.value = null
}

function previewLinkPath(): string {
  const drag = linkDragState.value
  if (!drag) {
    return ''
  }
  const dx = Math.max(80, Math.abs(drag.currentX - drag.startX) * 0.35)
  const c1x = drag.startX + dx
  const c2x = drag.currentX - dx
  return `M ${drag.startX} ${drag.startY} C ${c1x} ${drag.startY}, ${c2x} ${drag.currentY}, ${drag.currentX} ${drag.currentY}`
}

function cancelPendingLink(): void {
  pendingLink.value = null
  linkDragState.value = null
  selectedEdgeIndex.value = null
}

function completeLink(toNodeId: string, targetParam?: string): void {
  const wf = ensureWorkflow()
  const pending = pendingLink.value
  if (!pending) {
    return
  }

  if (pending.fromNodeId === toNodeId) {
    pendingLink.value = null
    return
  }

  const fromNode = wf.nodes.find((n) => n.id === pending.fromNodeId)
  if (!fromNode) {
    pendingLink.value = null
    return
  }

  if (hasSingleOutput(fromNode.type) && pending.kind === 'next') {
    wf.edges = wf.edges.filter((e) => e.from !== fromNode.id)
    wf.edges = [...wf.edges, { from: fromNode.id, to: toNodeId }]
    const metadata: Record<string, string> = { ...(fromNode.metadata ?? {}) }
    metadata['next_to'] = toNodeId
    fromNode.metadata = metadata

    const toNode = wf.nodes.find((n) => n.id === toNodeId)
    if (toNode?.type === 'tool' && targetParam) {
      const config = getNodeConfigRecord(toNode)
      const inputMapping = getToolInputMapping(toNode)
      const params = getToolStaticParams(toNode)
      inputMapping[targetParam] = fromNode.id
      delete params[targetParam]
      config['input_mapping'] = inputMapping
      config['params'] = params
      toNode.config = config
    }
  }

  if (fromNode.type === 'condition' && pending.kind === 'condition') {
    const key = pending.branch === 'true' ? 'true_to' : 'false_to'
    const metadata: Record<string, string> = { ...(fromNode.metadata ?? {}) }

    const prevTarget = metadata[key]
    if (prevTarget) {
      wf.edges = wf.edges.filter((e) => !(e.from === fromNode.id && e.to === prevTarget))
    }

    metadata[key] = toNodeId
    fromNode.metadata = metadata
    const label = pending.branch

    wf.edges = wf.edges.filter((e) => !(e.from === fromNode.id && e.to === toNodeId))
    wf.edges = [...wf.edges, { from: fromNode.id, to: toNodeId, label }]
  }

  if (fromNode.type === 'loop' && pending.kind === 'loop') {
    const label = pending.branch
    wf.edges = wf.edges.filter((e) => !(e.from === fromNode.id && e.label === label))
    wf.edges = [...wf.edges, { from: fromNode.id, to: toNodeId, label }]
    if (fromNode.loopConfig) {
      if (label === 'body' || label === 'loop') {
        fromNode.loopConfig = { ...fromNode.loopConfig, continueTo: toNodeId }
      }
      if (label === 'break' || label === 'exit') {
        fromNode.loopConfig = { ...fromNode.loopConfig, exitTo: toNodeId }
      }
    }
  }

  pendingLink.value = null
  selectedEdgeIndex.value = null
  saveDraft()
}

type DragState = {
  nodeId: string
  startMouseX: number
  startMouseY: number
  startX: number
  startY: number
}

type LinkDragState = {
  startX: number
  startY: number
  currentX: number
  currentY: number
  didDrop: boolean
}

const dragState = ref<DragState | null>(null)
const linkDragState = ref<LinkDragState | null>(null)

function onNodeMouseDown(event: MouseEvent, nodeId: string): void {
  if (event.button !== 0) {
    return
  }
  const target = event.target as HTMLElement | null
  if (target?.closest('.workflow-handle.input')) {
    return
  }
  const wf = ensureWorkflow()
  const node = wf.nodes.find((n) => n.id === nodeId)
  if (!node) {
    return
  }

  selectedNodeId.value = nodeId
  selectedEdgeIndex.value = null
  const pos = getNodePos(node)
  dragState.value = {
    nodeId,
    startMouseX: event.clientX,
    startMouseY: event.clientY,
    startX: pos.x,
    startY: pos.y,
  }
}

function onWindowMouseMove(event: MouseEvent): void {
  if (linkDragState.value) {
    const current = clientToCanvasPoint(event.clientX, event.clientY)
    linkDragState.value.currentX = current.x
    linkDragState.value.currentY = current.y
    return
  }

  const drag = dragState.value
  if (!drag) {
    return
  }

  const dx = event.clientX - drag.startMouseX
  const dy = event.clientY - drag.startMouseY
  const x = clampNumber(drag.startX + dx, 0, 4000)
  const y = clampNumber(drag.startY + dy, 0, 4000)
  setNodePos(drag.nodeId, x, y)
}

function onWindowMouseUp(): void {
  if (linkDragState.value) {
    if (!linkDragState.value.didDrop) {
      pendingLink.value = null
    }
    linkDragState.value = null
    return
  }

  if (!dragState.value) {
    return
  }
  dragState.value = null
}

const nodeBox = { width: 210, height: 92 }

function getNodeHeight(node: NodeDefinition): number {
  if (node.type !== 'tool') {
    return nodeBox.height
  }
  const toolName = getNodeConfig(node, 'tool_name')
  const paramCount = getToolParameters(toolName).length
  if (paramCount <= 1) {
    return nodeBox.height
  }
  return nodeBox.height + (paramCount-1) * 24
}

function nodeCenter(nodeId: string): { x: number; y: number } {
  const wf = ensureWorkflow()
  const node = wf.nodes.find((n) => n.id === nodeId)
  if (!node) {
    return { x: 0, y: 0 }
  }
  const pos = getNodePos(node)
  return { x: pos.x + nodeBox.width / 2, y: pos.y + getNodeHeight(node) / 2 }
}

function edgeMidpoint(edge: EdgeDefinition): { x: number; y: number } {
  const a = nodeCenter(edge.from)
  const b = nodeCenter(edge.to)
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
}

function edgePath(edge: EdgeDefinition): string {
  const a = nodeCenter(edge.from)
  const b = nodeCenter(edge.to)
  const dx = Math.max(120, Math.abs(b.x - a.x) * 0.35)
  const c1x = a.x + dx
  const c2x = b.x - dx
  return `M ${a.x} ${a.y} C ${c1x} ${a.y}, ${c2x} ${b.y}, ${b.x} ${b.y}`
}

function toggleInspector(): void {
  inspectorCollapsed.value = !inspectorCollapsed.value
}

onMounted(async () => {
  window.addEventListener('mousemove', onWindowMouseMove)
  window.addEventListener('mouseup', onWindowMouseUp)

  try {
    await refreshAgents()
    await refreshTools()
    await refreshWorkflows()

    if (pageError.value) {
      return
    }

    const allWorkflows = [...savedWorkflows.value, ...draftWorkflows.value]
    if (allWorkflows.length > 0) {
      await loadWorkflowById(allWorkflows[0].id)
      return
    }

    // If user has no workflows yet, auto-create one example to satisfy "show host/deepresearch/...".
    if (agents.value.length > 0) {
      try {
        await createExampleWorkflow()
      } catch {
        // ignore; user can retry via button
      }
    }
  } catch (err) {
    handlePageError(err, '初始化编排页面失败')
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('mousemove', onWindowMouseMove)
  window.removeEventListener('mouseup', onWindowMouseUp)
})
</script>

<template>
  <PageContainer mode="fluid">
  <div class="module-page module-page--workflow workflow-studio">
    <PageHeader
      eyebrow="ORCHESTRATOR"
      title="工作流编排"
      description="可视化管理节点、连接关系、测试运行与发布流程。"
    />

    <div class="layout workspace-layout workflow-studio__layout">
    <ModuleSectionCard>
      <aside class="sidebar workflow-sidebar-frame workflow-studio__sidebar">
      <div class="brand workflow-studio__sidebar-brand">
        <p class="eyebrow">workflow studio</p>
        <h1>工作流列表</h1>
        <p class="workflow-studio__sidebar-tip">集中管理当前可用工作流与草稿入口。</p>
      </div>

      <div class="workflow-sidebar-actions workflow-studio__sidebar-actions">
        <button v-if="!readOnlyMode" class="new-chat" type="button" @click="createWorkflow">新建工作流</button>
        <button v-if="!readOnlyMode" class="cancel" type="button" @click="createExampleWorkflow">生成示例编排</button>
        <p v-if="readOnlyMode" class="task-text">当前角色 {{ roleLabel }}：只读模式</p>
      </div>

      <ul class="conversation-list workflow-studio__workflow-list">
        <li
          v-for="item in draftWorkflows"
          :key="item.id"
          :class="['conversation-item workflow-studio__workflow-item', { active: item.id === activeWorkflowId }]"
          @click="loadWorkflowById(item.id)"
        >
          <div class="conversation-meta">
            <p class="conversation-title">{{ item.name || item.id }} <span class="draft-badge">草稿 {{ item.ttlMinutes }}分钟</span></p>
            <p class="workflow-studio__workflow-subtitle">临时草稿 · 待保存</p>
          </div>
        </li>
        <li
          v-for="item in savedWorkflows"
          :key="item.id"
          :class="['conversation-item workflow-studio__workflow-item', { active: item.id === activeWorkflowId }]"
          @click="loadWorkflowById(item.id)"
        >
          <div class="conversation-meta">
            <p class="conversation-title">{{ item.name || item.id }}</p>
            <p class="workflow-studio__workflow-subtitle">正式工作流 · {{ item.id }}</p>
            <p class="conversation-time">{{ new Date(item.updatedAt).toLocaleString() }}</p>
          </div>
        </li>
      </ul>
      </aside>
    </ModuleSectionCard>

    <ModuleSectionCard class="workflow-main-frame" v-if="workflow">
      <main class="chat-panel workflow-panel workflow-studio__panel">
      <header class="workflow-studio__topbar">
        <div class="workflow-studio__info-card">
          <p class="workflow-studio__eyebrow">Current Workflow</p>
          <div class="workflow-studio__info-title">
            <strong>{{ currentWorkflowDisplayName }}</strong>
            <span class="draft-badge" v-if="isDraft">草稿 {{ ttlMinutes }}分钟</span>
          </div>
          <div class="workflow-studio__info-meta">
            <span class="task-text">ID：{{ workflow.id }}</span>
            <span class="task-text">{{ currentWorkflowSavedAt }}</span>
          </div>
        </div>

        <div class="workflow-studio__actions">
          <div class="workflow-studio__actions-secondary">
            <button type="button" class="cancel" @click="toggleInspector">
              {{ inspectorCollapsed ? '展开属性面板' : '收起属性面板' }}
            </button>
            <button v-if="!readOnlyMode" type="button" class="cancel" @click="cancelPendingLink" :disabled="!pendingLink">
              取消连线
            </button>
            <button v-if="!readOnlyMode" type="button" class="cancel" @click="removeWorkflow">删除</button>
          </div>

          <div class="workflow-studio__actions-primary">
            <button v-if="!readOnlyMode" type="button" class="send" @click="saveWorkflow">保存</button>
            <button v-if="!readOnlyMode" type="button" class="test-btn" @click="openAgentTester" :disabled="!workflow.startNodeId">
              测试
            </button>
            <button v-if="!readOnlyMode" type="button" class="publish-btn" @click="handlePublishAgent" :disabled="publishing || !workflow.startNodeId">
              {{ publishing ? '发布中...' : '发布' }}
            </button>
          </div>
        </div>
      </header>

      <section class="agent-tip" v-if="pageError">
        <p>{{ pageError }}</p>
      </section>

      <section class="workflow-body workflow-studio__body" :class="{ 'inspector-collapsed': inspectorCollapsed }">
        <div class="workflow-canvas workflow-studio__canvas" ref="workflowCanvasRef" @click="selectedNodeId = ''; selectedEdgeIndex = null">
          <svg class="workflow-edges" :width="4200" :height="4200">
            <template v-for="(edge, edgeIndex) in workflow.edges" :key="`${edge.from}->${edge.to}-${edge.label || ''}-${edgeIndex}`">
              <path :d="edgePath(edge)" class="workflow-edge-hit" @click.stop="selectEdge(edgeIndex)" />
              <path :d="edgePath(edge)" :class="['workflow-edge', { selected: selectedEdgeIndex === edgeIndex }]" />
              <text
                v-if="inferEdgeLabel(edge, workflow)"
                :x="edgeMidpoint(edge).x"
                :y="edgeMidpoint(edge).y"
                class="workflow-edge-label"
              >
                {{ inferEdgeLabel(edge, workflow) }}
              </text>
              <foreignObject
                v-if="!readOnlyMode && selectedEdgeIndex === edgeIndex"
                :x="edgeMidpoint(edge).x - 16"
                :y="edgeMidpoint(edge).y - 13"
                width="32"
                height="24"
              >
                <button type="button" class="edge-delete-btn" @click.stop="deleteEdge(edgeIndex)">删</button>
              </foreignObject>
            </template>
            <path v-if="linkDragState" :d="previewLinkPath()" class="workflow-edge preview" />
          </svg>

          <div
            v-for="node in workflow.nodes"
            :key="node.id"
            :class="[
              'workflow-node',
              { selected: node.id === selectedNodeId, start: node.id === startNodeId },
            ]"
            :style="{
              left: `${getNodePos(node).x}px`,
              top: `${getNodePos(node).y}px`,
              width: `${nodeBox.width}px`,
              minHeight: `${getNodeHeight(node)}px`,
            }"
            @mousedown.stop="!readOnlyMode ? onNodeMouseDown($event, node.id) : undefined"
            @click.stop="selectedNodeId = node.id; selectedEdgeIndex = null"
          >
            <div class="workflow-node-header">
              <span class="workflow-node-id">{{ node.id }}</span>
              <span class="chip tiny completed" v-if="node.id === startNodeId">起点</span>
            </div>

            <div class="workflow-node-label" v-if="getUiLabel(node)">
              {{ getUiAgent(node) ? `${getUiAgent(node)} · ` : '' }}{{ getUiLabel(node) }}
            </div>

            <div class="workflow-node-body">
              <div class="workflow-handle input">
                <template v-if="node.type === 'tool' && getToolParameters(getNodeConfig(node, 'tool_name')).length > 0">
                  <button
                    v-for="param in getToolParameters(getNodeConfig(node, 'tool_name'))"
                    :key="`${node.id}-in-${param.name}`"
                    type="button"
                    :title="`参数 ${param.name}`"
                    class="param-handle"
                    @mousedown.stop.prevent
                    @click.stop
                    @mouseup.stop="finishLinkDragOnInput(node.id, param.name)"
                  >
                    {{ param.name }}
                  </button>
                </template>
                <template v-else>
                  <button type="button" @mousedown.stop.prevent @click.stop @mouseup.stop="finishLinkDragOnInput(node.id)">in</button>
                </template>
              </div>

              <div class="workflow-node-summary"></div>

              <div class="workflow-handle output">
                <template v-if="!readOnlyMode && hasSingleOutput(node.type)">
                  <button
                    type="button"
                    :class="{ active: pendingLink?.fromNodeId === node.id }"
                    @mousedown.stop="startOrFinishLinkFromOutputHandle($event, node.id, 'next')"
                    @mouseup.stop="linkDragState ? finishLinkDragOnInput(node.id) : undefined"
                  >
                    out
                  </button>
                </template>
                <template v-else-if="!readOnlyMode && node.type === 'condition'">
                  <button
                    type="button"
                    :class="{
                      active:
                        pendingLink?.fromNodeId === node.id &&
                        pendingLink.kind === 'condition' &&
                        pendingLink.branch === 'true',
                    }"
                    @mousedown.stop="startOrFinishLinkFromOutputHandle($event, node.id, 'condition', 'true')"
                    @mouseup.stop="linkDragState ? finishLinkDragOnInput(node.id) : undefined"
                  >
                    T
                  </button>
                  <button
                    type="button"
                    :class="{
                      active:
                        pendingLink?.fromNodeId === node.id &&
                        pendingLink.kind === 'condition' &&
                        pendingLink.branch === 'false',
                    }"
                    @mousedown.stop="startOrFinishLinkFromOutputHandle($event, node.id, 'condition', 'false')"
                    @mouseup.stop="linkDragState ? finishLinkDragOnInput(node.id) : undefined"
                  >
                    F
                  </button>
                </template>
                <template v-else-if="!readOnlyMode">
                  <button
                    type="button"
                    :class="{
                      active:
                        pendingLink?.fromNodeId === node.id &&
                        pendingLink.kind === 'loop' &&
                        pendingLink.branch === 'body',
                    }"
                    @mousedown.stop="startOrFinishLinkFromOutputHandle($event, node.id, 'loop', 'body')"
                    @mouseup.stop="linkDragState ? finishLinkDragOnInput(node.id) : undefined"
                  >
                    BODY
                  </button>
                  <button
                    type="button"
                    :class="{
                      active:
                        pendingLink?.fromNodeId === node.id &&
                        pendingLink.kind === 'loop' &&
                        pendingLink.branch === 'loop',
                    }"
                    @mousedown.stop="startOrFinishLinkFromOutputHandle($event, node.id, 'loop', 'loop')"
                    @mouseup.stop="linkDragState ? finishLinkDragOnInput(node.id) : undefined"
                  >
                    LOOP
                  </button>
                  <button
                    type="button"
                    :class="{
                      active:
                        pendingLink?.fromNodeId === node.id &&
                        pendingLink.kind === 'loop' &&
                        pendingLink.branch === 'break',
                    }"
                    @mousedown.stop="startOrFinishLinkFromOutputHandle($event, node.id, 'loop', 'break')"
                    @mouseup.stop="linkDragState ? finishLinkDragOnInput(node.id) : undefined"
                  >
                    BREAK
                  </button>
                  <button
                    type="button"
                    :class="{
                      active:
                        pendingLink?.fromNodeId === node.id &&
                        pendingLink.kind === 'loop' &&
                        pendingLink.branch === 'exit',
                    }"
                    @mousedown.stop="startOrFinishLinkFromOutputHandle($event, node.id, 'loop', 'exit')"
                    @mouseup.stop="linkDragState ? finishLinkDragOnInput(node.id) : undefined"
                  >
                    EXIT
                  </button>
                </template>
              </div>
            </div>
          </div>
        </div>

        <aside class="workflow-inspector workflow-studio__inspector" v-show="!inspectorCollapsed">
          <div class="workflow-inspector-header">
            <div class="workflow-studio__inspector-title">
              <p class="workflow-studio__eyebrow">Inspector</p>
              <strong>属性面板</strong>
            </div>
          </div>

          <div v-if="!readOnlyMode" class="workflow-inspector-actions workflow-studio__inspector-actions">
            <button type="button" class="cancel" @click="addNode('start')">新增开始节点</button>
            <button type="button" class="cancel" @click="addNode('end')">新增结束节点</button>
            <button type="button" class="cancel" @click="addNode('chat_model')">新增ChatModel节点</button>
            <button type="button" class="cancel" @click="addNode('tool')">新增Tool节点</button>
            <button type="button" class="cancel" @click="addNode('condition')">新增Condition节点</button>
            <button type="button" class="cancel" @click="addNode('loop')">新增Loop节点</button>
          </div>

          <template v-if="selectedNode && !readOnlyMode">
            <div class="workflow-form">
              <label>
                节点 ID
                <input type="text" :value="selectedNode.id" disabled />
              </label>

              <label>
                类型
                <select
                  :value="selectedNode.type"
                  @change="setSelectedNodeType(($event.target as HTMLSelectElement).value as NodeType)"
                >
                  <option value="start">start</option>
                  <option value="end">end</option>
                  <option value="chat_model">chat_model</option>
                  <option value="tool">tool</option>
                  <option value="condition">condition</option>
                  <option value="loop">loop</option>
                </select>
              </label>

              <template v-if="selectedNode.type === 'start'">
                <p class="task-text">start 节点用于定义工作流入口，并通过 next_to 或连线指定后继节点。</p>
                <label>
                  输入类型
                  <select v-model="selectedNode.inputType">
                    <option value="string">字符串</option>
                  </select>
                </label>
                <label>
                  输出类型
                  <select v-model="selectedNode.outputType">
                    <option value="string">字符串</option>
                  </select>
                </label>
              </template>

              <template v-else-if="selectedNode.type === 'end'">
                <p class="task-text">end 节点用于定义工作流出口，不执行实际任务。</p>
                <label>
                  输入类型
                  <select v-model="selectedNode.inputType">
                    <option value="string">字符串</option>
                  </select>
                </label>
                <label>
                  输出类型
                  <select v-model="selectedNode.outputType">
                    <option value="string">字符串</option>
                  </select>
                </label>
              </template>

              <template v-else-if="selectedNode.type === 'chat_model' || selectedNode.type === 'tool'">
                <label>
                  预输入
                  <textarea
                    v-model="selectedNode.preInput"
                    rows="2"
                    placeholder="在进入该节点前补充说明，例如：请先根据上下文重写问题"
                  ></textarea>
                </label>
                <label>
                  入参来源
                  <select
                    :value="getNodeInputSource(selectedNode)"
                    @change="setNodeInputSource(selectedNode.id, ($event.target as HTMLSelectElement).value as 'previous' | 'history')"
                  >
                    <option value="previous">上一节点值</option>
                    <option value="history">历史累加值</option>
                  </select>
                </label>

                <template v-if="selectedNode.agentId === 'send_task' && selectedNode.taskType === 'delegate'">
                  <label>
                    目标助手
                    <select
                      :value="getDelegateTarget(selectedNode)"
                      @change="setDelegateTarget(selectedNode.id, ($event.target as HTMLSelectElement).value)"
                    >
                      <option value="">(empty)</option>
                      <option v-for="a in agents" :key="a.id" :value="a.id">{{ a.name }}</option>
                    </select>
                  </label>

                  <label>
                    输入文本
                    <textarea
                      :value="getDelegateText(selectedNode)"
                      rows="3"
                      placeholder="请输入要交给目标 agent 处理的文本"
                      @input="setDelegateText(selectedNode.id, ($event.target as HTMLTextAreaElement).value)"
                    ></textarea>
                  </label>
                </template>

                <template v-else-if="selectedNode.agentId === 'noop' && selectedNode.taskType === 'noop'">
                  <label>
                    所属助手
                    <select
                      :value="getUiAgent(selectedNode)"
                      @change="setUiAgent(selectedNode.id, ($event.target as HTMLSelectElement).value)"
                    >
                      <option value="">(empty)</option>
                      <option value="host">host</option>
                      <option value="deepresearch">deepresearch</option>
                      <option value="urlreader">urlreader</option>
                      <option value="lbshelper">lbshelper</option>
                      <option value="custom">custom</option>
                    </select>
                  </label>

                  <label>
                    步骤名称
                    <input
                      type="text"
                      :value="getUiLabel(selectedNode)"
                      placeholder="例如：提取URL / Tavily检索 / 整理答案"
                      @input="setUiLabel(selectedNode.id, ($event.target as HTMLInputElement).value)"
                    />
                  </label>
                </template>

                <template v-if="selectedNode.type === 'chat_model'">
                  <label>
                    输入类型
                    <select v-model="selectedNode.inputType">
                      <option value="string">字符串</option>
                    </select>
                  </label>
                  <label>
                    输出类型
                    <select v-model="selectedNode.outputType">
                      <option value="string">字符串</option>
                      <option value="bool">布尔值</option>
                    </select>
                  </label>
                  <label>
                    模型
                    <input
                      type="text"
                      :value="getNodeConfig(selectedNode, 'model')"
                      @input="setNodeConfig(selectedNode.id, 'model', ($event.target as HTMLInputElement).value)"
                      placeholder="例如：qwen3-235b-a22b"
                    />
                  </label>
                  <label>
                    URL
                    <input
                      type="text"
                      :value="getNodeConfig(selectedNode, 'url')"
                      @input="setNodeConfig(selectedNode.id, 'url', ($event.target as HTMLInputElement).value)"
                      placeholder="模型 API URL"
                    />
                  </label>
                  <label>
                    API Key
                    <input
                      type="text"
                      :value="getNodeConfig(selectedNode, 'apikey')"
                      @input="setNodeConfig(selectedNode.id, 'apikey', ($event.target as HTMLInputElement).value)"
                      placeholder="API Key"
                    />
                  </label>
                </template>

                <template v-else-if="selectedNode.type === 'tool'">
                  <label>
                    选择工具
                    <select
                      :value="getNodeConfig(selectedNode, 'tool_name')"
                      @change="setToolNameForNode(selectedNode.id, ($event.target as HTMLSelectElement).value)"
                    >
                      <option value="">请选择工具</option>
                      <option v-for="t in tools" :key="t.name" :value="t.name">
                        {{ t.name }} ({{ t.type }})
                      </option>
                    </select>
                  </label>
                  <template v-if="getNodeConfig(selectedNode, 'tool_name')">
                    <label>
                      输入类型
                      <select disabled :value="getToolInputType(getNodeConfig(selectedNode, 'tool_name'))">
                        <option :value="getToolInputType(getNodeConfig(selectedNode, 'tool_name'))">
                          {{ getToolInputType(getNodeConfig(selectedNode, 'tool_name')) }}
                        </option>
                      </select>
                    </label>
                    <label>
                      输出类型
                      <select disabled :value="getToolOutputType(getNodeConfig(selectedNode, 'tool_name'))">
                        <option :value="getToolOutputType(getNodeConfig(selectedNode, 'tool_name'))">
                          {{ getToolOutputType(getNodeConfig(selectedNode, 'tool_name')) }}
                        </option>
                      </select>
                    </label>

                    <div class="workflow-tool-params">
                      <strong>工具参数</strong>
                      <div
                        v-for="param in getToolParameters(getNodeConfig(selectedNode, 'tool_name'))"
                        :key="`${selectedNode.id}-param-${param.name}`"
                        class="workflow-tool-param-row"
                      >
                        <label>
                          {{ param.name }}
                          <input
                            type="text"
                            :placeholder="param.description || `请输入 ${param.name}`"
                            :value="getToolParamValue(selectedNode, param.name)"
                            :disabled="isToolParamMapped(selectedNode, param.name)"
                            @input="setToolParamValue(selectedNode.id, param.name, ($event.target as HTMLInputElement).value)"
                          />
                        </label>
                        <p class="task-text" v-if="isToolParamMapped(selectedNode, param.name)">
                          已连接：{{ getMappedSourceNode(selectedNode, param.name) }}
                          <button
                            type="button"
                            class="cancel mini"
                            @click="clearToolParamMapping(selectedNode.id, param.name)"
                          >
                            解除
                          </button>
                        </p>
                      </div>
                    </div>
                  </template>
                </template>

                <template v-else>
                  <label>
                    助手
                    <input type="text" v-model="selectedNode.agentId" placeholder="host / send_task / deepresearch" />
                  </label>
                  <label>
                    Task 类型
                    <input type="text" v-model="selectedNode.taskType" placeholder="chat_model / tool / delegate / route" />
                  </label>
                  <label>
                    输入类型
                    <select v-model="selectedNode.inputType">
                      <option value="string">字符串</option>
                    </select>
                  </label>
                  <label>
                    输出类型
                    <select v-model="selectedNode.outputType">
                      <option value="string">字符串</option>
                    </select>
                  </label>
                </template>
              </template>

              <template v-else-if="selectedNode.type === 'condition'">
                <label>
                  预输入
                  <textarea
                    v-model="selectedNode.preInput"
                    rows="2"
                    placeholder="进入条件判断前的提示模板，可为空"
                  ></textarea>
                </label>
                <label>
                  左值（固定）
                  <input type="text" value="上一节点输出值" disabled />
                </label>
                <label>
                  运算符
                  <select
                    :value="getConditionConfig(selectedNode).operator"
                    @change="setConditionConfig(selectedNode.id, 'operator', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="eq">eq</option>
                    <option value="gt">gt</option>
                    <option value="lt">lt</option>
                  </select>
                </label>
                <label>
                  右值类型
                  <select
                    :value="getConditionConfig(selectedNode).right_type"
                    @change="setConditionConfig(selectedNode.id, 'right_type', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="string">string</option>
                    <option value="bool">bool</option>
                  </select>
                </label>
                <label>
                  右值
                  <input
                    type="text"
                    :value="getConditionConfig(selectedNode).right_value"
                    @input="setConditionConfig(selectedNode.id, 'right_value', ($event.target as HTMLInputElement).value)"
                    placeholder="例如：true"
                  />
                </label>
              </template>

              <template v-else>
                <label>
                  预输入
                  <textarea
                    v-model="selectedNode.preInput"
                    rows="2"
                    placeholder="进入循环前融合输入，可为空"
                  ></textarea>
                </label>
                <label>
                  最大迭代次数
                  <input
                    type="number"
                    min="1"
                    max="10"
                    :value="selectedNode.loopConfig?.maxIterations ?? 1"
                    @input="setLoopMaxIterations(selectedNode.id, Number(($event.target as HTMLInputElement).value))"
                  />
                </label>

                <p class="task-text">使用节点右侧 BODY/LOOP/BREAK/EXIT 端口拖拽连线来设置循环体与退出路径。</p>
              </template>

              <div class="workflow-inspector-row">
                <button type="button" class="send" @click="setStartNode(selectedNode.id)">设为起点</button>
                <button type="button" class="cancel" @click="removeNode(selectedNode.id)">删除节点</button>
              </div>
            </div>
          </template>

          <template v-else-if="selectedNode">
            <div class="workflow-form">
              <p class="task-text">当前为只读模式，可查看节点信息但不可编辑。</p>
              <label>
                节点 ID
                <input type="text" :value="selectedNode.id" disabled />
              </label>
              <label>
                类型
                <input type="text" :value="selectedNode.type" disabled />
              </label>
              <label>
                所属助手
                <input type="text" :value="getUiAgent(selectedNode)" disabled />
              </label>
              <label>
                节点标签
                <input type="text" :value="getUiLabel(selectedNode)" disabled />
              </label>
            </div>
          </template>

          <template v-else>
            <p class="task-text">请选择一个节点进行编辑。</p>
          </template>

          <div class="workflow-run">
            <strong>测试结果</strong>
            <p class="task-text" v-if="agentTestError">{{ agentTestError }}</p>
            <pre v-if="testResult" class="workflow-run-json">{{ JSON.stringify(testResult, null, 2) }}</pre>
          </div>
        </aside>
      </section>
      </main>
    </ModuleSectionCard>

    <ModuleSectionCard class="workflow-main-frame" v-else>
      <main class="chat-panel workflow-panel workflow-studio__panel">
      <header class="toolbar workflow-studio__empty-toolbar">
        <strong>未选择工作流</strong>
      </header>
      <section class="agent-tip" v-if="pageError">
        <p>{{ pageError }}</p>
      </section>
      <section class="agent-tip">
        <p>请从左侧选择一个工作流，或新建一个。</p>
      </section>
      </main>
    </ModuleSectionCard>
    </div>

    <!-- 助手测试模态框 -->
    <div v-if="showAgentTester && !readOnlyMode" class="modal-overlay" @click.self="closeAgentTester">
      <div class="agent-tester-modal">
        <h2>助手测试与发布</h2>
        
        <div class="tester-section">
          <h3>测试工作流</h3>
          <label>
            输入参数 (JSON)
            <textarea v-model="testInput" rows="4" placeholder='{"query": "测试输入"}'></textarea>
          </label>
          <div class="tester-buttons">
            <button class="test-btn" @click="handleTestAgent" :disabled="testing">
              {{ testing ? '测试中...' : '测试' }}
            </button>
          </div>
        </div>

        <div v-if="agentTestError" class="tester-error">
          {{ agentTestError }}
        </div>

        <div v-if="testResult" class="tester-result">
          <h3>测试结果</h3>
          <div class="result-header">
            <span class="state-badge" :class="testResult.state">{{ testResult.state }}</span>
          </div>
          <div v-if="testResult.error" class="result-error">{{ testResult.error }}</div>
          <div v-if="testResult.output" class="result-output">
            <pre>{{ JSON.stringify(testResult.output, null, 2) }}</pre>
          </div>
        </div>

        <div v-if="currentAgent" class="current-agent-info">
          <h3>已发布助手</h3>
          <div class="agent-info-grid">
            <div><strong>名称:</strong> {{ currentAgent.name }}</div>
            <div><strong>状态:</strong> {{ currentAgent.status }}</div>
            <div><strong>端口:</strong> {{ currentAgent.port || '-' }}</div>
            <div><strong>进程状态:</strong> {{ currentAgent.processStatus || '-' }}</div>
          </div>
          <div class="agent-buttons">
            <button class="stop-btn" @click="handleStopAgent">停止</button>
            <button class="restart-btn" @click="handleRestartAgent">重启</button>
          </div>
        </div>

        <div class="modal-close">
          <button @click="closeAgentTester">关闭</button>
        </div>
      </div>
    </div>
  </div>
  </PageContainer>
</template>

<style scoped>
.workflow-studio {
  gap: 14px;
}

.workflow-studio__layout {
  grid-template-columns: 300px minmax(0, 1fr);
  gap: 16px;
  align-items: stretch;
}

.workflow-sidebar-frame,
.workflow-main-frame {
  padding: 0;
  border: none;
  box-shadow: none;
  background: transparent;
}

.workflow-studio__sidebar {
  gap: 14px;
}

.workflow-studio__sidebar-brand {
  gap: 6px;
}

.workflow-studio__sidebar-tip,
.workflow-studio__workflow-subtitle {
  margin: 0;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.5;
}

.workflow-studio__sidebar-actions {
  flex-direction: column;
  align-items: stretch;
}

.workflow-studio__sidebar-actions .new-chat,
.workflow-studio__sidebar-actions .cancel {
  width: 100%;
}

.workflow-studio__workflow-list {
  gap: 10px;
}

.workflow-studio__workflow-item {
  padding: 12px;
  border-radius: 16px;
}

.workflow-studio__workflow-item.active {
  border-color: #b9c6d8;
  background: linear-gradient(132deg, rgba(204, 232, 220, 0.38), rgba(226, 216, 246, 0.26));
}

.workflow-studio__panel {
  padding: 16px;
  gap: 12px;
}

.workflow-studio__topbar {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 16px;
  align-items: center;
  border: 1px solid var(--line);
  border-radius: 18px;
  background:
    radial-gradient(circle at 10% 12%, rgba(182, 225, 207, 0.14), transparent 30%),
    radial-gradient(circle at 92% 14%, rgba(220, 203, 244, 0.14), transparent 32%),
    rgba(255, 255, 255, 0.84);
  padding: 16px 18px;
}

.workflow-studio__info-card,
.workflow-studio__inspector-title {
  display: grid;
  gap: 6px;
}

.workflow-studio__eyebrow {
  margin: 0;
  font-size: 11px;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: #5f8a78;
  font-weight: 700;
}

.workflow-studio__info-title {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.workflow-studio__info-title strong {
  font-family: var(--font-display);
  font-size: 28px;
  line-height: 1.08;
  letter-spacing: -0.02em;
}

.workflow-studio__info-meta {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.workflow-studio__actions {
  display: grid;
  gap: 8px;
  justify-items: end;
}

.workflow-studio__actions-secondary,
.workflow-studio__actions-primary {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.workflow-studio__body {
  grid-template-columns: minmax(0, 1fr) 360px;
  gap: 14px;
}

.workflow-studio__body.inspector-collapsed {
  grid-template-columns: 1fr;
}

.workflow-studio__canvas {
  min-height: 640px;
  border-radius: 18px;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.94), rgba(250, 251, 252, 0.94)),
    #fff;
}

.workflow-studio__inspector {
  border-radius: 18px;
}

.workflow-studio__inspector-actions {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.workflow-studio__inspector-actions .cancel {
  width: 100%;
}

.workflow-studio__empty-toolbar {
  border: 1px solid var(--line);
  border-radius: 18px;
  background: rgba(255, 255, 255, 0.84);
  padding: 14px 16px;
}

.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(34, 41, 51, 0.26);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.agent-tester-modal {
  background: #ffffff;
  border: 1px solid var(--line);
  border-radius: 16px;
  padding: 24px;
  width: 600px;
  max-height: 80vh;
  overflow-y: auto;
  box-shadow: var(--shadow-medium);
}

.agent-tester-modal h2 {
  margin: 0 0 20px 0;
  font-size: 20px;
}

.agent-tester-modal h3 {
  margin: 16px 0 12px 0;
  font-size: 16px;
  color: var(--text-main);
}

.tester-section {
  margin-bottom: 20px;
}

.tester-section label {
  display: block;
  margin-bottom: 8px;
  font-weight: 500;
}

.tester-section textarea {
  width: 100%;
  padding: 10px;
  border: 1px solid #d8dee5;
  border-radius: 10px;
  font-family: monospace;
  font-size: 13px;
  resize: vertical;
}

.tester-buttons {
  display: flex;
  gap: 12px;
  margin-top: 12px;
}

.test-btn {
  border: 1px solid transparent;
  padding: 10px 20px;
  border-radius: 10px;
  cursor: pointer;
  font-size: 14px;
}

.test-btn:hover:not(:disabled) {
  filter: brightness(0.97);
}

.test-btn:disabled {
  background: #ccc;
  cursor: not-allowed;
}

.publish-btn {
  border: 1px solid transparent;
  padding: 10px 20px;
  border-radius: 10px;
  cursor: pointer;
  font-size: 14px;
}

.publish-btn:hover:not(:disabled) {
  filter: brightness(0.97);
}

.publish-btn:disabled {
  background: #ccc;
  cursor: not-allowed;
}

.tester-error {
  background: #fff0f0;
  color: var(--danger-text);
  border: 1px solid #f2c6cb;
  padding: 12px;
  border-radius: 10px;
  margin-bottom: 16px;
}

.tester-result {
  background: #f9fbfc;
  border: 1px solid #edf1f3;
  border-radius: 10px;
  padding: 16px;
  margin-bottom: 16px;
}

.result-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.state-badge {
  padding: 4px 12px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
}

.state-badge.succeeded {
  background: #e9f7f0;
  color: #24634d;
}

.state-badge.failed {
  background: #fff0f0;
  color: var(--danger-text);
}

.state-badge.running {
  background: #fdf3dc;
  color: #9b6c1d;
}

.result-error {
  color: var(--danger-text);
  padding: 8px;
  background: #fff0f0;
  border-radius: 8px;
  margin-bottom: 8px;
}

.result-output pre {
  background: #f4f7fa;
  padding: 10px;
  border-radius: 8px;
  overflow-x: auto;
  font-size: 12px;
  margin: 0;
}

.current-agent-info {
  background: #eef7f2;
  border: 1px solid #cfe8db;
  border-radius: 10px;
  padding: 16px;
  margin-bottom: 16px;
}

.agent-info-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  margin-bottom: 12px;
}

.agent-info-grid div {
  font-size: 14px;
}

.agent-buttons {
  display: flex;
  gap: 12px;
}

.stop-btn {
  background: #fff0f0;
  color: var(--danger-text);
  border: 1px solid #f2c6cb;
  padding: 8px 16px;
  border-radius: 10px;
  cursor: pointer;
  font-size: 13px;
}

.stop-btn:hover {
  background: #ffe7e8;
}

.restart-btn {
  background: #f8f4e9;
  color: #8a631d;
  border: 1px solid #ead9b5;
  padding: 8px 16px;
  border-radius: 10px;
  cursor: pointer;
  font-size: 13px;
}

.restart-btn:hover {
  background: #f2e9d2;
}

.modal-close {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}

.modal-close button {
  background: #ffffff;
  border: 1px solid var(--line);
  padding: 10px 20px;
  border-radius: 10px;
  cursor: pointer;
  font-size: 14px;
}

.modal-close button:hover {
  background: #f5f7f9;
}

@media (max-width: 1280px) {
  .workflow-studio__topbar,
  .workflow-studio__layout {
    grid-template-columns: 1fr;
  }

  .workflow-studio__actions {
    justify-items: stretch;
  }

  .workflow-studio__actions-secondary,
  .workflow-studio__actions-primary {
    justify-content: flex-start;
  }
}

@media (max-width: 960px) {
  .workflow-studio__body,
  .workflow-studio__body.inspector-collapsed,
  .workflow-studio__inspector-actions {
    grid-template-columns: 1fr;
  }
}
</style>
