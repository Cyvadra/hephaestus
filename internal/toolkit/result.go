package toolkit

import "strings"

// artifactPathsNote points the LLM at send_file for delivering an artifact
// a tool produced but did not itself deliver to the user.
const artifactPathsNote = "Use `send_file` with one of these paths to send it to the user."

// ToolResult is the structured return value from tool execution. Only the
// LLM-facing content is consumed by the pipeline: ForLLM is what the model
// sees, IsError marks a failure, and ArtifactTags exposes local artifact
// paths the model may want to deliver.
type ToolResult struct {
	// ForLLM is the content appended to the conversation for the model.
	ForLLM string
	// IsError marks this result as a failure.
	IsError bool
	// Deliveries are files the tool explicitly asked the host to attach to
	// the final assistant message. They are not exposed to the LLM context.
	Deliveries []FileDelivery
	// ArtifactTags exposes local artifact paths back to the LLM when a
	// tool produced a reusable artifact but did not deliver it yet.
	ArtifactTags []string
}

// FileDelivery identifies a file that may be delivered to the user. Path is
// relative to the session's Project root and must be revalidated before it is
// persisted or downloaded.
type FileDelivery struct {
	Path string
	Name string
	Size int64
	MIME string
}

// ContentForLLM returns the normalized text to append to the conversation
// as this tool call's result.
func (r *ToolResult) ContentForLLM() string {
	if r == nil {
		return ""
	}
	content := r.ForLLM
	if len(r.ArtifactTags) > 0 {
		note := "Local artifact paths: " + strings.Join(r.ArtifactTags, " ") + "\n" + artifactPathsNote
		if content == "" {
			content = note
		} else {
			content = strings.TrimSpace(content) + "\n" + note
		}
	}
	return content
}

// NewToolResult creates a basic result with content for the LLM.
func NewToolResult(forLLM string) *ToolResult {
	return &ToolResult{ForLLM: forLLM}
}

// SilentResult is NewToolResult for results that are only ever shown to the
// LLM (e.g. file reads/writes, status updates). It is a named constructor
// for readability at call sites.
func SilentResult(forLLM string) *ToolResult {
	return NewToolResult(forLLM)
}

// ErrorResult creates a result representing a tool execution failure.
func ErrorResult(message string) *ToolResult {
	return &ToolResult{ForLLM: message, IsError: true}
}
