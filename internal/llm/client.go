// Package llm adapts the platform's Identity config and ChatMessage history
// onto github.com/Cyvadra/ds4's DeepSeek chat-completions client. It is the
// only package that imports ds4 directly.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

const continuationMaxTokens = 8192

// Client wraps a ds4.Client with Identity-aware request building.
type Client struct {
	ds4 *ds4.Client
}

var (
	maxTokensRangePattern = regexp.MustCompile(`(?i)valid range of max_tokens is\s*\[\s*\d+\s*,\s*(\d+)\s*\]`)
)

// New creates a Client authenticated with apiKey.
func New(apiKey string) *Client {
	return &Client{ds4: ds4.New(apiKey)}
}

// NewWithLocalModel creates a Client that also routes model names advertised
// by a local OpenAI-compatible endpoint to that endpoint.
func NewWithLocalModel(apiKey, localURL, localAPIKey string) *Client {
	client := ds4.New(apiKey)
	if localURL != "" {
		client.WithLocalKey(localAPIKey).WithLocalURL(localURL)
	}
	return &Client{ds4: client}
}

// NewWithBaseURL creates a Client authenticated with apiKey that sends
// requests to baseURL instead of the provider default. It exists for
// self-hosted endpoints and for tests that point at a stub server.
func NewWithBaseURL(apiKey, baseURL string) *Client {
	return &Client{ds4: ds4.New(apiKey).WithBaseURL(baseURL)}
}

// Call sends messages (in order) to the model configured by identity,
// optionally offering toolset, and returns the raw ds4 response.
//
// messages must already include every system/injected/impression/history
// entry the caller wants in context; Call does not add anything beyond
// identity's own system prompt and injected messages.
func (c *Client) Call(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool) (*ds4.ChatResponse, error) {
	builder, err := c.buildChat(ctx, identity, messages, toolset)
	if err != nil {
		return nil, err
	}
	resp, err := builder.DoWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("llm: chat completion: %w", err)
	}
	return resp, nil
}

// CallWithoutThinking sends the same identity configuration and message
// context as Call, but disables thinking and tools for a structured side call.
func (c *Client) CallWithoutThinking(ctx context.Context, identity registry.Identity, messages []store.ChatMessage) (string, error) {
	identity.ReasoningEffort = registry.ReasoningNone
	builder, err := c.buildChat(ctx, identity, messages, nil)
	if err != nil {
		return "", err
	}
	resp, err := builder.DoWithContext(ctx)
	if err != nil {
		return "", fmt.Errorf("llm: chat completion without thinking: %w", err)
	}
	return resp.Content(), nil
}

// StreamDelta is a normalized incremental update surfaced by CallStream.
type StreamDelta struct {
	Content          string
	ReasoningContent string
	ToolCalls        []ToolCallDelta
}

// IncompleteResponseError retains the response assembled before a streaming
// request ended unsuccessfully, allowing callers to persist visible output.
type IncompleteResponseError struct {
	Message ds4.Message
	Err     error
}

func (e *IncompleteResponseError) Error() string { return e.Err.Error() }

func (e *IncompleteResponseError) Unwrap() error { return e.Err }

// ToolCallDelta is the provider-independent incremental portion of a tool
// call. Arguments may arrive over multiple deltas for the same Index.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// CallStream behaves like Call but streams content/reasoning deltas to
// onDelta as they arrive. It still returns the same synthesized
// *ds4.ChatResponse shape Call returns (by accumulating every streamed
// chunk, including tool_calls keyed by their index), so callers such as the
// tool loop don't need to special-case streaming vs non-streaming responses.
func (c *Client) CallStream(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool, onDelta func(StreamDelta)) (*ds4.ChatResponse, error) {
	builder, err := c.buildChat(ctx, identity, messages, toolset)
	if err != nil {
		return nil, err
	}
	return c.stream(ctx, builder, onDelta)
}

