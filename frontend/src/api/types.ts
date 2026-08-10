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
}

export interface SendMessageResponse {
  command_response?: string
  message?: ChatMessage
  metadata?: Record<string, unknown>
}

export interface ConciergeItem {
  name: string
  identity: string
  impressions: string[]
  tool_groups: string[]
  plugins: string[]
}
