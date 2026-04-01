export type MonitorEventType =
  | 'workflow_started'
  | 'workflow_finished'
  | 'workflow_failed'
  | 'node_started'
  | 'node_finished'
  | 'node_failed'
  | 'agent_called'
  | 'tool_called'
  | 'retry_triggered'
  | 'timeout_triggered'
  | 'alert_triggered'

export type MonitorStatus =
  | 'pending'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'timeout'
  | 'retrying'

export interface MonitorRun {
  id: number
  runId: string
  workflowId: string
  userId: string
  sourceAgentId?: string
  taskId?: string
  status: string
  startedAt: string
  finishedAt?: string
  durationMs: number
  currentNodeId?: string
  errorMessage?: string
  alertCount: number
  createdAt: string
  updatedAt: string
}

export interface MonitorEvent {
  id: number
  eventId: string
  runId: string
  taskId?: string
  workflowId: string
  userId: string
  agentId?: string
  nodeId?: string
  eventType: MonitorEventType | string
  status: MonitorStatus | string
  message?: string
  inputSnapshot?: string
  outputSnapshot?: string
  errorMessage?: string
  durationMs: number
  createdAt: string
}

export interface MonitorAlert {
  id: number
  alertId: string
  runId: string
  workflowId: string
  agentId?: string
  nodeId?: string
  alertType: string
  severity: string
  title: string
  content?: string
  status: string
  triggeredAt: string
  resolvedAt?: string
  createdAt: string
}

export interface MonitorOverview {
  totalRuns: number
  succeededRuns: number
  failedRuns: number
  successRate: number
  averageDurationMs: number
  alertTotal: number
  recentRuns: MonitorRun[]
}

export interface MonitorRunDetail {
  run: MonitorRun
  alertCount: number
  nodeStatusSummary: Record<string, number>
  latestError?: string
}

export interface MonitorRunFamily {
  rootRunId: string
  runs: MonitorRun[]
}

export interface PagedResponse<T> {
  page: number
  pageSize: number
  total: number
  items: T[]
}
