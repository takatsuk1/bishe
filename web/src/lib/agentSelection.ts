import type { AgentModel } from '../types/chat'

const STORAGE_KEY = 'mmmanus.selectedAgent.v1'
const EVENT_NAME = 'mmmanus:selected-agent-change'

export function getGlobalSelectedAgent(): AgentModel | '' {
  if (typeof window === 'undefined') {
    return ''
  }
  return (localStorage.getItem(STORAGE_KEY) || '').trim()
}

export function setGlobalSelectedAgent(agent: AgentModel): void {
  if (typeof window === 'undefined') {
    return
  }
  const next = String(agent || '').trim()
  if (!next) {
    return
  }
  localStorage.setItem(STORAGE_KEY, next)
  window.dispatchEvent(new CustomEvent(EVENT_NAME, { detail: { agent: next } }))
}

export function onGlobalSelectedAgentChange(handler: (agent: AgentModel) => void): () => void {
  if (typeof window === 'undefined') {
    return () => {}
  }
  const listener = (event: Event) => {
    const custom = event as CustomEvent<{ agent?: string }>
    const next = String(custom.detail?.agent || '').trim()
    if (next) {
      handler(next)
    }
  }
  window.addEventListener(EVENT_NAME, listener as EventListener)
  return () => {
    window.removeEventListener(EVENT_NAME, listener as EventListener)
  }
}
