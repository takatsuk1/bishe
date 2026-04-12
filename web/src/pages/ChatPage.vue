<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import DOMPurify from 'dompurify'
import { marked } from 'marked'
import { AGENTS, DEFAULT_AGENT, getAgentDescription } from '../lib/agents'
import { getGlobalSelectedAgent, onGlobalSelectedAgentChange, setGlobalSelectedAgent } from '../lib/agentSelection'
import { currentUser, getAccessToken } from '../lib/authStore'
import { refreshSession } from '../lib/authApi'
import { getMonitorRunFamily, listMonitorRunEvents, listMonitorRuns } from '../lib/monitorApi'
import { getRun, listAgents } from '../lib/orchestratorApi'
import { decodeStepPayload, extractStepPayloads, extractToken, parseNdjsonStream } from '../lib/stream'
import { loadConversations, saveConversations } from '../lib/storage'
import PageContainer from '../components/PageContainer.vue'
import PageHeader from '../components/PageHeader.vue'
import type {
  AgentModel,
  ChatMessage,
  ChatRequest,
  Conversation,
  StepEvent,
  TaskState,
  UploadedFileMeta,
} from '../types/chat'
import type { MonitorEvent } from '../types/monitor'
import type { RunResult } from '../types/workflow'

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://127.0.0.1:11000'
const MAX_UPLOAD_SIZE = 20 * 1024 * 1024
const currentUserId = currentUser.value?.userId ?? ''

interface UploadFileResponseItem {
  id: string
  name: string
  size: number
  type: string
  extracted_text?: string
  warning?: string
}

interface UploadFilesResponse {
  files: UploadFileResponseItem[]
}

marked.setOptions({ gfm: true, breaks: true })

const conversations = ref<Conversation[]>(loadConversations(currentUserId))
const activeConversationId = ref<string>('')
const prompt = ref('')
const selectedModel = ref<AgentModel>(getGlobalSelectedAgent() || DEFAULT_AGENT)
const availableAgents = ref<{ label: string; value: AgentModel; description: string }[]>([...AGENTS])
const isStreaming = ref(false)
const status = ref<TaskState>('queued')
const errorText = ref('')
const draftUploads = ref<UploadedFileMeta[]>([])
let abortController: AbortController | null = null
let unsubscribeGlobalAgent: (() => void) | null = null

const runSnapshot = ref<RunResult | null>(null)
const runPollError = ref('')
let runPollTimer: number | null = null
const monitorStepEvents = ref<StepEvent[]>([])
const monitorStepError = ref('')
let monitorPollTimer: number | null = null

const stepScroller = ref<HTMLDivElement | null>(null)
const stepAutoFollow = ref(true)
const messageScroller = ref<HTMLDivElement | null>(null)
const messageAutoFollow = ref(true)

const liveStreamConversationId = ref('')
const liveStreamMessageId = ref('')
const liveStreamContent = ref('')
let liveStreamPending = ''
let liveStreamFlushTimer: number | null = null

const STREAM_FLUSH_INTERVAL_MS = 50

if (conversations.value.length === 0) {
  const initial = createConversation(selectedModel.value)
  conversations.value = [initial]
  activeConversationId.value = initial.id
}

if (!activeConversationId.value && conversations.value.length > 0) {
  activeConversationId.value = conversations.value[0].id
}

const activeConversation = computed(() =>
  conversations.value.find((item) => item.id === activeConversationId.value),
)

const canSend = computed(() => prompt.value.trim().length > 0 && !isStreaming.value)
const selectedAgentMeta = computed(() =>
  availableAgents.value.find((agent) => agent.value === selectedModel.value),
)
const conversationCount = computed(() => conversations.value.length)
const activeAttachmentCount = computed(() => draftUploads.value.length)
const latestStepLabel = computed(() => {
  const latest = runStepEvents.value[runStepEvents.value.length - 1]
  return latest?.messageZh ?? '等待新的执行步骤'
})
const activeRunStateLabel = computed(() => {
  const runState = (runSnapshot.value?.state ?? '').trim()
  if (!runState) {
    return formatTaskState(status.value)
  }
  if (runState === 'succeeded') {
    return formatTaskState('completed')
  }
  if (runState === 'failed') {
    return formatTaskState('failed')
  }
  if (runState === 'canceled') {
    return formatTaskState('canceled')
  }
  return formatTaskState('running')
})

watch(
  conversations,
  (value) => {
    saveConversations(currentUserId, value)
  },
  { deep: true },
)

watch(activeConversation, (value) => {
  if (!value) {
    return
  }
  selectedModel.value = value.model
  stepAutoFollow.value = true
  messageAutoFollow.value = true

  void nextTick(() => {
    scrollStepsToEnd(true)
    scrollMessagesToEnd(true)
  })
})

watch(isStreaming, async (streaming) => {
  if (streaming) {
    return
  }
  const runId = activeConversation.value?.runId ?? ''
  const taskId = activeConversation.value?.taskId ?? ''
  if (runId) {
    await pollRunOnce(runId)
  }
  await pollMonitorStepsOnce(runId, taskId)
})

watch(
  () => [activeConversation.value?.runId ?? '', activeConversation.value?.taskId ?? ''] as const,
  async ([nextRunId, nextTaskID]) => {
    stopRunPolling()
    stopMonitorPolling()
    if (!nextRunId && !nextTaskID) {
      runSnapshot.value = null
      runPollError.value = ''
      monitorStepEvents.value = []
      monitorStepError.value = ''
      return
    }
    runPollError.value = ''
    monitorStepError.value = ''
    if (nextRunId) {
      await pollRunOnce(nextRunId)
      startRunPolling(nextRunId)
    }
    await pollMonitorStepsOnce(nextRunId, nextTaskID)
    startMonitorPolling(nextRunId, nextTaskID)
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  stopRunPolling()
  stopMonitorPolling()
  clearLiveStreamFlushTimer()
  if (unsubscribeGlobalAgent) {
    unsubscribeGlobalAgent()
    unsubscribeGlobalAgent = null
  }
})

onMounted(() => {
  void loadAvailableAgents()
  unsubscribeGlobalAgent = onGlobalSelectedAgentChange((agent) => {
    if (!availableAgents.value.some((item) => item.value === agent)) {
      return
    }
    onModelChange(agent)
  })
  void nextTick(() => {
    scrollStepsToEnd(true)
    scrollMessagesToEnd(true)
  })
})

