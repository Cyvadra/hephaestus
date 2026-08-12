import { ChevronDown, ChevronUp, ChevronsDown, ChevronsUp, Eye, PenLine, Plus, Trash2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { listProjects } from '../../api/client'
import type {
  Configuration,
  ConfigurationByKind,
  ConfigurationCatalog,
  ConfigurationKind,
  ConfigurationMessage,
  JobWorkflowBinding,
} from '../../api/types'
import { JOB_INPUT_PLACEHOLDERS, JOB_TRIGGER_ENV } from '../../api/types'
import Markdown from '../Markdown'
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
  const [projects, setProjects] = useState<string[]>([])
  useEffect(() => {
    void listProjects().then(list => setProjects(list.map(item => item.Name))).catch(() => undefined)
  }, [])
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
        <Section title="基础信息" description="Identity 决定助理的模型参数、系统提示词和基础人格。">{name}<Field label="昵称" htmlFor="concierge-nickname" hint="留空则使用名称，最多 20 字" error={errors.nickname}><TextInput id="concierge-nickname" value={concierge.nickname} maxLength={20} onChange={nickname => set({ ...concierge, nickname })} /></Field><Field label="描述" wide><TextArea id="concierge-description" value={concierge.description} onChange={description => set({ ...concierge, description })} /></Field><Field label="Identity" htmlFor="concierge-identity" hint={`${catalog.identities.length} 个系统 Identity 可用`}><SuggestionInput id="concierge-identity" value={concierge.identity} suggestions={catalog.identities} onChange={identity => set({ ...concierge, identity })} placeholder="搜索 Identity" /></Field></Section>
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
        <Section title="调度"><Field label="触发表达式" htmlFor="job-trigger" wide hint="expr-lang/expr 表达式，使用服务所在时区；点击下方变量插入"><TriggerEditor id="job-trigger" rows={3} value={job.trigger} onChange={trigger => set({ ...job, trigger })} /></Field><Field label="每日最大执行次数" htmlFor="job-max"><NumberInput id="job-max" min={0} value={job.max_executions_per_day} onChange={max_executions_per_day => set({ ...job, max_executions_per_day: max_executions_per_day ?? 0 })} /></Field></Section>
        <Section title="工作流" description="每个绑定在指定 Project 内运行其 Workflow，max_attempts 为总尝试次数。"><Field label="Workflow Bindings" wide><WorkflowBindings values={job.workflows} suggestions={catalog.workflows} projects={projects} onChange={workflows => set({ ...job, workflows })} /></Field></Section>
      </>
    }
  }
}

function MessageEditor({ values, onChange }: { values: ConfigurationMessage[]; onChange: (values: ConfigurationMessage[]) => void }) {
  const blocks = messageBlocks(values)
  const update = (index: number, patch: Partial<ConfigurationMessage>) => onChange(values.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item))
  const remove = (index: number, count = 1) => onChange(values.filter((_, itemIndex) => itemIndex < index || itemIndex >= index + count))
  const move = (from: number, to: number) => {
    if (from === to) return
    const next = [...blocks]
    const [block] = next.splice(from, 1)
    next.splice(to, 0, block)
    onChange(next.flatMap(item => item.messages))
  }

  return <div className="configuration-repeat-list configuration-messages">
    {blocks.map((block, blockIndex) => {
      const [message, answer] = block.messages
      const order = <MessageOrderControls index={blockIndex} total={blocks.length} onMove={move} />

      if (answer) return <QuestionAnswerEditor key={block.start} question={message} answer={answer} number={blockIndex + 1} order={order} onQuestionChange={content => update(block.start, { content })} onAnswerChange={content => update(block.start + 1, { content })} onRemove={() => remove(block.start, 2)} />

      return <article className="configuration-instruction-row" key={block.start}>
        <header><span>{message.role === 'system' ? '指令' : `Message ${block.start + 1}`}</span><select aria-label={`消息 ${block.start + 1} 角色`} value={message.role} onChange={event => update(block.start, { role: event.target.value })}><option value="system">system</option><option value="user">user</option><option value="assistant">assistant</option><option value="tool">tool</option></select>{order}<button type="button" aria-label={`删除消息 ${block.start + 1}`} title="删除消息" onClick={() => remove(block.start)}><Trash2 size={15} /></button></header>
        <AutoResizeTextArea aria-label={`消息 ${block.start + 1} 内容`} value={message.content} onChange={content => update(block.start, { content })} placeholder="输入系统指令或上下文内容" />
      </article>
    })}
    <div className="configuration-message-actions"><button className="configuration-add-row" type="button" onClick={() => onChange([...values, { role: 'system', content: '' }])}><Plus size={15} />添加指令</button><button className="configuration-add-row" type="button" onClick={() => onChange([...values, { role: 'user', content: '' }, { role: 'assistant', content: '' }])}><Plus size={15} />添加对话</button></div>
  </div>
}

