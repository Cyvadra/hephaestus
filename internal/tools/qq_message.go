package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/pkg/qq"
)

// SendNotificationTool exposes QQ C2C markdown notifications to the LLM. The
// reusable QQ client (token caching, expiry-aware refresh, markdown delivery)
// lives in pkg/qq; this file is only the Tool adapter.
type SendNotificationTool struct {
	client *qq.Client
}

// NewSendNotificationTool exposes notifications through the application's
// shared QQ client.
func NewSendNotificationTool(client *qq.Client) *SendNotificationTool {
	return &SendNotificationTool{client: client}
}

func (SendNotificationTool) Name() string  { return "send_notification" }
func (SendNotificationTool) Audited() bool { return true }
func (t *SendNotificationTool) Available() bool {
	return t != nil && t.client != nil && t.client.Configured()
}
func (SendNotificationTool) Description() string {
	return "Sends a notification to the configured recipient. The only supported channel is qq, and the content must be QQ-compatible Markdown."
}
func (SendNotificationTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"channel": map[string]any{
				"type":        "string",
				"enum":        []string{"qq"},
				"description": "Notification channel. Currently only qq is supported.",
			},
			"markdown_content": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "QQ-compatible Markdown content to send.",
			},
		},
		"required": []string{"channel", "markdown_content"},
	}
}

func (t *SendNotificationTool) Execute(ctx context.Context, args map[string]any) *toolkit.ToolResult {
	if !t.Available() {
		return toolkit.ErrorResult("send_notification: QQ is not initialized")
	}
	channel, _ := args["channel"].(string)
	if channel != "qq" {
		return toolkit.ErrorResult("send_notification: channel must be qq")
	}
	content, _ := args["markdown_content"].(string)
	if strings.TrimSpace(content) == "" {
		return toolkit.ErrorResult("send_notification: markdown_content is required")
	}

	message, err := t.client.SendMarkdown(ctx, content)
	if err != nil {
		return toolkit.ErrorResult(fmt.Sprintf("send_notification: %s", err))
	}
	result := "QQ notification sent"
	if message.ID != "" {
		result += " (message_id: " + message.ID
		if message.Timestamp != "" {
			result += ", timestamp: " + message.Timestamp
		}
		result += ")"
	}
	return toolkit.NewToolResult(result)
}
