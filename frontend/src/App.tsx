import { lazy, Suspense, useState, useCallback, useEffect, useRef } from 'react'
import { useBlocker, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import SessionSidebar from './components/SessionSidebar'
import { Menu, PanelLeftClose, Plus, Settings } from 'lucide-react'
import type { ConfigurationKind, ConciergeItem, Session, SessionTarget } from './api/types'
import type { ConfigurationLists } from './components/ConfigurationSidebar'
import { parseRoute, routes } from './lib/routes'

const LAST_CONCIERGE_ID_KEY = 'hephaestus.lastConciergeId'
const ACTIVE_PROJECT_KEY = 'hephaestus.activeProject'
const ChatView = lazy(() => import('./components/ChatView'))
const ConfigurationWorkspace = lazy(() => import('./components/ConfigurationWorkspace'))

export default function App() {
  const { t } = useTranslation()
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
  const [sessionSidebarOpen, setSessionSidebarOpen] = useState(false)
  const [chatHeaderTitle, setChatHeaderTitle] = useState<string | null>(null)
  const configurationDirtyRef = useRef(false)

  const chatRoute = route.type === 'chat' || route.type === 'chat-new' ? route : null
  const configurationRoute = route.type === 'configuration-new' || route.type === 'configuration-edit' ? route : null
  const mode = route.type.startsWith('configuration') ? 'configurations' : 'chat'
  const project = chatRoute?.project ?? storedProject
  const sessionId = route.type === 'chat' ? route.sessionId : null
  const configurationKind = route.type === 'configuration-constants' ? 'constants' : configurationRoute?.kind ?? null
  const configurationName = route.type === 'configuration-edit' ? route.name : null
  const configurationIsNew = route.type === 'configuration-new'
  const isChoosingConcierge = route.type === 'chat-new' && draftConcierge == null
  const topbarTitle = sessionId == null ? project : chatHeaderTitle ?? project
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
    if (window.confirm(t('app.unsavedChanges'))) {
      handleConfigurationDirtyChange(false)
      navigationBlocker.proceed()
    } else {
      navigationBlocker.reset()
    }
  }, [handleConfigurationDirtyChange, navigationBlocker, t])

  useEffect(() => {
    if (route.type === 'chat-new') setDraftConcierge(null)
  }, [location.pathname, route.type])

  useEffect(() => {
    setChatHeaderTitle(null)
  }, [sessionId])

  const handleOpenConfigurations = useCallback(() => {
    navigate(routes.configurations())
    setSessionSidebarOpen(false)
    setConfigurationSidebarOpen(true)
  }, [navigate])
  const handleCloseConfigurations = useCallback(() => {
    setConfigurationSidebarOpen(false)
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
  const handleOpenConstants = useCallback(() => {
    navigate(routes.constants())
    setConfigurationSidebarOpen(false)
  }, [navigate])

  const handleSessionSelect = useCallback((id: number) => {
    if (project) navigate(routes.chat(project, id))
    setSessionSidebarOpen(false)
  }, [navigate, project])

  const handleConciergeResolved = useCallback((conciergeId: string) => {
    setLastConciergeId(conciergeId)
    localStorage.setItem(LAST_CONCIERGE_ID_KEY, conciergeId)
  }, [])

  const setActiveProject = useCallback((nextProject: string) => {
    localStorage.setItem(ACTIVE_PROJECT_KEY, nextProject)
    setStoredProject(nextProject)
  }, [])

  useEffect(() => {
    if (chatRoute?.project && chatRoute.project !== storedProject) {
      setActiveProject(chatRoute.project)
    }
  }, [chatRoute?.project, setActiveProject, storedProject])

  const handleStartDraft = useCallback((concierge: ConciergeItem) => {
    setDraftConcierge(concierge)
    handleConciergeResolved(concierge.name)
  }, [handleConciergeResolved])

  const handleOpenNewSession = useCallback(() => {
    setDraftConcierge(null)
    if (project) navigate(routes.chatNew(project))
    setSessionSidebarOpen(false)
  }, [navigate, project])

  useEffect(() => {
    if (!sessionSidebarOpen && !configurationSidebarOpen) return
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      setSessionSidebarOpen(false)
      setConfigurationSidebarOpen(false)
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [configurationSidebarOpen, sessionSidebarOpen])

  const handleProjectChange = useCallback((nextProject: string) => {
    setActiveProject(nextProject)
    setDraftConcierge(null)
    setSessionSidebarOpen(false)
    navigate(routes.chatNew(nextProject))
  }, [navigate, setActiveProject])

  const handleProjectsLoaded = useCallback((defaultProject: string) => {
    if (route.type !== 'root') return
    const nextProject = localStorage.getItem(ACTIVE_PROJECT_KEY) ?? defaultProject
    setActiveProject(nextProject)
    navigate(routes.chatNew(nextProject), { replace: true })
  }, [navigate, route.type, setActiveProject])

  const handleSessionCreated = useCallback((id: number) => {
    setDraftConcierge(null)
    setSidebarRefreshKey(v => v + 1)
    if (project) navigate(routes.chat(project, id), { replace: true })
  }, [navigate, project])

  const handleSessionUpdated = useCallback((session: Session) => {
    setSidebarSessionUpdate(session)
  }, [])

  const handleSessionTarget = useCallback((target: SessionTarget) => {
    setActiveProject(target.project)
    setDraftConcierge(null)
    navigate(routes.chat(target.project, target.id))
  }, [navigate, setActiveProject])

  return (
    <div className={`app-shell ${mode}-shell${sessionSidebarOpen ? ' session-sidebar-open' : ''}`}>
      {mode === 'chat' && <header className="app-topbar">
        <div className="app-topbar-leading">
          <button
            className="app-icon-button"
            type="button"
            aria-label={t('session.history')}
            aria-controls="session-sidebar"
            aria-expanded={sessionSidebarOpen}
            onClick={() => setSessionSidebarOpen(open => !open)}
          >
            {sessionSidebarOpen ? <PanelLeftClose size={20} /> : <Menu size={20} />}
          </button>
        </div>
        <div className="app-topbar-title" title={topbarTitle ?? 'Hephaestus'}>{topbarTitle ?? 'Hephaestus'}</div>
        <div className="app-topbar-actions">
          <button className="app-icon-button" type="button" aria-label={t('session.newChat')} title={t('session.newChat')} onClick={handleOpenNewSession}>
            <Plus size={20} />
          </button>
          <button className="app-icon-button app-configuration-button" type="button" aria-label={t('app.configurationManagement')} title={t('app.configurationManagement')} onClick={handleOpenConfigurations}>
            <Settings size={19} />
          </button>
        </div>
      </header>}
      {mode === 'chat' && sessionSidebarOpen && <button className="sidebar-scrim" type="button" aria-label={t('common.close')} onClick={() => setSessionSidebarOpen(false)} />}
      {mode === 'configurations' && configurationSidebarOpen && <button className="sidebar-scrim configuration-sidebar-scrim" type="button" aria-label={t('common.close')} onClick={() => setConfigurationSidebarOpen(false)} />}
      <div id="session-sidebar" className={`sidebar-drawer${mode === 'configurations' ? ` configuration-drawer${configurationSidebarOpen ? ' configuration-drawer-open' : ''}` : ''}`}>
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
          onConfigurationOpenConstants={handleOpenConstants}
          onConfigurationListsChange={setConfigurationLists}
        />
      </div>
      <main className="main-panel">
        {mode === 'chat' && <button className="mobile-configuration-entry" type="button" aria-label={t('app.configurationManagement')} title={t('app.configurationManagement')} onClick={handleOpenConfigurations}><Settings size={17} /></button>}
        <Suspense fallback={<div className="workspace-loading" role="status">{t('common.loading')}</div>}>
        {mode === 'configurations' ? <ConfigurationWorkspace
          kind={configurationKind}
          name={configurationName}
          isConstantsOverview={route.type === 'configuration-constants'}
          isNew={configurationIsNew}
          lists={configurationLists}
          selectionKey={`${configurationKind ?? 'overview'}:${configurationName ?? (configurationIsNew ? 'new' : '')}`}
          refreshKey={configurationRefreshKey}
          onDirtyChange={handleConfigurationDirtyChange}
          onCreate={handleConfigurationCreate}
          onSelect={handleConfigurationSelect}
          onOpenConstants={handleOpenConstants}
          onSaved={(kind, name) => {
            handleConfigurationDirtyChange(false)
            setConfigurationRefreshKey(value => value + 1)
            navigate(kind === 'constants' && !name ? routes.constants() : routes.configurationEdit(kind, name), { replace: configurationIsNew })
          }}
          onDeleted={() => {
            setConfigurationRefreshKey(value => value + 1)
            navigate(routes.configurations(), { replace: true })
          }}
          onReturnToOverview={() => navigate(routes.configurations())}
          onReturnToChat={handleCloseConfigurations}
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
          onSessionTarget={handleSessionTarget}
          onHeaderTitleChange={setChatHeaderTitle}
          project={project}
        />}
        </Suspense>
      </main>
    </div>
  )
}
