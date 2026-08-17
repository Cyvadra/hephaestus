import type { ChatRun, ChatRunKind, ConfigurationByKind, ConfigurationCatalog, ConfigurationKind, ConciergeItem, GenerationOptions, HistoryResponse, JobRun, JobRunDetail, Project, SendMessageResponse, Session, WorkflowRun, WorkflowRunDetail } from './types'
import { authFetch } from './auth'

const BASE = '/api/v1'

export const attachmentDownloadURL = (sessionId: number, attachmentId: number) =>
  `${BASE}/sessions/${sessionId}/attachments/${attachmentId}/download`

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await authFetch(url, init)
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

export const deleteProject = async (name: string, deleteDirectory = false) => {
  const res = await authFetch(`${BASE}/projects/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ delete_directory: deleteDirectory }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }
}

export const listConcierges = (project?: string) => {
  const query = project ? `?project=${encodeURIComponent(project)}` : ''
  return fetchJSON<ConciergeItem[]>(`${BASE}/concierges${query}`)
}

export const createSession = (concierge: string, project: string, toolGroups: string[], plugins: string[]) =>
  fetchJSON<Session>(`${BASE}/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ concierge, project, tool_groups: toolGroups, plugins }),
  })

export const forkSessionAtMessage = (sessionId: number, messageId: number) =>
  fetchJSON<Session>(`${BASE}/sessions/${sessionId}/messages/${messageId}/fork`, { method: 'POST' })

export const updateSession = (sessionId: number, changes: { title?: string; archived?: boolean; pinned?: boolean; reasoningEffort?: string; enableWebSearch?: boolean }) =>
  fetchJSON<Session>(`${BASE}/sessions/${sessionId}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      title: changes.title,
      archived: changes.archived,
      pinned: changes.pinned,
      reasoning_effort: changes.reasoningEffort,
      enable_web_search: changes.enableWebSearch,
    }),
  })

export const deleteSession = async (sessionId: number) => {
  const res = await authFetch(`${BASE}/sessions/${sessionId}`, { method: 'DELETE' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }
}

export const getHistory = (sessionId: number, signal?: AbortSignal) =>
  fetchJSON<HistoryResponse>(`${BASE}/sessions/${sessionId}/history`, { signal })

export const getActiveChatRun = (sessionId: number) =>
  fetchJSON<ChatRun>(`${BASE}/sessions/${sessionId}/chat-run`)

export const startChatRun = (sessionId: number, kind: ChatRunKind, text: string, options: GenerationOptions, messageId?: number, activeLeafMessageId?: number | null) =>
  fetchJSON<ChatRun | SendMessageResponse>(`${BASE}/sessions/${sessionId}/chat-runs`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      kind,
      text,
      message_id: messageId,
      options: {
        reasoning_effort: options.reasoningEffort,
        disabled_tools: options.webSearch ? [] : ['web_search', 'web_fetch'],
        ...(activeLeafMessageId === null ? { select_root: true } : {}),
        ...(activeLeafMessageId === undefined || activeLeafMessageId === null ? {} : { active_leaf_message_id: activeLeafMessageId }),
      },
    }),
  })

export const startChatRunWithFiles = (sessionId: number, text: string, files: File[], options: GenerationOptions, activeLeafMessageId?: number | null) => {
  const form = new FormData()
  form.set('text', text)
  if (activeLeafMessageId === null) form.set('select_root', 'true')
  else if (activeLeafMessageId !== undefined) form.set('active_leaf_message_id', String(activeLeafMessageId))
  form.set('reasoning_effort', options.reasoningEffort)
  ;(options.webSearch ? [] : ['web_search', 'web_fetch']).forEach(tool => form.append('disabled_tools', tool))
  files.forEach(file => form.append('files', file))
  return fetchJSON<ChatRun>(`${BASE}/sessions/${sessionId}/chat-runs`, { method: 'POST', body: form })
}

export const cancelActiveChatRun = (sessionId: number) =>
  fetchJSON<{ status: string }>(`${BASE}/sessions/${sessionId}/chat-runs/cancel`, { method: 'POST' })

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

const configurationURL = (kind: ConfigurationKind, name?: string) => {
  const base = `${BASE}/configurations/${encodeURIComponent(kind)}`
  return name == null ? base : `${base}/${encodeURIComponent(name)}`
}

export const getConfigurationCatalog = () =>
  fetchJSON<ConfigurationCatalog>(`${BASE}/configurations/catalog`)

export const listConfigurations = <K extends ConfigurationKind>(kind: K) =>
  fetchJSON<ConfigurationByKind[K][]>(configurationURL(kind))

export const getConfiguration = <K extends ConfigurationKind>(kind: K, name: string) =>
  fetchJSON<ConfigurationByKind[K]>(configurationURL(kind, name))

export const createConfiguration = <K extends ConfigurationKind>(kind: K, value: ConfigurationByKind[K]) =>
  fetchJSON<ConfigurationByKind[K]>(configurationURL(kind), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(value),
  })

export const replaceConfiguration = <K extends ConfigurationKind>(kind: K, name: string, value: ConfigurationByKind[K]) =>
  fetchJSON<ConfigurationByKind[K]>(configurationURL(kind, name), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(value),
  })

export const deleteConfiguration = async (kind: ConfigurationKind, name: string) => {
  const res = await authFetch(configurationURL(kind, name), { method: 'DELETE' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }
}

// --- 工作流 / 任务 运行（在线测试与运行记录） ---

export const startWorkflowRun = (workflowName: string, project: string, input: Record<string, unknown>) =>
  fetchJSON<WorkflowRun>(`${BASE}/workflows/${encodeURIComponent(workflowName)}/runs`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ project, input }),
  })

export const listWorkflowRuns = (workflow?: string, limit = 50, offset = 0) => {
  const params = new URLSearchParams()
  if (workflow) params.set('workflow', workflow)
  if (limit !== 50) params.set('limit', String(limit))
  if (offset > 0) params.set('offset', String(offset))
  const query = params.size > 0 ? `?${params.toString()}` : ''
  return fetchJSON<WorkflowRun[]>(`${BASE}/workflow-runs${query}`)
}

export const getWorkflowRun = (runID: number) =>
  fetchJSON<WorkflowRunDetail>(`${BASE}/workflow-runs/${runID}`)

export const cancelWorkflowRun = (runID: number) =>
  fetchJSON<{ status: string }>(`${BASE}/workflow-runs/${runID}/cancel`, { method: 'POST' })

export const listJobRuns = (job?: string, limit = 50, offset = 0) => {
  const params = new URLSearchParams()
  if (job) params.set('job', job)
  if (limit !== 50) params.set('limit', String(limit))
  if (offset > 0) params.set('offset', String(offset))
  const query = params.size > 0 ? `?${params.toString()}` : ''
  return fetchJSON<JobRun[]>(`${BASE}/job-runs${query}`)
}

export const getJobRun = (runID: number) =>
  fetchJSON<JobRunDetail>(`${BASE}/job-runs/${runID}`)

export const cancelJobRun = (runID: number) =>
  fetchJSON<{ status: string }>(`${BASE}/job-runs/${runID}/cancel`, { method: 'POST' })
