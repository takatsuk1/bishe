<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { getWorkflow } from '../lib/orchestratorApi'
import {
  ackMonitorAlert,
  getMonitorOverview,
  getMonitorRunFamily,
  getMonitorRunDetail,
  listMonitorAlerts,
  listMonitorRunEvents,
  listMonitorRuns,
  resolveMonitorAlert,
} from '../lib/monitorApi'
import { currentUser } from '../lib/authStore'
import { canManageMonitorAlerts, canReadAllMonitor, currentPrimaryRole } from '../lib/permission'
import type { MonitorAlert, MonitorEvent, MonitorRun, MonitorRunDetail } from '../types/monitor'
import type { EdgeDefinition, NodeDefinition, WorkflowDefinition } from '../types/workflow'

type RuntimeStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'timeout' | 'retrying' | 'skipped'
type PanelTab = 'node' | 'events' | 'alerts'

interface NodeRuntimeInfo {
  nodeId: string
  runtimeStatus: RuntimeStatus
  durationMs: number
  errorMessage: string
  alertLevel: string
  lastEventTime: string
  startedAt: string
  endedAt: string
  inputSnapshot: string
  outputSnapshot: string
}

const nodeBox = { width: 210, height: 92 }

const loading = ref(false)
const loadingRun = ref(false)
const pageError = ref('')
const runError = ref('')

const runs = ref<MonitorRun[]>([])
const runPage = ref(1)
const runPageSize = ref(20)
const runTotal = ref(0)
const monitorScope = ref<'own' | 'all'>('own')
const alertActionLoading = ref<Record<string, string>>({})

const overview = ref({
  totalRuns: 0,
  succeededRuns: 0,
  failedRuns: 0,
  successRate: 0,
  averageDurationMs: 0,
  alertTotal: 0,
  recentRuns: [] as MonitorRun[],
})

const selectedRunId = ref('')
const selectedGraphRunId = ref('')
const selectedRunDetail = ref<MonitorRunDetail | null>(null)
const runFamily = ref<MonitorRun[]>([])
const runDetailsById = ref<Record<string, MonitorRunDetail>>({})
const runEventsById = ref<Record<string, MonitorEvent[]>>({})
const runAlertsById = ref<Record<string, MonitorAlert[]>>({})
const workflowDefByRunId = ref<Record<string, WorkflowDefinition | null>>({})
const workflowResolveCache = new Map<string, WorkflowDefinition | null>()
let selectedRunPollTimer: number | null = null
let selectedRunPollFailures = 0

const selectedNodeId = ref('')
const activeTab = ref<PanelTab>('node')

const selectedNode = computed(() => {
  const wf = currentWorkflowDef.value
  if (!wf || !selectedNodeId.value) {
    return null
  }
  return wf.nodes.find((n) => n.id === selectedNodeId.value) ?? null
})

const currentRunId = computed(() => selectedGraphRunId.value || selectedRunId.value)

const currentRunDetail = computed(() => {
  const runId = currentRunId.value
  if (!runId) {
    return null
  }
  return runDetailsById.value[runId] ?? null
})

const currentRunEvents = computed(() => {
  const runId = currentRunId.value
  if (!runId) {
    return [] as MonitorEvent[]
  }
  return runEventsById.value[runId] ?? []
})

const currentRunAlerts = computed(() => {
  const runId = currentRunId.value
  if (!runId) {
    return [] as MonitorAlert[]
  }
  const alerts = runAlertsById.value[runId] ?? []
  if (monitorScope.value === 'all' || !canReadAllMonitor()) {
    return alerts
  }
  const userID = String(currentUser.value?.userId || '').trim()
  if (!userID) {
    return alerts
  }
  return alerts.filter((item) => {
    const owner = String((item as any).userId || '').trim()
    return !owner || owner === userID
  })
})

const visibleRuns = computed(() => {
  if (monitorScope.value === 'all' || !canReadAllMonitor()) {
    return runs.value
  }
  const userID = String(currentUser.value?.userId || '').trim()
  if (!userID) {
    return runs.value
  }
  return runs.value.filter((run) => String(run.userId || '').trim() === userID)
})

const canSwitchAllScope = computed(() => canReadAllMonitor())
const canOperateAlerts = computed(() => canManageMonitorAlerts())
const roleLabel = computed(() => currentPrimaryRole.value)

const currentWorkflowDef = computed(() => {
  const runId = currentRunId.value
  if (!runId) {
    return null
  }
  return workflowDefByRunId.value[runId] ?? null
})

const allEventsTimeline = computed(() => {
  const merged: MonitorEvent[] = []
  Object.values(runEventsById.value).forEach((items) => merged.push(...items))
  return merged.sort((a, b) => {
    return new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime()
  })
})

const eventsTimeline = computed(() => allEventsTimeline.value)

const terminalNodeStatusByRunNode = computed<Record<string, 'succeeded' | 'failed'>>(() => {
  const out: Record<string, 'succeeded' | 'failed'> = {}
  for (const ev of allEventsTimeline.value) {
    const runId = (ev.runId || '').trim()
    const nodeId = (ev.nodeId || '').trim()
    if (!runId || !nodeId) {
      continue
    }
    if (ev.eventType === 'node_failed' || ev.status === 'failed' || ev.status === 'timeout') {
      out[`${runId}|${nodeId}`] = 'failed'
      continue
    }
    if (ev.eventType === 'node_finished' || ev.status === 'succeeded') {
      if (!out[`${runId}|${nodeId}`]) {
        out[`${runId}|${nodeId}`] = 'succeeded'
      }
    }
  }
  return out
})

