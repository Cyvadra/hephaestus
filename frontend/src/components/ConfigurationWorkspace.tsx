import { AlertCircle, Check, ChevronDown, ChevronLeft, LoaderCircle, MessageSquareText, Plus, RotateCcw, Save, Trash2, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { createConfiguration, deleteConfiguration, getConfiguration, getConfigurationCatalog, listConfigurations, replaceConfiguration } from '../api/client'
import type { Configuration, ConfigurationByKind, ConfigurationCatalog, ConfigurationKind, ConstantConfiguration } from '../api/types'
import ConfigurationForm from './configuration/ConfigurationForm'
import { CONFIGURATION_META, createEmptyConfiguration, findUndefinedPromptVariables, getConfigurationMeta, type TranslationDescriptor, validateConfiguration } from './configuration/model'
import { JobRunsPanel, WorkflowRunTester } from './configuration/RunTester'
import type { ConfigurationLists } from './ConfigurationSidebar'

interface Props {
  kind: ConfigurationKind | null
  name: string | null
  isConstantsOverview: boolean
  isNew: boolean
  lists: ConfigurationLists
  selectionKey: string
  refreshKey: number
  onDirtyChange: (dirty: boolean) => void
  onCreate: (kind: ConfigurationKind) => void
  onSelect: (kind: ConfigurationKind, name: string) => void
  onOpenConstants: () => void
  onSaved: (kind: ConfigurationKind, name: string) => void
  onDeleted: () => void
  onReturnToOverview: () => void
  onReturnToChat: () => void
}

export default function ConfigurationWorkspace({ kind, name, isConstantsOverview, isNew, lists, selectionKey, refreshKey, onDirtyChange, onCreate, onSelect, onOpenConstants, onSaved, onDeleted, onReturnToOverview, onReturnToChat }: Props) {
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

  if (kind == null) return <ConfigurationOverview lists={lists} onCreate={onCreate} onSelect={onSelect} onOpenConstants={onOpenConstants} onReturnToChat={onReturnToChat} />
  if (isConstantsOverview) return <ConstantsWorkspace refreshKey={refreshKey} onDirtyChange={onDirtyChange} onSaved={() => { onDirtyChange(false); onSaved('constants', '') }} onReturnToOverview={onReturnToOverview} />
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
        <div><button className="configuration-eyebrow configuration-return" type="button" aria-label={t('app.configurationManagement')} title={t('app.configurationManagement')} onClick={onReturnToOverview}><ChevronLeft size={15} />{t('configuration.databaseConfiguration')} · {t(`${meta.translationKey}.singular`)}</button><h1>{isNew ? t('configuration.createNamed', { name: t(`${meta.translationKey}.label`) }) : currentValue?.name ?? name}</h1><p>{t(`${meta.translationKey}.description`)}</p></div>
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

function ConfigurationOverview({ lists, onCreate, onSelect, onOpenConstants, onReturnToChat }: { lists: ConfigurationLists; onCreate: (kind: ConfigurationKind) => void; onSelect: (kind: ConfigurationKind, name: string) => void; onOpenConstants: () => void; onReturnToChat: () => void }) {
  const { t } = useTranslation()
  const [expandedKind, setExpandedKind] = useState<ConfigurationKind | null>(null)
  return <div className="configuration-overview"><div className="configuration-overview-heading"><span className="configuration-registry-label">{t('configuration.registryConsole')}</span><button className="configuration-eyebrow configuration-return configuration-return-chat" type="button" onClick={onReturnToChat}><ChevronLeft size={15} /><MessageSquareText size={14} />{t('app.returnToChat')}</button><h1>{t('configuration.databaseConfiguration')}</h1><p>{t('configuration.overview')}</p></div><div className="configuration-overview-grid">{CONFIGURATION_META.map(meta => {
    const values = lists[meta.kind] ?? []
    const expanded = expandedKind === meta.kind
    const label = t(`${meta.translationKey}.label`)
    const toggle = () => meta.kind === 'constants' ? onOpenConstants() : setExpandedKind(current => current === meta.kind ? null : meta.kind)
    return <section className={`configuration-overview-item${expanded ? ' expanded' : ''}`} key={meta.kind} role="button" tabIndex={0} aria-expanded={expanded} onClick={toggle} onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); toggle() } }}>
      <button className="configuration-overview-group" type="button" aria-expanded={expanded} onClick={event => { event.stopPropagation(); toggle() }}><span><strong>{label}</strong><small>{t('configuration.recordCount', { count: values.length })}</small></span><ChevronDown size={17} /></button>
      <p>{t(`${meta.translationKey}.description`)}</p>
      {meta.kind !== 'constants' && <button className="configuration-overview-create" type="button" onClick={event => { event.stopPropagation(); onCreate(meta.kind) }}><Plus size={15} />{t('configuration.create', { name: label })}</button>}
      {expanded && meta.kind !== 'constants' && <div className="configuration-overview-records">{values.length === 0 ? <span>{t('configuration.createFirst', { name: label })}</span> : values.map(value => <button type="button" key={value.name} onClick={event => { event.stopPropagation(); onSelect(meta.kind, value.name) }}><strong>{value.name}</strong></button>)}</div>}
    </section>
  })}</div></div>
}

type ConstantDraft = ConstantConfiguration & { id: number }