// ContinueStream expands an assistant message using ds4's chat prefix-
// completion API. messages must exclude prefix, which is appended as the
// final assistant response before Continue marks it as the request prefix.
// The provider rejects function calls with prefixes, so tools are disabled
// for this continuation request only. Its beta endpoint also rejects prior
// tool-call messages, so those history entries are excluded for this request.
// Thinking is disabled because the prefix's original reasoning is immutable.
func (c *Client) ContinueStream(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, prefix store.ChatMessage, onDelta func(StreamDelta)) (*ds4.ChatResponse, error) {
	identity.ReasoningEffort = registry.ReasoningNone
	if identity.MaxTokens == 0 || identity.MaxTokens > continuationMaxTokens {
		identity.MaxTokens = continuationMaxTokens
	}
	builder, err := c.buildChat(ctx, identity, withoutToolCalls(messages), nil)
	if err != nil {
		return nil, err
	}
	builder.AppendResponse(&ds4.ChatResponse{Choices: []ds4.Choice{{Message: store2ds4(prefix)}}}).Continue(0)
	return c.stream(ctx, builder, onDelta)
}

func withoutToolCalls(messages []store.ChatMessage) []store.ChatMessage {
	out := make([]store.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == ds4.RoleTool || len(message.ToolCalls) > 0 {
			continue
		}
		out = append(out, message)
	}
	return out
}

