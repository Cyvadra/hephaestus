import { useState, useCallback } from 'react'
import SessionSidebar from './components/SessionSidebar'
import ChatView from './components/ChatView'
import type { ConciergeItem, Session } from './api/types'

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

  const handleSessionSelect = useCallback((id: number) => {
    setDraftConcierge(null)
    setIsChoosingConcierge(false)
    setSessionId(id)
    localStorage.setItem(LAST_SESSION_ID_KEY, String(id))
  }, [])

  const handleStartDraft = useCallback((concierge: ConciergeItem) => {
    setSessionId(null)
    setDraftConcierge(concierge)
    setIsChoosingConcierge(false)
    setLastConciergeId(concierge.name)
    localStorage.removeItem(LAST_SESSION_ID_KEY)
    localStorage.setItem(LAST_CONCIERGE_ID_KEY, concierge.name)
  }, [])

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
        activeSessionId={sessionId}
        refreshKey={sidebarRefreshKey}
        draftConcierge={draftConcierge}
        sessionUpdate={sidebarSessionUpdate}
        project={project}
        onProjectChange={handleProjectChange}
        onProjectsLoaded={handleProjectsLoaded}
        onSelect={handleSessionSelect}
        onOpenNewSession={handleOpenNewSession}
      />
      <main className="main-panel">
        <ChatView
          sessionId={sessionId}
          draftConcierge={draftConcierge}
          isChoosingConcierge={isChoosingConcierge}
          defaultConciergeId={lastConciergeId}
          onChooseConcierge={handleStartDraft}
          onSessionCreated={handleSessionCreated}
          onSessionUpdated={handleSessionUpdated}
          project={project}
        />
      </main>
    </div>
  )
}
