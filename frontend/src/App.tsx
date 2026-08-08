import { useState, useCallback } from 'react'
import SessionSidebar from './components/SessionSidebar'
import ChatView from './components/ChatView'
import type { ConciergeItem, Session } from './api/types'

export default function App() {
  const [project, setProject] = useState<string | null>(() => localStorage.getItem('hephaestus.activeProject'))
  const [sessionId, setSessionId] = useState<number | null>(null)
  const [draftConcierge, setDraftConcierge] = useState<ConciergeItem | null>(null)
  const [isChoosingConcierge, setIsChoosingConcierge] = useState(false)
  const [sidebarRefreshKey, setSidebarRefreshKey] = useState(0)
  const [sidebarSessionUpdate, setSidebarSessionUpdate] = useState<Session | null>(null)

  const handleSessionSelect = useCallback((id: number) => {
    setDraftConcierge(null)
    setIsChoosingConcierge(false)
    setSessionId(id)
  }, [])

  const handleStartDraft = useCallback((concierge: ConciergeItem) => {
    setSessionId(null)
    setDraftConcierge(concierge)
    setIsChoosingConcierge(false)
  }, [])

  const handleOpenNewSession = useCallback(() => {
    setSessionId(null)
    setDraftConcierge(null)
    setIsChoosingConcierge(true)
  }, [])

  const handleProjectChange = useCallback((nextProject: string) => {
    localStorage.setItem('hephaestus.activeProject', nextProject)
    setProject(nextProject)
    setSessionId(null)
    setDraftConcierge(null)
    setIsChoosingConcierge(false)
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
        {sessionId != null || draftConcierge != null || isChoosingConcierge ? (
          <ChatView
            sessionId={sessionId}
            draftConcierge={draftConcierge}
            isChoosingConcierge={isChoosingConcierge}
            onChooseConcierge={handleStartDraft}
            onSessionCreated={handleSessionCreated}
            onSessionUpdated={handleSessionUpdated}
            project={project}
          />
        ) : (
          <section className="chat-surface">
            <header className="chat-header">
              <div>
                <p className="chat-header-eyebrow">Workspace</p>
                <h2 className="chat-header-title">选择一段对话</h2>
              </div>
              <div className="chat-badge">
                <span className="chat-dot" />
                就绪
              </div>
            </header>
            <div className="messages-pane">
              <div className="welcome-card">
                <span className="welcome-badge">轻量 AI 工作台</span>
                <h1>从侧边栏开始新的对话</h1>
                <p>在这里可以整理提示词、查看代码、继续上下文，界面会更像一个专注的智能助手工作区。</p>
                <div className="quick-pill-row">
                  <span className="quick-pill">代码评审</span>
                  <span className="quick-pill">调试助手</span>
                  <span className="quick-pill">产品方案</span>
                </div>
              </div>
            </div>
          </section>
        )}
      </main>
    </div>
  )
}