async function loadAvailableAgents(): Promise<void> {
  try {
    const agents = await listAgents()
    if (agents.length === 0) {
      availableAgents.value = [...AGENTS]
      return
    }

    availableAgents.value = agents.map((agent) => ({
      label: agent.name || agent.id,
      value: agent.id,
      description: agent.description || getAgentDescription(agent.id),
    }))

    if (!availableAgents.value.some((agent) => agent.value === selectedModel.value)) {
      selectedModel.value = availableAgents.value[0].value
      setGlobalSelectedAgent(selectedModel.value)
      if (activeConversation.value) {
        updateConversation(activeConversation.value.id, (draft) => {
          draft.model = selectedModel.value
        })
      }
    }
  } catch {
    availableAgents.value = [...AGENTS]
  }
}

function nowIso(): string {
  return new Date().toISOString()
}

function uuid(): string {
  return crypto.randomUUID()
}

function createConversation(model: AgentModel): Conversation {
  const now = nowIso()
  return {
    id: uuid(),
    title: '新对话',
    model,
    createdAt: now,
    updatedAt: now,
    messages: [],
    stepEvents: [],
  }
}

function updateConversation(
  conversationId: string,
  updater: (target: Conversation) => void,
  options?: { touchUpdatedAt?: boolean },
): void {
  const target = conversations.value.find((item) => item.id === conversationId)
  if (!target) {
    return
  }
  updater(target)
  if (options?.touchUpdatedAt !== false) {
    target.updatedAt = nowIso()
  }
}

function startConversation(): void {
  const next = createConversation(selectedModel.value)
  conversations.value = [next, ...conversations.value]
  activeConversationId.value = next.id
  errorText.value = ''
  draftUploads.value = []
  status.value = 'queued'
}

function removeConversation(conversationId: string): void {
  if (isStreaming.value) {
    return
  }
  conversations.value = conversations.value.filter((item) => item.id !== conversationId)
  if (conversations.value.length === 0) {
    const fallback = createConversation(selectedModel.value)
    conversations.value = [fallback]
    activeConversationId.value = fallback.id
    return
  }
  if (activeConversationId.value === conversationId) {
    activeConversationId.value = conversations.value[0].id
  }
}

function renameConversation(conversationId: string): void {
  const target = conversations.value.find((item) => item.id === conversationId)
  if (!target) {
    return
  }
  const next = window.prompt('重命名对话', target.title)
  if (!next) {
    return
  }
  updateConversation(conversationId, (draft) => {
    draft.title = next.trim().slice(0, 80) || draft.title
  })
}

function onModelChange(model: AgentModel): void {
  if (!activeConversation.value) {
    return
  }
  selectedModel.value = model
  setGlobalSelectedAgent(model)
  updateConversation(activeConversation.value.id, (draft) => {
    draft.model = model
  })
}

function inferStatus(content: string): TaskState | undefined {
  const lowered = content.toLowerCase()
  if (lowered.includes('failed')) {
    return 'failed'
  }
  if (lowered.includes('cancel')) {
    return 'canceled'
  }
  if (lowered.includes('done') || lowered.includes('completed')) {
    return 'completed'
  }
  if (content.trim().length > 0) {
    return 'running'
  }
  return undefined
}

