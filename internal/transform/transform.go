// Package transform centralizes every platform-internal, single-shot LLM
// text transform: conversation compression, web-content summarization, and
// (over time) other structured rewrites. It is the single home for the
// system/user prompt templates behind each transform, and every transform
// is a plain method call on an *llm.Client via llm.RawCall — deliberately
// outside any Identity/Concierge pipeline.
package transform

import (
	"context"
	"errors"
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
	// Tool output (e.g. a shell command run against a binary file) can
	// contain raw NUL bytes, which Postgres text columns reject outright
	// (SQLSTATE 22021). Strip them before this content is ever persisted.
	content = strings.ReplaceAll(content, "\x00", "")
	return LimitTextBytes(content, remaining)
}

// TruncateRunes returns at most limit runes of content and the number of
// runes dropped (zero when content already fits).
func TruncateRunes(content string, limit int) (string, int) {
	runes := []rune(content)
	if len(runes) <= limit {
		return content, 0
	}
	return string(runes[:limit]), len(runes) - limit
}

// LimitTextBytes preserves the beginning and end of UTF-8 text within a byte
// budget. The returned string is always valid UTF-8 and never exceeds limit.
func LimitTextBytes(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	marker := "\n[... middle omitted ...]\n"
	if len(marker) >= limit {
		return TrimUTF8Prefix(content, limit)
	}
	retained := limit - len(marker)
	headLimit := retained / 2
	tailLimit := retained - headLimit
	head := TrimUTF8Prefix(content, headLimit)
	tail := TrimUTF8Suffix(content, tailLimit)
	return head + marker + tail
}

// TrimUTF8Prefix returns the longest prefix of content within limit bytes
// that ends on a rune boundary.
func TrimUTF8Prefix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	end := limit
	for end > 0 && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[:end]
}

