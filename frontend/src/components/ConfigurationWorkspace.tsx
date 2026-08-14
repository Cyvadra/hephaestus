import { AlertCircle, Check, Database, ListTree, LoaderCircle, RotateCcw, Save, Trash2, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createConfiguration, deleteConfiguration, getConfiguration, getConfigurationCatalog, replaceConfiguration } from '../api/client'
import type { Configuration, ConfigurationByKind, ConfigurationCatalog, ConfigurationKind } from '../api/types'
import ConfigurationForm from './configuration/ConfigurationForm'
import { CONFIGURATION_META, createEmptyConfiguration, findUndefinedPromptVariables, getConfigurationMeta, type TranslationDescriptor, validateConfiguration } from './configuration/model'
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
  const [notification, setNotification] = useState<{ type: 'error' | 'success'; message: string } | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteName, setDeleteName] = useState('')
  const [undefinedVariables, setUndefinedVariables] = useState<string[] | null>(null)
  const [variableDefaults, setVariableDefaults] = useState<Record<string, string>>({})
  const [catalog, setCatalog] = useState<ConfigurationCatalog>({ identities: [], impressions: [], tool_groups: [], concierges: [], workflows: [], jobs: [], constants: [], tools: [], plugins: [], plugin_descriptions: {} })

  const notify = (type: 'error' | 'success', message: string) => setNotification({ type, message })

  useEffect(() => {
    if (notification == null) return
    const timeout = window.setTimeout(() => setNotification(null), 3000)
    return () => window.clearTimeout(timeout)
  }, [notification])

  useEffect(() => {
    let active = true
    void getConfigurationCatalog().then(result => {
      if (active) setCatalog(result)
    }).catch(() => undefined)
    return () => { active = false }
  }, [refreshKey])

  useEffect(() => {
    if (kind == null) { setValue(null); setValueSelectionKey(''); setBaseline(''); return }
    setNotification(null)
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
    }).catch(reason => { if (active) notify('error', reason instanceof Error ? reason.message : t('configuration.loadFailed')) }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [kind, name, isNew, selectionKey, t])

  const currentValue = valueSelectionKey === selectionKey ? value : null
  const serialized = currentValue == null ? '' : JSON.stringify(currentValue)
  const dirty = currentValue != null && serialized !== baseline
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange])

  if (kind == null) return <ConfigurationOverview lists={lists} onCreate={onCreate} onOpenNavigation={onOpenNavigation} />
  const meta = getConfigurationMeta(kind)
  const validationDescriptors = currentValue == null ? {} : validateConfiguration(kind, currentValue, catalog.constants)
  const errors = Object.fromEntries(Object.entries(validationDescriptors).map(([field, descriptor]) => [field, renderDescriptor(t, descriptor)]))

  const persist = async () => {
    if (currentValue == null) return
    setSubmitting(true)
    try {
      const saved = isNew ? await createConfiguration(kind, currentValue as never) : await replaceConfiguration(kind, name ?? currentValue.name, currentValue as never)
      setValue(saved)
      setValueSelectionKey(selectionKey)
      setBaseline(JSON.stringify(saved))
      notify('success', t('configuration.saved'))
      onDirtyChange(false)
      onSaved(kind, saved.name)
    } catch (reason) {
      notify('error', reason instanceof Error ? reason.message : t('configuration.saveFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  const save = async () => {
    if (currentValue == null || Object.keys(errors).length > 0) return
    if (kind !== 'identities') {
      await persist()
      return
    }
    setSubmitting(true)
    try {
      const refreshedCatalog = await getConfigurationCatalog()
      setCatalog(refreshedCatalog)
      const identity = currentValue as ConfigurationByKind['identities']
      const missing = findUndefinedPromptVariables(
        [identity.system_prompt, ...identity.injected_messages.map(message => message.content)],
        refreshedCatalog.constants,
      )
      if (missing.length > 0) {
        setVariableDefaults(Object.fromEntries(missing.map(variable => [variable, ''])))
        setUndefinedVariables(missing)
        return
      }
    } catch (reason) {
      notify('error', reason instanceof Error ? reason.message : t('configuration.saveFailed'))
      return
    } finally {
      setSubmitting(false)
    }
    await persist()
  }

  const createVariablesAndSave = async () => {
    if (undefinedVariables == null) return
    setSubmitting(true)
    try {
      const latestCatalog = await getConfigurationCatalog()
      const missing = undefinedVariables.filter(name => !latestCatalog.constants.includes(name))
      await Promise.all(missing.map(name => createConfiguration('constants', { name, value: variableDefaults[name] ?? '' })))
      setUndefinedVariables(null)
      const refreshedCatalog = await getConfigurationCatalog()
      setCatalog(refreshedCatalog)
      await persist()
    } catch (reason) {
      notify('error', reason instanceof Error ? reason.message : t('configuration.saveFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  const remove = async () => {
    if (name == null || deleteName !== name) return
    setSubmitting(true)
    try {
      await deleteConfiguration(kind, name)
      setDeleteOpen(false)
      onDeleted()
    } catch (reason) {
      notify('error', reason instanceof Error ? reason.message : t('configuration.deleteFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="configuration-workspace">
      <header className="configuration-workspace-header">
        <button className="configuration-mobile-nav" type="button" onClick={onOpenNavigation}><ListTree size={16} />{t('configuration.list')}</button>
        <div><span className="configuration-eyebrow"><Database size={14} />{t('configuration.databaseConfiguration')} · {t(`${meta.translationKey}.singular`)}</span><h1>{isNew ? t('configuration.createNamed', { name: t(`${meta.translationKey}.label`) }) : currentValue?.name ?? name}</h1><p>{t(`${meta.translationKey}.description`)}</p></div>
      </header>
      <div className="configuration-workspace-scroll">
        {loading || currentValue == null ? <div className="configuration-detail-loading"><LoaderCircle className="spin" size={22} />{t('configuration.loading')}</div> : <><form id="configuration-form" onSubmit={event => { event.preventDefault(); void save() }}><ConfigurationForm kind={kind} value={currentValue} errors={errors} isNew={isNew} catalog={catalog} onChange={setValue} onNotify={notify} /></form>{kind === 'workflows' && name != null && !isNew && <WorkflowRunTester workflowName={currentValue.name} inputSchema={(currentValue as ConfigurationByKind['workflows']).input_schema} />}{kind === 'jobs' && name != null && !isNew && <JobRunsPanel jobName={currentValue.name} />}</>}
      </div>
      {currentValue && !loading && <footer className="configuration-action-bar">
        <div>{!isNew && <button className="danger-quiet" type="button" onClick={() => { setDeleteName(''); setDeleteOpen(true) }}><Trash2 size={15} />{t('common.delete')}</button>}</div>
        <div><button type="button" disabled={!dirty || submitting} onClick={() => setValue(JSON.parse(baseline) as Configuration)}><RotateCcw size={15} />{t('common.reset')}</button><button className="primary" form="configuration-form" type="submit" disabled={!dirty || submitting || Object.keys(errors).length > 0}>{submitting ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{isNew ? t('common.create') : t('common.saveChanges')}</button></div>
      </footer>}
      {notification && <div className={`configuration-toast ${notification.type}`} role={notification.type === 'error' ? 'alert' : 'status'}><span>{notification.type === 'error' ? <AlertCircle size={16} /> : <Check size={16} />}</span><p>{notification.message}</p><button type="button" aria-label={t('common.close')} onClick={() => setNotification(null)}><X size={15} /></button></div>}
      {deleteOpen && <div className="configuration-dialog-backdrop" role="presentation"><div className="configuration-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-configuration-title"><h2 id="delete-configuration-title">{t('configuration.deleteTitle')}</h2><p>{t('configuration.deleteBody', { name })}</p><input autoFocus value={deleteName} onChange={event => setDeleteName(event.target.value)} placeholder={name ?? ''} /><div><button type="button" onClick={() => setDeleteOpen(false)}>{t('common.cancel')}</button><button className="danger" type="button" disabled={deleteName !== name || submitting} onClick={() => void remove()}>{t('configuration.confirmDelete')}</button></div></div></div>}
      {undefinedVariables && <div className="configuration-dialog-backdrop" role="presentation"><div className="configuration-dialog" role="dialog" aria-modal="true" aria-labelledby="undefined-variables-title"><h2 id="undefined-variables-title">{t('configuration.undefinedVariablesTitle')}</h2><p>{t('configuration.undefinedVariablesBody')}</p>{undefinedVariables.map((variable, index) => <label className="configuration-variable-default" key={variable}><span>{`{{${variable}}}`}</span><input autoFocus={index === 0} value={variableDefaults[variable] ?? ''} onChange={event => setVariableDefaults(defaults => ({ ...defaults, [variable]: event.target.value }))} aria-label={t('configuration.variableDefaultValue', { name: variable })} /></label>)}<div><button type="button" disabled={submitting} onClick={() => setUndefinedVariables(null)}>{t('common.cancel')}</button><button className="primary" type="button" disabled={submitting} onClick={() => void createVariablesAndSave()}>{submitting ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{t('configuration.createVariablesAndSave')}</button></div></div></div>}
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