function escapeRegExp(source: string): string {
  return source.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function formatTaskState(state: TaskState): string {
  switch (state) {
    case 'queued':
      return '排队中'
    case 'running':
      return '运行中'
    case 'completed':
      return '已完成'
    case 'failed':
      return '失败'
    case 'canceled':
      return '已取消'
  }
}

function stepStateLabel(state: StepEvent['state']): string {
  switch (state) {
    case 'start':
      return '开始'
    case 'end':
      return '完成'
    case 'info':
      return '信息'
    case 'error':
      return '错误'
  }
}

function stepChipClass(state: StepEvent['state']): TaskState {
  if (state === 'end') {
    return 'completed'
  }
  if (state === 'error') {
    return 'failed'
  }
  return 'running'
}

const activeStepEvents = computed(() => activeConversation.value?.stepEvents ?? [])
const detailedStepEvents = computed(() => {
  const merged = [...activeStepEvents.value, ...monitorStepEvents.value]
  const dedup = new Map<string, StepEvent>()
  for (const ev of merged) {
    const key = `${ev.ts}|${ev.agent}|${ev.phase}|${ev.name}|${ev.state}|${ev.messageZh}`
    if (!dedup.has(key)) {
      dedup.set(key, ev)
    }
  }
  return [...dedup.values()].sort((a, b) => new Date(a.ts).getTime() - new Date(b.ts).getTime())
})

const runStepEvents = computed(() =>
  detailedStepEvents.value.filter((ev) => {
    const phase = (ev.phase ?? '').trim().toLowerCase()
    const name = (ev.name ?? '').trim().toLowerCase()
    if (
      phase === 'workflow_started' ||
      phase === 'workflow_finished' ||
      phase === 'workflow_failed' ||
      phase === 'model_called' ||
      phase === 'tool_called' ||
      phase === 'agent_called' ||
      phase === 'retry_triggered' ||
      phase === 'timeout_triggered' ||
      phase === 'alert_triggered'
    ) {
      return false
    }
    if (phase === 'semantic') {
      return false
    }
    if (name.includes('.llm.') || name.includes('.semantic.')) {
      return false
    }
    return true
  }),
)

const latestAssistantMessageId = computed(() => {
  const msgs = activeConversation.value?.messages ?? []
  for (let i = msgs.length - 1; i >= 0; i -= 1) {
    if (msgs[i].role === 'assistant') {
      return msgs[i].id
    }
  }
  return ''
})

function executionStepDedupKey(ev: StepEvent): string {
  return `${ev.ts}|${ev.agent}|${ev.phase}|${ev.name}|${ev.state}|${ev.messageZh}`
}

function isLatestAssistantMessage(msg: ChatMessage): boolean {
  return msg.role === 'assistant' && msg.id === latestAssistantMessageId.value
}

function showInlineProgress(msg: ChatMessage): boolean {
  return msg.role === 'assistant' && (msg.executionSteps?.length ?? 0) > 0
}

function isStreamingAssistantMessage(msg: ChatMessage): boolean {
  if (!isLatestAssistantMessage(msg) || !isStreaming.value) {
    return false
  }
  if (activeConversationId.value !== liveStreamConversationId.value) {
    return false
  }
  return msg.id === liveStreamMessageId.value
}

function messageContentForRender(msg: ChatMessage): string {
  if (isStreamingAssistantMessage(msg)) {
    return liveStreamContent.value
  }
  return msg.content ?? ''
}

function clearLiveStreamFlushTimer(): void {
  if (liveStreamFlushTimer != null) {
    window.clearTimeout(liveStreamFlushTimer)
    liveStreamFlushTimer = null
  }
}

function isMessagesNearBottom(threshold = 36): boolean {
  const el = messageScroller.value
  if (!el) {
    return true
  }
  const distanceToBottom = el.scrollHeight - (el.scrollTop + el.clientHeight)
  return distanceToBottom <= threshold
}

function scrollMessagesToEnd(force = false): void {
  const el = messageScroller.value
  if (!el) {
    return
  }
  if (!force && !messageAutoFollow.value) {
    return
  }
  el.scrollTop = el.scrollHeight
}

function onMessagesScroll(): void {
  messageAutoFollow.value = isMessagesNearBottom()
}

function beginLiveStream(conversationId: string, messageId: string): void {
  clearLiveStreamFlushTimer()
  liveStreamPending = ''
  liveStreamConversationId.value = conversationId
  liveStreamMessageId.value = messageId
  liveStreamContent.value = ''
  messageAutoFollow.value = true
}

function flushLiveStream(forceScroll = false): void {
  clearLiveStreamFlushTimer()
  if (liveStreamPending.length === 0) {
    return
  }
  liveStreamContent.value += liveStreamPending
  liveStreamPending = ''
  if (forceScroll || messageAutoFollow.value) {
    void nextTick(() => {
      scrollMessagesToEnd(forceScroll)
    })
  }
}

function enqueueLiveStreamChunk(chunk: string): void {
  if (!chunk) {
    return
  }
  liveStreamPending += chunk
  if (liveStreamFlushTimer != null) {
    return
  }
  liveStreamFlushTimer = window.setTimeout(() => {
    flushLiveStream(false)
  }, STREAM_FLUSH_INTERVAL_MS)
}

function endLiveStream(conversationId: string, messageId: string): string {
  flushLiveStream(true)
  if (liveStreamConversationId.value !== conversationId || liveStreamMessageId.value !== messageId) {
    return ''
  }
  const finalText = liveStreamContent.value
  liveStreamConversationId.value = ''
  liveStreamMessageId.value = ''
  liveStreamContent.value = ''
  liveStreamPending = ''
  return finalText
}


watch(
  () => [detailedStepEvents.value.length, activeConversationId.value] as const,
  async () => {
    await nextTick()
    scrollStepsToEnd()
  },
)

watch(
  detailedStepEvents,
  async (newSteps) => {
    if (!activeConversation.value) {
      return
    }
    
    const assistantMessage = activeConversation.value.messages.find(
      (msg) => msg.role === 'assistant' && msg.id === latestAssistantMessageId.value
    )
    
    if (!assistantMessage) {
      return
    }
    
    if (!assistantMessage.executionSteps) {
      assistantMessage.executionSteps = []
    }
    
    const existingKeys = new Set(assistantMessage.executionSteps.map((ev) => executionStepDedupKey(ev)))
    const newExecutionSteps = newSteps.filter((ev) => {
      const key = executionStepDedupKey(ev)
      return !existingKeys.has(key)
    })
    
    if (newExecutionSteps.length > 0) {
      assistantMessage.executionSteps.push(...newExecutionSteps)
      await nextTick()
      scrollMessagesToEnd()
    }
  },
  { deep: true }
)

function scrollStepsToEnd(force = false): void {
  const el = stepScroller.value
  if (!el) {
    return
  }
  if (!force && !stepAutoFollow.value) {
    return
  }
  el.scrollTop = el.scrollHeight
}

function onStepScroll(): void {
  const el = stepScroller.value
  if (!el) {
    return
  }
  const threshold = 24
  const distanceToBottom = el.scrollHeight - (el.scrollTop + el.clientHeight)
  stepAutoFollow.value = distanceToBottom <= threshold
}

function jumpStepsToBottom(): void {
  stepAutoFollow.value = true
  void nextTick(() => {
    scrollStepsToEnd(true)
  })
}

function isTerminalRunState(state: string): boolean {
  return state === 'succeeded' || state === 'failed' || state === 'canceled'
}

function normalizeAgentLabel(agentId: string, workflowId: string): string {
  const raw = (agentId || workflowId || '').trim()
  if (!raw) {
    return 'workflow'
  }
  const cleaned = raw
    .replace(/_worker$/i, '')
    .replace(/-default$/i, '')
    .replace(/-worker$/i, '')
  return cleaned || raw
}

function parseNodeTypeFromMessage(message: string): string {
  const text = (message || '').trim()
  if (!text) {
    return ''
  }
  const m = text.match(/\(([^)]+)\)\s+(started|finished|failed)$/i)
  if (!m) {
    return ''
  }
  return (m[1] || '').trim()
}

function nodeTypeFromEvents(events: MonitorEvent[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const ev of events) {
    const node = (ev.nodeId || '').trim()
    if (!node) {
      continue
    }
    if (ev.eventType === 'model_called') {
      out[node] = 'chat_model'
      continue
    }
    if (ev.eventType === 'tool_called') {
      out[node] = 'tool'
      continue
    }
    const parsed = parseNodeTypeFromMessage(ev.message || '')
    if (parsed) {
      out[node] = parsed
    }
  }
  return out
}