const terminalWorkflowStatusByRun = computed<Record<string, 'succeeded' | 'failed'>>(() => {
  const out: Record<string, 'succeeded' | 'failed'> = {}
  for (const ev of allEventsTimeline.value) {
    const runId = (ev.runId || '').trim()
    if (!runId) {
      continue
    }
    if (ev.eventType === 'workflow_failed') {
      out[runId] = 'failed'
      continue
    }
    if (ev.eventType === 'workflow_finished') {
      if (!out[runId]) {
        out[runId] = 'succeeded'
      }
    }
  }
  return out
})

const nodeRuntimeMap = computed<Record<string, NodeRuntimeInfo>>(() => {
  const map: Record<string, NodeRuntimeInfo> = {}
  const workflowNodeByCanonical: Record<string, string> = {}
  const workflowId = (currentRunDetail.value?.run.workflowId || '').trim().toLowerCase()
  const canonicalAliasMap: Record<string, string> = {}

  if (workflowId === 'host-default' || workflowId === 'host') {
    canonicalAliasMap.route = 'chat_model'
    canonicalAliasMap.agent_info = 'agent_info'
    canonicalAliasMap.direct_answer = 'direct_answer'
    canonicalAliasMap.call_agent = 'call_agent'
  }
  if (workflowId === 'deepresearch-default' || workflowId === 'deepresearch') {
    canonicalAliasMap.judge = 'judge_satisfied'
    canonicalAliasMap.extract_keywords = 'extract_query'
    canonicalAliasMap.tavily = 'tavily_search'
  }

  const resolveNodeId = (rawNodeId: string): string => {
    const trimmed = (rawNodeId || '').trim()
    if (!trimmed) {
      return ''
    }
    if (map[trimmed]) {
      return trimmed
    }
    const canonical = canonicalNodeId(trimmed)
    if (canonicalAliasMap[canonical] && workflowNodeByCanonical[canonicalAliasMap[canonical]]) {
      return workflowNodeByCanonical[canonicalAliasMap[canonical]]
    }
    return workflowNodeByCanonical[canonical] || trimmed
  }

  if (currentWorkflowDef.value) {
    for (const node of currentWorkflowDef.value.nodes) {
      workflowNodeByCanonical[canonicalNodeId(node.id)] = node.id
      map[node.id] = {
        nodeId: node.id,
        runtimeStatus: 'pending',
        durationMs: 0,
        errorMessage: '',
        alertLevel: '',
        lastEventTime: '',
        startedAt: '',
        endedAt: '',
        inputSnapshot: '',
        outputSnapshot: '',
      }
    }
  }

  // Always map common runtime node ids to explicit UI node ids when those ids exist in current graph.
  for (const [runtimeAlias, uiAlias] of Object.entries(canonicalAliasMap)) {
    const mappedUI = workflowNodeByCanonical[canonicalNodeId(uiAlias)]
    if (mappedUI) {
      workflowNodeByCanonical[runtimeAlias] = mappedUI
    }
  }

  const failedNodes = new Set<string>()
  const timeoutNodes = new Set<string>()
  const retryingNodes = new Set<string>()
  const succeededNodes = new Set<string>()
  const runningNodes = new Set<string>()

  for (const event of currentRunEvents.value) {
    const nodeId = resolveNodeId(event.nodeId ?? '')
    if (!nodeId) {
      continue
    }
    if (!map[nodeId]) {
      map[nodeId] = {
        nodeId,
        runtimeStatus: 'pending',
        durationMs: 0,
        errorMessage: '',
        alertLevel: '',
        lastEventTime: '',
        startedAt: '',
        endedAt: '',
        inputSnapshot: '',
        outputSnapshot: '',
      }
    }

    const item = map[nodeId]
    item.lastEventTime = event.createdAt
    if (event.inputSnapshot) {
      item.inputSnapshot = event.inputSnapshot
    }
    if (event.outputSnapshot) {
      item.outputSnapshot = event.outputSnapshot
    }
    if (event.errorMessage) {
      item.errorMessage = event.errorMessage
    }
    if (event.durationMs > 0) {
      item.durationMs = event.durationMs
    }

    if (event.eventType === 'node_started') {
      item.startedAt = event.createdAt
      runningNodes.add(nodeId)
      continue
    }
    if (event.eventType === 'node_finished') {
      item.endedAt = event.createdAt
      succeededNodes.add(nodeId)
      runningNodes.delete(nodeId)
      continue
    }
    if (event.eventType === 'node_failed') {
      item.endedAt = event.createdAt
      failedNodes.add(nodeId)
      runningNodes.delete(nodeId)
      continue
    }
    if (event.eventType === 'timeout_triggered' || event.status === 'timeout') {
      timeoutNodes.add(nodeId)
      runningNodes.delete(nodeId)
      continue
    }
    if (event.eventType === 'retry_triggered' || event.status === 'retrying') {
      retryingNodes.add(nodeId)
      runningNodes.add(nodeId)
    }
  }

  // Runtime status precedence: failed > timeout > retrying > succeeded > running > pending/skipped.
  Object.keys(map).forEach((nodeId) => {
    if (failedNodes.has(nodeId)) {
      map[nodeId].runtimeStatus = 'failed'
      return
    }
    if (timeoutNodes.has(nodeId)) {
      map[nodeId].runtimeStatus = 'timeout'
      return
    }
    if (retryingNodes.has(nodeId)) {
      map[nodeId].runtimeStatus = 'retrying'
      return
    }
    if (succeededNodes.has(nodeId)) {
      map[nodeId].runtimeStatus = 'succeeded'
      return
    }
    if (runningNodes.has(nodeId)) {
      map[nodeId].runtimeStatus = 'running'
    }
  })

  const currentNodeId = resolveNodeId(currentRunDetail.value?.run.currentNodeId ?? '')
  const runStatus = (currentRunDetail.value?.run.status || '').trim()
  if (currentNodeId && runStatus === 'running' && map[currentNodeId] && map[currentNodeId].runtimeStatus === 'pending') {
    map[currentNodeId].runtimeStatus = 'running'
  }

  for (const alert of currentRunAlerts.value) {
    const nodeId = resolveNodeId(alert.nodeId ?? '')
    if (!nodeId || !map[nodeId]) {
      continue
    }
    const nextLevel = (alert.severity ?? '').trim().toLowerCase()
    if (!map[nodeId].alertLevel || severityRank(nextLevel) > severityRank(map[nodeId].alertLevel)) {
      map[nodeId].alertLevel = nextLevel
    }
  }

  const isTerminal = runStatus === 'succeeded' || runStatus === 'failed' || runStatus === 'canceled'
  if (isTerminal) {
    Object.values(map).forEach((item) => {
      if (item.runtimeStatus === 'pending') {
        item.runtimeStatus = 'skipped'
      }
    })
  }

  return map
})

