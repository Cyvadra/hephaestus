// Package transform centralizes every platform-internal, single-shot LLM
// text transform: conversation compression, web-content summarization, and
// (over time) other structured rewrites. It is the single home for the
// system/user prompt templates behind each transform, and every transform
// is a plain method call on an *llm.Client via llm.RawCall — deliberately
// outside any Identity/Concierge pipeline.
package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Cyvadra/hephaestus/internal/llm"
)

// EstimateDivisor is the runes-per-token-unit used to estimate context
// length without a real tokenizer, per the design doc's "no tokenizer for
// now" decision.
//
// TODO: revisit once a real tokenizer (or provider-reported usage) is wired
// in; this is a deliberately rough estimate.
const EstimateDivisor = 2.0

// MaxToolExchangeBytes is the exclusive byte ceiling for one tool call's
// serialized arguments plus any result exposed or persisted by the runtime.
const MaxToolExchangeBytes = 256 * 1024

// LimitToolExchangeContent bounds content so arguments plus the returned text
// remain strictly below MaxToolExchangeBytes.
func LimitToolExchangeContent(arguments, content string) string {
	remaining := MaxToolExchangeBytes - 1 - len(arguments)
	if remaining <= 0 {
		return ""
	}
	return LimitTextBytes(content, remaining)
}

// LimitTextBytes preserves the beginning and end of UTF-8 text within a byte
// budget. The returned string is always valid UTF-8 and never exceeds limit.
func LimitTextBytes(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	marker := "\n[... middle omitted ...]\n"
	if len(marker) >= limit {
		return trimUTF8Prefix(content, limit)
	}
	retained := limit - len(marker)
	headLimit := retained / 2
	tailLimit := retained - headLimit
	head := trimUTF8Prefix(content, headLimit)
	tail := trimUTF8Suffix(content, tailLimit)
	return head + marker + tail
}

func trimUTF8Prefix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	end := limit
	for end > 0 && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[:end]
}

func trimUTF8Suffix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	start := len(content) - limit
	for start < len(content) && !utf8.RuneStart(content[start]) {
		start++
	}
	return content[start:]
}

// EstimateLength returns an approximate context-length unit for text, using
// rune count divided by EstimateDivisor rather than a real tokenizer.
func EstimateLength(text string) int {
	return int(float64(len([]rune(text))) / EstimateDivisor)
}

// Message is a compacted {role, content} entry. Role must be "user" or
// "assistant"; a compressed sequence never stores a "system" message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// compressionSystemPrompt instructs the model to produce a compacted
// message sequence.
//
// TODO: this is a first-pass template. Study how Codex, GitHub Copilot and
// Claude Code implement conversation compression before refining it.
const compressionSystemPrompt = `You compress chat history into a shorter sequence of ` +
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
		return nil, fmt.Errorf("transform: compress: marshal input messages: %w", err)
	}

	userPrompt := fmt.Sprintf(
		"Target length (estimated units): %d\n\nMessages to compress (JSON):\n%s",
		expectedLength, string(input),
	)

	content, err := client.RawCall(ctx, compressionSystemPrompt, userPrompt, maxOutputTokens(expectedLength))
	if err != nil {
		return nil, fmt.Errorf("transform: compress: llm call: %w", err)
	}

	var out []Message
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil, fmt.Errorf("transform: compress: parse model output as JSON message array: %w", err)
	}
	for i, m := range out {
		if m.Role != "user" && m.Role != "assistant" {
			return nil, fmt.Errorf("transform: compress: message %d has disallowed role %q", i, m.Role)
		}
	}

	return out, nil
}

// summarizeSystemPrompt instructs the model to condense fetched content into
// a compact digest that still preserves what a reader actually needs.
const summarizeSystemPrompt = `You summarize web page content into a concise ` +
	`digest that preserves the essential facts, key figures, names, dates, ` +
	`decisions and any directly relevant quotes. Respond with ONLY the summary ` +
	`text, in the same language as the source text. Do not include commentary, ` +
	`meta-notes, headings like "Summary:", or any text outside the summary.`

