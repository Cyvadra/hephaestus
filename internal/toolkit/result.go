package toolkit

import "strings"

// artifactPathsNote points the LLM at send_file for delivering an artifact
// a tool produced but did not itself deliver to the user.
const artifactPathsNote = "Use `send_file` with one of these paths to send it to the user."

// ToolResult is the structured return value from tool execution. It
// separates what the LLM sees (ForLLM) from what the user sees (ForUser),
// and carries error/async/media metadata alongside.
type ToolResult struct {
	// ForLLM is the content appended to the conversation for the model.
	ForLLM string
	// ForUser is shown directly to the user; ignored when Silent is true.
	ForUser string
	// Silent suppresses ForUser even when set.
	Silent bool
	// IsError marks this result as a failure.
	IsError bool
	// Async indicates the tool's work continues in the background; the
	// caller must be an AsyncExecutor and later invoke its callback.
	Async bool
	// Media holds references to media produced by the tool (e.g. a
	// generated image), for the caller to surface separately from text.
	Media []string
	// ArtifactTags exposes local artifact paths back to the LLM when a
	// tool produced a reusable artifact but did not deliver it yet.
	ArtifactTags []string
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

// SilentResult creates a result whose content is only sent to the LLM,
// never surfaced to the user (e.g. file reads/writes, status updates).
func SilentResult(forLLM string) *ToolResult {
	return &ToolResult{ForLLM: forLLM, Silent: true}
}

// ErrorResult creates a result representing a tool execution failure.
func ErrorResult(message string) *ToolResult {
	return &ToolResult{ForLLM: message, ForUser: message, IsError: true}
}

// AsyncResult creates a result for a tool whose work will complete later
// via an AsyncExecutor callback.
func AsyncResult(forLLM string) *ToolResult {
	return &ToolResult{ForLLM: forLLM, Async: true}
}

// UserResult creates a result whose content is shown to both the LLM and
// the user (e.g. command output, fetched content, query results).
func UserResult(content string) *ToolResult {
	return &ToolResult{ForLLM: content, ForUser: content}
}

// MediaResult creates a result carrying media refs for the caller to
// publish alongside the LLM-facing content.
func MediaResult(forLLM string, media []string) *ToolResult {
	return &ToolResult{ForLLM: forLLM, Media: media}
}