const selectedNodeRuntime = computed(() => {
  const nodeId = selectedNodeId.value
  if (!nodeId) {
    return null
  }
  return nodeRuntimeMap.value[nodeId] ?? null
})

function severityRank(level: string): number {
  switch (level) {
    case 'critical':
      return 4
    case 'high':
      return 3
    case 'medium':
      return 2
    case 'low':
      return 1
    default:
      return 0
  }
}

function canonicalNodeId(value: string): string {
  const raw = (value || '').trim().toLowerCase()
  if (!raw) {
    return ''
  }
  let normalized = raw
  if (normalized.startsWith('n_')) {
    normalized = normalized.slice(2)
  }
  return normalized.replace(/[^a-z0-9_]/g, '')
}

function getUiAgent(node: NodeDefinition): string {
  return node.metadata?.['ui.agent'] ?? node.agentId ?? ''
}

function getUiLabel(node: NodeDefinition): string {
  return node.metadata?.['ui.label'] ?? node.type
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
    x: clampNumber(Number.isFinite(x) ? x : 80, 0, 4000),
    y: clampNumber(Number.isFinite(y) ? y : 80, 0, 4000),
  }
}

function nodeCenter(nodeId: string): { x: number; y: number } {
  const wf = currentWorkflowDef.value
  if (!wf) {
    return { x: 0, y: 0 }
  }
  const node = wf.nodes.find((n) => n.id === nodeId)
  if (!node) {
    return { x: 0, y: 0 }
  }
  const pos = getNodePos(node)
  return { x: pos.x + nodeBox.width / 2, y: pos.y + nodeBox.height / 2 }
}

function edgePath(edge: EdgeDefinition): string {
  const a = nodeCenter(edge.from)
  const b = nodeCenter(edge.to)
  const dx = Math.max(120, Math.abs(b.x - a.x) * 0.35)
  const c1x = a.x + dx
  const c2x = b.x - dx
  return `M ${a.x} ${a.y} C ${c1x} ${a.y}, ${c2x} ${b.y}, ${b.x} ${b.y}`
}

function edgeMidpoint(edge: EdgeDefinition): { x: number; y: number } {
  const a = nodeCenter(edge.from)
  const b = nodeCenter(edge.to)
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
}

