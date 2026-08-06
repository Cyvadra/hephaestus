// Package compress replaces a run of chat history with a shorter
// {role, content} message sequence, via a direct LLM call outside of any
// Concierge/Identity pipeline. Failure here is handled by the caller exactly
// like /stop: the session's state is left unchanged.
package compress

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Cyvadra/hephaestus/internal/llm"
)

// EstimateDivisor is the runes-per-token-unit used to estimate context
// length without a real tokenizer, per the design doc's "no tokenizer for
// now" decision.
//
// TODO: revisit once a real tokenizer (or provider-reported usage) is wired
// in; this is a deliberately rough estimate.
const EstimateDivisor = 2.0

// EstimateLength returns an approximate context-length unit for text, using
// rune count divided by EstimateDivisor rather than a real tokenizer.
func EstimateLength(text string) int {
	return int(float64(len([]rune(text))) / EstimateDivisor)
}

// Message is a compacted {role, content} entry. Role must be "user" or
// "assistant"; Compression never stores a "system" message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// systemPrompt instructs the model to produce a compacted message sequence.
//
// TODO: this is a first-pass template. Study how Codex, GitHub Copilot and
// Claude Code implement conversation compression before refining it.
const systemPrompt = `You compress chat history into a shorter sequence of ` +
	`messages that preserves the essential facts, decisions and open ` +
	`threads a conversation needs to continue naturally. Respond with ONLY ` +
	`a JSON array of objects, each shaped as {"role": "user"|"assistant", ` +
	`"content": "..."}. Do not include any role other than "user" or ` +
	`"assistant". Do not include any text outside the JSON array.`

// Compress asks the model to replace messages with a shorter equivalent
// sequence targeting approximately expectedLength estimated units (see
// EstimateLength). It returns an error if the call fails or the model's
// response cannot be parsed as a valid user/assistant-only message array;
// callers must treat that exactly like /stop and leave session state as-is.
func Compress(ctx context.Context, client *llm.Client, messages []Message, expectedLength int) ([]Message, error) {
	input, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("compress: marshal input messages: %w", err)
	}

	userPrompt := fmt.Sprintf(
		"Target length (estimated units): %d\n\nMessages to compress (JSON):\n%s",
		expectedLength, string(input),
	)

	content, err := client.RawCall(ctx, systemPrompt, userPrompt, maxOutputTokens(expectedLength))
	if err != nil {
		return nil, fmt.Errorf("compress: llm call: %w", err)
	}

	var out []Message
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil, fmt.Errorf("compress: parse model output as JSON message array: %w", err)
	}
	for i, m := range out {
		if m.Role != "user" && m.Role != "assistant" {
			return nil, fmt.Errorf("compress: message %d has disallowed role %q", i, m.Role)
		}
	}

	return out, nil
}

func maxOutputTokens(expectedLength int) int {
	// Generous headroom over the target so the model isn't cut off
	// mid-JSON; still bounded so a runaway response can't consume the
	// whole context budget it's trying to free up.
	const headroomMultiplier = 3
	const minTokens = 512
	tokens := expectedLength * headroomMultiplier
	if tokens < minTokens {
		return minTokens
	}
	return tokens
}
