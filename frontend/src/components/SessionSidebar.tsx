import { useEffect, useState, useCallback, useRef } from 'react'
import { listSessions, listConcierges } from '../api/client'
import type { Session, ConciergeItem } from '../api/types'

interface Props {
  activeSessionId: number | null
  refreshKey: number
  draftConcierge: ConciergeItem | null
  onSelect: (id: number) => void
  onStartDraft: (concierge: ConciergeItem) => void
}

export default function SessionSidebar({ activeSessionId, refreshKey, draftConcierge, onSelect, onStartDraft }: Props) {
  const [sessions, setSessions] = useState<Session[]>([])
  const [concierges, setConcierges] = useState<ConciergeItem[]>([])
  const [showConciergeMenu, setShowConciergeMenu] = useState(false)
  const hideTimerRef = useRef<number | null>(null)

  const reload = useCallback(async () => {
    const [s, c] = await Promise.all([listSessions(), listConcierges()])
    setSessions(s)
    setConcierges(c)
  }, [])

  useEffect(() => {
    void reload()
  }, [reload, refreshKey])

  const clearHideTimer = () => {
    if (hideTimerRef.current != null) {
      window.clearTimeout(hideTimerRef.current)
      hideTimerRef.current = null
    }
  }

  const openConciergeMenu = () => {
    clearHideTimer()
    setShowConciergeMenu(true)
  }

  const closeConciergeMenuSoon = () => {
    clearHideTimer()
    hideTimerRef.current = window.setTimeout(() => {
      setShowConciergeMenu(false)
      hideTimerRef.current = null
    }, 140)
  }

  useEffect(() => {
    return () => clearHideTimer()
  }, [])

  const active = sessions.filter(s => !s.FlagArchived)
  const archived = sessions.filter(s => s.FlagArchived)
  const activeSession = sessions.find(s => s.ID === activeSessionId) ?? null
  const currentConcierge = activeSession?.SourceConcierge || draftConcierge?.name || '未选择'

  return (
    <aside className="sidebar">
      <div className="sidebar-section">
        <div className="sidebar-section-title">最近会话</div>
        <div className="session-list">
          {active.map(s => (
            <SessionItem key={s.ID} session={s} active={s.ID === activeSessionId} onSelect={onSelect} />
          ))}
          {archived.length > 0 && (
            <>
              <div className="sidebar-section-title mt-3">已归档</div>
              {archived.map(s => (
                <SessionItem key={s.ID} session={s} active={s.ID === activeSessionId} onSelect={onSelect} />
              ))}
            </>
          )}
        </div>
      </div>

      <div className="sidebar-footer">
        <div className="sidebar-footer-label" title={currentConcierge}>{currentConcierge}</div>
        <div
          className="sidebar-new-wrap"
          onMouseEnter={openConciergeMenu}
          onMouseLeave={closeConciergeMenuSoon}
        >
          <button
            onFocus={openConciergeMenu}
            onBlur={closeConciergeMenuSoon}
            className="sidebar-new-btn"
          >
            + 新建
          </button>
          {showConciergeMenu && (
            <div
              className="sidebar-new-menu"
              role="menu"
              aria-label="选择顾问"
              onMouseEnter={openConciergeMenu}
              onMouseLeave={closeConciergeMenuSoon}
            >
              {concierges.map(c => (
                <button
                  key={c.name}
                  className="sidebar-new-menu-item"
                  onMouseDown={(e) => {
                    e.preventDefault()
                    onStartDraft(c)
                    setShowConciergeMenu(false)
                  }}
                >
                  {c.name}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </aside>
  )
}

function SessionItem({ session, active, onSelect }: { session: Session; active: boolean; onSelect: (id: number) => void }) {
  const label = session.Title || `Session #${session.ID}`
  return (
    <button
      onClick={() => onSelect(session.ID)}
      className={'session-item ' + (active ? 'active' : '')}
    >
      <div className="session-item-title">{label}</div>
      <div className="session-item-meta">{session.FlagArchived ? '已归档' : '进行中'}</div>
    </button>
  )
}
