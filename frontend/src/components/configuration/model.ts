import type {
  Configuration,
  ConfigurationByKind,
  ConfigurationKind,
} from '../../api/types'

export interface ConfigurationKindMeta {
  kind: ConfigurationKind
  translationKey: string
}

export const CONFIGURATION_META: ConfigurationKindMeta[] = [
  { kind: 'identities', translationKey: 'configuration.kinds.identities' },
  { kind: 'impressions', translationKey: 'configuration.kinds.impressions' },
  { kind: 'tool-groups', translationKey: 'configuration.kinds.toolGroups' },
  { kind: 'concierges', translationKey: 'configuration.kinds.concierges' },
  { kind: 'workflows', translationKey: 'configuration.kinds.workflows' },
  { kind: 'jobs', translationKey: 'configuration.kinds.jobs' },
]

export const getConfigurationMeta = (kind: ConfigurationKind) =>
  CONFIGURATION_META.find(item => item.kind === kind) ?? CONFIGURATION_META[0]

export function createEmptyConfiguration<K extends ConfigurationKind>(kind: K): ConfigurationByKind[K] {
  const values: ConfigurationByKind = {
    identities: {
      name: '', description: '', preferred_model: '', reasoning_effort: 'none',
      context_window_tokens: 0, max_tokens: 0, temperature: null, top_p: null,
      system_prompt: '', injected_messages: [],
    },
    impressions: { name: '', description: '', enabled: true, messages: [] },
    'tool-groups': { name: '', tools: [] },
    concierges: { name: '', nickname: '', description: '', identity: '', impressions: [], tool_groups: [], plugins: [], available_projects: [] },
    workflows: { name: '', description: '', concierge: '', input_schema: {}, output_schema: {}, steps: [] },
    jobs: { name: '', title: '', description: '', goal: '', workflows: [], trigger: 'false', max_executions_per_day: 1 },
  }
  return structuredClone(values[kind])
}

export interface TranslationDescriptor {
  key?: string
  text?: string
  values?: Record<string, string | number>
}

export function configurationSummary(kind: ConfigurationKind, value: Configuration): TranslationDescriptor | null {
  switch (kind) {
    case 'identities': {
      const identity = value as ConfigurationByKind['identities']
      return identity.description || identity.preferred_model ? { text: identity.description || identity.preferred_model } : { key: 'configuration.summary.systemIdentity' }
    }
    case 'impressions': {
      const impression = value as ConfigurationByKind['impressions']
      return impression.description ? { text: impression.description } : { key: 'configuration.summary.messages', values: { count: impression.messages.length } }
    }
    case 'tool-groups':
      return { key: 'configuration.summary.tools', values: { count: (value as ConfigurationByKind['tool-groups']).tools.length } }
    case 'concierges':
      return { key: 'configuration.summary.concierge', values: { identity: (value as ConfigurationByKind['concierges']).identity || '__not_configured__' } }
    case 'workflows': {
      const workflow = value as ConfigurationByKind['workflows']
      return workflow.description ? { text: workflow.description } : { key: 'configuration.summary.steps', values: { count: workflow.steps.length } }
    }
    case 'jobs': {
      const job = value as ConfigurationByKind['jobs']
      return job.title || job.description ? { text: job.title || job.description } : { key: 'configuration.summary.workflows', values: { count: job.workflows.length } }
    }
  }
}

export function validateConfiguration(value: Configuration): Record<string, TranslationDescriptor> {
  const errors: Record<string, TranslationDescriptor> = {}
  if (!value.name.trim()) errors.name = { key: 'configuration.validation.nameRequired' }

  if ('nickname' in value && Array.from(value.nickname).length > 20) errors.nickname = { key: 'configuration.validation.nicknameTooLong' }
  return errors
}
