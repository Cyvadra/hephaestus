package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/store"
)

// sideQuestionInstruction frames an ephemeral aside. The toolset passed to
// the LLM is nil, so this text is guidance rather than an enforcement
// mechanism: no tool definitions are sent at all.
const sideQuestionInstruction = "[By-the-way question — answer using only the conversation above. " +
	"This is an ephemeral aside: do not call tools, do not propose or perform file changes, " +
	"and do not assume the question or answer will be saved. Be concise. " +
	"If the conversation above does not contain enough information, say so plainly.]"

func sideQuestionPrompt(question string) string {
	return sideQuestionInstruction + "\n\n" + question
}

// SideQuestion answers an ephemeral /btw question from the session's
// committed context without persisting anything: no ChatRun, ChatMessage,
// Compression, PluginState, ToolAudit, notification or steering write
// happens on this path.
//
// The answer uses the context up to the newest completed assistant message
// on the selected branch. selectRoot asks for static context only; leafID
// selects a specific branch; otherwise the session's active leaf is used.
// Tools are disabled by passing a nil toolset to the LLM.
func (p *Pipeline) SideQuestion(
	ctx context.Context,
	sessionID uint,
	question string,
	leafID *uint,
	selectRoot bool,
	onDelta func(string),
) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("chat: question is required")
	}

	sess, err := p.sessions.Get(sessionID)
	if err != nil {
		return "", fmt.Errorf("chat: load session %d: %w", sessionID, err)
	}

	reg := p.registries.Current()
	settings, _, err := p.sanitizeSettings(reg, *sess)
	if err != nil {
		return "", err
	}

	proj, err := p.projects.Get(sess.ProjectID)
	if err != nil {
		return "", fmt.Errorf("chat: project %d not found", sess.ProjectID)
	}
	workspace := p.projects.Path(*proj)
	vars := promptVars(proj, workspace, *sess, time.Now())
	identity, err := reg.RenderIdentity(reg.Identities[settings.Identity], vars)
	if err != nil {
		return "", fmt.Errorf("chat: %w", err)
	}

	var path []store.ChatMessage
	switch {
	case selectRoot:
	case leafID != nil:
		path, err = p.sessions.PathAtLeaf(*sess, leafID)
	default:
		path, err = p.sessions.ActivePath(*sess)
	}
	if err != nil {
		return "", err
	}
	path = truncateAtLastCompleteAssistant(path)

	compRow, err := p.sessions.ResolveCompressionReadOnly(*sess, path)
	if err != nil {
		return "", err
	}
	messages, err := p.buildContext(reg, settings, path, compRow, vars)
	if err != nil {
		return "", err
	}
	messages = append(messages, store.ChatMessage{
		Role:    ds4.RoleUser,
		Content: sideQuestionPrompt(question),
	})

	var streamed strings.Builder
	response, err := p.llm.CallStream(ctx, identity, messages, nil, func(delta llm.StreamDelta) {
		if delta.Content == "" {
			return
		}
		streamed.WriteString(delta.Content)
		if onDelta != nil {
			onDelta(delta.Content)
		}
	})
	if err != nil {
		return streamed.String(), err
	}
	if response == nil {
		return streamed.String(), nil
	}
	return response.Content(), nil
}

// truncateAtLastCompleteAssistant returns the prefix ending at the newest
// assistant message whose Status is not "incomplete". It then sanitizes the
// tail so the prefix can be replayed without tool definitions: trailing tool
// results and tool-call-only assistant messages are dropped, and tool calls
// on a visible final assistant message are stripped because their results
// do not lie inside the prefix.
func truncateAtLastCompleteAssistant(path []store.ChatMessage) []store.ChatMessage {
	end := -1
	for i := len(path) - 1; i >= 0; i-- {
		message := path[i]
		if message.Role == ds4.RoleAssistant && message.Status != store.MessageStatusIncomplete {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}

	out := append([]store.ChatMessage(nil), path[:end+1]...)
	for len(out) > 0 {
		last := out[len(out)-1]
		switch {
		case last.Role == ds4.RoleTool:
			out = out[:len(out)-1]
		case last.Role == ds4.RoleAssistant && len(last.ToolCalls) > 0:
			if strings.TrimSpace(last.Content) == "" {
				out = out[:len(out)-1]
				continue
			}
			last.ToolCalls = nil
			out[len(out)-1] = last
			return out
		default:
			return out
		}
	}
	return out
}
