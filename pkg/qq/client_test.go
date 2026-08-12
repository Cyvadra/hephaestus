package qq

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ProgramCX/GoQQBot/pkg/message"
)

type fakeAPIClient struct {
	path string
	body interface{}
	data []byte
	err  error
}

func (c *fakeAPIClient) Post(_ context.Context, path string, body interface{}) ([]byte, error) {
	c.path = path
	c.body = body
	return c.data, c.err
}

type fakeBot struct {
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newFakeBot() *fakeBot {
	return &fakeBot{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (b *fakeBot) Start() error {
	close(b.started)
	<-b.stopped
	return nil
}

func (b *fakeBot) Stop() error {
	b.once.Do(func() { close(b.stopped) })
	return nil
}

func TestClientSendMarkdownDelegatesToGoQQBot(t *testing.T) {
	apiClient := &fakeAPIClient{data: []byte(`{"id":"message-1","timestamp":"2026-08-12T12:00:00+08:00"}`)}
	client := &Client{
		appID:      "app",
		appSecret:  "secret",
		userOpenID: "user/openid",
		apiClient:  apiClient,
		bot:        newFakeBot(),
	}

	response, err := client.SendMarkdown(context.Background(), "# hello")
	if err != nil {
		t.Fatalf("SendMarkdown: %v", err)
	}
	if response.ID != "message-1" {
		t.Fatalf("message id = %q", response.ID)
	}
	if apiClient.path != "/v2/users/user%2Fopenid/messages" {
		t.Fatalf("path = %q", apiClient.path)
	}
	body, ok := apiClient.body.(*message.PrivateSendMessage)
	if !ok {
		t.Fatalf("body type = %T", apiClient.body)
	}
	if body.MsgType != message.MARKDOWN || body.Markdown == nil || body.Markdown.Content != "# hello" {
		t.Fatalf("unexpected message body: %#v", body)
	}
}

func TestClientSendMarkdownReportsGoQQBotErrors(t *testing.T) {
	client := &Client{
		appID:      "app",
		appSecret:  "secret",
		userOpenID: "user",
		apiClient:  &fakeAPIClient{err: errors.New("request failed")},
		bot:        newFakeBot(),
	}
	if _, err := client.SendMarkdown(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRejectsInvalidGoQQBotResponse(t *testing.T) {
	client := &Client{
		appID:      "app",
		appSecret:  "secret",
		userOpenID: "user",
		apiClient:  &fakeAPIClient{data: []byte(`not-json`)},
		bot:        newFakeBot(),
	}
	if _, err := client.SendMarkdown(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "decode QQ message response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientNotConfigured(t *testing.T) {
	client := New(Config{})
	if client.Configured() {
		t.Fatal("expected unconfigured")
	}
	if _, err := client.SendMarkdown(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("expected not-initialized error, got %v", err)
	}
}

func TestClientStartsAndStopsBotWithContext(t *testing.T) {
	bot := newFakeBot()
	client := &Client{appID: "app", appSecret: "secret", userOpenID: "user", bot: bot}
	if err := (&Client{}).Close(); err != nil {
		t.Fatalf("Close before Start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := client.Start(ctx)
	<-bot.started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-bot.stopped:
	default:
		t.Fatal("bot was not stopped after context cancellation")
	}
}

func TestGoQQBotMessageBodyUsesExpectedJSON(t *testing.T) {
	body := &message.PrivateSendMessage{CommonSendMessage: message.CommonSendMessage{MsgType: message.MARKDOWN}}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"msg_type":2`) {
		t.Fatalf("message JSON = %s", data)
	}
}
