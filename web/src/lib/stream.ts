import type { ChatResponseChunk, StepEvent } from '../types/chat'

const DECODER = new TextDecoder()

function splitLines(buffer: string): { lines: string[]; rest: string } {
  const normalized = buffer.replace(/\r\n/g, '\n')
  const parts = normalized.split('\n')
  const rest = parts.pop() ?? ''
  const lines = parts.map((line) => line.trim()).filter((line) => line.length > 0)
  return { lines, rest }
}

function toChunk(line: string): ChatResponseChunk | null {
  const trimmed = line.trim()
  if (!trimmed) {
    return null
  }

  // Support OpenAI-compatible SSE streaming where each payload is prefixed with `data:`
  // and terminated by `data: [DONE]`.
  let payload = trimmed
  if (payload.startsWith('data:')) {
    payload = payload.slice('data:'.length).trim()
  }
  if (!payload) {
    return null
  }
  if (payload === '[DONE]') {
    return { done: true }
  }

  try {
    const parsed: any = JSON.parse(payload)

    // Already in the shape the UI expects (our own NDJSON format).
    if (parsed && (parsed.message || typeof parsed.done === 'boolean' || parsed.error)) {
      return parsed as ChatResponseChunk
    }

    // OpenAI SSE chunk format: { choices: [{ delta: { content, role }, finish_reason }] }
    const choice = Array.isArray(parsed?.choices) ? parsed.choices[0] : undefined
    const delta = choice?.delta ?? {}
    const finishReason = choice?.finish_reason
    const content = typeof delta?.content === 'string' ? delta.content : ''
    const role = typeof delta?.role === 'string' ? delta.role : 'assistant'

    if (choice) {
      return {
        model: parsed?.model,
        message: {
          role,
          content,
        },
        done: finishReason != null,
        done_reason: finishReason ?? undefined,
      } satisfies ChatResponseChunk
    }

    return null
  } catch {
    return null
  }
}

export async function* parseNdjsonStream(stream: ReadableStream<Uint8Array>): AsyncGenerator<ChatResponseChunk> {
  const reader = stream.getReader()
  let pending = ''

  while (true) {
    const { value, done } = await reader.read()
    if (done) {
      break
    }

    pending += DECODER.decode(value, { stream: true })
    const { lines, rest } = splitLines(pending)
    pending = rest

    for (const line of lines) {
      const parsed = toChunk(line)
      if (parsed) {
        yield parsed
      }
    }
  }

  const tail = pending.trim()
  if (tail.length > 0) {
    const parsed = toChunk(tail)
    if (parsed) {
      yield parsed
    }
  }
}

export function extractToken(content: string, kind: 'task' | 'user' | 'run'): string | undefined {
  const pattern =
    kind === 'task'
      ? /\[]\(task:\/\/([^)]+)\)/g
      : kind === 'user'
        ? /\[]\(user:\/\/([^)]+)\)/g
        : /\[]\(run:\/\/([^)]+)\)/g
  let match: RegExpExecArray | null = null
  let found: string | undefined
  while ((match = pattern.exec(content)) !== null) {
    found = match[1]
  }
  if (found === 'done') {
    return undefined
  }
  return found
}

export function extractStepPayloads(content: string): string[] {
  const pattern = /\[]\(step:\/\/([^)]+)\)/g
  const out: string[] = []
  let match: RegExpExecArray | null = null
  while ((match = pattern.exec(content)) !== null) {
    if (match[1]) {
      out.push(match[1])
    }
  }
  return out
}

function base64UrlDecodeToBytes(input: string): Uint8Array | null {
  try {
    let b64 = input.replace(/-/g, '+').replace(/_/g, '/')
    while (b64.length % 4 !== 0) {
      b64 += '='
    }
    const bin = atob(b64)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i += 1) {
      bytes[i] = bin.charCodeAt(i)
    }
    return bytes
  } catch {
    return null
  }
}

export function decodeStepPayload(payload: string): StepEvent | null {
  const bytes = base64UrlDecodeToBytes(payload)
  if (!bytes) {
    return null
  }
  try {
    const text = new TextDecoder().decode(bytes)
    const parsed: any = JSON.parse(text)
    if (!parsed || typeof parsed !== 'object') {
      return null
    }
    if (typeof parsed.ts !== 'string' || typeof parsed.agent !== 'string' || typeof parsed.messageZh !== 'string') {
      return null
    }
    return parsed as StepEvent
  } catch {
    return null
  }
}