function ConstantsWorkspace({ refreshKey, onDirtyChange, onSaved, onReturnToOverview }: { refreshKey: number; onDirtyChange: (dirty: boolean) => void; onSaved: () => void; onReturnToOverview: () => void }) {
  const { t } = useTranslation()
  const nextDraftID = useRef(0)
  const createDraft = (constant: ConstantConfiguration): ConstantDraft => ({ ...constant, id: nextDraftID.current++ })
  const [constants, setConstants] = useState<ConstantDraft[]>([])
  const [baseline, setBaseline] = useState<ConstantDraft[]>([])
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [notification, setNotification] = useState<{ type: 'error' | 'success'; message: string } | null>(null)
  const dirty = JSON.stringify(constants) !== JSON.stringify(baseline)

  const reload = useCallback(async () => {
    const values = await listConfigurations('constants')
    const drafts = values.map(constant => ({ ...constant, id: nextDraftID.current++ }))
    setConstants(drafts)
    setBaseline(drafts)
  }, [])

  useEffect(() => {
    let active = true
    setLoading(true)
    void reload().catch(reason => { if (active) setNotification({ type: 'error', message: reason instanceof Error ? reason.message : t('configuration.loadFailed') }) }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [refreshKey, reload, t])

  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange])

  const update = (index: number, patch: Partial<ConstantConfiguration>) => setConstants(current => current.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item))
  const valid = constants.every(item => /^[A-Za-z_][A-Za-z0-9_]*$/.test(item.name)) && new Set(constants.map(item => item.name)).size === constants.length
  const save = async () => {
    if (!valid) return
    setSubmitting(true)
    try {
      const initial = new Map(baseline.map(item => [item.name, item]))
      const current = new Map(constants.map(item => [item.name, item]))
      const deleted = baseline.filter(item => !current.has(item.name))
      const changed = constants.filter(item => initial.get(item.name)?.value !== item.value)
      const results = await Promise.allSettled([
        ...deleted.map(item => deleteConfiguration('constants', item.name)),
        ...changed.map(({ id: _, ...item }) => initial.has(item.name) ? replaceConfiguration('constants', item.name, item) : createConfiguration('constants', item)),
      ])
      const failed = results.filter(result => result.status === 'rejected')
      await reload()
      if (failed.length > 0) {
        const firstReason = failed[0].reason
        const detail = firstReason instanceof Error ? firstReason.message : t('configuration.saveFailed')
        throw new Error(`${failed.length}/${results.length}: ${detail}`)
      }
      setNotification({ type: 'success', message: t('configuration.saved') })
      onSaved()
    } catch (reason) {
      setNotification({ type: 'error', message: reason instanceof Error ? reason.message : t('configuration.saveFailed') })
    } finally {
      setSubmitting(false)
    }
  }

  return <div className="configuration-workspace">
    <header className="configuration-workspace-header"><div><button className="configuration-eyebrow configuration-return" type="button" aria-label={t('app.configurationManagement')} title={t('app.configurationManagement')} onClick={onReturnToOverview}><ChevronLeft size={15} />{t('configuration.databaseConfiguration')}</button><h1>{t('configuration.kinds.constants.label')}</h1><p>{t('configuration.kinds.constants.description')}</p></div></header>
    <div className="configuration-workspace-scroll"><div className="configuration-constants-editor">{loading ? <div className="configuration-detail-loading"><LoaderCircle className="spin" size={22} />{t('configuration.loading')}</div> : <>{constants.map((item, index) => <div className="configuration-constant-row" key={item.id}><input aria-label={t('configuration.form.name')} value={item.name} placeholder="variable_name" onChange={event => update(index, { name: event.target.value })} /><input aria-label={t('configuration.form.value')} value={item.value} placeholder={t('configuration.form.value')} onChange={event => update(index, { value: event.target.value })} /><button type="button" aria-label={t('common.delete')} title={t('common.delete')} onClick={() => setConstants(current => current.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={15} /></button></div>)}<button className="configuration-add-row" type="button" onClick={() => setConstants(current => [...current, createDraft({ name: '', value: '' })])}><Plus size={15} />{t('common.create')}</button>{!valid && <p className="configuration-constants-error">{t('configuration.validation.constantNameInvalid')}</p>}</>}</div></div>
    {!loading && <footer className="configuration-action-bar"><div /><div><button type="button" disabled={!dirty || submitting} onClick={() => setConstants(baseline)}><RotateCcw size={15} />{t('common.reset')}</button><button className="primary" type="button" disabled={!dirty || submitting || !valid} onClick={() => void save()}>{submitting ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{t('common.saveChanges')}</button></div></footer>}
    {notification && <div className={`configuration-toast ${notification.type}`} role={notification.type === 'error' ? 'alert' : 'status'}><span>{notification.type === 'error' ? <AlertCircle size={16} /> : <Check size={16} />}</span><p>{notification.message}</p><button type="button" aria-label={t('common.close')} onClick={() => setNotification(null)}><X size={15} /></button></div>}
  </div>
}

function renderDescriptor(t: ReturnType<typeof useTranslation>['t'], descriptor: TranslationDescriptor) {
  return descriptor.key == null ? descriptor.text ?? '' : t(descriptor.key, descriptor.values)
}
