import { Check, ChevronDown, FolderPlus, Plus } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { createProject, listProjects } from '../api/client'
import type { Project } from '../api/types'

interface Props {
  activeProject: string | null
  onProjectChange: (project: string) => void
  onProjectsLoaded: (defaultProject: string) => void
}

export default function ProjectSwitcher({ activeProject, onProjectChange, onProjectsLoaded }: Props) {
  const [projects, setProjects] = useState<Project[]>([])
  const [open, setOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState<string | null>(null)
  const rootRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    void reload()
  }, [])

  useEffect(() => {
    const close = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [])

  async function reload() {
    try {
      const loaded = await listProjects()
      setProjects(loaded)
      const defaultProject = loaded.find(project => project.is_default)?.Name
      if (defaultProject) onProjectsLoaded(defaultProject)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }

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
      setOpen(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    }
  }

  const current = projects.find(project => project.Name === activeProject)
  return (
    <div className="project-switcher" ref={rootRef}>
      <button
        className="project-switcher-trigger"
        type="button"
        aria-expanded={open}
        aria-haspopup="menu"
        onClick={() => setOpen(current => !current)}
      >
        <span className="project-switcher-name">{current?.Name ?? activeProject ?? 'Loading projects'}</span>
        <ChevronDown aria-hidden="true" size={15} strokeWidth={1.8} />
      </button>
      {open && (
        <div className="project-switcher-menu" role="menu">
          <div className="project-switcher-list">
            {projects.map(project => (
              <button
                key={project.ID}
                className="project-switcher-option"
                type="button"
                role="menuitem"
                onClick={() => { onProjectChange(project.Name); setOpen(false) }}
              >
                <span>
                  <strong>{project.Name}</strong>
                  {project.Description && <small>{project.Description}</small>}
                </span>
                {project.Name === activeProject && <Check aria-label="当前项目" size={15} />}
              </button>
            ))}
          </div>
          <button className="project-switcher-create" type="button" onClick={() => setCreating(current => !current)}>
            <FolderPlus aria-hidden="true" size={15} />
            <span>New project</span>
          </button>
          {creating && (
            <form className="project-create-form" onSubmit={submit}>
              <input value={name} onChange={event => setName(event.target.value)} placeholder="project-name" maxLength={63} aria-label="项目名称" autoFocus />
              <input value={description} onChange={event => setDescription(event.target.value)} placeholder="Description (optional)" maxLength={1024} aria-label="项目说明" />
              <button type="submit"><Plus aria-hidden="true" size={15} />Create</button>
            </form>
          )}
          {error && <p className="project-switcher-error">{error}</p>}
        </div>
      )}
    </div>
  )
}
