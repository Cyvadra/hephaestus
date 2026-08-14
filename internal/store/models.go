// Package store defines the GORM-backed runtime data models (session, chat
// history, compression) and their Postgres migration. No raw SQL is used
// anywhere; AutoMigrate is the only schema management mechanism.
package store

import (
	"time"

	"gorm.io/datatypes"
)

const DefaultProjectName = "default-workspace"

const (
	MessageStatusComplete   = "complete"
	MessageStatusIncomplete = "incomplete"
)

// ChatRunStatus is the durable lifecycle state of a background chat turn.
type ChatRunStatus string

const (
	ChatRunPending     ChatRunStatus = "pending"
	ChatRunRunning     ChatRunStatus = "running"
	ChatRunSucceeded   ChatRunStatus = "succeeded"
	ChatRunFailed      ChatRunStatus = "failed"
	ChatRunCancelled   ChatRunStatus = "cancelled"
	ChatRunInterrupted ChatRunStatus = "interrupted"
)

// IsTerminal reports whether a chat run can no longer receive progress.
func (s ChatRunStatus) IsTerminal() bool {
	return s == ChatRunSucceeded || s == ChatRunFailed || s == ChatRunCancelled || s == ChatRunInterrupted
}

// ChatRunKind identifies the pipeline entry point being executed.
type ChatRunKind string

const (
	ChatRunMessage    ChatRunKind = "message"
	ChatRunRegenerate ChatRunKind = "regenerate"
	ChatRunContinue   ChatRunKind = "continue"
)

// ChatRunSnapshot is the terminal aggregate retained for quick inspection.
type ChatRunSnapshot struct {
	Sequence         uint64         `json:"sequence"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content"`
	ToolCalls        datatypes.JSON `json:"tool_calls"`
	Interaction      datatypes.JSON `json:"interaction"`
	SessionUpdate    datatypes.JSON `json:"session_update"`
}

// ChatRunEvent is one durable progress update. Its sequence is monotonically
// increasing within a run and permits exact replay after a reconnect.
type ChatRunEvent struct {
	ID        uint           `gorm:"primaryKey;autoIncrement"`
	RunID     uint           `gorm:"not null;index:idx_chat_run_events_sequence,unique,priority:1"`
	Sequence  uint64         `gorm:"not null;index:idx_chat_run_events_sequence,unique,priority:2"`
	Type      string         `gorm:"size:32;not null"`
	Payload   datatypes.JSON `gorm:"type:jsonb;not null"`
	CreatedAt time.Time
}

