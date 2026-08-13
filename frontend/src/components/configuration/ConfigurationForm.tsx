import { ChevronDown, ChevronUp, ChevronsDown, ChevronsUp, Eye, PenLine, Plus, Trash2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
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

const EMPTY_CATALOG: ConfigurationCatalog = { identities: [], impressions: [], tool_groups: [], concierges: [], workflows: [], jobs: [], tools: [], plugins: [], plugin_descriptions: {} }

export default function ConfigurationForm({ kind, value, errors, isNew, catalog = EMPTY_CATALOG, onChange }: Props) {
  const { t } = useTranslation()
  const set = <T extends Configuration>(next: T) => onChange(next)
  const [projects, setProjects] = useState<string[]>([])
  useEffect(() => {
    void listProjects().then(list => setProjects(list.map(item => item.Name))).catch(() => undefined)
  }, [])
  const name = (
    <Field label={t('configuration.form.name')} htmlFor="configuration-name" error={errors.name} hint={isNew ? t('configuration.form.nameNewHint') : t('configuration.form.nameEditHint')}>
      <TextInput id="configuration-name" value={value.name} disabled={!isNew} onChange={nameValue => set({ ...value, name: nameValue })} placeholder="lowercase-name" />
    </Field>
  )

  switch (kind) {
    case 'identities': {
      const identity = value as ConfigurationByKind['identities']
      return <>
        <Section title={t('configuration.form.basic')}>{name}<Field label={t('configuration.form.description')} htmlFor="identity-description"><TextInput id="identity-description" value={identity.description} onChange={description => set({ ...identity, description })} /></Field></Section>
        <Section title={t('configuration.form.modelParameters')} description={t('configuration.form.optionalSampling')}>
          <Field label={t('configuration.form.preferredModel')} htmlFor="preferred-model"><TextInput id="preferred-model" value={identity.preferred_model} onChange={preferred_model => set({ ...identity, preferred_model })} /></Field>
          <Field label={t('configuration.form.reasoningEffort')} htmlFor="reasoning-effort"><select id="reasoning-effort" value={identity.reasoning_effort} onChange={event => set({ ...identity, reasoning_effort: event.target.value })}><option value="none">None</option><option value="low">Low</option><option value="high">High</option><option value="max">Max</option></select></Field>
          <Field label={t('configuration.form.contextWindow')} htmlFor="context-window"><NumberInput id="context-window" min={0} value={identity.context_window_tokens} onChange={context_window_tokens => set({ ...identity, context_window_tokens: context_window_tokens ?? 0 })} /></Field>
          <Field label={t('configuration.form.maxTokens')} htmlFor="max-tokens"><NumberInput id="max-tokens" min={0} value={identity.max_tokens} onChange={max_tokens => set({ ...identity, max_tokens: max_tokens ?? 0 })} /></Field>
          <Field label="Temperature" htmlFor="temperature"><NumberInput id="temperature" nullable min={0} step={0.1} value={identity.temperature} onChange={temperature => set({ ...identity, temperature })} /></Field>
          <Field label="Top P" htmlFor="top-p"><NumberInput id="top-p" nullable min={0} max={1} step={0.05} value={identity.top_p} onChange={top_p => set({ ...identity, top_p })} /></Field>
        </Section>
        <Section title={t('configuration.form.systemPromptSection')} description={t('configuration.form.markdownSupport')}><Field label={t('configuration.form.systemPrompt')} wide><MarkdownEditor value={identity.system_prompt} onChange={system_prompt => set({ ...identity, system_prompt })} /></Field></Section>
        <Section title={t('configuration.form.injectedMessages')} description={t('configuration.form.injectedMessagesDescription')}><Field label={t('configuration.form.messages')} wide><MessageEditor values={identity.injected_messages} onChange={injected_messages => set({ ...identity, injected_messages })} /></Field></Section>
      </>
    }
    case 'impressions': {
      const impression = value as ConfigurationByKind['impressions']
      return <>
        <Section title={t('configuration.form.basic')}>{name}<Field label={t('configuration.form.enabledStatus')}><Toggle id="impression-enabled" checked={impression.enabled} onChange={enabled => set({ ...impression, enabled })} /></Field><Field label={t('configuration.form.description')} wide><TextArea id="impression-description" value={impression.description} onChange={description => set({ ...impression, description })} /></Field></Section>
        <Section title={t('configuration.form.messageSequence')}><Field label={t('configuration.form.messages')} wide><MessageEditor values={impression.messages} onChange={messages => set({ ...impression, messages })} /></Field></Section>
      </>
    }
    case 'tool-groups': {
      const group = value as ConfigurationByKind['tool-groups']
      return <Section title={t('chat.concierge.toolGroups')} description={t('configuration.form.contextCapabilitiesDescription')}>{name}<Field label={t('configuration.form.tools')} wide hint={t('configuration.form.availableTools', { count: catalog.tools.length })}><TagsInput values={group.tools} suggestions={catalog.tools} onChange={tools => set({ ...group, tools })} placeholder={t('configuration.form.searchAndAddTools')} /></Field></Section>
    }
    case 'concierges': {
      const concierge = value as ConfigurationByKind['concierges']
      return <>
        <Section title={t('configuration.form.basic')} description="Identity decides the assistant's model parameters, system prompt, and baseline persona.">{name}<Field label={t('configuration.form.nickname')} htmlFor="concierge-nickname" hint={t('configuration.form.nicknameHint')} error={errors.nickname}><TextInput id="concierge-nickname" value={concierge.nickname} maxLength={20} onChange={nickname => set({ ...concierge, nickname })} /></Field><Field label={t('configuration.form.description')} wide><TextArea id="concierge-description" value={concierge.description} onChange={description => set({ ...concierge, description })} /></Field><Field label="Identity" htmlFor="concierge-identity" hint={t('configuration.form.availableTools', { count: catalog.identities.length })}><SuggestionInput id="concierge-identity" value={concierge.identity} suggestions={catalog.identities} onChange={identity => set({ ...concierge, identity })} placeholder={t('configuration.form.searchIdentity')} /></Field></Section>
        <Section title={t('configuration.form.contextAndCapabilities')} description={t('configuration.form.contextCapabilitiesDescription')}>
          <Field label={t('configuration.form.impressions')} wide hint={t('configuration.form.injectedInOrder')}><TagsInput values={concierge.impressions} suggestions={catalog.impressions} onChange={impressions => set({ ...concierge, impressions })} placeholder={t('configuration.form.searchImpression')} /></Field>
          <Field label={t('configuration.form.toolGroups')} wide hint={t('configuration.form.enableToolGroups')}><TagsInput values={concierge.tool_groups} suggestions={catalog.tool_groups} onChange={tool_groups => set({ ...concierge, tool_groups })} placeholder={t('configuration.form.searchToolGroup')} /></Field>
          <Field label={t('configuration.form.plugins')} wide hint={t('configuration.form.registeredPlugins')}><TagsInput values={concierge.plugins} suggestions={catalog.plugins} onChange={plugins => set({ ...concierge, plugins })} placeholder={t('configuration.form.searchPlugin')} /></Field>
        </Section>
        <Section title={t('configuration.form.availableProjects')} description={t('configuration.form.availableProjectsDescription')}>
          <Field label={t('configuration.form.projects')} wide hint={t('configuration.form.availableProjectsCount', { count: projects.length })}><TagsInput values={concierge.available_projects} suggestions={projects} onChange={available_projects => set({ ...concierge, available_projects })} placeholder={t('configuration.form.searchProject')} /></Field>
        </Section>
      </>
    }
    case 'workflows': {
      const workflow = value as ConfigurationByKind['workflows']
      return <>
        <Section title={t('configuration.form.basic')}>{name}<Field label="Concierge" htmlFor="workflow-concierge"><SuggestionInput id="workflow-concierge" value={workflow.concierge} suggestions={catalog.concierges} onChange={concierge => set({ ...workflow, concierge })} placeholder={t('configuration.form.searchConcierge')} /></Field><Field label={t('configuration.form.description')} wide><TextArea id="workflow-description" value={workflow.description} onChange={description => set({ ...workflow, description })} /></Field></Section>
        <Section title={t('configuration.form.dataContract')}><Field label="Input Schema" wide><JsonEditor value={workflow.input_schema} onChange={input_schema => set({ ...workflow, input_schema })} /></Field><Field label="Output Schema" wide><JsonEditor value={workflow.output_schema} onChange={output_schema => set({ ...workflow, output_schema })} /></Field></Section>
        <Section title={t('configuration.form.executionSteps')}><Field label="Steps" wide><StringListEditor multiline values={workflow.steps} addLabel={t('configuration.form.addStep')} onChange={steps => set({ ...workflow, steps })} /></Field></Section>
      </>
    }
    case 'jobs': {
      const job = value as ConfigurationByKind['jobs']
      return <>
        <Section title={t('configuration.form.basic')}>{name}<Field label={t('configuration.form.title')} htmlFor="job-title"><TextInput id="job-title" value={job.title} onChange={title => set({ ...job, title })} /></Field><Field label={t('configuration.form.description')} wide><TextArea id="job-description" value={job.description} onChange={description => set({ ...job, description })} /></Field><Field label={t('configuration.form.goal')} wide><TextArea id="job-goal" value={job.goal} onChange={goal => set({ ...job, goal })} /></Field></Section>
        <Section title={t('configuration.form.schedule')}><Field label={t('configuration.form.triggerExpression')} htmlFor="job-trigger" wide hint={t('configuration.form.triggerHint')}><TriggerEditor id="job-trigger" rows={3} value={job.trigger} onChange={trigger => set({ ...job, trigger })} /></Field><Field label={t('configuration.form.dailyMax')} htmlFor="job-max"><NumberInput id="job-max" min={0} value={job.max_executions_per_day} onChange={max_executions_per_day => set({ ...job, max_executions_per_day: max_executions_per_day ?? 0 })} /></Field></Section>
        <Section title={t('configuration.form.workflows')} description={t('configuration.form.workflowDescription')}><Field label={t('configuration.form.workflowBindings')} wide><WorkflowBindings values={job.workflows} suggestions={catalog.workflows} projects={projects} onChange={workflows => set({ ...job, workflows })} /></Field></Section>
      </>
    }
  }
}

function MessageEditor({ values, onChange }: { values: ConfigurationMessage[] | null; onChange: (values: ConfigurationMessage[]) => void }) {
  const { t } = useTranslation()
  const messages = values ?? []
  const blocks = messageBlocks(messages)
  const update = (index: number, patch: Partial<ConfigurationMessage>) => onChange(messages.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item))
  const remove = (index: number, count = 1) => onChange(messages.filter((_, itemIndex) => itemIndex < index || itemIndex >= index + count))
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
        <header><span>{message.role === 'system' ? t('configuration.form.instruction') : t('configuration.form.message', { count: block.start + 1 })}</span><select aria-label={t('configuration.form.messageRole', { count: block.start + 1 })} value={message.role} onChange={event => update(block.start, { role: event.target.value })}><option value="system">system</option><option value="user">user</option><option value="assistant">assistant</option><option value="tool">tool</option></select>{order}<button type="button" aria-label={t('configuration.form.deleteMessage')} title={t('configuration.form.deleteMessage')} onClick={() => remove(block.start)}><Trash2 size={15} /></button></header>
        <AutoResizeTextArea aria-label={t('configuration.form.messageContent', { count: block.start + 1 })} value={message.content} onChange={content => update(block.start, { content })} placeholder={t('configuration.form.messagePlaceholder')} />
      </article>
    })}
    <div className="configuration-message-actions"><button className="configuration-add-row" type="button" onClick={() => onChange([...messages, { role: 'system', content: '' }])}><Plus size={15} />{t('configuration.form.addInstruction')}</button><button className="configuration-add-row" type="button" onClick={() => onChange([...messages, { role: 'user', content: '' }, { role: 'assistant', content: '' }])}><Plus size={15} />{t('configuration.form.addConversation')}</button></div>
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
  const { t } = useTranslation()
  return <div className="configuration-message-order" role="group" aria-label={t('configuration.form.adjustOrder')}>
    <button type="button" aria-label={t('configuration.form.moveTop')} title={t('configuration.form.moveTop')} disabled={index === 0} onClick={() => onMove(index, 0)}><ChevronsUp size={15} /></button>
    <button type="button" aria-label={t('configuration.form.moveUp')} title={t('configuration.form.moveUp')} disabled={index === 0} onClick={() => onMove(index, index - 1)}><ChevronUp size={15} /></button>
    <button type="button" aria-label={t('configuration.form.moveDown')} title={t('configuration.form.moveDown')} disabled={index === total - 1} onClick={() => onMove(index, index + 1)}><ChevronDown size={15} /></button>
    <button type="button" aria-label={t('configuration.form.moveBottom')} title={t('configuration.form.moveBottom')} disabled={index === total - 1} onClick={() => onMove(index, total - 1)}><ChevronsDown size={15} /></button>
  </div>
}