function inferEdgeLabel(edge: EdgeDefinition): string {
  const wf = currentWorkflowDef.value
  if (!wf) {
    return edge.label ?? ''
  }
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

function nodeRuntimeStatus(nodeId: string): RuntimeStatus {
  return nodeRuntimeMap.value[nodeId]?.runtimeStatus ?? 'pending'
}

function nodeRuntimeClass(nodeId: string): string {
  return `runtime-${nodeRuntimeStatus(nodeId)}`
}

function hasNodeAlert(nodeId: string): boolean {
  return Boolean(nodeRuntimeMap.value[nodeId]?.alertLevel)
}

function nodeDuration(nodeId: string): string {
  const value = nodeRuntimeMap.value[nodeId]?.durationMs ?? 0
  if (value <= 0) {
    return '-'
  }
  return `${value}ms`
}

function runStatusClass(status: string): string {
  if (status === 'succeeded') {
    return 'completed'
  }
  if (status === 'failed') {
    return 'failed'
  }
  if (status === 'running') {
    return 'running'
  }
  if (status === 'skipped') {
    return 'queued'
  }
  return 'queued'
}

function eventStatusClass(status: string): string {
  if (status === 'succeeded') {
    return 'completed'
  }
  if (status === 'failed' || status === 'timeout') {
    return 'failed'
  }
  if (status === 'running' || status === 'retrying') {
    return 'running'
  }
  return 'queued'
}

function eventSummary(event: MonitorEvent): string {
  const nodeText = event.nodeId ? `节点 ${event.nodeId}` : '工作流'
  const statusText = eventDisplayStatus(event)
  switch (event.eventType) {
    case 'workflow_started':
      return `开始：${event.workflowId || 'workflow'} 开始执行`
    case 'workflow_finished':
      return `完成：${event.workflowId || 'workflow'} 执行完成`
    case 'workflow_failed':
      return `完成：${event.workflowId || 'workflow'} 执行失败`
    case 'node_started':
      return `开始：${nodeText}`
    case 'node_finished':
      return `完成：${nodeText}`
    case 'node_failed':
      return `完成：${nodeText} 失败`
    case 'model_called':
      return `开始：${nodeText} 调用模型`
    case 'agent_called':
      return `开始：${nodeText} 调用子 Agent`
    case 'tool_called':
      return `开始：${nodeText} 调用工具`
    case 'alert_triggered':
      return `告警：${event.message || '触发告警'} (${statusText})`
    default:
      return event.message || `${event.eventType} (${statusText})`
  }
}

function eventDisplayStatus(event: MonitorEvent): string {
  const runId = (event.runId || '').trim()
  const nodeId = (event.nodeId || '').trim()

  if (event.eventType === 'workflow_started') {
    const terminal = terminalWorkflowStatusByRun.value[runId]
    return terminal || event.status || 'running'
  }

  if (event.eventType === 'node_started' || event.eventType === 'tool_called' || event.eventType === 'agent_called' || event.eventType === 'model_called') {
    const terminal = terminalNodeStatusByRunNode.value[`${runId}|${nodeId}`]
    return terminal || event.status || 'running'
  }

  return event.status || 'queued'
}

function graphRunLabel(run: MonitorRun): string {
  const agent = (run.sourceAgentId || '').trim()
  if (agent) {
    return `${agent} · ${run.workflowId}`
  }
  return run.workflowId
}

function formatDate(value?: string): string {
  if (!value) {
    return '-'
  }
  const ts = new Date(value)
  if (Number.isNaN(ts.getTime())) {
    return '-'
  }
  return ts.toLocaleString()
}

function formatDuration(durationMs: number): string {
  if (!durationMs || durationMs <= 0) {
    return '-'
  }
  if (durationMs < 1000) {
    return `${durationMs}ms`
  }
  return `${(durationMs / 1000).toFixed(2)}s`
}

async function loadOverview(): Promise<void> {
  overview.value = await getMonitorOverview()
}

async function loadRuns(autoSelect: boolean): Promise<void> {
  const data = await listMonitorRuns({ page: runPage.value, pageSize: runPageSize.value })
  runs.value = data.items
  runTotal.value = data.total

  if (!autoSelect) {
    return
  }

  const visible = visibleRuns.value
  if (visible.length > 0) {
    await selectRun(visible[0].runId)
  } else {
    selectedRunId.value = ''
    selectedGraphRunId.value = ''
    selectedRunDetail.value = null
    runFamily.value = []
    runDetailsById.value = {}
    runEventsById.value = {}
    runAlertsById.value = {}
    workflowDefByRunId.value = {}
    selectedNodeId.value = ''
  }
}

function canAck(alert: MonitorAlert): boolean {
  const status = String(alert.status || '').trim().toLowerCase()
  return status !== 'ack' && status !== 'resolved'
}

function canResolve(alert: MonitorAlert): boolean {
  const status = String(alert.status || '').trim().toLowerCase()
  return status !== 'resolved'
}

function actionLoading(alertId: string): string {
  return alertActionLoading.value[alertId] || ''
}

async function handleAlertAction(alertId: string, action: 'ack' | 'resolve'): Promise<void> {
  if (!canOperateAlerts.value) {
    return
  }
  alertActionLoading.value = {
    ...alertActionLoading.value,
    [alertId]: action,
  }
  try {
    if (action === 'ack') {
      await ackMonitorAlert(alertId)
    } else {
      await resolveMonitorAlert(alertId)
    }
    if (selectedRunId.value) {
      await selectRun(selectedRunId.value, { silent: true })
    }
  } catch (err) {
    runError.value = err instanceof Error ? err.message : '告警操作失败'
  } finally {
    const next = { ...alertActionLoading.value }
    delete next[alertId]
    alertActionLoading.value = next
  }
}

async function refreshAll(): Promise<void> {
  loading.value = true
  pageError.value = ''
  try {
    await Promise.all([loadOverview(), loadRuns(true)])
  } catch (err) {
    pageError.value = err instanceof Error ? err.message : '加载监控数据失败'
  } finally {
    loading.value = false
  }
}

async function selectRun(runId: string, opts?: { silent?: boolean }): Promise<void> {
  if (!runId) {
    return
  }
  const silent = opts?.silent === true
  selectedRunId.value = runId
  if (!silent) {
    loadingRun.value = true
    runError.value = ''
  }

  try {
    const familyResp = await getMonitorRunFamily(runId, { limit: 20 })
    const family = familyResp.runs.length > 0 ? familyResp.runs : [{ ...(await getMonitorRunDetail(runId)).run }]

    const detailsList = await Promise.all(family.map((run) => getMonitorRunDetail(run.runId)))
    const detailsMap: Record<string, MonitorRunDetail> = {}
    detailsList.forEach((detail) => {
      detailsMap[detail.run.runId] = detail
    })

    const eventsList = await Promise.all(
      family.map((run) => listMonitorRunEvents(run.runId, { page: 1, pageSize: 500 })),
    )
    const alertsList = await Promise.all(
      family.map((run) => listMonitorAlerts({ runId: run.runId, page: 1, pageSize: 200 })),
    )
    const workflowDefs = await Promise.all(
      detailsList.map((detail) => resolveWorkflowDefinition(detail)),
    )

    const eventsMap: Record<string, MonitorEvent[]> = {}
    const alertsMap: Record<string, MonitorAlert[]> = {}
    const wfMap: Record<string, WorkflowDefinition | null> = {}
    family.forEach((run, idx) => {
      eventsMap[run.runId] = eventsList[idx].items
      alertsMap[run.runId] = alertsList[idx].items
      wfMap[run.runId] = workflowDefs[idx]
    })

    runFamily.value = family
    runDetailsById.value = detailsMap
    runEventsById.value = eventsMap
    runAlertsById.value = alertsMap
    workflowDefByRunId.value = wfMap

    const graphRunId = family[0].runId
    selectedGraphRunId.value = graphRunId
    selectedRunDetail.value = detailsMap[graphRunId] ?? null

    const activeWorkflow = wfMap[graphRunId]
    const activeDetail = detailsMap[graphRunId]
    if (activeWorkflow) {
      const runningNodeId = activeDetail?.run.currentNodeId?.trim() ?? ''
      if (runningNodeId && activeWorkflow.nodes.some((n) => n.id === runningNodeId)) {
        selectedNodeId.value = runningNodeId
      } else if (activeWorkflow.nodes.length > 0) {
        selectedNodeId.value = activeWorkflow.nodes[0].id
      } else {
        selectedNodeId.value = ''
      }
    } else {
      selectedNodeId.value = ''
    }
  } catch (err) {
    runError.value = err instanceof Error ? err.message : '加载运行详情失败'
    selectedRunDetail.value = null
    selectedGraphRunId.value = ''
    runFamily.value = []
    runDetailsById.value = {}
    runEventsById.value = {}
    runAlertsById.value = {}
    workflowDefByRunId.value = {}
    selectedNodeId.value = ''
  } finally {
    if (!silent) {
      loadingRun.value = false
    }
  }
}

function selectGraphRun(runId: string): void {
  if (!runId || runId === selectedGraphRunId.value) {
    return
  }
  selectedGraphRunId.value = runId
  selectedRunDetail.value = runDetailsById.value[runId] ?? null

  const wf = workflowDefByRunId.value[runId]
  if (!wf || wf.nodes.length === 0) {
    selectedNodeId.value = ''
    return
  }

  const runningNodeId = selectedRunDetail.value?.run.currentNodeId?.trim() ?? ''
  if (runningNodeId && wf.nodes.some((n) => n.id === runningNodeId)) {
    selectedNodeId.value = runningNodeId
    return
  }
  selectedNodeId.value = wf.nodes[0].id
}

function buildWorkflowLookupCandidates(detail: MonitorRunDetail): string[] {
  const unique: string[] = []
  const add = (value?: string): void => {
    const id = (value ?? '').trim()
    if (!id || unique.includes(id)) {
      return
    }
    unique.push(id)
  }

  const workflowId = (detail.run.workflowId ?? '').trim()
  const sourceAgentId = (detail.run.sourceAgentId ?? '').trim()
  if (workflowId.endsWith('-default')) {
    add(sourceAgentId)
    add(workflowId.slice(0, -'-default'.length))
    add(workflowId)
  } else {
    add(workflowId)
    add(sourceAgentId)
  }

  return unique
}

function normalizeWorkflowDefinition(raw: any): WorkflowDefinition {
  const nodes = Array.isArray(raw?.nodes)
    ? raw.nodes.map((node: any) => ({
        ...node,
        id: (node?.id ?? node?.nodeId ?? '').toString(),
      }))
    : []

  const edges = Array.isArray(raw?.edges)
    ? raw.edges.map((edge: any) => ({
        ...edge,
        from: (edge?.from ?? edge?.fromNodeId ?? '').toString(),
        to: (edge?.to ?? edge?.toNodeId ?? '').toString(),
      }))
    : []

  return {
    ...(raw ?? {}),
    id: (raw?.id ?? raw?.workflowId ?? '').toString(),
    startNodeId: (raw?.startNodeId ?? raw?.start_node_id ?? '').toString(),
    nodes,
    edges,
  } as WorkflowDefinition
}

async function resolveWorkflowDefinition(detail: MonitorRunDetail): Promise<WorkflowDefinition | null> {
  const candidates = buildWorkflowLookupCandidates(detail)
  for (const workflowId of candidates) {
    if (workflowResolveCache.has(workflowId)) {
      const cached = workflowResolveCache.get(workflowId) ?? null
      if (cached) {
        return cached
      }
      continue
    }
    try {
      const resp = await getWorkflow(workflowId)
      const normalized = normalizeWorkflowDefinition(resp.definition)
      workflowResolveCache.set(workflowId, normalized)
      return normalized
    } catch {
      workflowResolveCache.set(workflowId, null)
    }
  }
  return null
}

function startSelectedRunPolling(): void {
  stopSelectedRunPolling()
  selectedRunPollTimer = window.setInterval(async () => {
    const runId = selectedRunId.value
    if (!runId || loadingRun.value) {
      return
    }

    const status = selectedRunDetail.value?.run.status ?? ''
    if (status && status !== 'running') {
      return
    }

    try {
      await selectRun(runId, { silent: true })
      selectedRunPollFailures = 0
    } catch {
      selectedRunPollFailures += 1
      if (selectedRunPollFailures >= 3) {
        stopSelectedRunPolling()
      }
    }
  }, 4000)
}

function stopSelectedRunPolling(): void {
  if (selectedRunPollTimer != null) {
    window.clearInterval(selectedRunPollTimer)
    selectedRunPollTimer = null
  }
  selectedRunPollFailures = 0
}

async function changeRunPage(nextPage: number): Promise<void> {
  if (nextPage <= 0 || nextPage === runPage.value) {
    return
  }
  runPage.value = nextPage
  loading.value = true
  pageError.value = ''
  try {
    await loadRuns(false)
  } catch (err) {
    pageError.value = err instanceof Error ? err.message : '加载运行列表失败'
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await refreshAll()
  startSelectedRunPolling()
})

onBeforeUnmount(() => {
  stopSelectedRunPolling()
})
</script>

<template>
  <div class="layout monitor-layout">
    <aside class="sidebar monitor-sidebar">
      <div class="brand">
        <p class="eyebrow">monitor center</p>
        <h1>监控中心</h1>
      </div>

      <p class="task-text" v-if="!canSwitchAllScope">当前角色 {{ roleLabel }}：仅个人视图</p>
      <div class="monitor-scope" v-else>
        <button type="button" class="cancel" :class="{ active: monitorScope === 'own' }" @click="monitorScope = 'own'">
          仅看我的
        </button>
        <button type="button" class="cancel" :class="{ active: monitorScope === 'all' }" @click="monitorScope = 'all'">
          查看全局
        </button>
      </div>

      <div class="workflow-sidebar-actions">
        <button class="new-chat" type="button" :disabled="loading" @click="refreshAll">刷新</button>
      </div>

      <p v-if="pageError" class="error">{{ pageError }}</p>

      <ul class="conversation-list monitor-run-list">
        <li
          v-for="run in visibleRuns"
          :key="run.runId"
          :class="['conversation-item', { active: run.runId === selectedRunId }]"
          @click="selectRun(run.runId)"
        >
          <div class="conversation-meta">
            <p class="conversation-title">{{ run.runId }}</p>
            <p class="task-text">workflow: {{ run.workflowId }}</p>
            <p class="task-text">开始：{{ formatDate(run.startedAt) }}</p>
            <p class="task-text">耗时：{{ formatDuration(run.durationMs) }}</p>
            <div class="run-list-foot">
              <span :class="['chip tiny', runStatusClass(run.status)]">{{ run.status }}</span>
              <span class="task-text">alerts: {{ run.alertCount }}</span>
            </div>
          </div>
        </li>
      </ul>

      <div class="monitor-pager">
        <button class="cancel" type="button" :disabled="runPage <= 1 || loading" @click="changeRunPage(runPage - 1)">
          上一页
        </button>
        <span class="task-text">第 {{ runPage }} 页 · 共 {{ runTotal }} 条</span>
        <button
          class="cancel"
          type="button"
          :disabled="loading || runPage * runPageSize >= runTotal"
          @click="changeRunPage(runPage + 1)"
        >
          下一页
        </button>
      </div>
    </aside>

    <main class="chat-panel workflow-panel monitor-main">
      <section class="monitor-overview-grid">
        <article class="monitor-card">
          <p class="task-text">总运行次数</p>
          <strong>{{ overview.totalRuns }}</strong>
        </article>
        <article class="monitor-card">
          <p class="task-text">成功次数</p>
          <strong>{{ overview.succeededRuns }}</strong>
        </article>
        <article class="monitor-card">
          <p class="task-text">失败次数</p>
          <strong>{{ overview.failedRuns }}</strong>
        </article>
        <article class="monitor-card">
          <p class="task-text">成功率</p>
          <strong>{{ (overview.successRate * 100).toFixed(1) }}%</strong>
        </article>
        <article class="monitor-card">
          <p class="task-text">平均耗时</p>
          <strong>{{ formatDuration(overview.averageDurationMs) }}</strong>
        </article>
        <article class="monitor-card">
          <p class="task-text">告警总数</p>
          <strong>{{ overview.alertTotal }}</strong>
        </article>
      </section>

      <header class="toolbar monitor-toolbar">
        <div class="workflow-title">
          <strong>{{ currentRunDetail?.run.runId || '未选择运行记录' }}</strong>
          <span class="task-text">workflow: {{ currentRunDetail?.run.workflowId || '-' }}</span>
          <span class="task-text">开始时间: {{ formatDate(currentRunDetail?.run.startedAt) }}</span>
        </div>
        <div class="workflow-buttons">
          <span v-if="currentRunDetail" :class="['chip', runStatusClass(currentRunDetail.run.status)]">
            {{ currentRunDetail.run.status }}
          </span>
          <span class="task-text">总耗时: {{ formatDuration(currentRunDetail?.run.durationMs || 0) }}</span>
        </div>
      </header>

      <section class="monitor-chain" v-if="runFamily.length > 0">
        <span class="task-text">执行链路：</span>
        <button
          v-for="run in runFamily"
          :key="run.runId"
          type="button"
          class="cancel"
          :class="{ active: run.runId === currentRunId }"
          @click="selectGraphRun(run.runId)"
        >
          {{ graphRunLabel(run) }}
        </button>
      </section>

      <section class="workflow-body monitor-body">
        <div class="workflow-canvas" v-if="selectedRunId">
          <svg class="workflow-edges" :width="4200" :height="4200" v-if="currentWorkflowDef">
            <template v-for="(edge, edgeIndex) in currentWorkflowDef.edges" :key="`${edge.from}-${edge.to}-${edge.label || ''}-${edgeIndex}`">
              <path :d="edgePath(edge)" class="workflow-edge" />
              <text v-if="inferEdgeLabel(edge)" :x="edgeMidpoint(edge).x" :y="edgeMidpoint(edge).y" class="workflow-edge-label">
                {{ inferEdgeLabel(edge) }}
              </text>
            </template>
          </svg>

          <template v-if="currentWorkflowDef">
            <div
              v-for="node in currentWorkflowDef.nodes"
              :key="node.id"
              :class="['workflow-node', nodeRuntimeClass(node.id), { selected: node.id === selectedNodeId }]"
              :style="{
                left: `${getNodePos(node).x}px`,
                top: `${getNodePos(node).y}px`,
                width: `${nodeBox.width}px`,
                minHeight: `${nodeBox.height}px`,
              }"
              @click.stop="selectedNodeId = node.id"
            >
              <div class="workflow-node-header">
                <span class="workflow-node-id">{{ node.id }}</span>
                <span :class="['chip tiny', runStatusClass(nodeRuntimeStatus(node.id))]">
                  {{ nodeRuntimeStatus(node.id) }}
                </span>
                <span v-if="hasNodeAlert(node.id)" class="monitor-alert-dot" title="该节点存在告警">!</span>
              </div>
              <div class="workflow-node-label">{{ getUiAgent(node) ? `${getUiAgent(node)} · ` : '' }}{{ getUiLabel(node) }}</div>
              <div class="monitor-node-summary">耗时 {{ nodeDuration(node.id) }}</div>
            </div>
          </template>

          <section class="agent-tip" v-else>
            <p>未找到该运行对应的工作流定义，当前仅可查看事件与告警。</p>
          </section>
        </div>

        <aside class="workflow-inspector monitor-inspector" v-if="selectedRunId">
          <div class="monitor-tabs">
            <button type="button" class="cancel" :class="{ active: activeTab === 'node' }" @click="activeTab = 'node'">节点详情</button>
            <button type="button" class="cancel" :class="{ active: activeTab === 'events' }" @click="activeTab = 'events'">事件时间线</button>
            <button type="button" class="cancel" :class="{ active: activeTab === 'alerts' }" @click="activeTab = 'alerts'">告警列表</button>
          </div>

          <p v-if="loadingRun" class="task-text">加载运行数据中...</p>
          <p v-else-if="runError" class="error">{{ runError }}</p>

          <template v-else-if="activeTab === 'node'">
            <template v-if="selectedNode && selectedNodeRuntime">
              <div class="monitor-detail-grid">
                <p><strong>节点 ID:</strong> {{ selectedNode.id }}</p>
                <p><strong>节点类型:</strong> {{ selectedNode.type }}</p>
                <p><strong>所属 Agent:</strong> {{ getUiAgent(selectedNode) || '-' }}</p>
                <p><strong>状态:</strong> {{ selectedNodeRuntime.runtimeStatus }}</p>
                <p><strong>开始时间:</strong> {{ formatDate(selectedNodeRuntime.startedAt) }}</p>
                <p><strong>结束时间:</strong> {{ formatDate(selectedNodeRuntime.endedAt) }}</p>
                <p><strong>耗时:</strong> {{ formatDuration(selectedNodeRuntime.durationMs) }}</p>
                <p><strong>最后事件:</strong> {{ formatDate(selectedNodeRuntime.lastEventTime) }}</p>
                <p><strong>告警级别:</strong> {{ selectedNodeRuntime.alertLevel || '-' }}</p>
              </div>

              <div class="monitor-block">
                <strong>输入摘要</strong>
                <pre>{{ selectedNodeRuntime.inputSnapshot || '-' }}</pre>
              </div>
              <div class="monitor-block">
                <strong>输出摘要</strong>
                <pre>{{ selectedNodeRuntime.outputSnapshot || '-' }}</pre>
              </div>
              <div class="monitor-block" v-if="selectedNodeRuntime.errorMessage">
                <strong>错误信息</strong>
                <pre>{{ selectedNodeRuntime.errorMessage }}</pre>
              </div>
            </template>
            <p v-else class="task-text">请选择中间画布中的一个节点查看详情。</p>
          </template>

          <template v-else-if="activeTab === 'events'">
            <ul class="monitor-list">
              <li v-for="event in eventsTimeline" :key="event.eventId" class="monitor-list-item">
                <div class="monitor-list-head">
                  <span class="task-text">{{ formatDate(event.createdAt) }}</span>
                  <span :class="['chip tiny', eventStatusClass(eventDisplayStatus(event))]">{{ eventDisplayStatus(event) }}</span>
                </div>
                <p><strong>{{ event.eventType }}</strong></p>
                <p class="task-text">run: {{ event.runId }} | node: {{ event.nodeId || '-' }} | agent: {{ event.agentId || '-' }}</p>
                <p class="task-text">{{ eventSummary(event) }}</p>
              </li>
            </ul>
          </template>

          <template v-else>
            <ul class="monitor-list">
              <li v-for="alert in currentRunAlerts" :key="alert.alertId" class="monitor-list-item">
                <div class="monitor-list-head">
                  <span class="task-text">{{ formatDate(alert.triggeredAt) }}</span>
                  <span :class="['chip tiny', eventStatusClass(alert.status)]">{{ alert.status }}</span>
                </div>
                <p><strong>{{ alert.title }}</strong></p>
                <p class="task-text">类型: {{ alert.alertType }} | 级别: {{ alert.severity }}</p>
                <p class="task-text">run: {{ alert.runId }} | node: {{ alert.nodeId || '-' }}</p>
                <p class="task-text">{{ alert.content || '-' }}</p>
                <div v-if="canOperateAlerts" class="alert-actions">
                  <button
                    type="button"
                    class="cancel"
                    :disabled="!!actionLoading(alert.alertId) || !canAck(alert)"
                    @click="handleAlertAction(alert.alertId, 'ack')"
                  >
                    {{ actionLoading(alert.alertId) === 'ack' ? '处理中...' : 'ACK' }}
                  </button>
                  <button
                    type="button"
                    class="send"
                    :disabled="!!actionLoading(alert.alertId) || !canResolve(alert)"
                    @click="handleAlertAction(alert.alertId, 'resolve')"
                  >
                    {{ actionLoading(alert.alertId) === 'resolve' ? '处理中...' : 'Resolve' }}
                  </button>
                </div>
              </li>
            </ul>
          </template>
        </aside>

        <section v-else class="agent-tip">
          <p>请选择左侧某次运行记录，查看执行流运行态、事件时间线与告警信息。</p>
        </section>
      </section>
    </main>
  </div>
