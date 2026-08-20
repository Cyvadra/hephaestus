import { useEffect, useRef, useState, useCallback, type CSSProperties } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronRight, Pencil, Pin, Plus, Trash2, Undo2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { deleteSession, listSessions, updateSession } from '../api/client'
import type { Session } from '../api/types'
import type { ConfigurationKind } from '../api/types'
import ProjectSwitcher from './ProjectSwitcher'
import ConfigurationSidebar, { type ConfigurationLists } from './ConfigurationSidebar'
import SidebarSettingsMenu from './SidebarSettingsMenu'

interface Props {
  mode: 'chat' | 'configurations'
  configurationSidebarOpen: boolean
  activeSessionId: number | null
  refreshKey: number
  sessionUpdate: Session | null
  project: string | null
  onProjectChange: (project: string) => void
  onProjectsLoaded: (defaultProject: string) => void
  onSelect: (id: number) => void
  onOpenNewSession: () => void
  onOpenConfigurations: () => void
  onCloseConfigurations: () => void
  configurationKind: ConfigurationKind | null
  configurationName: string | null
  configurationRefreshKey: number
  onConfigurationSelect: (kind: ConfigurationKind, name: string) => void
  onConfigurationCreate: (kind: ConfigurationKind) => void
  onConfigurationOpenConstants: () => void
  onConfigurationListsChange: (lists: ConfigurationLists) => void
}