function QuestionAnswerEditor({ question, answer, number, order, onQuestionChange, onAnswerChange, onRemove }: { question: ConfigurationMessage; answer: ConfigurationMessage; number: number; order: React.ReactNode; onQuestionChange: (content: string) => void; onAnswerChange: (content: string) => void; onRemove: () => void }) {
  const { t } = useTranslation()
  const [preview, setPreview] = useState(true)
  return <article className="configuration-qa-row">
    <header><span>{t('configuration.form.conversation', { count: number })}</span>{order}<button type="button" aria-label={t('configuration.form.deleteConversation')} title={t('configuration.form.deleteConversation')} onClick={onRemove}><Trash2 size={15} /></button></header>
    <label className="configuration-qa-question"><span>User</span><AutoResizeTextArea aria-label={t('configuration.form.userQuestion', { count: number })} value={question.content} onChange={onQuestionChange} placeholder={t('configuration.form.userQuestionPlaceholder')} /></label>
    <div className="configuration-qa-answer"><div className="configuration-qa-answer-header"><span>Assistant</span><button type="button" className={preview ? 'active' : ''} aria-pressed={preview} title={preview ? t('configuration.form.editAnswer') : t('configuration.form.previewMarkdown')} onClick={() => setPreview(current => !current)}>{preview ? <PenLine size={14} /> : <Eye size={14} />}{preview ? t('configuration.form.edit') : t('configuration.form.preview')}</button></div>{preview ? <div className="configuration-qa-preview">{answer.content ? <Markdown>{answer.content}</Markdown> : <span>{t('configuration.form.emptyMarkdownPreview')}</span>}</div> : <AutoResizeTextArea aria-label={t('configuration.form.assistantAnswer', { count: number })} value={answer.content} onChange={onAnswerChange} placeholder={t('configuration.form.assistantAnswerPlaceholder')} />}</div>
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
  const { t } = useTranslation()
  const formatted = JSON.stringify(value, null, 2)
  const [source, setSource] = useState(formatted)
  useEffect(() => setSource(formatted), [formatted])
  return <textarea className="configuration-json-editor" rows={10} value={source} onChange={event => setSource(event.target.value)} onBlur={event => { try { onChange(JSON.parse(source)); event.target.setCustomValidity('') } catch { event.target.setCustomValidity(t('configuration.form.validJson')); event.target.reportValidity() } }} />
}