function monitorEventToStep(event: MonitorEvent, nodeTypeByID: Record<string, string>): StepEvent {
  const agent = normalizeAgentLabel(event.agentId ?? '', event.workflowId ?? '')
  const node = (event.nodeId || '').trim()
  const nodeType = (nodeTypeByID[node] || '').trim() || 'unknown'
  let state: StepEvent['state'] = 'info'
  let messageZh = event.message || event.eventType

  switch (event.eventType) {
    case 'workflow_started':
      state = 'start'
      messageZh = `${agent} 初始化任务`
      break
    case 'workflow_finished':
      state = 'end'
      messageZh = `${agent} 整理结果结束，回复用户`
      break
    case 'workflow_failed':
      state = 'error'
      messageZh = `${agent} 执行失败`
      break
    case 'node_started':
      state = 'start'
      messageZh = `节点名:${node || 'unknown'} 节点类型:${nodeType}`
      break
    case 'node_finished':
      state = 'end'
      messageZh = `节点名:${node || 'unknown'} 节点类型:${nodeType}`
      break
    case 'node_failed':
      state = 'error'
      messageZh = `节点名:${node || 'unknown'} 节点类型:${nodeType} 执行失败`
      break
    case 'model_called':
      state = 'info'
      messageZh = event.message || `chat model called by node ${node || 'unknown'}`
      break
    case 'agent_called':
      state = 'info'
      messageZh = event.message || `agent called by node ${node || 'unknown'}`
      break
    case 'tool_called':
      state = 'info'
      messageZh = event.message || `tool called by node ${node || 'unknown'}`
      break
    case 'retry_triggered':
      state = 'info'
      messageZh = `${agent}${node ? ` ${node}` : ''} 触发重试`
      break
    case 'timeout_triggered':
      state = 'error'
      messageZh = `${agent}${node ? ` ${node}` : ''} 触发超时`
      break
    case 'alert_triggered':
      state = 'error'
      messageZh = event.message || `${agent}${node ? ` ${node}` : ''} 触发告警`
      break
  }

  return {
    ts: event.createdAt,
    agent,
    phase: event.eventType,
    name: node || event.eventType,
    state,
    messageZh,
  }
}

async function resolveMonitorRunID(runId: string, taskId: string): Promise<string> {
  const direct = runId.trim()
  if (direct) {
    return direct
  }
  const task = taskId.trim()
  if (!task) {
    return ''
  }
  const page = await listMonitorRuns({ page: 1, pageSize: 10, taskId: task })
  const first = (page.items ?? [])[0]
  return first?.runId ?? ''
}

async function pollMonitorStepsOnce(runId: string, taskId: string): Promise<void> {
  try {
    const resolvedRunID = await resolveMonitorRunID(runId, taskId)
    if (!resolvedRunID) {
      monitorStepError.value = ''
      return
    }

    const family = await getMonitorRunFamily(resolvedRunID, { limit: 20 })
    const runs = family.runs ?? []
    if (runs.length === 0) {
      monitorStepError.value = ''
      return
    }

    const pages = await Promise.all(
      runs.map((run) => listMonitorRunEvents(run.runId, { page: 1, pageSize: 300 })),
    )
    const allEvents: MonitorEvent[] = []
    for (const page of pages) {
      allEvents.push(...(page.items ?? []))
    }

    const dedup = new Map<string, MonitorEvent>()
    for (const ev of allEvents) {
      const key = (ev.eventId || `${ev.runId}|${ev.eventType}|${ev.nodeId || ''}|${ev.createdAt}`).trim()
      if (!dedup.has(key)) {
        dedup.set(key, ev)
      }
    }

    const sorted = [...dedup.values()].sort((a, b) => {
      const ta = new Date(a.createdAt).getTime()
      const tb = new Date(b.createdAt).getTime()
      return ta - tb
    })

    const nodeTypeByID = nodeTypeFromEvents(sorted)
    const nextSteps = sorted.map((ev) => monitorEventToStep(ev, nodeTypeByID))
    const merged = [...monitorStepEvents.value, ...nextSteps]
    const stepDedup = new Map<string, StepEvent>()
    for (const ev of merged) {
      const key = `${ev.ts}|${ev.agent}|${ev.phase}|${ev.name}|${ev.state}|${ev.messageZh}`
      if (!stepDedup.has(key)) {
        stepDedup.set(key, ev)
      }
    }
    monitorStepEvents.value = [...stepDedup.values()]
      .sort((a, b) => new Date(a.ts).getTime() - new Date(b.ts).getTime())
      .slice(-5000)
    monitorStepError.value = ''
  } catch (err) {
    monitorStepError.value = (err as Error).message
  }
}

function startMonitorPolling(runId: string, taskId: string): void {
  stopMonitorPolling()
  monitorPollTimer = window.setInterval(async () => {
    await pollMonitorStepsOnce(runId, taskId)
  }, 1200)
}

function stopMonitorPolling(): void {
  if (monitorPollTimer != null) {
    window.clearInterval(monitorPollTimer)
    monitorPollTimer = null
  }
}

async function pollRunOnce(runId: string): Promise<void> {
  try {
    runSnapshot.value = await getRun(runId)
    runPollError.value = ''
    if (runSnapshot.value?.state && isTerminalRunState(runSnapshot.value.state)) {
      stopRunPolling()
    }
  } catch (err) {
    runPollError.value = (err as Error).message
  }
}

function startRunPolling(runId: string): void {
  stopRunPolling()
  runPollTimer = window.setInterval(async () => {
    await pollRunOnce(runId)
  }, 600)
}

function stopRunPolling(): void {
  if (runPollTimer != null) {
    window.clearInterval(runPollTimer)
    runPollTimer = null
  }
}

const runStepNodeIds = computed(() => {
  const out: string[] = []
  const snapshot = runSnapshot.value
  if (!snapshot) {
    return out
  }
  for (const item of snapshot.nodeResults ?? []) {
    if (item?.nodeId && !out.includes(item.nodeId)) {
      out.push(item.nodeId)
    }
  }
  const cur = snapshot.currentNodeId
  if (cur && !out.includes(cur)) {
    out.push(cur)
  }
  return out
})

function buildRequest(conversation: Conversation, message: ChatMessage): ChatRequest {
  const baseMessages = conversation.messages
    .filter((item) => item.id !== message.id)
    .map((item) => ({ role: item.role, content: item.content }))

  const uploadHint =
    message.attachments && message.attachments.length > 0
      ? `\n\n[upload]\n${message.attachments
          .map((item) => {
            const lines: string[] = [`${item.name} (${item.type || 'unknown'}, ${item.size} bytes)`]
            if (item.extractedText && item.extractedText.trim().length > 0) {
              lines.push(`[content]\n${item.extractedText.trim()}`)
            }
            if (item.parseWarning && item.parseWarning.trim().length > 0) {
              lines.push(`[warning] ${item.parseWarning.trim()}`)
            }
            return lines.join('\n')
          })
          .join('\n')}`
      : ''

  return {
    model: conversation.model,
    conversation_id: conversation.id,
    messages: [...baseMessages, { role: 'user', content: `${message.content}${uploadHint}` }],
    stream: true,
  }
}

