import { useEffect, useRef, useState, useCallback, type CSSProperties } from 'react'
import { Check, ChevronRight, Pencil, Pin, Plus, Settings, Trash2, Undo2 } from 'lucide-react'
import { deleteSession, listSessions, updateSession } from '../api/client'
import type { Session, ConciergeItem } from '../api/types'
import ProjectSwitcher from './ProjectSwitcher'

interface Props {
  activeSessionId: number | null
  refreshKey: number
  draftConcierge: ConciergeItem | null
  sessionUpdate: Session | null
  project: string | null
  onProjectChange: (project: string) => void
  onProjectsLoaded: (defaultProject: string) => void
  onSelect: (id: number) => void
  onOpenNewSession: () => void
}

export default function SessionSidebar({ activeSessionId, refreshKey, draftConcierge, sessionUpdate, project, onProjectChange, onProjectsLoaded, onSelect, onOpenNewSession }: Props) {
  const [sessions, setSessions] = useState<Session[]>([])
  const [menuSessionId, setMenuSessionId] = useState<number | null>(null)
  const [renamingId, setRenamingId] = useState<number | null>(null)
  const [deleteCandidate, setDeleteCandidate] = useState<Session | null>(null)
  const [archivedExpanded, setArchivedExpanded] = useState(false)

  const reload = useCallback(async () => {
    if (project != null) setSessions(await listSessions(project))
  }, [project])

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

  useEffect(() => {
    if (menuSessionId == null) return
    const handlePointerDown = (event: MouseEvent) => {
      if (!(event.target as HTMLElement | null)?.closest('.session-menu')) setMenuSessionId(null)
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenuSessionId(null)
    }
    document.addEventListener('mousedown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('mousedown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [menuSessionId])

  const isPinned = (session: Session) => session.FlagPinned === 1
  const active = sessions.filter(s => !s.FlagArchived)
  const pinnedSessions = active.filter(isPinned)
  const groups = groupSessions(active.filter(s => !isPinned(s)))
  const archived = sessions.filter(s => s.FlagArchived)
  const activeSession = sessions.find(s => s.ID === activeSessionId) ?? null
  const currentConcierge = activeSession?.SourceConcierge || draftConcierge?.name || '未选择'

  function renderSession(s: Session) {
    return (
      <SessionItem
        key={s.ID}
        session={s}
        active={s.ID === activeSessionId}
        menuOpen={s.ID === menuSessionId}
        pinned={isPinned(s)}
        renaming={s.ID === renamingId}
        onSelect={onSelect}
        onMenuOpen={() => setMenuSessionId(s.ID)}
        onRenameStart={() => { setRenamingId(s.ID); setMenuSessionId(null) }}
        onRenameSubmit={title => void handleRenameSubmit(s, title)}
        onRenameCancel={() => setRenamingId(null)}
        onPin={() => void togglePin(s)}
        onArchive={() => void handleArchive(s)}
        onDelete={() => { setDeleteCandidate(s); setMenuSessionId(null) }}
      />
    )
  }

  async function handleRenameSubmit(session: Session, title: string) {
    const trimmed = title.trim()
    if (trimmed && trimmed !== (session.Title || '')) {
      const updated = await updateSession(session.ID, { title: trimmed })
      setSessions(current => current.map(item => item.ID === updated.ID ? updated : item))
    }
    setRenamingId(null)
  }

  return (
    <aside className="sidebar">
      <div className="sidebar-brand">
        <img src="/deepseek-logo.svg" alt="DeepSeek" />
        <span>DeepSeek</span>
      </div>
      <ProjectSwitcher activeProject={project} onProjectChange={onProjectChange} onProjectsLoaded={onProjectsLoaded} />
      <button
        className="sidebar-new-btn"
        onClick={onOpenNewSession}
      >
        <Plus aria-hidden="true" size={16} strokeWidth={1.7} />
        <span>New chat</span>
      </button>

      <div className="sidebar-section">
        <div className="session-list">
          {pinnedSessions.length > 0 && (
            <div className="session-group">
              <div className="sidebar-section-title">置顶</div>
              {pinnedSessions.map(renderSession)}
            </div>
          )}
          {groups.map(group => (
            <div key={group.key} className="session-group">
              <div className="sidebar-section-title">{group.label}</div>
              {group.sessions.map(renderSession)}
            </div>
          ))}
          {archived.length > 0 && (
            <div className="session-group">
              <button
                className="sidebar-section-title archived-sessions-toggle"
                type="button"
                aria-expanded={archivedExpanded}
                aria-controls="archived-session-list"
                onClick={() => setArchivedExpanded(current => !current)}
              >
                <ChevronRight aria-hidden="true" size={14} />
                <span>已归档</span>
              </button>
              {archivedExpanded && <div id="archived-session-list">{archived.slice(0, 20).map(renderSession)}</div>}
            </div>
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
      {deleteCandidate && <DeleteDialog session={deleteCandidate} onClose={() => setDeleteCandidate(null)} onConfirm={async () => { await deleteSession(deleteCandidate.ID); setSessions(current => current.filter(session => session.ID !== deleteCandidate.ID)); if (deleteCandidate.ID === activeSessionId) onOpenNewSession(); setDeleteCandidate(null) }} />}
    </aside>
  )

  async function togglePin(session: Session) {
    const updated = await updateSession(session.ID, { pinned: !isPinned(session) })
    setSessions(current => current.map(item => item.ID === updated.ID ? updated : item))
    setMenuSessionId(null)
  }

  async function handleArchive(session: Session) {
    const updated = await updateSession(session.ID, { archived: !session.FlagArchived })
    setSessions(current => current.map(item => item.ID === updated.ID ? updated : item))
    setMenuSessionId(null)
  }
}

function SessionItem({ session, active, menuOpen, pinned, renaming, onSelect, onMenuOpen, onRenameStart, onRenameSubmit, onRenameCancel, onPin, onArchive, onDelete }: { session: Session; active: boolean; menuOpen: boolean; pinned: boolean; renaming: boolean; onSelect: (id: number) => void; onMenuOpen: () => void; onRenameStart: () => void; onRenameSubmit: (title: string) => void; onRenameCancel: () => void; onPin: () => void; onArchive: () => void; onDelete: () => void }) {
  const label = session.Title || `Session #${session.ID}`
  const titleRef = useRef<HTMLSpanElement>(null)
  const [titleScroll, setTitleScroll] = useState({ distance: 0, duration: 0 })

  useEffect(() => {
    const title = titleRef.current
    if (!title) return

    const updateTitleScroll = () => {
      const distance = Math.max(0, title.scrollWidth - title.clientWidth)
      setTitleScroll(current => {
        const duration = distance > 0 ? Math.max(0.5, distance / 100) : 0
        return current.distance === distance && current.duration === duration ? current : { distance, duration }
      })
    }
    const observer = new ResizeObserver(updateTitleScroll)
    observer.observe(title)
    updateTitleScroll()
    return () => observer.disconnect()
  }, [label])

  const titleStyle = {
    '--session-title-scroll-distance': `${titleScroll.distance}px`,
    '--session-title-scroll-duration': `${titleScroll.duration}s`,
  } as CSSProperties

  return (
    <div className={'session-item-wrap' + (active ? ' active' : '') + (menuOpen ? ' menu-open' : '')} onContextMenu={event => { event.preventDefault(); onMenuOpen() }}>
      {renaming ? (
        <RenameInput defaultValue={label} onSubmit={onRenameSubmit} onCancel={onRenameCancel} />
      ) : (
        <>
          <button onClick={() => onSelect(session.ID)} className="session-item">
            <span ref={titleRef} className={'session-item-title' + (titleScroll.distance > 0 ? ' overflowing' : '')} style={titleStyle}>
              <span className="session-item-title-text">{label}</span>
            </span>
          </button>
          <button className="session-item-archive" type="button" aria-label={`${session.FlagArchived ? '取消归档' : '归档'} ${label}`} onClick={event => { event.stopPropagation(); onArchive() }}>
            {session.FlagArchived ? <Undo2 aria-hidden="true" size={13} /> : <Check aria-hidden="true" size={14} />}
          </button>
          <button className={'session-item-pin' + (pinned ? ' pinned' : '')} type="button" aria-label={`${pinned ? '取消置顶' : '置顶'} ${label}`} onClick={event => { event.stopPropagation(); onPin() }}><Pin aria-hidden="true" size={12} /></button>
        </>
      )}
      {menuOpen && !renaming && <div className="session-menu" role="menu">
        <button type="button" role="menuitem" onClick={onRenameStart}><Pencil aria-hidden="true" size={16} />重命名</button>
        <button className="danger" type="button" role="menuitem" onClick={onDelete}><Trash2 aria-hidden="true" size={16} />删除</button>
      </div>}
    </div>
  )
}

function RenameInput({ defaultValue, onSubmit, onCancel }: { defaultValue: string; onSubmit: (value: string) => void; onCancel: () => void }) {
  const cancelled = useRef(false)
  return (
    <input
      className="session-rename-input"
      autoFocus
      defaultValue={defaultValue}
      maxLength={64}
      aria-label="会话名称"
      onFocus={event => event.target.select()}
      onKeyDown={event => {
        if (event.key === 'Enter') {
          event.preventDefault()
          onSubmit(event.currentTarget.value)
        } else if (event.key === 'Escape') {
          event.preventDefault()
          cancelled.current = true
          onCancel()
        }
      }}
      onBlur={event => { if (!cancelled.current) onSubmit(event.target.value) }}
    />
  )
}

function DeleteDialog({ session, onClose, onConfirm }: { session: Session; onClose: () => void; onConfirm: () => Promise<void> }) {
  const [deleting, setDeleting] = useState(false)
  const title = session.Title || `Session #${session.ID}`

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return <div className="session-dialog-backdrop" role="presentation" onMouseDown={onClose}><div className="session-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-session-title" onMouseDown={event => event.stopPropagation()}><h2 id="delete-session-title">确认删除该对话？</h2><p>删除后，该对话「{title}」将不可恢复。</p><div className="session-dialog-actions"><button type="button" onClick={onClose} disabled={deleting}>取消</button><button className="danger-button" type="button" disabled={deleting} onClick={async () => { setDeleting(true); try { await onConfirm() } finally { setDeleting(false) } }}>{deleting ? '删除中...' : '删除'}</button></div></div></div>
}

interface SessionGroup {
  key: string
  label: string
  sessions: Session[]
}

function groupSessions(sessions: Session[]): SessionGroup[] {
  const dayMs = 86_400_000
  const startOfToday = new Date()
  startOfToday.setHours(0, 0, 0, 0)

  const ageInDays = (updatedAt: string): number => {
    const date = new Date(updatedAt)
    if (Number.isNaN(date.getTime())) return 0
    date.setHours(0, 0, 0, 0)
    return Math.max(0, Math.round((startOfToday.getTime() - date.getTime()) / dayMs))
  }

  const buckets: { key: string; label: string; match: (age: number) => boolean }[] = [
    { key: 'today', label: '今天', match: age => age < 1 },
    { key: 'yesterday', label: '昨天', match: age => age === 1 },
    { key: 'week', label: '7 天内', match: age => age > 1 && age <= 7 },
    { key: 'month', label: '30 天内', match: age => age > 7 && age <= 30 },
    { key: 'earlier', label: '更早', match: age => age > 30 },
  ]

  return buckets
    .map(bucket => ({ key: bucket.key, label: bucket.label, sessions: sessions.filter(session => bucket.match(ageInDays(session.UpdatedAt))) }))
    .filter(group => group.sessions.length > 0)
}
