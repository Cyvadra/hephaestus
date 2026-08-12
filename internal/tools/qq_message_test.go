package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/pkg/qq"
)

func TestSendNotificationToolNotAvailableWithoutCredentials(t *testing.T) {
	tool := NewSendNotificationTool(qq.New(qq.Config{}))
	if tool.Available() {
		t.Fatal("expected unavailable without credentials")
	}
	result := tool.Execute(context.Background(), map[string]any{"channel": "qq", "markdown_content": "hi"})
	if !result.IsError || !strings.Contains(result.ForLLM, "not initialized") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSendNotificationToolRejectsNonQQChannel(t *testing.T) {
	tool := NewSendNotificationTool(qq.New(qq.Config{AppID: "a", AppSecret: "s", UserOpenID: "u"}))
	result := tool.Execute(context.Background(), map[string]any{"channel": "slack", "markdown_content": "hi"})
	if !result.IsError || !strings.Contains(result.ForLLM, "must be qq") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSendNotificationToolRejectsEmptyContent(t *testing.T) {
	tool := NewSendNotificationTool(qq.New(qq.Config{AppID: "a", AppSecret: "s", UserOpenID: "u"}))
	result := tool.Execute(context.Background(), map[string]any{"channel": "qq", "markdown_content": "  "})
	if !result.IsError || !strings.Contains(result.ForLLM, "markdown_content is required") {
		t.Fatalf("unexpected result: %+v", result)
	}
}
func TestSendNotificationToolSchemaAndAudited(t *testing.T) {
	tool := NewSendNotificationTool(qq.New(qq.Config{}))
	if !tool.Audited() {
		t.Fatal("notification tool should be audited")
	}
	properties, ok := tool.Parameters()["properties"].(map[string]any)
	if !ok || len(properties) != 2 || properties["channel"] == nil || properties["markdown_content"] == nil {
		t.Fatalf("unexpected parameter schema: %#v", tool.Parameters())
	}

	configured := NewSendNotificationTool(qq.New(qq.Config{AppID: "app", AppSecret: "secret", UserOpenID: "user"}))
	if !configured.Available() {
		t.Fatal("configured tool should be available")
	}
}