async function sendMessage(): Promise<void> {
  const content = prompt.value.trim()
  if (!content || !activeConversation.value || isStreaming.value) {
    return
  }

  errorText.value = ''
  status.value = 'running'
  isStreaming.value = true
  abortController = new AbortController()
  const requestController = abortController

  const userMessage: ChatMessage = {
    id: uuid(),
    role: 'user',
    content,
    createdAt: nowIso(),
    status: 'running',
    attachments: draftUploads.value,
  }

  const assistantMessage: ChatMessage = {
    id: uuid(),
    role: 'assistant',
    content: '',
    createdAt: nowIso(),
    status: 'queued',
    executionSteps: [],
  }

  const conversationId = activeConversation.value.id
  const requestPayload = buildRequest(activeConversation.value, userMessage)

  updateConversation(conversationId, (draft) => {
    draft.model = selectedModel.value
    draft.taskId = ''
    draft.runId = ''
    draft.stepEvents = []
    draft.messages.push(userMessage)
    draft.messages.push(assistantMessage)
    if (draft.title === '新对话') {
      draft.title = content.slice(0, 42)
    }
  })
  beginLiveStream(conversationId, assistantMessage.id)
  void nextTick(() => {
    scrollMessagesToEnd(true)
  })

  prompt.value = ''
  draftUploads.value = []
  stepAutoFollow.value = true

  try {
    const sendChatRequest = async (retry = true): Promise<Response> => {
      const token = getAccessToken()
      const response = await fetch(`${API_BASE_URL}/v1/chat/completions`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify(requestPayload),
        signal: requestController.signal,
      })
      if (response.status === 401 && retry) {
        await refreshSession()
        return sendChatRequest(false)
      }
      return response
    }

    const response = await sendChatRequest()

    if (!response.ok || !response.body) {
      throw new Error(`请求失败（${response.status}）`)
    }

    for await (const chunk of parseNdjsonStream(response.body)) {
      if (chunk.error) {
        throw new Error(chunk.error)
      }

      const contentChunk = chunk.message?.content ?? ''
      const parsedTaskId = extractToken(contentChunk, 'task')
      const parsedUserId = extractToken(contentChunk, 'user')
      const parsedRunId = extractToken(contentChunk, 'run')

      const stepPayloads = extractStepPayloads(contentChunk)
      const decodedSteps = stepPayloads
        .map((payload) => decodeStepPayload(payload))
        .filter((item): item is StepEvent => Boolean(item))
      const filteredSteps = decodedSteps.filter((ev) => {
        const name = (ev.name ?? '').trim().toLowerCase()
        return !(name.endsWith('.llm.delta') || name.endsWith('.llm.streaming'))
      })


      let cleanedChunk = contentChunk
        .replace(/\[]\(task:\/\/[^)]+\)/g, '')
        .replace(/\[]\(user:\/\/[^)]+\)/g, '')
        .replace(/\[]\(run:\/\/[^)]+\)/g, '')
        .replace(/\[]\(step:\/\/[^)]+\)/g, '')

      if (filteredSteps.length > 0) {
        for (const ev of filteredSteps) {
          const msg = ev.messageZh?.trim()
          if (!msg) {
            continue
          }
          const escaped = escapeRegExp(msg)
          cleanedChunk = cleanedChunk.replace(new RegExp(`(^|\\n)\\s*${escaped}\\s*(?=\\n|$)`, 'g'), '$1')
        }
      }

      if (cleanedChunk.length > 0) {
        enqueueLiveStreamChunk(cleanedChunk)
      }

      const shouldUpdateConversation =
        Boolean(parsedTaskId || parsedUserId || parsedRunId) ||
        filteredSteps.length > 0 ||
        Boolean(chunk.done)

      if (shouldUpdateConversation) {
        updateConversation(conversationId, (draft) => {
        if (parsedTaskId) {
          draft.taskId = parsedTaskId
        }
        if (parsedUserId) {
          draft.userId = parsedUserId
        }
        if (parsedRunId) {
          draft.runId = parsedRunId
        }

        if (filteredSteps.length > 0) {
          const existing = draft.stepEvents ?? []
          const seen = new Set(existing.map((ev) => `${ev.ts}|${ev.agent}|${ev.name}|${ev.state}`))
          for (const ev of filteredSteps) {
            const key = `${ev.ts}|${ev.agent}|${ev.name}|${ev.state}`
            if (!seen.has(key)) {
              existing.push(ev)
              seen.add(key)
            }
          }
          draft.stepEvents = existing.slice(-5000)

          const assistant = draft.messages.find((item) => item.id === assistantMessage.id)
          if (assistant) {
            if (!assistant.executionSteps) {
              assistant.executionSteps = []
            }
            const lineSeen = new Set(assistant.executionSteps.map((ev) => executionStepDedupKey(ev)))
            for (const ev of filteredSteps) {
              const lineKey = executionStepDedupKey(ev)
              if (!lineSeen.has(lineKey)) {
                assistant.executionSteps.push(ev)
                lineSeen.add(lineKey)
              }
            }
          }
        }

        const assistant = draft.messages.find((item) => item.id === assistantMessage.id)
        if (assistant) {
          if (chunk.done) {
            assistant.status = 'completed'
          } else if (assistant.status !== 'running') {
            assistant.status = inferStatus(contentChunk) ?? 'running'
          }
        }

        const user = draft.messages.find((item) => item.id === userMessage.id)
        if (user && user.status !== 'completed') {
          user.status = 'completed'
        }
        }, { touchUpdatedAt: false })
      }

     if (filteredSteps.length > 0) {
        await nextTick()
        scrollStepsToEnd()
        scrollMessagesToEnd()
      }

      if (chunk.done) {
        status.value = 'completed'
      }
    }

    if (status.value !== 'completed') {
      status.value = 'completed'
    }
    const finalStreamText = endLiveStream(conversationId, assistantMessage.id)
    updateConversation(conversationId, (draft) => {
      const assistant = draft.messages.find((item) => item.id === assistantMessage.id)
      if (assistant) {
        assistant.content = finalStreamText
        assistant.status = 'completed'
      }
    })
  } catch (error) {
    const canceled = error instanceof Error && error.name === 'AbortError'
    status.value = canceled ? 'canceled' : 'failed'
    errorText.value = canceled ? '请求已取消。' : (error as Error).message
    if (canceled) {
      errorText.value = '请求已取消。'
    }

    const partialStreamText = endLiveStream(conversationId, assistantMessage.id)
    updateConversation(conversationId, (draft) => {
      const user = draft.messages.find((item) => item.id === userMessage.id)
      if (user && user.status === 'running') {
        user.status = canceled ? 'canceled' : 'failed'
      }
      const assistant = draft.messages.find((item) => item.id === assistantMessage.id)
      if (assistant) {
        if (partialStreamText) {
          assistant.content = partialStreamText
        }
        assistant.status = canceled ? 'canceled' : 'failed'
      }
    })
  } finally {
    isStreaming.value = false
    abortController = null
  }
}