func (c *Client) stream(ctx context.Context, builder *ds4.ChatBuilder, onDelta func(StreamDelta)) (*ds4.ChatResponse, error) {

	var content, reasoning strings.Builder
	toolCalls := map[int]*ds4.ToolCall{}
	var toolCallOrder []int
	finishReason := ""

	err := builder.StreamWithContext(ctx, func(chunk ds4.ChatStreamChunk) error {
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
			reasoning.WriteString(choice.Delta.ReasoningContent)
			delta := StreamDelta{Content: choice.Delta.Content, ReasoningContent: choice.Delta.ReasoningContent}
			for _, tc := range choice.Delta.ToolCalls {
				existing, ok := toolCalls[tc.Index]
				if !ok {
					existing = &ds4.ToolCall{}
					toolCalls[tc.Index] = existing
					toolCallOrder = append(toolCallOrder, tc.Index)
				}
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Type != "" {
					existing.Type = tc.Type
				}
				if tc.Function.Name != "" {
					existing.Function.Name = tc.Function.Name
				}
				existing.Function.Arguments += tc.Function.Arguments
				delta.ToolCalls = append(delta.ToolCalls, ToolCallDelta{
					Index:     tc.Index,
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
			if onDelta != nil && (delta.Content != "" || delta.ReasoningContent != "" || len(delta.ToolCalls) > 0) {
				onDelta(delta)
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
		return nil
	})
	sort.Ints(toolCallOrder)
	calls := make([]ds4.ToolCall, 0, len(toolCallOrder))
	for _, idx := range toolCallOrder {
		calls = append(calls, *toolCalls[idx])
	}
	message := ds4.Message{
		Role:             ds4.RoleAssistant,
		Content:          content.String(),
		ReasoningContent: reasoning.String(),
		ToolCalls:        calls,
	}
	if err != nil {
		return nil, &IncompleteResponseError{
			Message: message,
			Err:     fmt.Errorf("llm: stream chat completion: %w", err),
		}
	}

	return &ds4.ChatResponse{
		Choices: []ds4.Choice{{
			Message:      message,
			FinishReason: finishReason,
		}},
	}, nil
}

// buildChat constructs the shared ChatBuilder state for both Call and
// CallStream from an Identity, its history, and its available tools.
func (c *Client) buildChat(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool) (*ds4.ChatBuilder, error) {
	builder := c.ds4.Chat().Model(modelOrDefault(identity.PreferredModel))

	builder.System(identity.SystemPrompt)
	for _, m := range identity.InjectedMessages {
		builder.AddMessage(ds4.Message{Role: m.Role, Content: m.Content})
	}
	for _, m := range messages {
		builder.AddMessage(store2ds4(m))
	}

	applyThinking(builder, identity.ReasoningEffort)

	if identity.MaxTokens > 0 {
		builder.MaxTokens(identity.MaxTokens)
	}
	if identity.Temperature != nil {
		builder.Temperature(*identity.Temperature)
	}
	if identity.TopP != nil {
		builder.TopP(*identity.TopP)
	}

	for _, t := range toolset {
		description := t.Description()
		// Tools that carry a concrete example (input + response data) get it
		// appended to their description whenever they are registered to a
		// request, so the model sees a real usage pair up front.
		if example, ok := t.(toolkit.Example); ok {
			if text := example.Example(); text != "" {
				description += "\n\nExample:\n" + text
			}
		}
		builder.Tool(ds4.NewFunction(t.Name(), description, t.Parameters()))
	}

	if err := addFinalUserImages(ctx, builder, messages); err != nil {
		return nil, err
	}
	return builder, nil
}

func addFinalUserImages(ctx context.Context, builder *ds4.ChatBuilder, messages []store.ChatMessage) error {
	if len(messages) == 0 {
		return nil
	}
	final := messages[len(messages)-1]
	if final.Role != ds4.RoleUser {
		return nil
	}
	workspace, ok := toolkit.WorkspaceFromContext(ctx)
	for _, attachment := range final.Attachments {
		if attachment.Kind != store.MessageAttachmentVisualInput {
			continue
		}
		if !ok {
			return fmt.Errorf("llm: read visual upload %q: workspace is unavailable", attachment.Name)
		}
		path, err := resolveVisualUpload(workspace, attachment.Path)
		if err != nil {
			return fmt.Errorf("llm: read visual upload %q: %w", attachment.Name, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("llm: read visual upload %q: %w", attachment.Name, err)
		}
		if !supportedVisualMIME(attachment.MIME) {
			return fmt.Errorf("llm: visual upload %q has unsupported MIME type %q", attachment.Name, attachment.MIME)
		}
		builder.WithImageBase64(data, attachment.MIME, ds4.ImageDetailOriginal)
	}
	return nil
}

func resolveVisualUpload(workspace, relativePath string) (string, error) {
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("attachment path must be relative")
	}
	candidate := filepath.Join(root, filepath.FromSlash(relativePath))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("attachment path escapes workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("attachment path is not a regular file")
	}
	return resolved, nil
}

func supportedVisualMIME(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func modelOrDefault(model string) string {
	if model == "" {
		return ds4.ModelDeepSeekV4Flash
	}
	return model
}

// RawCall issues a direct chat completion outside of any Identity/Concierge,
// for platform-internal callers (e.g. compression) that need their own
// private system/user prompts. Thinking is left disabled since these calls
// are structured, single-shot text transforms rather than conversation.
func (c *Client) RawCall(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	resp, err := c.rawCall(ctx, systemPrompt, userPrompt, maxTokens)
	if maxAllowed, ok := maxTokensUpperBound(err, maxTokens); ok {
		resp, err = c.rawCall(ctx, systemPrompt, userPrompt, maxAllowed)
	}
	if err != nil {
		return "", fmt.Errorf("llm: raw call: %w", err)
	}
	return resp.Content(), nil
}

func (c *Client) rawCall(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (*ds4.ChatResponse, error) {
	return c.ds4.Chat().
		Thinking(false).
		System(systemPrompt).
		User(userPrompt).
		MaxTokens(maxTokens).
		DoWithContext(ctx)
}

// maxTokensUpperBound returns the provider-reported maximum only for a 400
// max_tokens range error and only when it lowers the original request.
func maxTokensUpperBound(err error, requested int) (int, bool) {
	var apiErr *ds4.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		return 0, false
	}

	matches := maxTokensRangePattern.FindStringSubmatch(apiErr.Message)
	if len(matches) != 2 {
		return 0, false
	}
	maxAllowed, parseErr := strconv.Atoi(matches[1])
	if parseErr != nil || maxAllowed < 1 || maxAllowed >= requested {
		return 0, false
	}
	return maxAllowed, true
}

// applyThinking maps the platform's "none"/"low"/"high"/"max" reasoning
// effort onto ds4's Thinking toggle plus ReasoningEffort field. "none" is an
// alias for disabled thinking with no effort field sent at all.
func applyThinking(b *ds4.ChatBuilder, effort string) {
	if effort == "" || effort == registry.ReasoningNone {
		b.Thinking(false)
		return
	}
	b.Thinking(true)
	b.ReasoningEffort(effort)
}

func store2ds4(m store.ChatMessage) ds4.Message {
	msg := ds4.Message{
		Role:             m.Role,
		Content:          m.Content,
		ReasoningContent: m.ReasoningContent,
		ToolCallID:       m.ToolCallID,
	}
	// A tool-calling reasoning sequence must replay the full assistant
	// message, tool_calls included, or the API can reject the next call.
	if len(m.ToolCalls) > 0 {
		_ = json.Unmarshal(m.ToolCalls, &msg.ToolCalls)
	}
	return msg
}
