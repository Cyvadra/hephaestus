import { useState, useCallback, useEffect, useRef } from 'react'
import { useBlocker, useLocation, useNavigate } from 'react-router-dom'
import SessionSidebar from './components/SessionSidebar'
import ChatView from './components/ChatView'
import ConfigurationWorkspace from './components/ConfigurationWorkspace'
import { Settings } from 'lucide-react'
import type { ConfigurationKind, ConciergeItem, Session } from './api/types'
import type { ConfigurationLists } from './components/ConfigurationSidebar'
import { parseRoute, routes } from './lib/routes'

const LAST_CONCIERGE_ID_KEY = 'hephaestus.lastConciergeId'
const ACTIVE_PROJECT_KEY = 'hephaestus.activeProject'

export default function App() {
  const location = useLocation()
  const navigate = useNavigate()
  const route = parseRoute(location.pathname)
  const [storedProject, setStoredProject] = useState<string | null>(() => localStorage.getItem(ACTIVE_PROJECT_KEY))
  const [draftConcierge, setDraftConcierge] = useState<ConciergeItem | null>(null)
  const [lastConciergeId, setLastConciergeId] = useState<string | null>(() => localStorage.getItem(LAST_CONCIERGE_ID_KEY))
  const [sidebarRefreshKey, setSidebarRefreshKey] = useState(0)
  const [sidebarSessionUpdate, setSidebarSessionUpdate] = useState<Session | null>(null)
  const [configurationDirty, setConfigurationDirty] = useState(false)
  const [configurationRefreshKey, setConfigurationRefreshKey] = useState(0)
  const [configurationLists, setConfigurationLists] = useState<ConfigurationLists>({})
  const [configurationSidebarOpen, setConfigurationSidebarOpen] = useState(false)
  const configurationDirtyRef = useRef(false)

  const chatRoute = route.type === 'chat' || route.type === 'chat-new' ? route : null
  const configurationRoute = route.type === 'configuration-new' || route.type === 'configuration-edit' ? route : null
  const mode = route.type.startsWith('configuration') ? 'configurations' : 'chat'
  const project = chatRoute?.project ?? storedProject
  const sessionId = route.type === 'chat' ? route.sessionId : null
  const configurationKind = configurationRoute?.kind ?? null
  const configurationName = route.type === 'configuration-edit' ? route.name : null
  const configurationIsNew = route.type === 'configuration-new'
  const isChoosingConcierge = route.type === 'chat-new' && draftConcierge == null
  const navigationBlocker = useBlocker(() => configurationDirtyRef.current)

  const handleConfigurationDirtyChange = useCallback((dirty: boolean) => {
    configurationDirtyRef.current = dirty
    setConfigurationDirty(dirty)
  }, [])

  useEffect(() => {
    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!configurationDirty) return
      event.preventDefault()
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [configurationDirty])

  useEffect(() => {
    if (route.type === 'invalid') navigate('/', { replace: true })
  }, [navigate, route.type])

  useEffect(() => {
    if (navigationBlocker.state !== 'blocked') return
    if (window.confirm('当前配置有未保存的更改。放弃更改并继续吗？')) {
      handleConfigurationDirtyChange(false)
      navigationBlocker.proceed()
    } else {
      navigationBlocker.reset()
    }
  }, [handleConfigurationDirtyChange, navigationBlocker])

  useEffect(() => {
    if (route.type === 'chat-new') setDraftConcierge(null)
  }, [location.pathname, route.type])

  const handleOpenConfigurations = useCallback(() => {
    navigate(routes.configurations())
    setConfigurationSidebarOpen(true)
  }, [navigate])
  const handleCloseConfigurations = useCallback(() => {
    if (project) navigate(routes.chatNew(project))
    else navigate('/')
  }, [navigate, project])
  const handleConfigurationSelect = useCallback((kind: ConfigurationKind, name: string) => {
    navigate(routes.configurationEdit(kind, name))
    setConfigurationSidebarOpen(false)
  }, [navigate])
  const handleConfigurationCreate = useCallback((kind: ConfigurationKind) => {
    navigate(routes.configurationNew(kind))
    setConfigurationSidebarOpen(false)
  }, [navigate])

  const handleSessionSelect = useCallback((id: number) => {
    if (project) navigate(routes.chat(project, id))
  }, [navigate, project])

  const handleConciergeResolved = useCallback((conciergeId: string) => {
    setLastConciergeId(conciergeId)
    localStorage.setItem(LAST_CONCIERGE_ID_KEY, conciergeId)
  }, [])

  const handleStartDraft = useCallback((concierge: ConciergeItem) => {
    setDraftConcierge(concierge)
    handleConciergeResolved(concierge.name)
  }, [handleConciergeResolved])

  const handleOpenNewSession = useCallback(() => {
    setDraftConcierge(null)
    if (project) navigate(routes.chatNew(project))
  }, [navigate, project])

  const handleProjectChange = useCallback((nextProject: string) => {
    localStorage.setItem(ACTIVE_PROJECT_KEY, nextProject)
    setStoredProject(nextProject)
    setDraftConcierge(null)
    navigate(routes.chatNew(nextProject))
  }, [navigate])

  const handleProjectsLoaded = useCallback((defaultProject: string) => {
    if (route.type !== 'root') return
    const nextProject = localStorage.getItem(ACTIVE_PROJECT_KEY) ?? defaultProject
    localStorage.setItem(ACTIVE_PROJECT_KEY, nextProject)
    setStoredProject(nextProject)
    navigate(routes.chatNew(nextProject), { replace: true })
  }, [navigate, route.type])

  const handleSessionCreated = useCallback((id: number) => {
    setDraftConcierge(null)
    setSidebarRefreshKey(v => v + 1)
    if (project) navigate(routes.chat(project, id), { replace: true })
  }, [navigate, project])

  const handleSessionUpdated = useCallback((session: Session) => {
    setSidebarSessionUpdate(session)
  }, [])

  return (
    <div className="app-shell">
      <SessionSidebar
        mode={mode}
        configurationSidebarOpen={configurationSidebarOpen}
        activeSessionId={sessionId}
        refreshKey={sidebarRefreshKey}
        sessionUpdate={sidebarSessionUpdate}
        project={project}
        onProjectChange={handleProjectChange}
        onProjectsLoaded={handleProjectsLoaded}
        onSelect={handleSessionSelect}
        onOpenNewSession={handleOpenNewSession}
        onOpenConfigurations={handleOpenConfigurations}
        onCloseConfigurations={handleCloseConfigurations}
        configurationKind={configurationKind}
        configurationName={configurationName}
        configurationRefreshKey={configurationRefreshKey}
        onConfigurationSelect={handleConfigurationSelect}
        onConfigurationCreate={handleConfigurationCreate}
        onConfigurationListsChange={setConfigurationLists}
      />
      <main className="main-panel">
        {mode === 'chat' && <button className="mobile-configuration-entry" type="button" aria-label="配置管理" title="配置管理" onClick={handleOpenConfigurations}><Settings size={17} /></button>}
        {mode === 'configurations' ? <ConfigurationWorkspace
          kind={configurationKind}
          name={configurationName}
          isNew={configurationIsNew}
          lists={configurationLists}
          selectionKey={`${configurationKind ?? 'overview'}:${configurationName ?? (configurationIsNew ? 'new' : '')}`}
          refreshKey={configurationRefreshKey}
          onDirtyChange={handleConfigurationDirtyChange}
          onCreate={handleConfigurationCreate}
          onSaved={(kind, name) => {
            handleConfigurationDirtyChange(false)
            setConfigurationRefreshKey(value => value + 1)
            navigate(routes.configurationEdit(kind, name), { replace: configurationIsNew })
          }}
          onDeleted={() => {
            setConfigurationRefreshKey(value => value + 1)
            navigate(routes.configurations(), { replace: true })
          }}
          onOpenNavigation={() => setConfigurationSidebarOpen(true)}
        /> : <ChatView
          sessionId={sessionId}
          draftConcierge={draftConcierge}
          isChoosingConcierge={isChoosingConcierge}
          defaultConciergeId={lastConciergeId}
          configurationRefreshKey={configurationRefreshKey}
          onChooseConcierge={handleStartDraft}
          onDefaultConciergeResolved={handleConciergeResolved}
          onSessionCreated={handleSessionCreated}
          onSessionUpdated={handleSessionUpdated}
          project={project}
        />}
      </main>
    </div>
  )
}