function cancelRequest(): void {
  if (!abortController) {
    return
  }
  abortController.abort()
}

function formatTime(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function onFileInput(event: Event): void {
  const input = event.target as HTMLInputElement
  const files = input.files
  if (!files || files.length === 0) {
    return
  }
  void appendFiles(Array.from(files))
  input.value = ''
}

function onDrop(event: DragEvent): void {
  event.preventDefault()
  if (!event.dataTransfer?.files) {
    return
  }
  void appendFiles(Array.from(event.dataTransfer.files))
}

async function appendFiles(files: File[]): Promise<void> {
  const validFiles = files.filter((file) => file.size <= MAX_UPLOAD_SIZE)
  if (validFiles.length !== files.length) {
    errorText.value = '部分文件超过 20MB，已跳过。'
  }
  if (validFiles.length === 0) {
    return
  }

  const formData = new FormData()
  for (const file of validFiles) {
    formData.append('files', file, file.name)
  }

  const doUpload = async (retry = true): Promise<Response> => {
    const token = getAccessToken()
    const res = await fetch(`${API_BASE_URL}/v1/files/upload`, {
      method: 'POST',
      body: formData,
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    })
    if (res.status === 401 && retry) {
      await refreshSession()
      return doUpload(false)
    }
    return res
  }

  try {
    const response = await doUpload()
    if (!response.ok) {
      throw new Error(`附件上传失败（${response.status}）`)
    }
    const payload = (await response.json()) as UploadFilesResponse
    const mapped: UploadedFileMeta[] = (payload.files ?? []).map((item) => ({
      id: item.id || uuid(),
      fileId: item.id,
      name: item.name,
      size: item.size,
      type: item.type,
      extractedText: item.extracted_text,
      parseWarning: item.warning,
    }))
    draftUploads.value = [...draftUploads.value, ...mapped]
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : '附件上传失败'
  }
}

function removeUpload(fileId: string): void {
  draftUploads.value = draftUploads.value.filter((item) => item.id !== fileId)
}

function preventDefaults(event: DragEvent): void {
  event.preventDefault()
}

function renderMarkdown(content: string): string {
  const parsed = marked.parse(content ?? '')
  return DOMPurify.sanitize(typeof parsed === 'string' ? parsed : String(parsed))
}
</script>

<template>
  <PageContainer mode="fluid">
  <div class="module-page module-page--chat assistant-console">
    <PageHeader
      eyebrow="ASSISTANT CONSOLE"
      title="对话控制台"
      description="管理会话、查看回复内容与执行过程。"
    />

    <section class="assistant-console__info-bar module-section" v-if="activeConversation">
      <div class="assistant-console__info-main">
        <p class="assistant-console__eyebrow">Console Status</p>
        <div class="assistant-console__info-title">
          <strong>{{ selectedAgentMeta?.label ?? selectedModel }}</strong>
          <span class="task-text">当前助手标识：{{ selectedModel }}</span>
        </div>
        <p class="assistant-console__info-desc">
          {{ selectedAgentMeta?.description || '当前助手正在等待新的输入。' }}
        </p>
      </div>

      <div class="assistant-console__info-meta">
        <div class="assistant-console__metric">
          <span>状态</span>
          <strong :class="['chip', status]">{{ formatTaskState(status) }}</strong>
        </div>
        <div class="assistant-console__metric">
          <span>任务编号</span>
          <strong>{{ activeConversation.taskId ?? '—' }}</strong>
        </div>
        <div class="assistant-console__metric">
          <span>运行编号</span>
          <strong>{{ activeConversation.runId ?? '—' }}</strong>
        </div>
        <div class="assistant-console__metric">
          <span>会话总数</span>
          <strong>{{ conversationCount }}</strong>
        </div>
      </div>
    </section>

    <div class="layout layout--chat workspace-layout assistant-console__layout">
      <aside class="sidebar assistant-console__sidebar">
        <div class="assistant-console__sidebar-head">
          <div class="brand assistant-console__brand">
            <p class="eyebrow">多智能体助手平台</p>
            <h1>会话列表</h1>
            <p class="assistant-console__sidebar-tip">管理历史对话并切换当前上下文。</p>
          </div>

          <button class="new-chat assistant-console__new-chat" type="button" @click="startConversation">新建对话</button>
        </div>

        <ul class="conversation-list assistant-console__conversation-list">
          <li
            v-for="item in conversations"
            :key="item.id"
            :class="['conversation-item assistant-console__conversation-item', { active: item.id === activeConversationId }]"
            @click="activeConversationId = item.id"
          >
            <div class="conversation-meta assistant-console__conversation-meta">
              <div>
                <p class="conversation-title">{{ item.title }}</p>
                <p class="assistant-console__conversation-subtitle">
                  {{ item.model }}
                  <span v-if="item.taskId">· {{ item.taskId }}</span>
                </p>
              </div>
              <p class="conversation-time">{{ formatTime(item.updatedAt) }}</p>
            </div>
            <div class="conversation-actions">
              <button type="button" @click.stop="renameConversation(item.id)">重命名</button>
              <button type="button" @click.stop="removeConversation(item.id)">删除</button>
            </div>
          </li>
        </ul>
      </aside>

      <main class="chat-panel chat-page chat-main module-section assistant-console__main" v-if="activeConversation">
        <header class="assistant-console__main-head">
          <div class="assistant-console__main-title">
            <p class="assistant-console__eyebrow">Conversation</p>
            <h2>{{ activeConversation.title }}</h2>
          </div>
          <div class="assistant-console__main-badges">
            <span class="chip tiny">{{ selectedAgentMeta?.label ?? selectedModel }}</span>
            <span class="chip tiny queued">附件 {{ activeAttachmentCount }}</span>
          </div>
        </header>

        <section class="chat-main-body assistant-console__body">
          <section class="messages-panel assistant-console__messages-panel">
            <section class="messages assistant-console__messages" ref="messageScroller" @scroll.passive="onMessagesScroll">
              <article
                v-for="msg in activeConversation.messages"
                :key="msg.id"
                :class="['message assistant-console__message', msg.role, { streaming: isStreamingAssistantMessage(msg) }]"
              >
                <header>
                  <strong>{{ msg.role === 'user' ? '你' : '助手' }}</strong>
                  <span>{{ formatTime(msg.createdAt) }}</span>
                  <span v-if="msg.role === 'user'" :class="['chip tiny', msg.status ?? 'queued']">
                    {{ formatTaskState(msg.status ?? 'queued') }}
                  </span>
                </header>
                <section v-if="showInlineProgress(msg)" class="assistant-progress-card">
                  <div class="assistant-progress-head">执行进度</div>
                  <ul class="assistant-progress-list">
                    <li
                      v-for="ev in msg.executionSteps"
                      :key="executionStepDedupKey(ev)"
                      class="assistant-progress-item"
                    >
                      <div class="assistant-progress-row">
                        <span class="assistant-progress-time">{{ formatTime(ev.ts) }}</span>
                        <span :class="['chip', 'tiny', stepChipClass(ev.state)]">{{ stepStateLabel(ev.state) }}</span>
                        <span v-if="(ev.agent ?? '').trim()" class="assistant-progress-agent">{{ ev.agent }}</span>
                      </div>
                      <span class="assistant-progress-text">{{ ev.messageZh }}</span>
                    </li>
                  </ul>
                </section>
                <p v-if="messageContentForRender(msg) && isStreamingAssistantMessage(msg)" class="content">
                  {{ messageContentForRender(msg) }}
                </p>
                <div
                  v-else-if="messageContentForRender(msg)"
                  class="content markdown"
                  v-html="renderMarkdown(messageContentForRender(msg))"
                ></div>
                <p v-else class="content">{{ msg.role === 'assistant' ? '...' : '' }}</p>
                <ul v-if="msg.attachments && msg.attachments.length" class="attachment-list">
                  <li v-for="file in msg.attachments" :key="file.id">{{ file.name }} ({{ file.size }} bytes)</li>
                </ul>
              </article>
            </section>
          </section>

          <section class="composer-panel assistant-console__composer-panel">
            <section class="upload-zone assistant-console__upload-zone" @dragenter="preventDefaults" @dragover="preventDefaults" @drop="onDrop">
              <div class="assistant-console__upload-head">
                <label for="upload-input">添加附件</label>
                <span class="task-text">支持 PDF / Office，单个最大 20MB</span>
              </div>
              <input
                id="upload-input"
                type="file"
                accept=".pdf,.doc,.docx,.xls,.xlsx"
                multiple
                @change="onFileInput"
              />
              <p>拖拽文件到这里或点击选择，当前版本仅发送文件元信息与可提取文本内容。</p>
              <ul v-if="draftUploads.length" class="draft-files">
                <li v-for="file in draftUploads" :key="file.id">
                  <span>{{ file.name }} ({{ file.size }} bytes)</span>
                  <button type="button" @click="removeUpload(file.id)">移除</button>
                </li>
              </ul>
            </section>

            <footer class="composer assistant-console__composer">
              <textarea
                v-model="prompt"
                rows="4"
                placeholder="请输入问题，按 Shift+Enter 换行。"
                @keydown.enter.exact.prevent="sendMessage"
              />
              <div class="composer-actions assistant-console__composer-actions">
                <p class="error" v-if="errorText">{{ errorText }}</p>
                <div class="buttons">
                  <button type="button" class="cancel" :disabled="!isStreaming" @click="cancelRequest">取消</button>
                  <button type="button" class="send" :disabled="!canSend" @click="sendMessage">发送</button>
                </div>
              </div>
            </footer>
          </section>
        </section>
      </main>

      <aside class="preview-panel chat-execution module-section assistant-console__execution" v-if="activeConversation">
        <header class="assistant-console__execution-head">
          <div>
            <p class="assistant-console__eyebrow">Execution</p>
            <strong>执行过程</strong>
          </div>
          <span class="task-text" v-if="runPollError">{{ runPollError }}</span>
        </header>



        <section class="step-bar assistant-console__step-bar">
          <strong class="step-bar-title">运行步骤</strong>
          <p class="task-text" v-if="monitorStepError">{{ monitorStepError }}</p>
          <p class="task-text" v-if="!runStepEvents.length">暂无步骤</p>
          <div
            v-if="runStepEvents.length"
            class="step-scroller"
            ref="stepScroller"
            title="可上下滚动查看所有步骤"
            @scroll.passive="onStepScroll"
          >
            <div class="step-track">
              <div
                v-for="ev in runStepEvents"
                :key="`${ev.ts}-${ev.agent}-${ev.phase}-${ev.name}-${ev.state}-${ev.messageZh}`"
                class="step-row assistant-console__step-row"
              >
                <span class="task-text">{{ formatTime(ev.ts) }}</span>
                <span :class="['chip', 'tiny', stepChipClass(ev.state)]">{{ stepStateLabel(ev.state) }}</span>
                <span class="task-text">{{ ev.agent }}</span>
                <span class="step-row-message">{{ ev.messageZh }}</span>
              </div>
            </div>
          </div>
          <div class="composer-actions" v-if="!stepAutoFollow && runStepEvents.length">
            <div class="buttons">
              <button type="button" class="send" @click="jumpStepsToBottom">回到底部</button>
            </div>
          </div>
        </section>
      </aside>
    </div>
  </div>
  </PageContainer>
</template>

<style scoped>
.assistant-console {
  gap: 14px;
}

.assistant-console__info-bar {
  display: grid;
  grid-template-columns: minmax(0, 1.2fr) minmax(420px, 0.8fr);
  gap: 16px;
  padding: 16px 18px;
  background:
    radial-gradient(circle at 10% 12%, rgba(182, 225, 207, 0.18), transparent 28%),
    radial-gradient(circle at 92% 18%, rgba(220, 203, 244, 0.18), transparent 30%),
    var(--bg-panel);
}

.assistant-console__info-main,
.assistant-console__info-meta,
.assistant-console__main-title {
  display: grid;
  gap: 8px;
}

.assistant-console__eyebrow {
  margin: 0;
  font-size: 11px;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: #5f8a78;
  font-weight: 700;
}

.assistant-console__info-title {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
}

.assistant-console__info-title strong {
  font-family: var(--font-display);
  font-size: 28px;
  line-height: 1.1;
}

.assistant-console__info-desc,
.assistant-console__sidebar-tip,
.assistant-console__conversation-subtitle {
  margin: 0;
  color: var(--text-muted);
  font-size: 13px;
  line-height: 1.6;
}

.assistant-console__info-meta {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.assistant-console__metric {
  border: 1px solid var(--line);
  border-radius: 16px;
  background: rgba(255, 255, 255, 0.74);
  padding: 12px 14px;
  display: grid;
  gap: 8px;
}

.assistant-console__metric span {
  color: var(--text-muted);
  font-size: 12px;
}

.assistant-console__metric strong {
  font-size: 14px;
  line-height: 1.4;
  word-break: break-word;
}

.assistant-console__layout.layout--chat {
  grid-template-columns: 280px minmax(720px, 1fr) 320px;
  gap: 16px;
}

.assistant-console__sidebar,
.assistant-console__main,
.assistant-console__execution {
  max-height: calc(100vh - 218px);
}

.assistant-console__sidebar {
  padding: 14px;
  gap: 14px;
}

.assistant-console__sidebar-head {
  display: grid;
  gap: 12px;
}

.assistant-console__brand {
  gap: 6px;
}

.assistant-console__brand h1 {
  margin: 6px 0 0;
}

.assistant-console__new-chat {
  width: 100%;
  min-height: 44px;
}

.assistant-console__conversation-list {
  gap: 10px;
}

.assistant-console__conversation-item {
  padding: 12px;
  border-radius: 16px;
}

.assistant-console__conversation-item.active {
  border-color: #b9c6d8;
  background: linear-gradient(132deg, rgba(204, 232, 220, 0.38), rgba(226, 216, 246, 0.26));
}

.assistant-console__conversation-meta {
  align-items: flex-start;
}

.assistant-console__main {
  padding: 16px;
  gap: 14px;
}

.assistant-console__main-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: flex-start;
  border: 1px solid var(--line);
  border-radius: 18px;
  background: rgba(255, 255, 255, 0.78);
  padding: 14px 16px;
}

.assistant-console__main-title h2 {
  margin: 0;
  font-family: var(--font-display);
  font-size: 24px;
  line-height: 1.15;
}

.assistant-console__main-badges {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.assistant-console__body {
  gap: 12px;
}

.assistant-console__messages-panel,
.assistant-console__composer-panel {
  border-radius: 18px;
}

.assistant-console__messages-panel {
  padding: 12px;
}

.assistant-console__messages {
  gap: 12px;
  padding-right: 6px;
}

.assistant-console__message {
  border-radius: 18px;
  padding: 12px 14px;
}

.assistant-console__message.user {
  background: linear-gradient(135deg, rgba(244, 250, 246, 0.96), rgba(255, 255, 255, 0.94));
}

.assistant-console__message.assistant {
  background: linear-gradient(135deg, rgba(255, 255, 255, 0.96), rgba(249, 250, 252, 0.94));
}

.assistant-console__upload-zone {
  border-radius: 16px;
  background: linear-gradient(135deg, rgba(247, 250, 248, 0.9), rgba(249, 248, 252, 0.88));
}

.assistant-console__upload-head {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
}

.assistant-console__composer {
  border-top: none;
  padding-top: 0;
}

.assistant-console__composer textarea {
  min-height: 116px;
  border-radius: 14px;
}

.assistant-console__composer-actions {
  align-items: flex-end;
}

.assistant-console__execution {
  padding: 14px;
  gap: 12px;
}

.assistant-console__execution-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: flex-start;
}

.assistant-console__execution-summary {
  border-radius: 16px;
  background: linear-gradient(135deg, rgba(247, 250, 248, 0.88), rgba(249, 248, 252, 0.86));
}

.assistant-console__step-bar {
  border-radius: 16px;
}

.assistant-console__step-row {
  align-items: center;
}

.assistant-progress-card {
  margin-bottom: 10px;
  padding: 10px 12px;
  border-radius: 12px;
  border: 1px solid var(--line, #e2e8f0);
  background: rgba(255, 255, 255, 0.65);
}

.assistant-progress-head {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
  color: var(--text-muted, #64748b);
  margin-bottom: 8px;
}

.assistant-progress-list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.assistant-progress-item {
  padding: 6px 0;
  border-bottom: 1px dashed rgba(148, 163, 184, 0.35);
  font-size: 13px;
  line-height: 1.45;
}

.assistant-progress-item:last-child {
  border-bottom: none;
  padding-bottom: 0;
}

.assistant-progress-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.assistant-progress-time {
  font-size: 11px;
  color: var(--text-muted, #94a3b8);
  font-variant-numeric: tabular-nums;
}

.assistant-progress-agent {
  font-size: 11px;
  color: var(--text-muted, #94a3b8);
}

.assistant-progress-text {
  display: block;
  color: var(--text, #1e293b);
}

@media (max-width: 1280px) {
  .assistant-console__info-bar {
    grid-template-columns: 1fr;
  }

  .assistant-console__info-meta {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .assistant-console__layout.layout--chat {
    grid-template-columns: 250px minmax(0, 1fr) 300px;
  }
}

@media (max-width: 960px) {
  .assistant-console__layout.layout--chat {
    grid-template-columns: 1fr;
  }

  .assistant-console__sidebar,
  .assistant-console__main,
  .assistant-console__execution {
    max-height: none;
  }

  .assistant-console__info-meta {
    grid-template-columns: 1fr;
  }
}
</style>
