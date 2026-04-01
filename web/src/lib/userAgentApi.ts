import type { WorkflowDefinition } from '../types/workflow'
import { getAccessToken } from './authStore'
import { refreshSession } from './authApi'

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://127.0.0.1:11000'

async function requestJson<T>(path: string, init?: RequestInit, retry = true): Promise<T> {
  const url = `${API_BASE_URL}${path}`
  console.log('[API] requestJson:', init?.method || 'GET', url)
  console.log('[API] request body:', init?.body)
  const token = getAccessToken()
  
  const res = await fetch(url, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers ?? {}),
    },
  })
  
  console.log('[API] response status:', res.status, res.statusText)
  if (res.status === 401 && retry) {
    try {
      await refreshSession()
      return requestJson<T>(path, init, false)
    } catch {
      // fall through and surface original unauthorized error
    }
  }
  
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    console.error('[API] request failed:', text)
    throw new Error(text || `请求失败（${res.status}）`)
  }
  
  const json = await res.json()
  console.log('[API] response data:', json)
  return json as T
}

export interface ToolParameter {
  name: string
  type: string
  required: boolean
  description: string
  default?: unknown
  enum?: unknown[]
}

export interface UserTool {
  toolId: string
  userId: string
  name: string
  description: string
  toolType: string
  config: Record<string, unknown>
  parameters: ToolParameter[]
}

export interface MCPServerTool {
  name: string
  description: string
}

export interface MCPStartResponse {
  started: boolean
  server?: string
  tools: MCPServerTool[]
}

export interface CreateUserToolRequest {
  toolId: string
  name: string
  description: string
  toolType: string
  config: Record<string, unknown>
  parameters: ToolParameter[]
}

export interface UpdateUserToolRequest {
  name?: string
  description?: string
  config?: Record<string, unknown>
  parameters?: ToolParameter[]
}

export async function listUserTools(): Promise<UserTool[]> {
  const data = await requestJson<UserTool[] | null>('/v1/orchestrator/user-tools')
  return Array.isArray(data) ? data : []
}

export async function getUserTool(toolId: string): Promise<UserTool> {
  return requestJson<UserTool>(`/v1/orchestrator/user-tools/${encodeURIComponent(toolId)}`)
}

export async function listMCPTools(toolId: string): Promise<MCPServerTool[]> {
  const data = await requestJson<MCPServerTool[] | null>(`/v1/orchestrator/user-tools/${encodeURIComponent(toolId)}/mcp-tools`)
  return Array.isArray(data) ? data : []
}

export async function startMCPServer(toolId: string): Promise<MCPStartResponse> {
  return requestJson<MCPStartResponse>(`/v1/orchestrator/user-tools/${encodeURIComponent(toolId)}/mcp-start`, {
    method: 'POST',
  })
}

export async function createUserTool(tool: CreateUserToolRequest): Promise<UserTool> {
  return requestJson<UserTool>('/v1/orchestrator/user-tools', {
    method: 'POST',
    body: JSON.stringify(tool),
  })
}

export async function updateUserTool(toolId: string, tool: UpdateUserToolRequest): Promise<UserTool> {
  return requestJson<UserTool>(`/v1/orchestrator/user-tools/${encodeURIComponent(toolId)}`, {
    method: 'PUT',
    body: JSON.stringify(tool),
  })
}

export async function deleteUserTool(toolId: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/v1/orchestrator/user-tools/${encodeURIComponent(toolId)}`, {
    method: 'DELETE',
  })
}

export interface UserAgent {
  agentId: string
  userId: string
  name: string
  description: string
  workflowId: string
  status: string
  port?: number
  processPid?: number
  processStatus?: string
  codePath?: string
  publishedAt?: string
}

export interface CreateUserAgentRequest {
  agentId: string
  name: string
  description: string
  workflowId: string
}

export interface UpdateUserAgentRequest {
  name?: string
  description?: string
  workflowId?: string
}

export interface TestAgentRequest {
  workflowDef: WorkflowDefinition
  input: Record<string, unknown>
}

export interface ExecutionResult {
  runId: string
  workflowId: string
  state: string
  output?: Record<string, unknown>
  error?: string
  nodeResults: NodeExecutionResult[]
}

export interface NodeExecutionResult {
  nodeId: string
  nodeType: string
  state: string
  output?: Record<string, unknown>
  error?: string
  duration: number
}

export async function listUserAgents(): Promise<UserAgent[]> {
  return requestJson<UserAgent[]>('/v1/orchestrator/user-agents')
}

export async function getUserAgent(agentId: string): Promise<UserAgent> {
  return requestJson<UserAgent>(`/v1/orchestrator/user-agents/${encodeURIComponent(agentId)}`)
}

export async function createUserAgent(agent: CreateUserAgentRequest): Promise<UserAgent> {
  return requestJson<UserAgent>('/v1/orchestrator/user-agents', {
    method: 'POST',
    body: JSON.stringify(agent),
  })
}

export async function updateUserAgent(agentId: string, agent: UpdateUserAgentRequest): Promise<UserAgent> {
  return requestJson<UserAgent>(`/v1/orchestrator/user-agents/${encodeURIComponent(agentId)}`, {
    method: 'PUT',
    body: JSON.stringify(agent),
  })
}

export async function deleteUserAgent(agentId: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/v1/orchestrator/user-agents/${encodeURIComponent(agentId)}`, {
    method: 'DELETE',
  })
}

export async function testWorkflow(request: TestAgentRequest): Promise<ExecutionResult> {
  return requestJson<ExecutionResult>('/v1/orchestrator/user-agents/test', {
    method: 'POST',
    body: JSON.stringify(request),
  })
}

export async function testUserAgent(agentId: string, input: Record<string, unknown>): Promise<ExecutionResult> {
  return requestJson<ExecutionResult>(`/v1/orchestrator/user-agents/${encodeURIComponent(agentId)}/test`, {
    method: 'POST',
    body: JSON.stringify({ input }),
  })
}

export async function publishUserAgent(agentId: string): Promise<UserAgent> {
  return requestJson<UserAgent>(`/v1/orchestrator/user-agents/${encodeURIComponent(agentId)}/publish`, {
    method: 'POST',
  })
}

export async function stopUserAgent(agentId: string): Promise<{ stopped: boolean }> {
  return requestJson<{ stopped: boolean }>(`/v1/orchestrator/user-agents/${encodeURIComponent(agentId)}/stop`, {
    method: 'POST',
  })
}

export async function restartUserAgent(agentId: string): Promise<{ restarted: boolean }> {
  return requestJson<{ restarted: boolean }>(`/v1/orchestrator/user-agents/${encodeURIComponent(agentId)}/restart`, {
    method: 'POST',
  })
}