// TrimUTF8Suffix returns the longest suffix of content within limit bytes
// that starts on a rune boundary.
func TrimUTF8Suffix(content string, limit int) string {
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

// Message is a stored checkpoint entry: the replacement user message that
// buildContext replays. New rows hold exactly one framed checkpoint, but
// historical rows may still hold a multi-message compacted sequence.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompressMessage is one history entry handed to the summarizer. It is
// separate from Message so stored rows and buildContext replay stay
// {role, content} only. ToolCalls is the raw tool_calls payload and
// ToolCallID links a tool result to its call.
type CompressMessage struct {
	Role       string
	Content    string
	ToolCalls  string
	ToolCallID string
}

// checkpointPreamble tells the resumed model that the preceding checkpoint is
// established background, so it neither restates nor acknowledges it.
const checkpointPreamble = "This is a generated checkpoint condensing an " +
	"earlier span of the conversation to free up context. Treat the captured " +
	"context as established background and build on it without restating it. " +
	"Continue the task directly from the messages that follow, without " +
	"acknowledging this checkpoint."

const (
	summaryOpenTag  = "<compacted-summary>"
	summaryCloseTag = "</compacted-summary>"
)

var (
	// ErrCompressionEmpty reports a model response with no checkpoint text.
	ErrCompressionEmpty = errors.New("transform: compress: model returned empty output")
	// ErrCompressionNotSmaller reports a checkpoint that does not shrink the
	// history it would replace. Callers may keep the uncompressed context.
	ErrCompressionNotSmaller = errors.New("transform: compress: checkpoint does not shrink input")
)

// compressionSystemPrompt instructs the model to produce a structured
// checkpoint that a resumed model can continue from.
const compressionSystemPrompt = `You are now acting as a compaction engine for this AI coding assistant. Condense the conversation provided below into a structured checkpoint that lets another model resume the work with no loss of essential context.

Output EXACTLY the Markdown structure below: keep every section, in order. Use terse bullets, not prose paragraphs. Write "(none)" for an empty section - never drop a section.

## Primary Request and Intent
- [the user's original and evolving goals; quote verbatim where the exact wording matters]

## Key Technical Concepts
- [technologies, frameworks, patterns, and conventions in play]

## Files and Code
- [exact path: why it matters, key changes or snippets]

## Errors and Fixes
- [error: how it was resolved, plus any related user feedback]

## Pending Jobs
- [explicitly requested work not yet completed]

## Current Work
- [precisely what was in progress at this checkpoint]

## Next Step
- [the single next action, directly in line with the most recent request, or "(none)"]

## Critical Context
- [decisions and their rationale, constraints, user preferences, open questions, data needed to continue]

Rules:
- Write concise English engineering prose. Preserve exact file paths, commands, error strings, identifiers, numeric values, function signatures, and syntax fragments.
- Capture user feedback and explicit instructions faithfully, especially corrections.
- Do NOT mention this summarization request or that the context was compacted.
- Output only the checkpoint text: no code fences, preambles, or commentary, and no <compacted-summary> tags; the caller adds them.
- The user prompt may include a PRIOR checkpoint. Do not copy it forward verbatim: preserve still-true facts, drop stale ones, and merge newer information into a single consolidated summary under the same structure.`

// Compress asks the model to condense prior and messages into one checkpoint
// targeting approximately expectedLength estimated units (see EstimateLength).
// prior is the checkpoint being replaced; messages are the newly covered
// history. It returns one replacement user message framed with
// checkpointPreamble and <compacted-summary> tags. An empty model response
// yields ErrCompressionEmpty, and a checkpoint no smaller than prior+messages
// yields ErrCompressionNotSmaller; other failures come from the LLM call.
// Callers must treat failures exactly like /stop and leave session state
// as-is, except that ErrCompressionNotSmaller may be skipped harmlessly.
func Compress(ctx context.Context, client *llm.Client, prior []Message, messages []CompressMessage, expectedLength int) (Message, error) {
	userPrompt := compressionPrompt(prior, messages, expectedLength)

	content, err := client.RawCall(ctx, compressionSystemPrompt, userPrompt, maxOutputTokens(expectedLength))
	if err != nil {
		return Message{}, fmt.Errorf("transform: compress: llm call: %w", err)
	}

	// Postgres jsonb rejects \u0000, and this text is persisted in the
	// compression row.
	content = strings.ReplaceAll(content, "\x00", "")
	content = strings.TrimSpace(content)
	if content == "" {
		return Message{}, ErrCompressionEmpty
	}

	framed := checkpointPreamble + "\n\n" + summaryOpenTag + "\n" + content + "\n" + summaryCloseTag
	if EstimateLength(framed) >= priorUnits(prior)+compressSourceUnits(messages) {
		return Message{}, ErrCompressionNotSmaller
	}

	return Message{Role: "user", Content: framed}, nil
}

// compressionPrompt renders the checkpoint request as a readable transcript.
// Raw content is used rather than JSON escaping so paths, commands, and
// newlines stay legible to the summarizer.
func compressionPrompt(prior []Message, messages []CompressMessage, expectedLength int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Target length (estimated units): %d\n\n", expectedLength)
	if len(prior) > 0 {
		b.WriteString("Prior checkpoint to merge (may be empty):\n")
		for _, m := range prior {
			writeTranscriptMessage(&b, CompressMessage{Role: m.Role, Content: m.Content})
		}
	}
	b.WriteString("Messages to compress:\n")
	for _, m := range messages {
		writeTranscriptMessage(&b, m)
	}
	return b.String()
}

// writeTranscriptMessage appends one role-tagged entry, including tool-call
// arguments and the call id that pairs a result with its request.
func writeTranscriptMessage(b *strings.Builder, m CompressMessage) {
	b.WriteString("[")
	b.WriteString(m.Role)
	if m.ToolCallID != "" {
		b.WriteString(" call_id=")
		b.WriteString(m.ToolCallID)
	}
	b.WriteString("]\n")
	b.WriteString(m.Content)
	if m.ToolCalls != "" {
		if m.Content != "" {
			b.WriteString("\n")
		}
		b.WriteString("tool_calls: ")
		b.WriteString(m.ToolCalls)
	}
	b.WriteString("\n\n")
}

func priorUnits(prior []Message) int {
	total := 0
	for _, m := range prior {
		total += EstimateLength(m.Content)
	}
	return total
}

func compressSourceUnits(messages []CompressMessage) int {
	total := 0
	for _, m := range messages {
		total += EstimateLength(m.Content) + EstimateLength(m.ToolCalls)
	}
	return total
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
