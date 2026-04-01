export type ChatRole = 'user' | 'assistant'

export interface ChatMessage {
  id: string
  role: ChatRole
  content: string
  createdAt: string
  status?: TaskState
  attachments?: UploadedFileMeta[]
}

export type AgentModel = string

export interface UploadedFileMeta {
  id: string
  name: string
  size: number
  type: string
  previewUrl?: string
}

export interface Conversation {
  id: string
  title: string
  model: AgentModel
  createdAt: string
  updatedAt: string
  taskId?: string
  userId?: string
  runId?: string
  stepEvents?: StepEvent[]
  messages: ChatMessage[]
}

export type StepState = 'start' | 'end' | 'info' | 'error'

export interface StepEvent {
  ts: string
  agent: string
  phase: string
  name: string
  state: StepState
  messageZh: string

  round?: number
  keyword?: string
  query?: string
  url?: string
  model?: string
}

export type TaskState = 'queued' | 'running' | 'completed' | 'failed' | 'canceled'

export interface OpenAIMessage {
  role: 'user' | 'assistant'
  content: string
}

export interface ChatRequest {
  model: string
  conversation_id?: string
  messages: OpenAIMessage[]
  stream?: boolean
}

export interface ChatResponseChunk {
  model?: string
  created_at?: string
  message?: {
    role?: string
    content?: string
  }
  done?: boolean
  done_reason?: string
  error?: string
}