// ChatRun represents one durable, background chat generation. A partial
// unique index permits at most one pending/running run for each session.
type ChatRun struct {
	ID        uint          `gorm:"primaryKey;autoIncrement"`
	SessionID uint          `gorm:"not null;index:idx_chat_runs_active_session,unique,where:status = 'pending' OR status = 'running';index"`
	ProjectID uint          `gorm:"not null;index"`
	Kind      ChatRunKind   `gorm:"size:32;not null"`
	Status    ChatRunStatus `gorm:"size:32;not null;index"`

	// Request stores immutable normalized turn input/options for diagnosis.
	Request  datatypes.JSONType[map[string]any]  `gorm:"type:jsonb"`
	Snapshot datatypes.JSONType[ChatRunSnapshot] `gorm:"type:jsonb"`
	Result   datatypes.JSON                      `gorm:"type:jsonb"`

	FinalMessageID *uint
	Error          string `gorm:"type:text"`
	StartedAt      *time.Time
	FinishedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Terminal implements runctrl.TerminalRow.
func (r ChatRun) Terminal() bool { return r.Status.IsTerminal() }

// SessionSettings is the mutable, per-session snapshot of which identity,
// impressions, tool groups and plugins are active. It starts as a copy of
// the source Concierge and may diverge from it over the session's lifetime.
type SessionSettings struct {
	Identity    string   `json:"identity"`
	Impressions []string `json:"impressions"`
	ToolGroups  []string `json:"tool_groups"`
	Plugins     []string `json:"plugins"`

	// Project is retained only to migrate legacy sessions to Session.ProjectID.
	Project string `json:"project"`
}

// Session is a real, addressable conversation.
type Session struct {
	ID        uint    `gorm:"primaryKey;autoIncrement"`
	ProjectID uint    `gorm:"not null;index"`
	Project   Project `gorm:"foreignKey:ProjectID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;"`

	// SourceConcierge is the Concierge name used at creation time, kept
	// for reference only; it has no further business influence.
	SourceConcierge string `gorm:"size:255"`

	// Settings is the live, mutable configuration for this session.
	Settings datatypes.JSONType[SessionSettings] `gorm:"type:jsonb"`

	// ReasoningEffort is this session's persisted thinking mode
	// (none/low/high/max), initialized from the identity at creation and
	// updated whenever the user changes the composer control. Empty is
	// backward compatible with legacy sessions, treated as the identity
	// default by clients.
	ReasoningEffort string `gorm:"size:32"`

	// EnableWebSearch persists whether the web_search/web_fetch tools stay
	// enabled for this session. Nil means "not yet set" and is treated as
	// enabled by clients, keeping legacy sessions backward compatible.
	EnableWebSearch *bool

	// Title and Summary are maintained by the session-summary Plugin
	// (title <=20 chars, summary <=300 chars per design doc); both are
	// empty until that plugin has run at least once.
	Title   string `gorm:"size:64"`
	Summary string `gorm:"size:1024"`

	// ActiveLeafMessageID points at the tip of the currently active
	// branch in ChatMessage; nil for a session with no messages yet.
	ActiveLeafMessageID *uint

	// CompressionID references the most recently applicable Compression
	// row for this session, if any.
	CompressionID *uint
	// CompressionLastMessageID is the ChatMessage id up to which
	// CompressionID's compression covers.
	CompressionLastMessageID *uint

	FlagArchived bool  `gorm:"default:false"`
	FlagPinned   uint8 `gorm:"default:0"`

	// LastMessageTime is the activity time of the current active message
	// branch. It is independent from UpdatedAt so metadata writes do not
	// affect conversation ordering.
	LastMessageTime time.Time `gorm:"not null;default:CURRENT_TIMESTAMP;index:idx_session_last_message_time"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChatMessage is one node in a session's full, unpruned message tree.
// Branching is reconstructed in memory by walking ParentMessageID from a
// Session's ActiveLeafMessageID.
type ChatMessage struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	SessionID uint `gorm:"index:idx_session_timestamp,priority:1"`

	// ParentMessageID is nil for the first message of a session.
	ParentMessageID *uint

	Timestamp time.Time `gorm:"index:idx_session_timestamp,priority:2"`

	Role    string `gorm:"size:32"`
	Content string `gorm:"type:text"`
	// Status is complete unless generation ended before the Agent finished.
	Status string `gorm:"size:32;default:complete"`

	// ReasoningContent holds chain-of-thought output, when the model
	// provided one.
	ReasoningContent string `gorm:"type:text"`

	// ToolCalls is the raw tool_calls payload for an assistant message,
	// or empty for other roles.
	ToolCalls datatypes.JSON `gorm:"type:jsonb"`

	// ToolCallID links a tool-role message back to the assistant
	// message's tool_calls entry it answers.
	ToolCallID string `gorm:"size:255"`

	// Attachments are files explicitly delivered by the assistant. They are
	// loaded for API responses and never included in LLM context messages.
	Attachments []MessageAttachment `gorm:"foreignKey:MessageID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

// MessageAttachment is a project-relative reference to a file delivered by
// an assistant message. The source file is not copied: it is revalidated when
// downloaded, so its contents may change or become unavailable later.
type MessageAttachment struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	SessionID uint `gorm:"not null;index"`
	MessageID uint `gorm:"not null;index"`
	ProjectID uint `gorm:"not null;index"`

	Path string `gorm:"size:2048;not null"`
	Name string `gorm:"size:1024;not null"`
	Size int64
	MIME string `gorm:"size:255"`

	CreatedAt time.Time
}

// Compression is a compacted replacement for a contiguous message range,
// generated by a direct LLM call outside of any Concierge pipeline.
type Compression struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	SessionID      uint `gorm:"index"`
	FirstMessageID uint
	LastMessageID  uint

	// Messages is the compacted {role, content} sequence, stored as JSON
	// text. Only "user" and "assistant" roles are allowed.
	Messages datatypes.JSON `gorm:"type:jsonb"`

	CreatedAt time.Time
}

// Project is a named, on-disk folder the Agent may create (gated behind an
// opt-in ToolGroup so creation only happens with the user's explicit
// authorization) to scope file/shell tools and take memory-retrieval
// priority over raw chat history. Its directory lives under the process's
// projects root and is named after it, with a skeleton AGENTS.md inside.
type Project struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	Name                   string   `gorm:"size:255;uniqueIndex"`
	Description            string   `gorm:"size:1024"`
	AvailableConciergeList []string `json:"available_concierge_list" gorm:"serializer:json;type:jsonb"`

	CreatedAt time.Time
}

// PluginState is a generic per-session, per-plugin key-value store, so any
// Plugin can persist its own state (e.g. a storyline snapshot, the last
// time a session was summarized) without a one-off table per plugin.
type PluginState struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	SessionID  uint   `gorm:"uniqueIndex:idx_session_plugin"`
	PluginName string `gorm:"size:255;uniqueIndex:idx_session_plugin"`

	Data datatypes.JSON `gorm:"type:jsonb"`

	UpdatedAt time.Time
}

// ToolAudit records externally visible tool actions even when a turn aborts.
// Ownership is scope-relative: a chat turn sets SessionID; a workflow step
// sets WorkflowRunID and WorkflowStepRunID. Exactly one group is set.
type ToolAudit struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	SessionID         *uint          `gorm:"index"`
	WorkflowRunID     *uint          `gorm:"index"`
	WorkflowStepRunID *uint          `gorm:"index"`
	ToolCallID        string         `gorm:"size:255;index"`
	ToolName          string         `gorm:"size:255"`
	Arguments         datatypes.JSON `gorm:"type:jsonb"`
	Result            string         `gorm:"type:text"`
	IsError           bool

	CreatedAt time.Time
	UpdatedAt time.Time
}
