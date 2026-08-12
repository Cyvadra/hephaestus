// Package workflow implements durable, isolated execution of a Workflow:
// each natural-language step runs as its own agent/tool loop in a selected
// Project, persisting its transcript and output before the next step starts.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
)

// buildStepPrompt renders the deterministic prompt for one workflow step:
// the workflow identity, its declared input, prior step outputs, and the
// current step's instructions.
func buildStepPrompt(wf registry.Workflow, input map[string]any, priorOutputs []string, index int) string {
	var b strings.Builder
	b.WriteString("You are executing one step of a workflow.\n")
	b.WriteString(fmt.Sprintf("Workflow: %s\n", wf.Name))
	if wf.Description != "" {
		b.WriteString(fmt.Sprintf("Description: %s\n", wf.Description))
	}
	if len(input) > 0 {
		if raw, err := json.Marshal(input); err == nil {
			b.WriteString(fmt.Sprintf("Workflow input: %s\n", raw))
		}
	}
	if len(priorOutputs) > 0 {
		b.WriteString("Outputs of prior completed steps:\n")
		for i, output := range priorOutputs {
			b.WriteString(fmt.Sprintf("Step %d: %s\n", i+1, output))
		}
	}
	b.WriteString(fmt.Sprintf("\nCurrent step %d of %d: %s\n", index+1, len(wf.Steps), wf.Steps[index]))
	b.WriteString("Execute only this step and report its result as your reply.")
	return b.String()
}

// stepOutput extracts a step's final assistant text from an agent result.
func stepOutput(result agent.Result) string {
	if n := len(result.Messages); n > 0 {
		return result.Messages[n-1].Content
	}
	return ""
}

// normalizeFinalOutput turns the final step's assistant text into the run's
// Output. With an output schema, the text must be a single complete JSON
// value satisfying it; without one, the text is persisted as a JSON string.
// Markdown fences are deliberately not stripped.
func normalizeFinalOutput(outputText string, wf registry.Workflow) ([]byte, error) {
	if len(wf.OutputSchema) == 0 {
		return json.Marshal(outputText)
	}
	compiled, err := registry.CompileSchema(wf.OutputSchema)
	if err != nil {
		return nil, fmt.Errorf("workflow: output schema: %w", err)
	}
	var value any
	if err := json.Unmarshal([]byte(strings.TrimSpace(outputText)), &value); err != nil {
		return nil, fmt.Errorf("workflow: final step output is not a single JSON value: %w", err)
	}
	if err := compiled.Validate(value); err != nil {
		return nil, fmt.Errorf("workflow: final step output does not satisfy the output schema: %w", err)
	}
	return json.Marshal(value)
}

// classifyStatus maps an execution outcome to a durable run status.
// Cancellation wins over generic retryable failures; fatal errors are
// decided before steps begin (input/schema/setup), so a step-level failure
// is retryable.
func classifyStatus(ctx context.Context, err error) (store.WorkflowRunStatus, string) {
	if err == nil {
		return store.WorkflowRunSucceeded, ""
	}
	if ctx.Err() != nil {
		return store.WorkflowRunCancelled, "cancelled: " + ctx.Err().Error()
	}
	return store.WorkflowRunFailed, err.Error()
}
