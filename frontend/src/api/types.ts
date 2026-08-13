// Go store.Session and store.ChatMessage have no json tags → exported field names used as keys.
// Handler request/response structs DO have snake_case json tags.

export interface SessionSettings {
  identity: string
  impressions: string[]
  tool_groups: string[]
  plugins: string[]
}

export interface Session {
  ID: number
  ProjectID: number
  SourceConcierge: string
  Settings: SessionSettings
  Title: string
  Summary: string
  ActiveLeafMessageID: number | null
  CompressionID: number | null
  CompressionLastMessageID: number | null
  FlagArchived: boolean
  FlagPinned: number
  ReasoningEffort: string
  EnableWebSearch: boolean | null
  CreatedAt: string
  UpdatedAt: string
}

export interface Project {
  ID: number
  Name: string
  Description: string
  AvailableConciergeList: string[]
  CreatedAt: string
  is_default: boolean
}

export interface ChatMessage {
  ID: number
  SessionID: number
  ParentMessageID: number | null
  Timestamp: string
  Role: 'user' | 'assistant' | 'tool' | 'system'
  Content: string
  Status: 'complete' | 'incomplete' | ''
  ReasoningContent: string
  ToolCalls: ToolCall[] | null
  ToolCallID: string
  Attachments: MessageAttachment[]
}

export interface MessageAttachment {
  ID: number
  SessionID: number
  MessageID: number
  ProjectID: number
  Path: string
  Name: string
  Size: number
  MIME: string
  CreatedAt: string
}

export interface ToolCall {
  id?: string
  type?: string
  function?: {
    name?: string
    arguments?: string
  }
}

export interface StreamToolCall {
  call_index: number
  index: number
  id?: string
  name?: string
  arguments?: string
  result?: string
  status: 'calling' | 'complete' | 'error'
  output_cursor?: number
  output_pending_control?: string
  output_carriage_return?: boolean
}

export interface InteractionRequest {
  id: number
  session_id: number
  kind: 'permission'
  title: string
  details: string
  created_at: string
}

export interface HistoryResponse {
  session: Session
  messages: ChatMessage[]
  reasoning_effort: string
}

export type ReasoningEffort = 'none' | 'high' | 'max'

export interface GenerationOptions {
  reasoningEffort: ReasoningEffort
  webSearch: boolean
}

export interface SendMessageResponse {
  command_response?: string
  session_target?: SessionTarget
  message?: ChatMessage
  metadata?: Record<string, unknown>
}

export interface SessionTarget {
  id: number
  project: string
}

export interface UploadAttachment {
  path: string
  size: number
  content_included: boolean
}

export interface UploadResult {
  attachments: UploadAttachment[]
  warnings?: string[]
}

export interface ConciergeItem {
  name: string
  nickname: string
  description: string
  identity: string
  reasoning_effort: string
  impressions: string[]
  tool_groups: string[]
  plugins: string[]
}

export const CONFIGURATION_KINDS = [
  'identities',
  'impressions',
  'tool-groups',
  'concierges',
  'workflows',
  'jobs',
] as const

export type ConfigurationKind = typeof CONFIGURATION_KINDS[number]

export interface ConfigurationMessage {
  role: string
  content: string
}

export interface IdentityConfiguration {
  name: string
  description: string
  preferred_model: string
  reasoning_effort: string
  context_window_tokens: number
  max_tokens: number
  temperature: number | null
  top_p: number | null
  system_prompt: string
  injected_messages: ConfigurationMessage[]
}

export interface ImpressionConfiguration {
  name: string
  description: string
  enabled: boolean
  messages: ConfigurationMessage[]
}

export interface ToolGroupConfiguration {
  name: string
  tools: string[]
}

export interface ConciergeConfiguration {
  name: string
  nickname: string
  description: string
  identity: string
  impressions: string[]
  tool_groups: string[]
  plugins: string[]
  available_projects: string[]
}

export interface WorkflowConfiguration {
  name: string
  description: string
  concierge: string
  input_schema: unknown
  output_schema: unknown
  steps: string[]
}

export interface JobWorkflowBinding {
  workflow: string
  project: string
  input: Record<string, unknown>
  max_attempts: number
  retry_delay_seconds: number
}

