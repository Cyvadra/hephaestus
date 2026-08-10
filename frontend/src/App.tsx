import { useState, useCallback, useEffect } from 'react'
import SessionSidebar from './components/SessionSidebar'
import ChatView from './components/ChatView'
import ConfigurationWorkspace from './components/ConfigurationWorkspace'
import { Settings } from 'lucide-react'
import type { ConfigurationKind, ConciergeItem, Session } from './api/types'
import type { ConfigurationLists } from './components/ConfigurationSidebar'

const LAST_SESSION_ID_KEY = 'hephaestus.lastSessionId'
const LAST_CONCIERGE_ID_KEY = 'hephaestus.lastConciergeId'

function readStoredSessionId(): number | null {
  const value = Number(localStorage.getItem(LAST_SESSION_ID_KEY))
  return Number.isInteger(value) && value > 0 ? value : null
}

export default function App() {
  const [project, setProject] = useState<string | null>(() => localStorage.getItem('hephaestus.activeProject'))
  const [sessionId, setSessionId] = useState<number | null>(readStoredSessionId)
  const [draftConcierge, setDraftConcierge] = useState<ConciergeItem | null>(null)
  const [lastConciergeId, setLastConciergeId] = useState<string | null>(() => localStorage.getItem(LAST_CONCIERGE_ID_KEY))
  const [isChoosingConcierge, setIsChoosingConcierge] = useState(() => readStoredSessionId() == null)
  const [sidebarRefreshKey, setSidebarRefreshKey] = useState(0)
  const [sidebarSessionUpdate, setSidebarSessionUpdate] = useState<Session | null>(null)
  const [mode, setMode] = useState<'chat' | 'configurations'>('chat')
  const [configurationKind, setConfigurationKind] = useState<ConfigurationKind | null>(null)
  const [configurationName, setConfigurationName] = useState<string | null>(null)
  const [configurationIsNew, setConfigurationIsNew] = useState(false)
  const [configurationDirty, setConfigurationDirty] = useState(false)
  const [configurationRefreshKey, setConfigurationRefreshKey] = useState(0)
  const [configurationLists, setConfigurationLists] = useState<ConfigurationLists>({})
  const [configurationSidebarOpen, setConfigurationSidebarOpen] = useState(false)

  useEffect(() => {
    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!configurationDirty) return
      event.preventDefault()
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [configurationDirty])

  const allowConfigurationNavigation = useCallback(() =>
    !configurationDirty || window.confirm('当前配置有未保存的更改。放弃更改并继续吗？'), [configurationDirty])

  const handleOpenConfigurations = useCallback(() => { setMode('configurations'); setConfigurationSidebarOpen(true) }, [])
  const handleCloseConfigurations = useCallback(() => {
    if (allowConfigurationNavigation()) setMode('chat')
  }, [allowConfigurationNavigation])
  const handleConfigurationSelect = useCallback((kind: ConfigurationKind, name: string) => {
    if (!allowConfigurationNavigation()) return
    setConfigurationKind(kind)
    setConfigurationName(name)
    setConfigurationIsNew(false)
    setConfigurationSidebarOpen(false)
  }, [allowConfigurationNavigation])
  const handleConfigurationCreate = useCallback((kind: ConfigurationKind) => {
    if (!allowConfigurationNavigation()) return
    setConfigurationKind(kind)
    setConfigurationName(null)
    setConfigurationIsNew(true)
    setConfigurationSidebarOpen(false)
  }, [allowConfigurationNavigation])

  const handleSessionSelect = useCallback((id: number) => {
    setDraftConcierge(null)
    setIsChoosingConcierge(false)
    setSessionId(id)
    localStorage.setItem(LAST_SESSION_ID_KEY, String(id))
  }, [])

  const handleConciergeResolved = useCallback((conciergeId: string) => {
    setLastConciergeId(conciergeId)
    localStorage.setItem(LAST_CONCIERGE_ID_KEY, conciergeId)
  }, [])

  const handleStartDraft = useCallback((concierge: ConciergeItem) => {
    setSessionId(null)
    setDraftConcierge(concierge)
    setIsChoosingConcierge(false)
    localStorage.removeItem(LAST_SESSION_ID_KEY)
    handleConciergeResolved(concierge.name)
  }, [handleConciergeResolved])

  const handleOpenNewSession = useCallback(() => {
    setSessionId(null)
    setDraftConcierge(null)
    setIsChoosingConcierge(true)
    localStorage.removeItem(LAST_SESSION_ID_KEY)
  }, [])

  const handleProjectChange = useCallback((nextProject: string) => {
    localStorage.setItem('hephaestus.activeProject', nextProject)
    setProject(nextProject)
    setSessionId(null)
    setDraftConcierge(null)
    setIsChoosingConcierge(true)
    localStorage.removeItem(LAST_SESSION_ID_KEY)
  }, [])

  const handleProjectsLoaded = useCallback((defaultProject: string) => {
    setProject(current => {
      if (current != null) return current
      localStorage.setItem('hephaestus.activeProject', defaultProject)
      return defaultProject
    })
  }, [])

  const handleSessionCreated = useCallback((id: number) => {
    setSessionId(id)
    setDraftConcierge(null)
    setIsChoosingConcierge(false)
    setSidebarRefreshKey(v => v + 1)
    localStorage.setItem(LAST_SESSION_ID_KEY, String(id))
  }, [])

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
        draftConcierge={draftConcierge}
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
          onDirtyChange={setConfigurationDirty}
          onCreate={handleConfigurationCreate}
          onSaved={(kind, name) => {
            setConfigurationKind(kind)
            setConfigurationName(name)
            setConfigurationIsNew(false)
            setConfigurationRefreshKey(value => value + 1)
          }}
          onDeleted={() => {
            setConfigurationKind(null)
            setConfigurationName(null)
            setConfigurationIsNew(false)
            setConfigurationRefreshKey(value => value + 1)
          }}
          onOpenNavigation={() => setConfigurationSidebarOpen(true)}
        /> : <ChatView
          sessionId={sessionId}
          draftConcierge={draftConcierge}
          isChoosingConcierge={isChoosingConcierge}
          defaultConciergeId={lastConciergeId}
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
