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
  SourceConcierge: string
  Settings: { Data: SessionSettings }
  Title: string
  Summary: string
  ActiveLeafMessageID: number | null
  CompressionID: number | null
  CompressionLastMessageID: number | null
  FlagArchived: boolean
  CreatedAt: string
  UpdatedAt: string
}

export interface ChatMessage {
  ID: number
  SessionID: number
  ParentMessageID: number | null
  Timestamp: string
  Role: 'user' | 'assistant' | 'tool' | 'system'
  Content: string
  ReasoningContent: string
  ToolCalls: unknown
  ToolCallID: string
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