// Summarize condenses text (typically a fetched web page or a page of search
// results) into a shorter digest of at most maxOutputLen estimated units (see
// EstimateLength). It returns an error if the call fails or the model returns
// empty output; callers may fall back to returning the raw text.
func Summarize(ctx context.Context, client *llm.Client, text string, maxOutputLen int) (string, error) {
	userPrompt := fmt.Sprintf(
		"Target length (estimated units): %d\n\nText to summarize:\n%s",
		maxOutputLen, text,
	)

	content, err := client.RawCall(ctx, summarizeSystemPrompt, userPrompt, maxOutputTokens(maxOutputLen))
	if err != nil {
		return "", fmt.Errorf("transform: summarize: llm call: %w", err)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("transform: summarize: model returned empty output")
	}
	return content, nil
}

// sessionTitleSummarySystemPrompt drives the fixed session_summary plugin's
// side call for titles and summaries.
const sessionTitleSummarySystemPrompt = "You produce concise session titles and summaries."

// SessionTitleSummary asks the model to produce a session title and summary
// from the caller-built prompt, returning the raw two-line model output.
func SessionTitleSummary(ctx context.Context, client *llm.Client, prompt string, maxTokens int) (string, error) {
	return raw(ctx, client, "session title/summary", sessionTitleSummarySystemPrompt, prompt, maxTokens)
}

// suggestOptionsSystemPrompt drives the fixed options plugin's suggestion
// call.
const suggestOptionsSystemPrompt = "You suggest brief next-message options for a chat user."

// SuggestOptions asks the model to suggest next-message options from the
// caller-built prompt, returning the raw model output (a JSON array of
// strings).
func SuggestOptions(ctx context.Context, client *llm.Client, prompt string, maxTokens int) (string, error) {
	return raw(ctx, client, "suggest options", suggestOptionsSystemPrompt, prompt, maxTokens)
}

// storylineStatusSystemPrompt drives the fixed storyline_status plugin's
// status-line update call.
const storylineStatusSystemPrompt = "You track and update a compact status line for an ongoing storyline."

// StorylineStatus asks the model to produce the updated one-line status from
// the caller-built prompt, returning the raw trimmed model output.
func StorylineStatus(ctx context.Context, client *llm.Client, prompt string, maxTokens int) (string, error) {
	return raw(ctx, client, "storyline status", storylineStatusSystemPrompt, prompt, maxTokens)
}

// searchResultsSystemPrompt instructs the model to condense a ranked list of
// search results into a compact digest that keeps every result's URL.
const searchResultsSystemPrompt = `You condense a ranked list of web search results ` +
	`into a compact digest. Keep every result in order, preserving its URL and ` +
	`a one-line gist of its title and snippet; drop only redundant or empty ` +
	`snippets. Respond with ONLY the digest, in the same language as the ` +
	`results. Do not include commentary or any text outside the digest.`

// SummarizeSearchResults condenses a rendered ranked list of search results
// into a digest of at most maxOutputLen estimated units, preserving every
// result's URL. Callers may fall back to the raw rendered list on error.
func SummarizeSearchResults(ctx context.Context, client *llm.Client, rendered string, maxOutputLen int) (string, error) {
	userPrompt := fmt.Sprintf(
		"Target length (estimated units): %d\n\nSearch results:\n%s",
		maxOutputLen, rendered,
	)

	content, err := client.RawCall(ctx, searchResultsSystemPrompt, userPrompt, maxOutputTokens(maxOutputLen))
	if err != nil {
		return "", fmt.Errorf("transform: summarize search results: llm call: %w", err)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("transform: summarize search results: model returned empty output")
	}
	return content, nil
}

// raw is the shared single-shot call path: it names the transform for error
// context and trims the model output.
func raw(ctx context.Context, client *llm.Client, name, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	content, err := client.RawCall(ctx, systemPrompt, userPrompt, maxTokens)
	if err != nil {
		return "", fmt.Errorf("transform: %s: llm call: %w", name, err)
	}
	return strings.TrimSpace(content), nil
}

// maxOutputTokens bounds the response so the model isn't cut off mid-output,
// while still capping runaway responses that could consume the context the
// transform is trying to free up.
func maxOutputTokens(expectedLength int) int {
	// Generous headroom over the target so the model isn't cut off
	// mid-output; still bounded so a runaway response can't consume the
	// whole context budget it's trying to free up.
	const headroomMultiplier = 3
	const minTokens = 512
	tokens := expectedLength * headroomMultiplier
	if tokens < minTokens {
		return minTokens
	}
	return tokens
}
