import type { ConciergeItem, HistoryResponse, Project, SendMessageResponse, Session } from './types'

const BASE = '/api/v1'

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }
  return res.json()
}

export const listSessions = (project?: string) => {
  const query = project ? `?project=${encodeURIComponent(project)}` : ''
  return fetchJSON<Session[]>(`${BASE}/sessions${query}`)
}

export const listProjects = () =>
  fetchJSON<Project[]>(`${BASE}/projects`)

export const createProject = (name: string, description: string) =>
  fetchJSON<Project>(`${BASE}/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, description }),
  })

export const listConcierges = () =>
  fetchJSON<ConciergeItem[]>(`${BASE}/concierges`)

export const createSession = (concierge: string, project: string) =>
  fetchJSON<Session>(`${BASE}/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ concierge, project }),
  })

export const updateSession = (sessionId: number, changes: { title?: string; archived?: boolean; pinned?: boolean }) =>
  fetchJSON<Session>(`${BASE}/sessions/${sessionId}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(changes),
  })

export const deleteSession = async (sessionId: number) => {
  const res = await fetch(`${BASE}/sessions/${sessionId}`, { method: 'DELETE' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }
}

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

export const respondToInteraction = (sessionId: number, approved: boolean) =>
  fetchJSON<SendMessageResponse>(`${BASE}/sessions/${sessionId}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text: approved ? '/interact approve' : '/interact deny' }),
  })
