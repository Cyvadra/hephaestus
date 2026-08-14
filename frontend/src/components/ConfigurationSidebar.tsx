import { Bot, Braces, BriefcaseBusiness, ChevronDown, ChevronLeft, CircleUserRound, Plus, Search, Sparkles, Workflow, Wrench } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { listConfigurations } from '../api/client'
import type { Configuration, ConfigurationKind } from '../api/types'
import { CONFIGURATION_META, configurationSummary, type TranslationDescriptor } from './configuration/model'

export type ConfigurationLists = Partial<Record<ConfigurationKind, Configuration[]>>

interface Props {
  activeKind: ConfigurationKind | null
  activeName: string | null
  refreshKey: number
  onBack: () => void
  onSelect: (kind: ConfigurationKind, name: string) => void
  onCreate: (kind: ConfigurationKind) => void
  onOpenConstants: () => void
  onListsChange: (lists: ConfigurationLists) => void
}

const ICONS = {
  identities: CircleUserRound,
  impressions: Sparkles,
  'tool-groups': Wrench,
  concierges: Bot,
  workflows: Workflow,
  jobs: BriefcaseBusiness,
  constants: Braces,
}

export default function ConfigurationSidebar({ activeKind, activeName, refreshKey, onBack, onSelect, onCreate, onOpenConstants, onListsChange }: Props) {
  const { t } = useTranslation()
  const [lists, setLists] = useState<ConfigurationLists>({})
  const [errors, setErrors] = useState<Partial<Record<ConfigurationKind, string>>>({})
  const [loading, setLoading] = useState<ConfigurationKind[]>([])
  const [collapsed, setCollapsed] = useState<ConfigurationKind[]>(() => CONFIGURATION_META.map(item => item.kind))
  const [query, setQuery] = useState('')

  useEffect(() => onListsChange(lists), [lists, onListsChange])

  const load = useCallback(async (kinds = CONFIGURATION_META.map(item => item.kind)) => {
    setLoading(current => [...new Set([...current, ...kinds])])
    const results = await Promise.allSettled(kinds.map(async kind => ({ kind, values: await listConfigurations(kind) as Configuration[] })))
    setLists(current => {
      const next = { ...current }
      for (const result of results) if (result.status === 'fulfilled') next[result.value.kind] = result.value.values
      return next
    })
    setErrors(current => {
      const next = { ...current }
      results.forEach((result, index) => {
        const kind = kinds[index]
        if (result.status === 'fulfilled') delete next[kind]
        else next[kind] = result.reason instanceof Error ? result.reason.message : t('configuration.loadFailed')
      })
      return next
    })
    setLoading(current => current.filter(kind => !kinds.includes(kind)))
  }, [t])

  useEffect(() => { void load() }, [load, refreshKey])

  return (
    <>
      <div className="configuration-sidebar-header">
        <button type="button" aria-label={t('app.returnToChat')} title={t('app.returnToChat')} onClick={onBack}>
          <ChevronLeft size={17} />
          <div><strong>{t('app.configurationManagement')}</strong><span>{t('configuration.registryConsole')}</span></div>
        </button>
      </div>
      <div className="configuration-search"><Search aria-hidden="true" size={15} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder={t('configuration.search')} /></div>
      <div className="configuration-sidebar-list">
        {CONFIGURATION_META.map(meta => {
          const Icon = ICONS[meta.kind]
          const values = lists[meta.kind] ?? []
          const label = t(`${meta.translationKey}.label`)
          const visible = values.filter(value => `${value.name} ${renderDescriptor(t, configurationSummary(meta.kind, value))}`.toLowerCase().includes(query.trim().toLowerCase()))
          const isCollapsed = collapsed.includes(meta.kind) && !(query.trim() && visible.length > 0)
          if (meta.kind === 'constants') return (
            <section className="configuration-sidebar-group" key={meta.kind}>
              <div className="configuration-group-heading">
                <button className={`configuration-group-toggle${activeKind === 'constants' && activeName == null ? ' active' : ''}`} type="button" onClick={onOpenConstants}>
                  <ChevronDown className="configuration-group-entry-icon" size={13} aria-hidden="true" /><Icon size={15} /><span>{label}</span><small>{values.length}</small>
                </button>
                <span className="configuration-group-trailing" aria-hidden="true" />
              </div>
            </section>
          )
          return (
            <section className="configuration-sidebar-group" key={meta.kind}>
              <div className="configuration-group-heading">
                <button className="configuration-group-toggle" type="button" aria-expanded={!isCollapsed} onClick={() => setCollapsed(current => current.includes(meta.kind) ? current.filter(kind => kind !== meta.kind) : [...current, meta.kind])}>
                  <ChevronDown size={13} /><Icon size={15} /><span>{label}</span><small>{values.length}</small>
                </button>
                <button className="configuration-group-create configuration-group-trailing" type="button" aria-label={t('configuration.create', { name: label })} title={t('configuration.create', { name: label })} onClick={() => onCreate(meta.kind)}><Plus size={14} /></button>
              </div>
              {!isCollapsed && <div className="configuration-group-items">
                {loading.includes(meta.kind) && values.length === 0 ? <div className="configuration-list-skeleton"><i /><i /></div> : errors[meta.kind] ? <button className="configuration-list-error" type="button" onClick={() => void load([meta.kind])}>{t('configuration.retryLoading')}</button> : visible.length === 0 ? <button className="configuration-list-empty" type="button" onClick={() => onCreate(meta.kind)}>{query ? t('configuration.noMatches') : t('configuration.createFirst', { name: label })}</button> : visible.map(value => (
                  <button className={`configuration-list-item${activeKind === meta.kind && activeName === value.name ? ' active' : ''}`} type="button" key={value.name} onClick={() => onSelect(meta.kind, value.name)}>
                    <strong>{value.name}</strong><span>{renderDescriptor(t, configurationSummary(meta.kind, value))}</span>
                  </button>
                ))}
              </div>}
            </section>
          )
        })}
      </div>
    </>
  )
}

function renderDescriptor(t: ReturnType<typeof useTranslation>['t'], descriptor: TranslationDescriptor | null) {
  if (descriptor == null) return ''
  if (descriptor.text != null) return descriptor.text
  const values = descriptor.values?.identity === '__not_configured__'
    ? { ...descriptor.values, identity: t('configuration.summary.notConfigured') }
    : descriptor.values
  return descriptor.key == null ? '' : t(descriptor.key, values)
}
