import type {
  Configuration,
  ConfigurationByKind,
  ConfigurationKind,
} from '../../api/types'

export interface ConfigurationKindMeta {
  kind: ConfigurationKind
  label: string
  singular: string
  description: string
}

export const CONFIGURATION_META: ConfigurationKindMeta[] = [
  { kind: 'identities', label: '身份', singular: 'Identity', description: '模型、推理参数与系统提示词' },
  { kind: 'impressions', label: '印象', singular: 'Impression', description: '可复用的上下文消息序列' },
  { kind: 'tool-groups', label: '工具组', singular: 'Tool Group', description: '按用途组织可调用工具' },
  { kind: 'concierges', label: '助理', singular: 'Concierge', description: '组合身份、印象、工具组与插件' },
  { kind: 'workflows', label: '工作流', singular: 'Workflow', description: '定义输入输出与顺序执行步骤' },
  { kind: 'jobs', label: '任务', singular: 'Job', description: '通过触发器调度工作流' },
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
    concierges: { name: '', nickname: '', description: '', identity: '', impressions: [], tool_groups: [], plugins: [] },
    workflows: { name: '', description: '', concierge: '', input_schema: {}, output_schema: {}, steps: [] },
    jobs: { name: '', title: '', description: '', goal: '', workflows: [], trigger: 'false', max_executions_per_day: 1 },
  }
  return structuredClone(values[kind])
}

export function configurationSummary(kind: ConfigurationKind, value: Configuration): string {
  switch (kind) {
    case 'identities': {
      const identity = value as ConfigurationByKind['identities']
      return identity.description || identity.preferred_model || '系统身份'
    }
    case 'impressions': {
      const impression = value as ConfigurationByKind['impressions']
      return impression.description || `${impression.messages.length} 条消息`
    }
    case 'tool-groups':
      return `${(value as ConfigurationByKind['tool-groups']).tools.length} 个工具`
    case 'concierges':
      return `Identity · ${(value as ConfigurationByKind['concierges']).identity || '未设置'}`
    case 'workflows': {
      const workflow = value as ConfigurationByKind['workflows']
      return workflow.description || `${workflow.steps.length} 个步骤`
    }
    case 'jobs': {
      const job = value as ConfigurationByKind['jobs']
      return job.title || job.description || `${job.workflows.length} 个工作流`
    }
  }
}

export function validateConfiguration(value: Configuration): Record<string, string> {
  const errors: Record<string, string> = {}
  if (!value.name.trim()) errors.name = '名称不能为空'

  if ('nickname' in value && Array.from(value.nickname).length > 20) errors.nickname = '昵称不能超过 20 字'
  return errors
}
