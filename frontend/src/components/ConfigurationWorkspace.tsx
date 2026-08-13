import { AlertCircle, Check, Database, ListTree, LoaderCircle, RotateCcw, Save, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createConfiguration, deleteConfiguration, getConfiguration, getConfigurationCatalog, replaceConfiguration } from '../api/client'
import type { Configuration, ConfigurationByKind, ConfigurationCatalog, ConfigurationKind } from '../api/types'
import ConfigurationForm from './configuration/ConfigurationForm'
import { CONFIGURATION_META, createEmptyConfiguration, getConfigurationMeta, type TranslationDescriptor, validateConfiguration } from './configuration/model'
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
  const { t } = useTranslation()
  const [value, setValue] = useState<Configuration | null>(null)
  const [valueSelectionKey, setValueSelectionKey] = useState('')
  const [baseline, setBaseline] = useState('')
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteName, setDeleteName] = useState('')
  const [notice, setNotice] = useState('')
  const [catalog, setCatalog] = useState<ConfigurationCatalog>({ identities: [], impressions: [], tool_groups: [], concierges: [], workflows: [], jobs: [], tools: [], plugins: [], plugin_descriptions: {} })

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
    }).catch(reason => { if (active) setError(reason instanceof Error ? reason.message : t('configuration.loadFailed')) }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [kind, name, isNew, selectionKey, t])

  const currentValue = valueSelectionKey === selectionKey ? value : null
  const serialized = currentValue == null ? '' : JSON.stringify(currentValue)
  const dirty = currentValue != null && serialized !== baseline
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange])

  if (kind == null) return <ConfigurationOverview lists={lists} onCreate={onCreate} onOpenNavigation={onOpenNavigation} />
  const meta = getConfigurationMeta(kind)
  const validationDescriptors = currentValue == null ? {} : validateConfiguration(currentValue)
  const errors = Object.fromEntries(Object.entries(validationDescriptors).map(([field, descriptor]) => [field, renderDescriptor(t, descriptor)]))

  const save = async () => {
    if (currentValue == null || Object.keys(errors).length > 0) return
    setSubmitting(true)
    setError('')
    try {
      const saved = isNew ? await createConfiguration(kind, currentValue as never) : await replaceConfiguration(kind, name ?? currentValue.name, currentValue as never)
      setValue(saved)
      setValueSelectionKey(selectionKey)
      setBaseline(JSON.stringify(saved))
      setNotice(t('configuration.saved'))
      onDirtyChange(false)
      onSaved(kind, saved.name)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t('configuration.saveFailed'))
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
      setError(reason instanceof Error ? reason.message : t('configuration.deleteFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="configuration-workspace">
      <header className="configuration-workspace-header">
        <button className="configuration-mobile-nav" type="button" onClick={onOpenNavigation}><ListTree size={16} />{t('configuration.list')}</button>
        <div><span className="configuration-eyebrow"><Database size={14} />{t('configuration.databaseConfiguration')} · {t(`${meta.translationKey}.singular`)}</span><h1>{isNew ? t('configuration.createNamed', { name: t(`${meta.translationKey}.label`) }) : currentValue?.name ?? name}</h1><p>{t(`${meta.translationKey}.description`)}</p></div>
        <div className="configuration-restart-note"><AlertCircle size={15} /><span>{t('configuration.changesApplyImmediately')}</span></div>
      </header>
      <div className="configuration-workspace-scroll">
        {error && <div className="configuration-alert error" role="alert"><AlertCircle size={16} /><span>{error}</span></div>}
        {notice && <div className="configuration-alert success" role="status"><Check size={16} /><span>{notice}</span></div>}
        {loading || currentValue == null ? <div className="configuration-detail-loading"><LoaderCircle className="spin" size={22} />{t('configuration.loading')}</div> : <><form id="configuration-form" onSubmit={event => { event.preventDefault(); void save() }}><ConfigurationForm kind={kind} value={currentValue} errors={errors} isNew={isNew} catalog={catalog} onChange={setValue} /></form>{kind === 'workflows' && name != null && !isNew && <WorkflowRunTester workflowName={currentValue.name} inputSchema={(currentValue as ConfigurationByKind['workflows']).input_schema} />}{kind === 'jobs' && name != null && !isNew && <JobRunsPanel jobName={currentValue.name} />}</>}
      </div>
      {currentValue && !loading && <footer className="configuration-action-bar">
        <div>{!isNew && <button className="danger-quiet" type="button" onClick={() => { setDeleteName(''); setDeleteOpen(true) }}><Trash2 size={15} />{t('common.delete')}</button>}</div>
        <div><button type="button" disabled={!dirty || submitting} onClick={() => setValue(JSON.parse(baseline) as Configuration)}><RotateCcw size={15} />{t('common.reset')}</button><button className="primary" form="configuration-form" type="submit" disabled={!dirty || submitting || Object.keys(errors).length > 0}>{submitting ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{isNew ? t('common.create') : t('common.saveChanges')}</button></div>
      </footer>}
      {deleteOpen && <div className="configuration-dialog-backdrop" role="presentation"><div className="configuration-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-configuration-title"><h2 id="delete-configuration-title">{t('configuration.deleteTitle')}</h2><p>{t('configuration.deleteBody', { name })}</p>{error && <div className="configuration-dialog-error">{error}</div>}<input autoFocus value={deleteName} onChange={event => setDeleteName(event.target.value)} placeholder={name ?? ''} /><div><button type="button" onClick={() => setDeleteOpen(false)}>{t('common.cancel')}</button><button className="danger" type="button" disabled={deleteName !== name || submitting} onClick={() => void remove()}>{t('configuration.confirmDelete')}</button></div></div></div>}
    </div>
  )
}

function ConfigurationOverview({ lists, onCreate, onOpenNavigation }: { lists: ConfigurationLists; onCreate: (kind: ConfigurationKind) => void; onOpenNavigation: () => void }) {
  const { t } = useTranslation()
  return <div className="configuration-overview"><button className="configuration-mobile-nav" type="button" onClick={onOpenNavigation}><ListTree size={16} />{t('configuration.list')}</button><div className="configuration-overview-heading"><span>{t('configuration.registryConsole')}</span><h1>{t('configuration.databaseConfiguration')}</h1><p>{t('configuration.overview')}</p></div><div className="configuration-overview-grid">{CONFIGURATION_META.map(meta => <button className="configuration-overview-item" type="button" key={meta.kind} onClick={() => onCreate(meta.kind)}><strong>{t(`${meta.translationKey}.label`)}</strong><span>{t(`${meta.translationKey}.description`)}</span><small>{t('configuration.recordCount', { count: lists[meta.kind]?.length ?? 0 })}</small></button>)}</div></div>
}

function renderDescriptor(t: ReturnType<typeof useTranslation>['t'], descriptor: TranslationDescriptor) {
  return descriptor.key == null ? descriptor.text ?? '' : t(descriptor.key, descriptor.values)
}
