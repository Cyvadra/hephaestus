import { Bot, BriefcaseBusiness, ChevronDown, ChevronLeft, CircleUserRound, Plus, Search, Sparkles, Workflow, Wrench } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { listConfigurations } from '../api/client'
import type { Configuration, ConfigurationKind } from '../api/types'
import { CONFIGURATION_META, configurationSummary } from './configuration/model'

export type ConfigurationLists = Partial<Record<ConfigurationKind, Configuration[]>>

interface Props {
  activeKind: ConfigurationKind | null
  activeName: string | null
  refreshKey: number
  onBack: () => void
  onSelect: (kind: ConfigurationKind, name: string) => void
  onCreate: (kind: ConfigurationKind) => void
  onListsChange: (lists: ConfigurationLists) => void
}

const ICONS = {
  identities: CircleUserRound,
  impressions: Sparkles,
  'tool-groups': Wrench,
  concierges: Bot,
  workflows: Workflow,
  jobs: BriefcaseBusiness,
}

export default function ConfigurationSidebar({ activeKind, activeName, refreshKey, onBack, onSelect, onCreate, onListsChange }: Props) {
  const [lists, setLists] = useState<ConfigurationLists>({})
  const [errors, setErrors] = useState<Partial<Record<ConfigurationKind, string>>>({})
  const [loading, setLoading] = useState<ConfigurationKind[]>([])
  const [collapsed, setCollapsed] = useState<ConfigurationKind[]>([])
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
        else next[kind] = result.reason instanceof Error ? result.reason.message : '加载失败'
      })
      return next
    })
    setLoading(current => current.filter(kind => !kinds.includes(kind)))
  }, [])

  useEffect(() => { void load() }, [load, refreshKey])

  return (
    <>
      <div className="configuration-sidebar-header">
        <button type="button" aria-label="返回聊天" title="返回聊天" onClick={onBack}><ChevronLeft size={17} /></button>
        <div><strong>配置管理</strong><span>Database registry</span></div>
      </div>
      <div className="configuration-search"><Search aria-hidden="true" size={15} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索数据库配置" /></div>
      <div className="configuration-sidebar-list">
        {CONFIGURATION_META.map(meta => {
          const Icon = ICONS[meta.kind]
          const values = lists[meta.kind] ?? []
          const visible = values.filter(value => `${value.name} ${configurationSummary(meta.kind, value)}`.toLowerCase().includes(query.trim().toLowerCase()))
          const isCollapsed = collapsed.includes(meta.kind)
          return (
            <section className="configuration-sidebar-group" key={meta.kind}>
              <div className="configuration-group-heading">
                <button className="configuration-group-toggle" type="button" aria-expanded={!isCollapsed} onClick={() => setCollapsed(current => current.includes(meta.kind) ? current.filter(kind => kind !== meta.kind) : [...current, meta.kind])}>
                  <ChevronDown size={13} /><Icon size={15} /><span>{meta.label}</span><small>{values.length}</small>
                </button>
                <button type="button" aria-label={`新建${meta.label}`} title={`新建${meta.label}`} onClick={() => onCreate(meta.kind)}><Plus size={14} /></button>
              </div>
              {!isCollapsed && <div className="configuration-group-items">
                {loading.includes(meta.kind) && values.length === 0 ? <div className="configuration-list-skeleton"><i /><i /></div> : errors[meta.kind] ? <button className="configuration-list-error" type="button" onClick={() => void load([meta.kind])}>加载失败，点击重试</button> : visible.length === 0 ? <button className="configuration-list-empty" type="button" onClick={() => onCreate(meta.kind)}>{query ? '无匹配项' : `新建第一个${meta.label}`}</button> : visible.map(value => (
                  <button className={`configuration-list-item${activeKind === meta.kind && activeName === value.name ? ' active' : ''}`} type="button" key={value.name} onClick={() => onSelect(meta.kind, value.name)}>
                    <strong>{value.name}</strong><span>{configurationSummary(meta.kind, value)}</span>
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
