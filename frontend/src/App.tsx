import { useState, useCallback } from 'react'
import SessionSidebar from './components/SessionSidebar'
import ChatView from './components/ChatView'
import type { ConciergeItem } from './api/types'

export default function App() {
  const [sessionId, setSessionId] = useState<number | null>(null)
  const [draftConcierge, setDraftConcierge] = useState<ConciergeItem | null>(null)
  const [sidebarRefreshKey, setSidebarRefreshKey] = useState(0)

  const handleSessionSelect = useCallback((id: number) => {
    setDraftConcierge(null)
    setSessionId(id)
  }, [])

  const handleStartDraft = useCallback((concierge: ConciergeItem) => {
    setSessionId(null)
    setDraftConcierge(concierge)
  }, [])

  const handleSessionCreated = useCallback((id: number) => {
    setSessionId(id)
    setDraftConcierge(null)
    setSidebarRefreshKey(v => v + 1)
  }, [])

  return (
    <div className="app-shell">
      <SessionSidebar
        activeSessionId={sessionId}
        refreshKey={sidebarRefreshKey}
        draftConcierge={draftConcierge}
        onSelect={handleSessionSelect}
        onStartDraft={handleStartDraft}
      />
      <main className="main-panel">
        {sessionId != null || draftConcierge != null ? (
          <ChatView
            sessionId={sessionId}
            draftConcierge={draftConcierge}
            onSessionCreated={handleSessionCreated}
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