export interface JobConfiguration {
  name: string
  title: string
  description: string
  goal: string
  workflows: JobWorkflowBinding[]
  trigger: string
  max_executions_per_day: number
}

// 在线测试与运行记录
export type WorkflowRunStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'fatal' | 'cancelled' | 'interrupted'
export type WorkflowStepRunStatus = WorkflowRunStatus
export type JobRunStatus = 'pending' | 'running' | 'succeeded' | 'completed_with_errors' | 'failed' | 'cancelled' | 'interrupted'

// Go store.WorkflowRun / store.JobRun have no json tags → exported field names.
export interface WorkflowDefinition {
  name: string
  description: string
  concierge: string
  input_schema: unknown
  output_schema: unknown
  steps: string[]
}

export interface WorkflowRun {
  ID: number
  JobRunID: number | null
  JobName: string
  BindingIndex: number
  WorkflowName: string
  Concierge: string
  ProjectName: string
  Workflow: WorkflowDefinition
  Input: unknown
  Output: unknown
  Attempt: number
  Status: WorkflowRunStatus
  Error: string
  Cancelled: boolean
  StartedAt: string | null
  FinishedAt: string | null
  CreatedAt: string
  UpdatedAt: string
}

export interface WorkflowStepRun {
  ID: number
  WorkflowRunID: number
  Index: number
  Text: string
  Transcript: unknown
  Output: string
  Status: WorkflowStepRunStatus
  Error: string
  StartedAt: string | null
  FinishedAt: string | null
  CreatedAt: string
  UpdatedAt: string
}

export interface JobDefinition {
  name: string
  title: string
  description: string
  goal: string
  workflows: JobWorkflowBinding[]
  trigger: string
  max_executions_per_day: number
}

export interface JobRun {
  ID: number
  JobName: string
  LocalDate: string
  Job: JobDefinition
  Status: JobRunStatus
  Error: string
  Cancelled: boolean
  StartedAt: string | null
  FinishedAt: string | null
  CreatedAt: string
  UpdatedAt: string
}

export interface WorkflowRunDetail {
  run: WorkflowRun
  steps: WorkflowStepRun[]
}

export interface JobRunDetail {
  run: JobRun
  workflow_runs: WorkflowRun[]
}

// 工作流运行实时进度（SSE）。后端把 agent.StreamEvent 序列化为
// { type, text?, tool_call? }，run/step 事件携带完整快照。
export interface WorkflowStreamToolCall {
  call_index: number
  index: number
  id?: string
  name?: string
  arguments?: string
  result?: string
  status: string
}

export interface WorkflowDelta {
  type: 'delta' | 'reasoning' | 'tool_call' | 'tool_output' | 'tool_result'
  text?: string
  tool_call?: WorkflowStreamToolCall
}

export interface WorkflowProgressEvent {
  type: 'run' | 'step' | 'delta' | 'done'
  run?: WorkflowRun
  step?: WorkflowStepRun
  delta?: WorkflowDelta
}

export interface WorkflowProgressEnvelope {
  sequence: number
  data: WorkflowProgressEvent | WorkflowRun
}

// Job 输入占位符与触发器求值环境，用于表单自动补全。
export const JOB_INPUT_PLACEHOLDERS = [
  'job.name',
  'job.title',
  'job.goal',
  'run.local_date',
  'run.started_at',
  'trigger.last_succeeded_at',
  'now',
] as const

export const JOB_TRIGGER_ENV = [
  'Now', 'Date', 'Hour', 'Minute', 'Weekday',
  'HasMessages', 'LastMessageAt', 'IdleSeconds',
  'ExecutionsToday', 'HasLastStarted', 'LastStartedAt', 'HasLastSucceeded', 'LastSucceededAt',
] as const

export interface ConfigurationByKind {
  identities: IdentityConfiguration
  impressions: ImpressionConfiguration
  'tool-groups': ToolGroupConfiguration
  concierges: ConciergeConfiguration
  workflows: WorkflowConfiguration
  jobs: JobConfiguration
}

export type Configuration = ConfigurationByKind[ConfigurationKind]

export interface ConfigurationCatalog {
  identities: string[]
  impressions: string[]
  tool_groups: string[]
  concierges: string[]
  workflows: string[]
  jobs: string[]
  tools: string[]
  plugins: string[]
}
