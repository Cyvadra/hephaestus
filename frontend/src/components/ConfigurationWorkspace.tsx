import { AlertCircle, Check, Database, ListTree, LoaderCircle, RotateCcw, Save, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { createConfiguration, deleteConfiguration, getConfiguration, getConfigurationCatalog, replaceConfiguration } from '../api/client'
import type { Configuration, ConfigurationByKind, ConfigurationCatalog, ConfigurationKind } from '../api/types'
import ConfigurationForm from './configuration/ConfigurationForm'
import { CONFIGURATION_META, createEmptyConfiguration, getConfigurationMeta, validateConfiguration } from './configuration/model'
import { JobRunsPanel, WorkflowRunTester } from './configuration/RunTester'
import type { ConfigurationLists } from './ConfigurationSidebar'

interface Props {
  kind: ConfigurationKind | null
  name: string | null
  isNew: boolean
  lists: ConfigurationLists
  selectionKey: string
  refreshKey: number
  onDirtyChange: (dirty: boolean) => void
  onCreate: (kind: ConfigurationKind) => void
  onSaved: (kind: ConfigurationKind, name: string) => void
  onDeleted: () => void
  onOpenNavigation: () => void
}

export default function ConfigurationWorkspace({ kind, name, isNew, lists, selectionKey, refreshKey, onDirtyChange, onCreate, onSaved, onDeleted, onOpenNavigation }: Props) {
  const [value, setValue] = useState<Configuration | null>(null)
  const [valueSelectionKey, setValueSelectionKey] = useState('')
  const [baseline, setBaseline] = useState('')
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteName, setDeleteName] = useState('')
  const [notice, setNotice] = useState('')
  const [catalog, setCatalog] = useState<ConfigurationCatalog>({ identities: [], impressions: [], tool_groups: [], concierges: [], workflows: [], jobs: [], tools: [], plugins: [] })

  useEffect(() => {
    let active = true
    void getConfigurationCatalog().then(result => {
      if (active) setCatalog(result)
    }).catch(() => undefined)
    return () => { active = false }
  }, [refreshKey])

  useEffect(() => {
    if (kind == null) { setValue(null); setValueSelectionKey(''); setBaseline(''); return }
    setError('')
    setNotice('')
    if (isNew) {
      const empty = createEmptyConfiguration(kind) as Configuration
      setValue(empty)
      setValueSelectionKey(selectionKey)
      setBaseline(JSON.stringify(empty))
      return
    }
    if (name == null) return
    let active = true
    setLoading(true)
    void getConfiguration(kind, name).then(result => {
      if (!active) return
      setValue(result)
      setValueSelectionKey(selectionKey)
      setBaseline(JSON.stringify(result))
    }).catch(reason => { if (active) setError(reason instanceof Error ? reason.message : '加载配置失败') }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [kind, name, isNew, selectionKey])

  const currentValue = valueSelectionKey === selectionKey ? value : null
  const serialized = currentValue == null ? '' : JSON.stringify(currentValue)
  const dirty = currentValue != null && serialized !== baseline
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange])

  if (kind == null) return <ConfigurationOverview lists={lists} onCreate={onCreate} onOpenNavigation={onOpenNavigation} />
  const meta = getConfigurationMeta(kind)
  const errors = currentValue == null ? {} : validateConfiguration(currentValue)

  const save = async () => {
    if (currentValue == null || Object.keys(errors).length > 0) return
    setSubmitting(true)
    setError('')
    try {
      const saved = isNew ? await createConfiguration(kind, currentValue as never) : await replaceConfiguration(kind, name ?? currentValue.name, currentValue as never)
      setValue(saved)
      setValueSelectionKey(selectionKey)
      setBaseline(JSON.stringify(saved))
      setNotice('已保存，新请求和下一轮对话立即生效')
      onSaved(kind, saved.name)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '保存失败')
    } finally {
      setSubmitting(false)
    }
  }

  const remove = async () => {
    if (name == null || deleteName !== name) return
    setSubmitting(true)
    setError('')
    try {
      await deleteConfiguration(kind, name)
      setDeleteOpen(false)
      onDeleted()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '删除失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="configuration-workspace">
      <header className="configuration-workspace-header">
        <button className="configuration-mobile-nav" type="button" onClick={onOpenNavigation}><ListTree size={16} />配置列表</button>
        <div><span className="configuration-eyebrow"><Database size={14} />数据库配置 · {meta.singular}</span><h1>{isNew ? `新建${meta.label}` : currentValue?.name ?? name}</h1><p>{meta.description}</p></div>
        <div className="configuration-restart-note"><AlertCircle size={15} /><span>更改对新请求和下一轮对话立即生效</span></div>
      </header>
      <div className="configuration-workspace-scroll">
        {error && <div className="configuration-alert error" role="alert"><AlertCircle size={16} /><span>{error}</span></div>}
        {notice && <div className="configuration-alert success" role="status"><Check size={16} /><span>{notice}</span></div>}
        {loading || currentValue == null ? <div className="configuration-detail-loading"><LoaderCircle className="spin" size={22} />读取配置...</div> : <><form id="configuration-form" onSubmit={event => { event.preventDefault(); void save() }}><ConfigurationForm kind={kind} value={currentValue} errors={errors} isNew={isNew} catalog={catalog} onChange={setValue} /></form>{kind === 'workflows' && name != null && !isNew && <WorkflowRunTester workflowName={currentValue.name} inputSchema={(currentValue as ConfigurationByKind['workflows']).input_schema} />}{kind === 'jobs' && name != null && !isNew && <JobRunsPanel jobName={currentValue.name} />}</>}
      </div>
      {currentValue && !loading && <footer className="configuration-action-bar">
        <div>{!isNew && <button className="danger-quiet" type="button" onClick={() => { setDeleteName(''); setDeleteOpen(true) }}><Trash2 size={15} />删除</button>}</div>
        <div><button type="button" disabled={!dirty || submitting} onClick={() => setValue(JSON.parse(baseline) as Configuration)}><RotateCcw size={15} />撤销</button><button className="primary" form="configuration-form" type="submit" disabled={!dirty || submitting || Object.keys(errors).length > 0}>{submitting ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{isNew ? '创建配置' : '保存更改'}</button></div>
      </footer>}
      {deleteOpen && <div className="configuration-dialog-backdrop" role="presentation"><div className="configuration-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-configuration-title"><h2 id="delete-configuration-title">删除数据库配置？</h2><p>删除 <strong>{name}</strong> 后会立即失效；若存在同名默认模板，下次启动时将恢复。请输入名称确认。</p>{error && <div className="configuration-dialog-error">{error}</div>}<input autoFocus value={deleteName} onChange={event => setDeleteName(event.target.value)} placeholder={name ?? ''} /><div><button type="button" onClick={() => setDeleteOpen(false)}>取消</button><button className="danger" type="button" disabled={deleteName !== name || submitting} onClick={() => void remove()}>确认删除</button></div></div></div>}
    </div>
  )
}

function ConfigurationOverview({ lists, onCreate, onOpenNavigation }: { lists: ConfigurationLists; onCreate: (kind: ConfigurationKind) => void; onOpenNavigation: () => void }) {
  return <div className="configuration-overview"><button className="configuration-mobile-nav" type="button" onClick={onOpenNavigation}><ListTree size={16} />配置列表</button><div className="configuration-overview-heading"><span>Registry console</span><h1>数据库配置</h1><p>从左侧选择配置进行查看和编辑，或在任一分组中新建记录。</p></div><div className="configuration-overview-grid">{CONFIGURATION_META.map(meta => <button className="configuration-overview-item" type="button" key={meta.kind} onClick={() => onCreate(meta.kind)}><strong>{meta.label}</strong><span>{meta.description}</span><small>{lists[meta.kind]?.length ?? 0} 条数据库记录</small></button>)}</div></div>
}
