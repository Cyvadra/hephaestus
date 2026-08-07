import { useEffect, useState, useCallback } from 'react'
import { Plus, Settings } from 'lucide-react'
import { listSessions } from '../api/client'
import type { Session, ConciergeItem } from '../api/types'

interface Props {
  activeSessionId: number | null
  refreshKey: number
  draftConcierge: ConciergeItem | null
  sessionUpdate: Session | null
  onSelect: (id: number) => void
  onOpenNewSession: () => void
}

export default function SessionSidebar({ activeSessionId, refreshKey, draftConcierge, sessionUpdate, onSelect, onOpenNewSession }: Props) {
  const [sessions, setSessions] = useState<Session[]>([])

  const reload = useCallback(async () => {
    setSessions(await listSessions())
  }, [])

  useEffect(() => {
    void reload()
  }, [reload, refreshKey])

  useEffect(() => {
    if (sessionUpdate == null) return
    setSessions(current => {
      const withoutUpdated = current.filter(session => session.ID !== sessionUpdate.ID)
      return [sessionUpdate, ...withoutUpdated].sort((left, right) =>
        new Date(right.UpdatedAt).getTime() - new Date(left.UpdatedAt).getTime(),
      )
    })
  }, [sessionUpdate])

  const active = sessions.filter(s => !s.FlagArchived)
  const archived = sessions.filter(s => s.FlagArchived)
  const activeSession = sessions.find(s => s.ID === activeSessionId) ?? null
  const currentConcierge = activeSession?.SourceConcierge || draftConcierge?.name || '未选择'

  return (
    <aside className="sidebar">
      <div className="sidebar-brand">
        <img src="/deepseek-logo.svg" alt="DeepSeek" />
        <span>DeepSeek</span>
      </div>
      <button
        className="sidebar-new-btn"
        onClick={onOpenNewSession}
      >
        <Plus aria-hidden="true" size={16} strokeWidth={1.7} />
        <span>New chat</span>
      </button>

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
        <div className="settings-tooltip">
          <button className="sidebar-settings-btn" aria-label="设置" type="button">
            <Settings aria-hidden="true" size={16} strokeWidth={1.7} />
          </button>
          <span role="tooltip">Coming soon</span>
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
      <div className="session-item-meta">{formatRelativeTime(session.UpdatedAt)}</div>
    </button>
  )
}

function formatRelativeTime(updatedAt: string): string {
  const timestamp = new Date(updatedAt).getTime()
  if (Number.isNaN(timestamp)) return '刚刚更新'

  const elapsedMinutes = Math.max(0, Math.floor((Date.now() - timestamp) / 60_000))
  if (elapsedMinutes < 1) return '刚刚更新'
  if (elapsedMinutes < 60) return `${elapsedMinutes} 分钟前`

  const elapsedHours = Math.floor(elapsedMinutes / 60)
  if (elapsedHours < 24) return `${elapsedHours} 小时前`

  return `${Math.floor(elapsedHours / 24)} 天前`
}
