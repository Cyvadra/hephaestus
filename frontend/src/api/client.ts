import type { ConciergeItem, HistoryResponse, SendMessageResponse, Session } from './types'

const BASE = '/api/v1'

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }
  return res.json()
}

export const listSessions = () =>
  fetchJSON<Session[]>(`${BASE}/sessions`)

export const listConcierges = () =>
  fetchJSON<ConciergeItem[]>(`${BASE}/concierges`)

export const createSession = (concierge: string) =>
  fetchJSON<Session>(`${BASE}/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ concierge }),
  })

export const getHistory = (sessionId: number, signal?: AbortSignal) =>
  fetchJSON<HistoryResponse>(`${BASE}/sessions/${sessionId}/history`, { signal })

export const editAssistantMessage = (
  sessionId: number,
  messageId: number,
  activeLeafMessageId: number,
  content: string,
  reasoningContent: string,
) =>
  fetchJSON<SendMessageResponse>(`${BASE}/sessions/${sessionId}/messages/${messageId}/edit`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      active_leaf_message_id: activeLeafMessageId,
      content,
      reasoning_content: reasoningContent,
    }),
  })

export const regenerate = (sessionId: number) =>
  fetchJSON<SendMessageResponse>(`${BASE}/sessions/${sessionId}/regenerate`, {
    method: 'POST',
  })