function messageBlocks(values: ConfigurationMessage[]) {
  const blocks: { start: number; messages: ConfigurationMessage[] }[] = []
  for (let index = 0; index < values.length; index += 1) {
    const message = values[index]
    const answer = message.role === 'user' && values[index + 1]?.role === 'assistant' ? values[index + 1] : undefined
    blocks.push({ start: index, messages: answer ? [message, answer] : [message] })
    if (answer) index += 1
  }
  return blocks
}

function MessageOrderControls({ index, total, onMove }: { index: number; total: number; onMove: (from: number, to: number) => void }) {
  return <div className="configuration-message-order" role="group" aria-label="调整消息顺序">
    <button type="button" aria-label="移至顶部" title="移至顶部" disabled={index === 0} onClick={() => onMove(index, 0)}><ChevronsUp size={15} /></button>
    <button type="button" aria-label="上移" title="上移" disabled={index === 0} onClick={() => onMove(index, index - 1)}><ChevronUp size={15} /></button>
    <button type="button" aria-label="下移" title="下移" disabled={index === total - 1} onClick={() => onMove(index, index + 1)}><ChevronDown size={15} /></button>
    <button type="button" aria-label="移至底部" title="移至底部" disabled={index === total - 1} onClick={() => onMove(index, total - 1)}><ChevronsDown size={15} /></button>
  </div>
}

function QuestionAnswerEditor({ question, answer, number, order, onQuestionChange, onAnswerChange, onRemove }: { question: ConfigurationMessage; answer: ConfigurationMessage; number: number; order: React.ReactNode; onQuestionChange: (content: string) => void; onAnswerChange: (content: string) => void; onRemove: () => void }) {
  const [preview, setPreview] = useState(true)
  return <article className="configuration-qa-row">
    <header><span>对话 {number}</span>{order}<button type="button" aria-label={`删除对话 ${number}`} title="删除对话" onClick={onRemove}><Trash2 size={15} /></button></header>
    <label className="configuration-qa-question"><span>User</span><AutoResizeTextArea aria-label={`对话 ${number} 用户问题`} value={question.content} onChange={onQuestionChange} placeholder="输入用户问题" /></label>
    <div className="configuration-qa-answer"><div className="configuration-qa-answer-header"><span>Assistant</span><button type="button" className={preview ? 'active' : ''} aria-pressed={preview} title={preview ? '编辑回答' : '预览 Markdown'} onClick={() => setPreview(current => !current)}>{preview ? <PenLine size={14} /> : <Eye size={14} />}{preview ? '编辑' : '预览'}</button></div>{preview ? <div className="configuration-qa-preview">{answer.content ? <Markdown>{answer.content}</Markdown> : <span>Markdown 预览将显示在这里</span>}</div> : <AutoResizeTextArea aria-label={`对话 ${number} 助理回答`} value={answer.content} onChange={onAnswerChange} placeholder="输入助理回答，支持 Markdown" />}</div>
  </article>
}

function AutoResizeTextArea({ value, onChange, ...props }: Omit<React.TextareaHTMLAttributes<HTMLTextAreaElement>, 'onChange'> & { value: string; onChange: (value: string) => void }) {
  const ref = useRef<HTMLTextAreaElement>(null)
  const resize = () => {
    const element = ref.current
    if (!element) return
    element.style.height = 'auto'
    element.style.height = `${element.scrollHeight}px`
  }
  useEffect(resize, [value])
  return <textarea {...props} ref={ref} className="configuration-auto-textarea" rows={1} value={value} onChange={event => { onChange(event.target.value); resize() }} />
}

function JsonEditor({ value, onChange }: { value: unknown; onChange: (value: unknown) => void }) {
  const formatted = JSON.stringify(value, null, 2)
  const [source, setSource] = useState(formatted)
  useEffect(() => setSource(formatted), [formatted])
  return <textarea className="configuration-json-editor" rows={10} value={source} onChange={event => setSource(event.target.value)} onBlur={event => { try { onChange(JSON.parse(source)); event.target.setCustomValidity('') } catch { event.target.setCustomValidity('请输入合法 JSON'); event.target.reportValidity() } }} />
}