</template>

<style scoped>
.monitor-layout {
  grid-template-columns: 360px minmax(0, 1fr);
}

.monitor-sidebar {
  overflow: hidden;
}

.monitor-run-list {
  min-height: 0;
  flex: 1 1 auto;
}

.run-list-foot {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.monitor-pager {
  margin-top: auto;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.monitor-main {
  gap: 12px;
}

.monitor-chain {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 0 2px;
}

.monitor-chain .cancel.active {
  border-color: #b8c0f0;
  background: #f4f3fe;
}

.monitor-overview-grid {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 10px;
}

.monitor-card {
  border: 1px solid var(--line);
  border-radius: 12px;
  background: #fff;
  padding: 10px 12px;
}

.monitor-card strong {
  font-size: 22px;
  line-height: 1.2;
}

.monitor-body {
  grid-template-columns: minmax(0, 1fr) 420px;
}

.monitor-inspector {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.monitor-tabs {
  display: flex;
  gap: 8px;
}

.monitor-tabs .cancel.active {
  border-color: #b8c0f0;
  background: #f4f3fe;
}

.monitor-scope {
  display: flex;
  gap: 8px;
  margin-bottom: 10px;
}

.monitor-scope .cancel.active {
  border-color: #b8c0f0;
  background: #f4f3fe;
}

.monitor-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
  overflow: auto;
}

.monitor-list-item {
  border: 1px solid var(--line);
  border-radius: 10px;
  background: #fff;
  padding: 10px;
}

.monitor-list-item p {
  margin: 4px 0;
}

.monitor-list-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
}

.monitor-detail-grid {
  display: grid;
  gap: 6px;
}

.monitor-detail-grid p {
  margin: 0;
  font-size: 13px;
}

.monitor-block {
  border: 1px solid var(--line);
  border-radius: 10px;
  background: #fbfcfd;
  padding: 8px;
}

.monitor-block pre {
  margin: 6px 0 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
}

.alert-actions {
  margin-top: 8px;
  display: flex;
  gap: 8px;
}

.monitor-node-summary {
  margin-top: 10px;
  font-size: 12px;
  color: var(--text-muted);
}

.monitor-alert-dot {
  margin-left: auto;
  width: 18px;
  height: 18px;
  border-radius: 999px;
  background: #fff3db;
  color: #9b6c1d;
  border: 1px solid #eed9aa;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-weight: 800;
}

.workflow-node.runtime-pending {
  border-color: #d6dce3;
  background: linear-gradient(150deg, #ffffff, #f8f9fc);
}

.workflow-node.runtime-skipped {
  border-color: #d6dce3;
  background: linear-gradient(150deg, #fafafa, #f2f4f7);
  opacity: 0.9;
}

.workflow-node.runtime-running {
  border-color: #9eb7f4;
  background: linear-gradient(150deg, #f5f8ff, #edf3ff);
}

.workflow-node.runtime-succeeded {
  border-color: #97cfb8;
  background: linear-gradient(150deg, #effaf4, #e5f5ed);
}

.workflow-node.runtime-failed {
  border-color: #e6a6aa;
  background: linear-gradient(150deg, #fff5f5, #ffecef);
}

.workflow-node.runtime-timeout,
.workflow-node.runtime-retrying {
  border-color: #efc88e;
  background: linear-gradient(150deg, #fff9ef, #fff3dd);
}

@media (max-width: 1320px) {
  .monitor-overview-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .monitor-body {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 980px) {
  .monitor-layout {
    grid-template-columns: 1fr;
  }

  .monitor-overview-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