function WorkflowBindings({ values, suggestions, projects, onChange }: { values: JobWorkflowBinding[]; suggestions: string[]; projects: string[]; onChange: (values: JobWorkflowBinding[]) => void }) {
  const { t } = useTranslation()
  return <div className="configuration-repeat-list"><datalist id="workflow-suggestions">{suggestions.map(item => <option key={item} value={item} />)}</datalist><datalist id="project-suggestions">{projects.map(item => <option key={item} value={item} />)}</datalist>{values.map((binding, index) => {
    const update = (patch: Partial<JobWorkflowBinding>) => onChange(values.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item))
    return <div className="configuration-binding-card" key={index}>
      <div className="configuration-binding-fields">
        <label><span>{t('configuration.form.workflow')}</span><input aria-label={t('configuration.form.workflow')} list="workflow-suggestions" placeholder={t('configuration.form.selectOrEnter')} value={binding.workflow} onChange={event => update({ workflow: event.target.value })} /></label>
        <label><span>{t('configuration.form.project')}</span><input aria-label={t('configuration.form.project')} list="project-suggestions" placeholder="default-workspace" value={binding.project} onChange={event => update({ project: event.target.value })} /></label>
        <label><span>{t('configuration.form.maxAttempts')}</span><input aria-label={t('configuration.form.maxAttempts')} type="number" min={1} value={binding.max_attempts} onChange={event => update({ max_attempts: Number(event.target.value) })} /></label>
        <label><span>{t('configuration.form.retryDelay')}</span><input aria-label={t('configuration.form.retryDelay')} type="number" min={0} value={binding.retry_delay_seconds} onChange={event => update({ retry_delay_seconds: Number(event.target.value) })} /></label>
        <button type="button" aria-label={t('configuration.form.deleteBinding')} title={t('configuration.form.deleteBinding')} onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={15} /></button>
      </div>
      <div className="configuration-binding-input"><span>{t('configuration.form.input')} <small>{t('configuration.form.inputPlaceholderHint')}</small></span><InputJsonEditor value={binding.input} onChange={input => update({ input })} /></div>
    </div>
  })}<button className="configuration-add-row" type="button" onClick={() => onChange([...values, { workflow: '', project: '', input: {}, max_attempts: 1, retry_delay_seconds: 0 }])}><Plus size={15} />{t('configuration.form.addWorkflow')}</button></div>
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
  const { t } = useTranslation()
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
      <textarea ref={ref} id={id} rows={rows} value={value} onChange={event => onChange(event.target.value)} placeholder={t('configuration.form.triggerExample')} />
      <div className="configuration-placeholder-chips">{JOB_TRIGGER_ENV.map(token => <button type="button" key={token} onClick={() => insert(token)}>{token}</button>)}</div>
    </div>
  )
}