function WorkflowBindings({ values, suggestions, projects, onChange }: { values: JobWorkflowBinding[]; suggestions: string[]; projects: string[]; onChange: (values: JobWorkflowBinding[]) => void }) {
  return <div className="configuration-repeat-list"><datalist id="workflow-suggestions">{suggestions.map(item => <option key={item} value={item} />)}</datalist><datalist id="project-suggestions">{projects.map(item => <option key={item} value={item} />)}</datalist>{values.map((binding, index) => {
    const update = (patch: Partial<JobWorkflowBinding>) => onChange(values.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item))
    return <div className="configuration-binding-card" key={index}>
      <div className="configuration-binding-fields">
        <label><span>工作流</span><input aria-label="Workflow" list="workflow-suggestions" placeholder="选择或输入" value={binding.workflow} onChange={event => update({ workflow: event.target.value })} /></label>
        <label><span>项目</span><input aria-label="Project" list="project-suggestions" placeholder="default-workspace" value={binding.project} onChange={event => update({ project: event.target.value })} /></label>
        <label><span>最大尝试</span><input aria-label="最大尝试次数" type="number" min={1} value={binding.max_attempts} onChange={event => update({ max_attempts: Number(event.target.value) })} /></label>
        <label><span>重试延迟(秒)</span><input aria-label="重试延迟秒数" type="number" min={0} value={binding.retry_delay_seconds} onChange={event => update({ retry_delay_seconds: Number(event.target.value) })} /></label>
        <button type="button" aria-label="删除绑定" title="删除绑定" onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={15} /></button>
      </div>
      <div className="configuration-binding-input"><span>输入 <small>JSON；字符串叶子可引用 ${'{'}...{'}'} 占位符</small></span><InputJsonEditor value={binding.input} onChange={input => update({ input })} /></div>
    </div>
  })}<button className="configuration-add-row" type="button" onClick={() => onChange([...values, { workflow: '', project: '', input: {}, max_attempts: 1, retry_delay_seconds: 0 }])}><Plus size={15} />添加工作流</button></div>
}

function parseJSONLoose(source: string): unknown | undefined {
  try {
    return JSON.parse(source) as unknown
  } catch {
    return undefined
  }
}

// InputJsonEditor 是带 `${...}` 占位符一键插入的 JSON 输入框。
function InputJsonEditor({ value, onChange }: { value: Record<string, unknown>; onChange: (value: Record<string, unknown>) => void }) {
  const formatted = JSON.stringify(value ?? {}, null, 2)
  const [source, setSource] = useState(formatted)
  const ref = useRef<HTMLTextAreaElement>(null)
  useEffect(() => setSource(formatted), [formatted])
  const insert = (name: string) => {
    const token = `\${${name}}`
    const el = ref.current
    const start = el?.selectionStart ?? source.length
    const end = el?.selectionEnd ?? source.length
    const next = source.slice(0, start) + token + source.slice(end)
    setSource(next)
    const parsed = parseJSONLoose(next)
    if (parsed !== undefined && typeof parsed === 'object' && parsed !== null) onChange(parsed as Record<string, unknown>)
    requestAnimationFrame(() => { el?.focus(); el?.setSelectionRange(start + token.length, start + token.length) })
  }
  return (
    <div className="configuration-placeholder-editor">
      <div className="configuration-placeholder-chips">{JOB_INPUT_PLACEHOLDERS.map(name => <button type="button" key={name} onClick={() => insert(name)}>{name}</button>)}</div>
      <textarea ref={ref} className="configuration-json-editor" rows={8} value={source} onChange={event => setSource(event.target.value)} onBlur={event => { const parsed = parseJSONLoose(source); if (parsed !== undefined && typeof parsed === 'object' && parsed !== null) onChange(parsed as Record<string, unknown>); event.target.setCustomValidity('') }} />
    </div>
  )
}

// TriggerEditor 是带求值环境变量一键插入的触发表达式输入框。
function TriggerEditor({ id, value, onChange, rows }: { id: string; value: string; onChange: (value: string) => void; rows: number }) {
  const ref = useRef<HTMLTextAreaElement>(null)
  const insert = (token: string) => {
    const el = ref.current
    const start = el?.selectionStart ?? value.length
    const end = el?.selectionEnd ?? value.length
    const next = value.slice(0, start) + token + value.slice(end)
    onChange(next)
    requestAnimationFrame(() => { el?.focus(); el?.setSelectionRange(start + token.length, start + token.length) })
  }
  return (
    <div className="configuration-placeholder-editor">
      <textarea ref={ref} id={id} rows={rows} value={value} onChange={event => onChange(event.target.value)} placeholder='例如 Hour >= 9 && ExecutionsToday == 0' />
      <div className="configuration-placeholder-chips">{JOB_TRIGGER_ENV.map(token => <button type="button" key={token} onClick={() => insert(token)}>{token}</button>)}</div>
    </div>
  )
}
