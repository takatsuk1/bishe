export type NodeType = 'start' | 'end' | 'condition' | 'loop' | 'chat_model' | 'tool'

export interface PortDefinition {
  name: string
  type: string
  description?: string
}

export interface LoopConfig {
  maxIterations: number
  continueTo: string
  exitTo: string
}

export interface WorkflowSummary {
  id: string
  name: string
  updatedAt: string
}

export interface DraftWorkflowSummary {
  id: string
  name: string
  ttlMinutes: number
  isDraft: boolean
}

export interface WorkflowListResponse {
  saved: WorkflowSummary[]
  drafts: DraftWorkflowSummary[]
}

export interface NodeDefinition {
  id: string
  type: NodeType
  config?: Record<string, unknown>
  agentId?: string
  taskType?: string
  inputType?: string
  outputType?: string
  inputPorts?: PortDefinition[]
  outputPorts?: PortDefinition[]
  inputMapping?: Record<string, string>
  outputMapping?: Record<string, string>
  schemaVersion?: number
  condition?: string
  preInput?: string
  loopConfig?: LoopConfig
  metadata?: Record<string, string>
}

export interface EdgeDefinition {
  from: string
  to: string
  label?: string
  mapping?: Record<string, string>
}

export interface WorkflowDefinition {
  id: string
  name: string
  description?: string
  schemaVersion?: number
  startNodeId: string
  nodes: NodeDefinition[]
  edges: EdgeDefinition[]
}

export interface WorkflowGetResponse {
  definition: WorkflowDefinition
  updatedAt: string
  isDraft?: boolean
  ttlMinutes?: number
}

export interface AgentInfo {
  id: string
  name: string
  description?: string
}

export interface AgentWorkflowDetail {
  id: string
  name: string
  type: string
  description: string
  version: string
  configuration: {
    timeout: number
    retryPolicy: {
      maxAttempts: number
      initialDelayMs: number
      maxDelayMs: number
      backoffMultiplier: number
    }
    inputSchema: Record<string, unknown>
    outputSchema: Record<string, unknown>
    environmentVars: Array<{
      name: string
      required: boolean
      default?: string
      description?: string
    }>
  }
  dependencies: Array<{
    agentId: string
    type: string
    required: boolean
    description: string
    inputMapping?: Record<string, string>
  }>
  executionOrder: {
    startNodeId: string
    sequence: string[]
    parallel?: string[][]
  }
  nodes: NodeDefinition[]
  edges: EdgeDefinition[]
  metadata: {
    createdAt: string
    updatedAt: string
    author: string
    tags: string[]
    labels: Record<string, string>
  }
}

export interface AgentWorkflowsResponse {
  apiVersion: string
  timestamp: string
  agents: AgentWorkflowDetail[]
}

export type RunState = 'running' | 'succeeded' | 'failed' | 'canceled' | 'paused'

export interface NodeRunResult {
  nodeId: string
  taskId: string
  state: string
  output?: Record<string, unknown>
  errorMsg?: string
}

export interface RunResult {
  runId: string
  workflowId: string
  state: RunState
  startedAt: string
  finishedAt: string
  updatedAt: string
  currentNodeId?: string
  currentTaskId?: string
  nodeResults: NodeRunResult[]
  finalOutput?: Record<string, unknown>
  errorMessage?: string
}
