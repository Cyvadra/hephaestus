import { Check, ChevronUp, FolderPlus, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createProject, deleteProject, listProjects } from '../api/client'
import type { Project } from '../api/types'
import { useHoverMenu } from '../lib/useHoverMenu'

interface Props {
  activeProject: string | null
  onProjectChange: (project: string) => void
  onProjectsLoaded: (defaultProject: string) => void
}

export default function ProjectSwitcher({ activeProject, onProjectChange, onProjectsLoaded }: Props) {
  const { t } = useTranslation()
  const [projects, setProjects] = useState<Project[]>([])
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [deleteCandidate, setDeleteCandidate] = useState<Project | null>(null)
  const rootRef = useRef<HTMLDivElement>(null)
  const menu = useHoverMenu(rootRef)

  const reload = useCallback(async () => {
    try {
      const loaded = await listProjects()
      setProjects(loaded)
      const defaultProject = loaded.find(project => project.is_default)?.Name
      if (defaultProject) onProjectsLoaded(defaultProject)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }, [onProjectsLoaded])

  useEffect(() => {
    void reload()
  }, [reload])

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    const trimmedName = name.trim()
    if (!trimmedName) return
    setError(null)
    try {
      const created = await createProject(trimmedName, description.trim())
      setProjects(current => [...current, created])
      onProjectChange(created.Name)
      setName('')
      setDescription('')
      setCreating(false)
      menu.close()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }

  const current = projects.find(project => project.Name === activeProject)
  const defaultProject = projects.find(project => project.is_default)?.Name
  return (
    <div className="project-switcher" ref={rootRef} onMouseEnter={menu.openOnHover} onMouseLeave={menu.scheduleClose}>
      <button
        className="project-switcher-trigger"
        type="button"
        aria-expanded={menu.open}
        aria-haspopup="menu"
        onClick={menu.togglePinned}
        onFocus={menu.pinOpen}
      >
        <span className="project-switcher-name">{current?.Name ?? activeProject ?? t('project.loading')}</span>
        <ChevronUp aria-hidden="true" size={15} strokeWidth={1.8} />
      </button>
      {menu.open && (
        <div className="project-switcher-menu" role="menu">
          <button className="project-switcher-create" type="button" onClick={() => setCreating(current => !current)}>
            <FolderPlus aria-hidden="true" size={15} />
            <span>{t('project.new')}</span>
          </button>
          <div className="project-switcher-list">
            {projects.map(project => (
              <div className="project-switcher-option" key={project.ID} role="menuitem">
                <button className="project-switcher-select" type="button" onClick={() => { onProjectChange(project.Name); menu.close() }}>
                  <span>
                    <strong>{project.Name}</strong>
                    {project.Description && <small>{project.Description}</small>}
                  </span>
                  {project.Name === activeProject && <Check aria-label={t('project.current')} size={15} />}
                </button>
                {!project.is_default && <button className="project-switcher-delete" type="button" aria-label={t('project.delete', { name: project.Name })} onClick={() => setDeleteCandidate(project)}><Trash2 aria-hidden="true" size={14} /></button>}
              </div>
            ))}
          </div>
          {creating && (
            <form className="project-create-form" onSubmit={submit}>
              <input value={name} onChange={event => setName(event.target.value)} placeholder="project-name" maxLength={63} aria-label={t('project.name')} autoFocus />
              <input value={description} onChange={event => setDescription(event.target.value)} placeholder={t('project.descriptionPlaceholder')} maxLength={1024} aria-label={t('project.description')} />
              <button type="submit"><Plus aria-hidden="true" size={15} />{t('project.create')}</button>
            </form>
          )}
          {error && <p className="project-switcher-error">{error}</p>}
        </div>
      )}
      {deleteCandidate && <DeleteProjectDialog project={deleteCandidate} onClose={() => setDeleteCandidate(null)} onConfirm={async deleteDirectory => {
        await deleteProject(deleteCandidate.Name, deleteDirectory)
        setProjects(current => current.filter(project => project.ID !== deleteCandidate.ID))
        if (deleteCandidate.Name === activeProject && defaultProject) onProjectChange(defaultProject)
        setDeleteCandidate(null)
      }} />}
    </div>
  )
}

function DeleteProjectDialog({ project, onClose, onConfirm }: { project: Project; onClose: () => void; onConfirm: (deleteDirectory: boolean) => Promise<void> }) {
  const { t } = useTranslation()
  const [confirmation, setConfirmation] = useState('')
  const [deleteDirectory, setDeleteDirectory] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !deleting) onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [deleting, onClose])

  return <div className="session-dialog-backdrop" role="presentation" onMouseDown={() => { if (!deleting) onClose() }}><div className="session-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-project-title" onMouseDown={event => event.stopPropagation()}><h2 id="delete-project-title">{t('project.deleteTitle')}</h2><p>{t('project.deleteBody', { name: project.Name })}</p>{error && <p className="project-switcher-error">{error}</p>}<input autoFocus value={confirmation} onChange={event => setConfirmation(event.target.value)} placeholder={project.Name} aria-label={t('project.confirmation')} /><label className="project-delete-directory"><input type="checkbox" checked={deleteDirectory} onChange={event => setDeleteDirectory(event.target.checked)} disabled={deleting} />{t('project.deleteDirectory')}</label><div className="session-dialog-actions"><button type="button" onClick={onClose} disabled={deleting}>{t('common.cancel')}</button><button className="danger-button" type="button" disabled={confirmation !== project.Name || deleting} onClick={async () => { setDeleting(true); setError(null); try { await onConfirm(deleteDirectory) } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setDeleting(false) } }}>{deleting ? t('project.deleting') : t('common.delete')}</button></div></div></div>
}
