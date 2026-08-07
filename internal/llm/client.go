// Package llm adapts the platform's Identity config and ChatMessage history
// onto github.com/Cyvadra/ds4's DeepSeek chat-completions client. It is the
// only package that imports ds4 directly.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
)

// Client wraps a ds4.Client with Identity-aware request building.
type Client struct {
	ds4 *ds4.Client
}

// New creates a Client authenticated with apiKey.
func New(apiKey string) *Client {
	return &Client{ds4: ds4.New(apiKey)}
}

// Call sends messages (in order) to the model configured by identity,
// optionally offering toolset, and returns the raw ds4 response.
//
// messages must already include every system/injected/impression/history
// entry the caller wants in context; Call does not add anything beyond
// identity's own system prompt and injected messages.
func (c *Client) Call(ctx context.Context, identity registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool) (*ds4.ChatResponse, error) {
	builder := c.buildChat(identity, messages, toolset)
	resp, err := builder.DoWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("llm: chat completion: %w", err)
	}
	return resp, nil
}

// StreamDelta is a normalized incremental update surfaced by CallStream.
type StreamDelta struct {
	Content          string
	ReasoningContent string
	ToolCalls        []ToolCallDelta
}

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
	builder := c.buildChat(identity, messages, toolset)

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
	if err != nil {
		return nil, fmt.Errorf("llm: stream chat completion: %w", err)
	}

	sort.Ints(toolCallOrder)
	calls := make([]ds4.ToolCall, 0, len(toolCallOrder))
	for _, idx := range toolCallOrder {
		calls = append(calls, *toolCalls[idx])
	}

	return &ds4.ChatResponse{
		Choices: []ds4.Choice{{
			Message: ds4.Message{
				Role:             ds4.RoleAssistant,
				Content:          content.String(),
				ReasoningContent: reasoning.String(),
				ToolCalls:        calls,
			},
			FinishReason: finishReason,
		}},
	}, nil
}

// buildChat constructs the shared ChatBuilder state for both Call and
// CallStream from an Identity, its history, and its available tools.
func (c *Client) buildChat(identity registry.Identity, messages []store.ChatMessage, toolset []toolkit.Tool) *ds4.ChatBuilder {
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
		builder.Tool(ds4.NewFunction(t.Name(), t.Description(), t.Parameters()))
	}

	return builder
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

var maxTokensRangePattern = regexp.MustCompile(`(?i)valid range of max_tokens is\s*\[\s*\d+\s*,\s*(\d+)\s*\]`)

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
