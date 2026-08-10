import { Plus, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import type {
  Configuration,
  ConfigurationByKind,
  ConfigurationCatalog,
  ConfigurationKind,
  ConfigurationMessage,
  JobWorkflowBinding,
} from '../../api/types'
import MarkdownEditor from './MarkdownEditor'
import { Field, NumberInput, Section, StringListEditor, SuggestionInput, TagsInput, TextArea, TextInput, Toggle } from './fields'

interface Props {
  kind: ConfigurationKind
  value: Configuration
  errors: Record<string, string>
  isNew: boolean
  catalog?: ConfigurationCatalog
  onChange: (value: Configuration) => void
}

const EMPTY_CATALOG: ConfigurationCatalog = { identities: [], impressions: [], tool_groups: [], concierges: [], workflows: [], jobs: [], tools: [], plugins: [] }

export default function ConfigurationForm({ kind, value, errors, isNew, catalog = EMPTY_CATALOG, onChange }: Props) {
  const set = <T extends Configuration>(next: T) => onChange(next)
  const name = (
    <Field label="名称" htmlFor="configuration-name" error={errors.name} hint={isNew ? '保存后名称不可修改' : '名称用于稳定引用，编辑时不可修改'}>
      <TextInput id="configuration-name" value={value.name} disabled={!isNew} onChange={nameValue => set({ ...value, name: nameValue })} placeholder="lowercase-name" />
    </Field>
  )

  switch (kind) {
    case 'identities': {
      const identity = value as ConfigurationByKind['identities']
      return <>
        <Section title="基础信息">{name}<Field label="描述" htmlFor="identity-description"><TextInput id="identity-description" value={identity.description} onChange={description => set({ ...identity, description })} /></Field></Section>
        <Section title="模型参数" description="留空的可选采样参数将以 null 提交。">
          <Field label="首选模型" htmlFor="preferred-model"><TextInput id="preferred-model" value={identity.preferred_model} onChange={preferred_model => set({ ...identity, preferred_model })} /></Field>
          <Field label="推理强度" htmlFor="reasoning-effort"><select id="reasoning-effort" value={identity.reasoning_effort} onChange={event => set({ ...identity, reasoning_effort: event.target.value })}><option value="none">None</option><option value="low">Low</option><option value="high">High</option><option value="max">Max</option></select></Field>
          <Field label="上下文窗口" htmlFor="context-window"><NumberInput id="context-window" min={0} value={identity.context_window_tokens} onChange={context_window_tokens => set({ ...identity, context_window_tokens: context_window_tokens ?? 0 })} /></Field>
          <Field label="最大输出 Tokens" htmlFor="max-tokens"><NumberInput id="max-tokens" min={0} value={identity.max_tokens} onChange={max_tokens => set({ ...identity, max_tokens: max_tokens ?? 0 })} /></Field>
          <Field label="Temperature" htmlFor="temperature"><NumberInput id="temperature" nullable min={0} step={0.1} value={identity.temperature} onChange={temperature => set({ ...identity, temperature })} /></Field>
          <Field label="Top P" htmlFor="top-p"><NumberInput id="top-p" nullable min={0} max={1} step={0.05} value={identity.top_p} onChange={top_p => set({ ...identity, top_p })} /></Field>
        </Section>
        <Section title="System Prompt" description="支持 GitHub Flavored Markdown 与数学公式。"><Field label="系统提示词" wide><MarkdownEditor value={identity.system_prompt} onChange={system_prompt => set({ ...identity, system_prompt })} /></Field></Section>
        <Section title="注入消息" description="消息将按当前顺序追加到身份上下文。"><Field label="Messages" wide><MessageEditor values={identity.injected_messages} onChange={injected_messages => set({ ...identity, injected_messages })} /></Field></Section>
      </>
    }
    case 'impressions': {
      const impression = value as ConfigurationByKind['impressions']
      return <>
        <Section title="基础信息">{name}<Field label="启用状态"><Toggle id="impression-enabled" checked={impression.enabled} onChange={enabled => set({ ...impression, enabled })} /></Field><Field label="描述" wide><TextArea id="impression-description" value={impression.description} onChange={description => set({ ...impression, description })} /></Field></Section>
        <Section title="消息序列"><Field label="Messages" wide><MessageEditor values={impression.messages} onChange={messages => set({ ...impression, messages })} /></Field></Section>
      </>
    }
    case 'tool-groups': {
      const group = value as ConfigurationByKind['tool-groups']
      return <Section title="工具组" description="从系统已注册工具中选择；也可以输入未来将注册的工具名称。">{name}<Field label="工具" wide hint={`${catalog.tools.length} 个系统工具可用`}><TagsInput values={group.tools} suggestions={catalog.tools} onChange={tools => set({ ...group, tools })} placeholder="搜索并添加工具" /></Field></Section>
    }
    case 'concierges': {
      const concierge = value as ConfigurationByKind['concierges']
      return <>
        <Section title="基础信息" description="Identity 决定助理的模型参数、系统提示词和基础人格。">{name}<Field label="Identity" htmlFor="concierge-identity" hint={`${catalog.identities.length} 个系统 Identity 可用`}><SuggestionInput id="concierge-identity" value={concierge.identity} suggestions={catalog.identities} onChange={identity => set({ ...concierge, identity })} placeholder="搜索 Identity" /></Field></Section>
        <Section title="上下文与能力" description="选项来自当前系统完整配置，包括静态文件和数据库覆盖。">
          <Field label="Impressions" wide hint="按添加顺序注入上下文"><TagsInput values={concierge.impressions} suggestions={catalog.impressions} onChange={impressions => set({ ...concierge, impressions })} placeholder="搜索 Impression" /></Field>
          <Field label="Tool Groups" wide hint="为助理启用成组工具"><TagsInput values={concierge.tool_groups} suggestions={catalog.tool_groups} onChange={tool_groups => set({ ...concierge, tool_groups })} placeholder="搜索 Tool Group" /></Field>
          <Field label="Plugins" wide hint="系统运行时已注册的插件"><TagsInput values={concierge.plugins} suggestions={catalog.plugins} onChange={plugins => set({ ...concierge, plugins })} placeholder="搜索 Plugin" /></Field>
        </Section>
      </>
    }
    case 'workflows': {
      const workflow = value as ConfigurationByKind['workflows']
      return <>
        <Section title="基础信息">{name}<Field label="Concierge" htmlFor="workflow-concierge"><SuggestionInput id="workflow-concierge" value={workflow.concierge} suggestions={catalog.concierges} onChange={concierge => set({ ...workflow, concierge })} placeholder="搜索 Concierge" /></Field><Field label="描述" wide><TextArea id="workflow-description" value={workflow.description} onChange={description => set({ ...workflow, description })} /></Field></Section>
        <Section title="数据契约"><Field label="Input Schema" wide><JsonEditor value={workflow.input_schema} onChange={input_schema => set({ ...workflow, input_schema })} /></Field><Field label="Output Schema" wide><JsonEditor value={workflow.output_schema} onChange={output_schema => set({ ...workflow, output_schema })} /></Field></Section>
        <Section title="执行步骤"><Field label="Steps" wide><StringListEditor multiline values={workflow.steps} addLabel="添加步骤" onChange={steps => set({ ...workflow, steps })} /></Field></Section>
      </>
    }
    case 'jobs': {
      const job = value as ConfigurationByKind['jobs']
      return <>
        <Section title="基础信息">{name}<Field label="标题" htmlFor="job-title"><TextInput id="job-title" value={job.title} onChange={title => set({ ...job, title })} /></Field><Field label="描述" wide><TextArea id="job-description" value={job.description} onChange={description => set({ ...job, description })} /></Field><Field label="目标" wide><TextArea id="job-goal" value={job.goal} onChange={goal => set({ ...job, goal })} /></Field></Section>
        <Section title="调度"><Field label="触发表达式" htmlFor="job-trigger" wide hint="expr-lang/expr 表达式，使用服务所在时区"><TextArea id="job-trigger" rows={3} value={job.trigger} onChange={trigger => set({ ...job, trigger })} /></Field><Field label="每日最大执行次数" htmlFor="job-max"><NumberInput id="job-max" min={0} value={job.max_executions_per_day} onChange={max_executions_per_day => set({ ...job, max_executions_per_day: max_executions_per_day ?? 0 })} /></Field></Section>
        <Section title="工作流"><Field label="Workflow Bindings" wide><WorkflowBindings values={job.workflows} suggestions={catalog.workflows} onChange={workflows => set({ ...job, workflows })} /></Field></Section>
      </>
    }
  }
}

function MessageEditor({ values, onChange }: { values: ConfigurationMessage[]; onChange: (values: ConfigurationMessage[]) => void }) {
  return <div className="configuration-repeat-list configuration-messages">{values.map((message, index) => <article className="configuration-message-row" key={index}><header><span>Message {index + 1}</span><select aria-label={`消息 ${index + 1} 角色`} value={message.role} onChange={event => onChange(values.map((item, itemIndex) => itemIndex === index ? { ...item, role: event.target.value } : item))}><option value="system">system</option><option value="user">user</option><option value="assistant">assistant</option><option value="tool">tool</option></select><button type="button" aria-label={`删除消息 ${index + 1}`} title="删除消息" onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={15} /></button></header><textarea aria-label={`消息 ${index + 1} 内容`} rows={message.content.length > 240 ? 12 : 6} value={message.content} onChange={event => onChange(values.map((item, itemIndex) => itemIndex === index ? { ...item, content: event.target.value } : item))} /></article>)}<button className="configuration-add-row" type="button" onClick={() => onChange([...values, { role: 'user', content: '' }])}><Plus size={15} />添加消息</button></div>
}

function JsonEditor({ value, onChange }: { value: unknown; onChange: (value: unknown) => void }) {
  const formatted = JSON.stringify(value, null, 2)
  const [source, setSource] = useState(formatted)
  useEffect(() => setSource(formatted), [formatted])
  return <textarea className="configuration-json-editor" rows={10} value={source} onChange={event => setSource(event.target.value)} onBlur={event => { try { onChange(JSON.parse(source)); event.target.setCustomValidity('') } catch { event.target.setCustomValidity('请输入合法 JSON'); event.target.reportValidity() } }} />
}

function WorkflowBindings({ values, suggestions, onChange }: { values: JobWorkflowBinding[]; suggestions: string[]; onChange: (values: JobWorkflowBinding[]) => void }) {
  return <div className="configuration-repeat-list"><datalist id="workflow-suggestions">{suggestions.map(item => <option key={item} value={item} />)}</datalist>{values.map((binding, index) => <div className="configuration-binding-row" key={index}><input aria-label="Workflow" list="workflow-suggestions" value={binding.workflow} onChange={event => onChange(values.map((item, itemIndex) => itemIndex === index ? { ...item, workflow: event.target.value } : item))} /><input aria-label="重试延迟秒数" type="number" min={0} value={binding.retry_delay_seconds} onChange={event => onChange(values.map((item, itemIndex) => itemIndex === index ? { ...item, retry_delay_seconds: Number(event.target.value) } : item))} /><input aria-label="重试次数" type="number" min={0} value={binding.retry_count} onChange={event => onChange(values.map((item, itemIndex) => itemIndex === index ? { ...item, retry_count: Number(event.target.value) } : item))} /><button type="button" aria-label="删除绑定" onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={15} /></button></div>)}<button className="configuration-add-row" type="button" onClick={() => onChange([...values, { workflow: '', retry_delay_seconds: 0, retry_count: 0 }])}><Plus size={15} />添加工作流</button></div>
}
