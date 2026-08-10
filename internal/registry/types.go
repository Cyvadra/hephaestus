// Package registry loads, persists, and validates identity, impression,
// toolgroup, concierge, workflow, and job configuration.
package registry

// Message is a single {role, content} entry used by Identity's injected
// messages and Impression's message sequence.
type Message struct {
	Role    string `toml:"role" yaml:"role" json:"role"`
	Content string `toml:"content" yaml:"content" json:"content"`
}

// DefaultSystemPrompt is used when an Identity omits SystemPrompt.
const DefaultSystemPrompt = "You're a helpful assistant."

// ReasoningEffort values accepted in Identity config.
const (
	ReasoningNone = "none"
	ReasoningLow  = "low"
	ReasoningHigh = "high"
	ReasoningMax  = "max"
)

// Identity is an agent's core, developer-maintained persona.
type Identity struct {
	Name        string `toml:"name" json:"name" gorm:"primaryKey;size:255"`
	Description string `toml:"description" json:"description" gorm:"type:text"`

	PreferredModel      string   `toml:"preferred_model" json:"preferred_model" gorm:"size:255"`
	ReasoningEffort     string   `toml:"reasoning_effort" json:"reasoning_effort" gorm:"size:32"`
	ContextWindowTokens int      `toml:"context_window_tokens" json:"context_window_tokens"`
	MaxTokens           int      `toml:"max_tokens" json:"max_tokens"`
	Temperature         *float64 `toml:"temperature" json:"temperature"`
	TopP                *float64 `toml:"top_p" json:"top_p"`

	SystemPrompt     string    `toml:"system_prompt" json:"system_prompt" gorm:"type:text"`
	InjectedMessages []Message `toml:"injected_messages" json:"injected_messages" gorm:"serializer:json;type:jsonb"`
}

// Impression is a named, ordered message sequence appended after Identity's
// injected messages, toggled as a whole via Enabled.
type Impression struct {
	Name        string    `toml:"name" json:"name" gorm:"primaryKey;size:255"`
	Description string    `toml:"description" json:"description" gorm:"type:text"`
	Enabled     bool      `toml:"enabled" json:"enabled"`
	Messages    []Message `toml:"messages" json:"messages" gorm:"serializer:json;type:jsonb"`
}

// ToolGroup is a named collection of actual tool names, used so clients
// select a group rather than individual tools.
type ToolGroup struct {
	Name  string   `yaml:"name" json:"name" gorm:"primaryKey;size:255"`
	Tools []string `yaml:"tools" json:"tools" gorm:"serializer:json;type:jsonb"`
}

// Concierge bundles an Identity with ordered lists of Impressions,
// ToolGroups and Plugins. It is a static definition with no runtime context.
type Concierge struct {
	Name        string   `yaml:"name" json:"name" gorm:"primaryKey;size:255"`
	Description string   `yaml:"description" json:"description" gorm:"type:text"`
	Identity    string   `yaml:"identity" json:"identity" gorm:"size:255"`
	Impressions []string `yaml:"impressions" json:"impressions" gorm:"serializer:json;type:jsonb"`
	ToolGroups  []string `yaml:"tool_groups" json:"tool_groups" gorm:"serializer:json;type:jsonb"`
	Plugins     []string `yaml:"plugins" json:"plugins" gorm:"serializer:json;type:jsonb"`
}

// Workflow is a named sequence of natural-language steps executed by the LLM
// via a Concierge. It is currently loaded and validated but not yet executed
// by any scheduler.
type Workflow struct {
	Name         string   `yaml:"name" json:"name" gorm:"primaryKey;size:255"`
	Description  string   `yaml:"description" json:"description" gorm:"type:text"`
	Concierge    string   `yaml:"concierge" json:"concierge" gorm:"size:255"`
	InputSchema  any      `yaml:"input_schema" json:"input_schema" gorm:"serializer:json;type:jsonb"`
	OutputSchema any      `yaml:"output_schema" json:"output_schema" gorm:"serializer:json;type:jsonb"`
	Steps        []string `yaml:"steps" json:"steps" gorm:"serializer:json;type:jsonb"`
}

// JobWorkflowBinding configures one Workflow invocation triggered by a Job,
// including its independent retry policy.
type JobWorkflowBinding struct {
	Workflow          string `yaml:"workflow" json:"workflow"`
	RetryDelaySeconds int    `yaml:"retry_delay_seconds" json:"retry_delay_seconds"`
	RetryCount        int    `yaml:"retry_count" json:"retry_count"`
}

// Job is a scheduling definition: a trigger condition plus the Workflows it
// runs when that condition fires.
type Job struct {
	Name        string               `yaml:"name" json:"name" gorm:"primaryKey;size:255"`
	Title       string               `yaml:"title" json:"title" gorm:"size:255"`
	Description string               `yaml:"description" json:"description" gorm:"type:text"`
	Goal        string               `yaml:"goal" json:"goal" gorm:"type:text"`
	Workflows   []JobWorkflowBinding `yaml:"workflows" json:"workflows" gorm:"serializer:json;type:jsonb"`
	// Trigger is an expr-lang/expr expression, evaluated in the host's
	// local timezone.
	Trigger             string `yaml:"trigger" json:"trigger" gorm:"type:text"`
	MaxExecutionsPerDay int    `yaml:"max_executions_per_day" json:"max_executions_per_day"`
}
