import type {
  AgentInfo,
  AgentWorkflowsResponse,
  RunResult,
  WorkflowDefinition,
  WorkflowGetResponse,
  WorkflowListResponse,
} from '../types/workflow'
import { getAccessToken } from './authStore'
import { refreshSession } from './authApi'

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? 'http://127.0.0.1:11000'

async function requestJson<T>(path: string, init?: RequestInit, retry = true): Promise<T> {
  const token = getAccessToken()
  const res = await fetch(`${API_BASE_URL}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers ?? {}),
    },
  })
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
    throw new Error(text || `请求失败（${res.status}）`)
  }
  return (await res.json()) as T
}

export async function listAgents(): Promise<AgentInfo[]> {
  return requestJson<AgentInfo[]>('/v1/orchestrator/agents')
}

export interface ToolInfo {
  name: string
  type: string
  description: string
  parameters: Array<{
    name: string
    type: string
    required: boolean
    description: string
  }>
  outputParameters: Array<{
    name: string
    type: string
    required: boolean
    description: string
  }>
}

export async function listTools(): Promise<ToolInfo[]> {
  return requestJson<ToolInfo[]>('/v1/orchestrator/tools')
}

export async function getAgentWorkflows(): Promise<AgentWorkflowsResponse> {
  return requestJson<AgentWorkflowsResponse>('/v1/orchestrator/agent-workflows')
}

export async function listWorkflows(): Promise<WorkflowListResponse> {
  return requestJson<WorkflowListResponse>('/v1/orchestrator/workflows')
}

export async function getWorkflow(id: string): Promise<WorkflowGetResponse> {
  return requestJson<WorkflowGetResponse>(`/v1/orchestrator/workflows/${encodeURIComponent(id)}`)
}

export async function putWorkflow(id: string, definition: WorkflowDefinition): Promise<{ id: string; updatedAt: string }> {
  return requestJson<{ id: string; updatedAt: string }>(
    `/v1/orchestrator/workflows/${encodeURIComponent(id)}`,
    {
      method: 'PUT',
      body: JSON.stringify(definition),
    },
  )
}

export async function saveWorkflowToDB(id: string, definition: WorkflowDefinition): Promise<{ workflowId: string; createdAt: string; updatedAt: string }> {
  return requestJson<{ workflowId: string; createdAt: string; updatedAt: string }>(
    '/v1/orchestrator/user-workflows/save',
    {
      method: 'POST',
      body: JSON.stringify({
        workflowId: id,
        name: definition.name,
        description: definition.description ?? '',
        startNodeId: definition.startNodeId,
        nodes: definition.nodes,
        edges: definition.edges,
      }),
    },
  )
}

export async function deleteWorkflow(id: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/v1/orchestrator/workflows/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

export async function startWorkflowRun(id: string, input: Record<string, unknown>): Promise<{ runId: string }> {
  return requestJson<{ runId: string }>(`/v1/orchestrator/workflows/${encodeURIComponent(id)}/runs`, {
    method: 'POST',
    body: JSON.stringify({ input }),
  })
}

export async function getRun(runId: string): Promise<RunResult> {
  return requestJson<RunResult>(`/v1/orchestrator/runs/${encodeURIComponent(runId)}`)
}
