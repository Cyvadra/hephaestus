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
  Settings: { Data: SessionSettings }
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
  message?: ChatMessage
  metadata?: Record<string, unknown>
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
  identity: string
  impressions: string[]
  tool_groups: string[]
  plugins: string[]
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
  retry_delay_seconds: number
  retry_count: number
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
