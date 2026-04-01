import type { Conversation } from '../types/chat'

const STORAGE_KEY_PREFIX = 'mmmanus.web.conversations.v1'

function keyForUser(userId: string): string {
  return `${STORAGE_KEY_PREFIX}.${userId}`
}

export function loadConversations(userId: string): Conversation[] {
  if (typeof window === 'undefined') {
    return []
  }

  if (!userId) {
    return []
  }

  const raw = localStorage.getItem(keyForUser(userId))
  if (!raw) {
    return []
  }

  try {
    const parsed = JSON.parse(raw) as Conversation[]
    if (!Array.isArray(parsed)) {
      return []
    }
    return parsed
  } catch {
    return []
  }
}

export function saveConversations(userId: string, conversations: Conversation[]): void {
  if (typeof window === 'undefined') {
    return
  }
  if (!userId) {
    return
  }
  localStorage.setItem(keyForUser(userId), JSON.stringify(conversations))
}
