import { getAccessToken } from './authStore'
import { refreshSession } from './authApi'
import type {
  MonitorAlert,
  MonitorEvent,
  MonitorOverview,
  MonitorRunFamily,
  MonitorRun,
  MonitorRunDetail,
  PagedResponse,
} from '../types/monitor'

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
      // surface original unauthorized response below
    }
  }

  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `请求失败（${res.status}）`)
  }

  return (await res.json()) as T
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  Object.entries(params).forEach(([k, v]) => {
    if (v === undefined || v === '' || Number.isNaN(v)) {
      return
    }
    search.set(k, String(v))
  })
  const raw = search.toString()
  return raw ? `?${raw}` : ''
}

export async function getMonitorOverview(): Promise<MonitorOverview> {
  return requestJson<MonitorOverview>('/v1/monitor/overview')
}

export async function listMonitorRuns(params?: {
  page?: number
  pageSize?: number
  workflowId?: string
  taskId?: string
  status?: string
}): Promise<PagedResponse<MonitorRun>> {
  const q = buildQuery({
    page: params?.page,
    pageSize: params?.pageSize,
    workflowId: params?.workflowId,
    taskId: params?.taskId,
    status: params?.status,
  })
  return requestJson<PagedResponse<MonitorRun>>(`/v1/monitor/runs${q}`)
}

export async function getMonitorRunDetail(runId: string): Promise<MonitorRunDetail> {
  return requestJson<MonitorRunDetail>(`/v1/monitor/runs/${encodeURIComponent(runId)}`)
}

export async function listMonitorRunEvents(
  runId: string,
  params?: { page?: number; pageSize?: number },
): Promise<PagedResponse<MonitorEvent>> {
  const q = buildQuery({ page: params?.page, pageSize: params?.pageSize })
  return requestJson<PagedResponse<MonitorEvent>>(
    `/v1/monitor/runs/${encodeURIComponent(runId)}/events${q}`,
  )
}

export async function getMonitorRunFamily(
  runId: string,
  params?: { limit?: number },
): Promise<MonitorRunFamily> {
  const q = buildQuery({ limit: params?.limit })
  return requestJson<MonitorRunFamily>(`/v1/monitor/runs/${encodeURIComponent(runId)}/family${q}`)
}

export async function listMonitorAlerts(params?: {
  page?: number
  pageSize?: number
  runId?: string
  workflowId?: string
  status?: string
}): Promise<PagedResponse<MonitorAlert>> {
  const q = buildQuery({
    page: params?.page,
    pageSize: params?.pageSize,
    runId: params?.runId,
    workflowId: params?.workflowId,
    status: params?.status,
  })
  return requestJson<PagedResponse<MonitorAlert>>(`/v1/monitor/alerts${q}`)
}

export async function ackMonitorAlert(alertId: string): Promise<void> {
  await requestJson(`/v1/monitor/alerts/${encodeURIComponent(alertId)}/ack`, {
    method: 'POST',
  })
}

export async function resolveMonitorAlert(alertId: string): Promise<void> {
  await requestJson(`/v1/monitor/alerts/${encodeURIComponent(alertId)}/resolve`, {
    method: 'POST',
  })
}
