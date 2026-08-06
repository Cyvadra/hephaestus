// Package registry loads and validates the static config files (identity,
// impression, toolgroup, concierge, workflow, job) that are the single
// source of truth for these concepts. Nothing here is persisted to the
// database; everything is loaded fresh at process startup.
package registry

// Message is a single {role, content} entry used by Identity's injected
// messages and Impression's message sequence.
type Message struct {
	Role    string `toml:"role" yaml:"role"`
	Content string `toml:"content" yaml:"content"`
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
	Name        string `toml:"name"`
	Description string `toml:"description"`

	PreferredModel      string   `toml:"preferred_model"`
	ReasoningEffort     string   `toml:"reasoning_effort"`
	ContextWindowTokens int      `toml:"context_window_tokens"`
	MaxTokens           int      `toml:"max_tokens"`
	Temperature         *float64 `toml:"temperature"`
	TopP                *float64 `toml:"top_p"`

	SystemPrompt     string    `toml:"system_prompt"`
	InjectedMessages []Message `toml:"injected_messages"`
}

// Impression is a named, ordered message sequence appended after Identity's
// injected messages, toggled as a whole via Enabled.
type Impression struct {
	Name        string    `toml:"name"`
	Description string    `toml:"description"`
	Enabled     bool      `toml:"enabled"`
	Messages    []Message `toml:"messages"`
}

// ToolGroup is a named collection of actual tool names, used so clients
// select a group rather than individual tools.
type ToolGroup struct {
	Name  string   `yaml:"name"`
	Tools []string `yaml:"tools"`
}

// Concierge bundles an Identity with ordered lists of Impressions,
// ToolGroups and Plugins. It is a static definition with no runtime context.
type Concierge struct {
	Name        string   `yaml:"name"`
	Identity    string   `yaml:"identity"`
	Impressions []string `yaml:"impressions"`
	ToolGroups  []string `yaml:"tool_groups"`
	Plugins     []string `yaml:"plugins"`
}

// WorkflowStep is a single natural-language instruction executed by the LLM.
type Workflow struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Concierge    string   `yaml:"concierge"`
	InputSchema  any      `yaml:"input_schema"`
	OutputSchema any      `yaml:"output_schema"`
	Steps        []string `yaml:"steps"`
}

// JobWorkflowBinding configures one Workflow invocation triggered by a Job,
// including its independent retry policy.
type JobWorkflowBinding struct {
	Workflow          string `yaml:"workflow"`
	RetryDelaySeconds int    `yaml:"retry_delay_seconds"`
	RetryCount        int    `yaml:"retry_count"`
}

// Job is a scheduling definition: a trigger condition plus the Workflows it
// runs when that condition fires.
type Job struct {
	Name        string               `yaml:"name"`
	Title       string               `yaml:"title"`
	Description string               `yaml:"description"`
	Goal        string               `yaml:"goal"`
	Workflows   []JobWorkflowBinding `yaml:"workflows"`
	// Trigger is an expr-lang/expr expression, evaluated in the host's
	// local timezone.
	Trigger             string `yaml:"trigger"`
	MaxExecutionsPerDay int    `yaml:"max_executions_per_day"`
}