export default function SessionSidebar({ mode, configurationSidebarOpen, activeSessionId, refreshKey, sessionUpdate, project, onProjectChange, onProjectsLoaded, onSelect, onOpenNewSession, onOpenConfigurations, onCloseConfigurations, configurationKind, configurationName, configurationRefreshKey, onConfigurationSelect, onConfigurationCreate, onConfigurationOpenConstants, onConfigurationListsChange }: Props) {
  const { t } = useTranslation()
  const [sessions, setSessions] = useState<Session[]>([])
  const [menu, setMenu] = useState<{ sessionID: number; left: number; top: number } | null>(null)
  const [renamingId, setRenamingId] = useState<number | null>(null)
  const [deleteCandidate, setDeleteCandidate] = useState<Session | null>(null)
  const [archivedExpanded, setArchivedExpanded] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const reloadControllerRef = useRef<AbortController | null>(null)

  const reload = useCallback(async () => {
    reloadControllerRef.current?.abort()
    if (project == null) {
      setSessions([])
      return
    }
    const controller = new AbortController()
    reloadControllerRef.current = controller
    try {
      const loaded = await listSessions(project, controller.signal)
      if (reloadControllerRef.current === controller) {
        setSessions(loaded)
        setError(null)
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause))
    }
  }, [project])

  useEffect(() => {
    setSessions([])
    void reload()
    return () => reloadControllerRef.current?.abort()
  }, [reload, refreshKey])

  useEffect(() => {
    if (mode !== 'chat' || project == null) return
    const interval = window.setInterval(() => { void reload() }, 5_000)
    return () => window.clearInterval(interval)
  }, [mode, project, reload])

  useEffect(() => {
    if (sessionUpdate == null) return
    setSessions(current => {
      const withoutUpdated = current.filter(session => session.ID !== sessionUpdate.ID)
      return [sessionUpdate, ...withoutUpdated].sort((left, right) =>
		new Date(right.LastMessageTime).getTime() - new Date(left.LastMessageTime).getTime(),
      )
    })
  }, [sessionUpdate])

  useEffect(() => {
    if (menu == null) return
    const handlePointerDown = (event: MouseEvent) => {
      if (!(event.target as HTMLElement | null)?.closest('.session-menu')) setMenu(null)
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenu(null)
    }
    document.addEventListener('mousedown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('mousedown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [menu])

  const isPinned = (session: Session) => session.FlagPinned === 1
  const active = sessions.filter(s => !s.FlagArchived)
  const pinnedSessions = active.filter(isPinned)
  const groups = groupSessions(active.filter(s => !isPinned(s)), t)
  const archived = sessions.filter(s => s.FlagArchived)

  function renderSession(s: Session) {
    return (
      <SessionItem
        key={s.ID}
        session={s}
        active={s.ID === activeSessionId}
        menu={s.ID === menu?.sessionID ? menu : null}
        pinned={isPinned(s)}
        renaming={s.ID === renamingId}
        onSelect={onSelect}
        onMenuOpen={(target) => {
          const rect = target.getBoundingClientRect()
          const width = 132
          const height = 76
          const margin = 4
          setMenu({
            sessionID: s.ID,
            left: Math.max(margin, Math.min(rect.right - width - 3, window.innerWidth - width - margin)),
            top: Math.max(margin, rect.bottom + height + margin <= window.innerHeight ? rect.bottom - 2 : rect.top - height + 2),
          })
        }}
        onRenameStart={() => { setRenamingId(s.ID); setMenu(null) }}
        onRenameSubmit={title => void handleRenameSubmit(s, title)}
        onRenameCancel={() => setRenamingId(null)}
        onPin={() => void togglePin(s)}
        onArchive={() => void handleArchive(s)}
        onDelete={() => { setDeleteCandidate(s); setMenu(null) }}
      />
    )
  }

  async function handleRenameSubmit(session: Session, title: string) {
    const trimmed = title.trim()
    try {
      if (trimmed && trimmed !== (session.Title || '')) {
        const updated = await updateSession(session.ID, { title: trimmed })
        setSessions(current => current.map(item => item.ID === updated.ID ? updated : item))
      }
      setRenamingId(null)
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }

  return (
    <aside className={`sidebar${mode === 'configurations' ? ` configuration-mode${configurationSidebarOpen ? ' mobile-open' : ''}` : ''}`}>
      <div className="sidebar-brand">
        <img className="sidebar-brand-icon" src="/deepseek-logo.svg" alt="" />
        <strong>Hephaestus</strong>
      </div>
      {mode === 'configurations' ? <ConfigurationSidebar
        activeKind={configurationKind}
        activeName={configurationName}
        refreshKey={configurationRefreshKey}
        onBack={onCloseConfigurations}
        onSelect={onConfigurationSelect}
        onCreate={onConfigurationCreate}
        onOpenConstants={onConfigurationOpenConstants}
        onListsChange={onConfigurationListsChange}
      /> : <><button
        className="sidebar-new-btn"
        onClick={onOpenNewSession}
      >
        <span className="sidebar-new-icon"><Plus aria-hidden="true" size={11} strokeWidth={2} /></span>
        <span>{t('session.newChat')}</span>
      </button>

      <div className="sidebar-section">
        {error && <div className="sidebar-error" role="alert">{error}</div>}
        <div className="session-list">
          {pinnedSessions.length > 0 && (
            <div className="session-group">
              <div className="sidebar-section-title">{t('session.pinned')}</div>
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
                <span>{t('session.archived')}</span>
              </button>
              {archivedExpanded && <div id="archived-session-list">{archived.slice(0, 20).map(renderSession)}</div>}
            </div>
          )}
        </div>
      </div>

      </>}

      <div className="sidebar-footer">
        {mode === 'configurations'
          ? <div className="sidebar-footer-label" title={t('configuration.databaseConfiguration')}>{t('configuration.registryConsole')}</div>
          : <ProjectSwitcher activeProject={project} onProjectChange={onProjectChange} onProjectsLoaded={onProjectsLoaded} />}
        <SidebarSettingsMenu mode={mode} onOpenConfigurations={onOpenConfigurations} onCloseConfigurations={onCloseConfigurations} />
      </div>
      {deleteCandidate && <DeleteDialog session={deleteCandidate} onClose={() => setDeleteCandidate(null)} onConfirm={async () => {
        try {
          await deleteSession(deleteCandidate.ID)
          setSessions(current => current.filter(session => session.ID !== deleteCandidate.ID))
          if (deleteCandidate.ID === activeSessionId) onOpenNewSession()
          setDeleteCandidate(null)
          setError(null)
        } catch (cause) {
          const message = cause instanceof Error ? cause.message : String(cause)
          setError(message)
          throw cause
        }
      }} />}
    </aside>
  )

  async function togglePin(session: Session) {
    try {
      const updated = await updateSession(session.ID, { pinned: !isPinned(session) })
      setSessions(current => current.map(item => item.ID === updated.ID ? updated : item))
      setMenu(null)
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }

  async function handleArchive(session: Session) {
    try {
      const updated = await updateSession(session.ID, { archived: !session.FlagArchived })
      setSessions(current => current.map(item => item.ID === updated.ID ? updated : item))
      setMenu(null)
      setError(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }
}

function SessionItem({ session, active, menu, pinned, renaming, onSelect, onMenuOpen, onRenameStart, onRenameSubmit, onRenameCancel, onPin, onArchive, onDelete }: { session: Session; active: boolean; menu: { left: number; top: number } | null; pinned: boolean; renaming: boolean; onSelect: (id: number) => void; onMenuOpen: (target: HTMLElement) => void; onRenameStart: () => void; onRenameSubmit: (title: string) => void; onRenameCancel: () => void; onPin: () => void; onArchive: () => void; onDelete: () => void }) {
  const { t } = useTranslation()
  const label = session.Title || t('session.unnamed', { id: session.ID })
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
    <div className={'session-item-wrap' + (active ? ' active' : '') + (menu ? ' menu-open' : '')} onContextMenu={event => { event.preventDefault(); onMenuOpen(event.currentTarget) }}>
      {renaming ? (
        <RenameInput defaultValue={label} onSubmit={onRenameSubmit} onCancel={onRenameCancel} />
      ) : (
        <>
          <button onClick={() => onSelect(session.ID)} className="session-item">
            <span ref={titleRef} className={'session-item-title' + (titleScroll.distance > 0 ? ' overflowing' : '')} style={titleStyle}>
              <span className="session-item-title-text">{label}</span>
            </span>
          </button>
          <button className="session-item-archive" type="button" aria-label={t(session.FlagArchived ? 'session.unarchive' : 'session.archive', { title: label })} onClick={event => { event.stopPropagation(); onArchive() }}>
            {session.FlagArchived ? <Undo2 aria-hidden="true" size={13} /> : <Check aria-hidden="true" size={14} />}
          </button>
          <button className={'session-item-pin' + (pinned ? ' pinned' : '')} type="button" aria-label={t(pinned ? 'session.unpin' : 'session.pin', { title: label })} onClick={event => { event.stopPropagation(); onPin() }}><Pin aria-hidden="true" size={12} /></button>
        </>
      )}
      {menu && !renaming && createPortal(<div className="session-menu" role="menu" style={{ left: menu.left, top: menu.top }}>
        <button type="button" role="menuitem" onClick={onRenameStart}><Pencil aria-hidden="true" size={16} />{t('session.rename')}</button>
        <button className="danger" type="button" role="menuitem" onClick={onDelete}><Trash2 aria-hidden="true" size={16} />{t('common.delete')}</button>
      </div>, document.body)}
    </div>
  )
}

function RenameInput({ defaultValue, onSubmit, onCancel }: { defaultValue: string; onSubmit: (value: string) => void; onCancel: () => void }) {
  const { t } = useTranslation()
  const cancelled = useRef(false)
  return (
    <input
      className="session-rename-input"
      autoFocus
      defaultValue={defaultValue}
      maxLength={64}
      aria-label={t('session.name')}
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
  const { t } = useTranslation()
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const title = session.Title || t('session.unnamed', { id: session.ID })

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return <div className="session-dialog-backdrop" role="presentation" onMouseDown={onClose}><div className="session-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-session-title" onMouseDown={event => event.stopPropagation()}><h2 id="delete-session-title">{t('session.deleteTitle')}</h2><p>{t('session.deleteBody', { title })}</p>{error && <p className="sidebar-error" role="alert">{error}</p>}<div className="session-dialog-actions"><button type="button" onClick={onClose} disabled={deleting}>{t('common.cancel')}</button><button className="danger-button" type="button" disabled={deleting} onClick={async () => { setDeleting(true); setError(null); try { await onConfirm() } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setDeleting(false) } }}>{deleting ? t('session.deleting') : t('common.delete')}</button></div></div></div>
}

interface SessionGroup {
  key: string
  label: string
  sessions: Session[]
}

function groupSessions(sessions: Session[], t: ReturnType<typeof useTranslation>['t']): SessionGroup[] {
  const dayMs = 86_400_000
  const startOfToday = new Date()
  startOfToday.setHours(0, 0, 0, 0)

  const ageInDays = (lastMessageTime: string): number => {
	const date = new Date(lastMessageTime)
    if (Number.isNaN(date.getTime())) return 0
    date.setHours(0, 0, 0, 0)
    return Math.max(0, Math.round((startOfToday.getTime() - date.getTime()) / dayMs))
  }

  const buckets: { key: string; label: string; match: (age: number) => boolean }[] = [
    { key: 'today', label: t('session.time.today'), match: age => age < 1 },
    { key: 'yesterday', label: t('session.time.yesterday'), match: age => age === 1 },
    { key: 'week', label: t('session.time.week'), match: age => age > 1 && age <= 7 },
    { key: 'month', label: t('session.time.month'), match: age => age > 7 && age <= 30 },
    { key: 'earlier', label: t('session.time.earlier'), match: age => age > 30 },
  ]

  return buckets
    .map(bucket => ({ key: bucket.key, label: bucket.label, sessions: sessions.filter(session => bucket.match(ageInDays(session.LastMessageTime))) }))
    .filter(group => group.sessions.length > 0)
}
