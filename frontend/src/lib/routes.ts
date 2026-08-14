import { matchPath } from 'react-router-dom'
import type { ConfigurationKind } from '../api/types'

const configurationKinds = new Set<ConfigurationKind>([
  'identities',
  'impressions',
  'tool-groups',
  'concierges',
  'workflows',
  'jobs',
  'constants',
])

export const routes = {
  chatNew: (project: string) => `/projects/${encodeURIComponent(project)}/chats/new`,
  chat: (project: string, sessionId: number) => `/projects/${encodeURIComponent(project)}/chats/${sessionId}`,
  configurations: () => '/configurations',
  configurationNew: (kind: ConfigurationKind) => `/configurations/${kind}/new`,
  configurationEdit: (kind: ConfigurationKind, name: string) => `/configurations/${kind}/edit/${encodeURIComponent(name)}`,
}

export type RouteState =
  | { type: 'root' }
  | { type: 'chat-new'; project: string }
  | { type: 'chat'; project: string; sessionId: number }
  | { type: 'configurations' }
  | { type: 'configuration-new'; kind: ConfigurationKind }
  | { type: 'configuration-edit'; kind: ConfigurationKind; name: string }
  | { type: 'invalid' }

export function parseRoute(pathname: string): RouteState {
  if (pathname === '/') return { type: 'root' }

  const chatNewMatch = matchPath('/projects/:project/chats/new', pathname)
  if (chatNewMatch) {
    const { project } = chatNewMatch.params
    if (!project) return { type: 'invalid' }
    return { type: 'chat-new', project }
  }

  const chatMatch = matchPath('/projects/:project/chats/:sessionId', pathname)
  if (chatMatch) {
    const { project, sessionId } = chatMatch.params
    const parsedSessionId = Number(sessionId)
    if (!project || !Number.isSafeInteger(parsedSessionId) || parsedSessionId <= 0) return { type: 'invalid' }
    return { type: 'chat', project, sessionId: parsedSessionId }
  }

  if (pathname === '/configurations') return { type: 'configurations' }

  const configurationNewMatch = matchPath('/configurations/:kind/new', pathname)
  if (configurationNewMatch) {
    const { kind } = configurationNewMatch.params
    if (!kind || !configurationKinds.has(kind as ConfigurationKind)) return { type: 'invalid' }
    return { type: 'configuration-new', kind: kind as ConfigurationKind }
  }

  const configurationEditMatch = matchPath('/configurations/:kind/edit/:name', pathname)
  if (configurationEditMatch) {
    const { kind, name } = configurationEditMatch.params
    if (!kind || !name || !configurationKinds.has(kind as ConfigurationKind)) return { type: 'invalid' }
    return { type: 'configuration-edit', kind: kind as ConfigurationKind, name }
  }

  return { type: 'invalid' }
}