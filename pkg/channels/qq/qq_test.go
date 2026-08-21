package qq

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/pkg/channels"
	"github.com/ProgramCX/GoQQBot/pkg/goqqrobot"
	"github.com/ProgramCX/GoQQBot/pkg/message"
)

type retryAPIClient struct {
	mu       sync.Mutex
	attempts int
	failures int
	path     string
	body     interface{}
}

func (c *retryAPIClient) Post(_ context.Context, path string, body interface{}) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	c.path = path
	c.body = body
	if c.attempts <= c.failures {
		return nil, errors.New("temporary failure")
	}
	return []byte(`{}`), nil
}

type retryBot struct {
	mu       sync.Mutex
	attempts int
	failures int
	done     chan struct{}
}

func (b *retryBot) Start() error {
	b.mu.Lock()
	b.attempts++
	attempt := b.attempts
	b.mu.Unlock()
	if attempt <= b.failures {
		return errors.New("connection failed")
	}
	close(b.done)
	return nil
}

func (*retryBot) Stop() error { return nil }

func (*retryBot) OnCommand(string, message.EventId, goqqrobot.Handler) {}
func (*retryBot) OnEvent(message.EventId, goqqrobot.Handler)           {}

type commandBot struct {
	commands map[message.EventId][]string
	events   []message.EventId
}

func (*commandBot) Start() error { return nil }

func (*commandBot) Stop() error { return nil }

func (b *commandBot) OnCommand(command string, eventID message.EventId, _ goqqrobot.Handler) {
	b.commands[eventID] = append(b.commands[eventID], command)
}

func (b *commandBot) OnEvent(eventID message.EventId, _ goqqrobot.Handler) {
	b.events = append(b.events, eventID)
}

type retryHTTPClient struct {
	mu       sync.Mutex
	attempts int
	failures int
}

func (c *retryHTTPClient) Do(*http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	status := http.StatusOK
	if c.attempts <= c.failures {
		status = http.StatusServiceUnavailable
	}
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status),
		Body: io.NopCloser(strings.NewReader("attachment")),
	}, nil
}

func TestSupportedCommands(t *testing.T) {
	want := []string{
		"help", "ping", "stop", "status", "list", "detail",
		"switch", "activate", "deactivate", "clear", "new", "last", "replay", "interact",
	}
	if !slices.Equal(command.Names(), want) {
		t.Fatalf("command.Names() = %v, want %v", command.Names(), want)
	}
}

func TestRegisterCommandsSupportsBothPrivateMessageEvents(t *testing.T) {
	bot := &commandBot{commands: make(map[message.EventId][]string)}
	registerCommands(bot, nil)

	for _, eventID := range []message.EventId{message.C2C_MSG_RECEIVE, message.C2C_MESSAGE_CREATE} {
		if !slices.Equal(bot.commands[eventID], command.Names()) {
			t.Errorf("commands for %s = %v, want %v", eventID, bot.commands[eventID], command.Names())
		}
	}
}

func TestRegisterEventsSupportsBothPrivateMessageEvents(t *testing.T) {
	bot := &commandBot{commands: make(map[message.EventId][]string)}
	registerEvents(bot, nil)
	want := []message.EventId{message.C2C_MSG_RECEIVE, message.C2C_MESSAGE_CREATE}
	if !slices.Equal(bot.events, want) {
		t.Fatalf("registered events = %v, want %v", bot.events, want)
	}
}

func TestDuplicateMessageIDIsIgnored(t *testing.T) {
	channel := &Channel{seen: make(map[string]time.Time)}
	if channel.duplicate("message-1") {
		t.Fatal("first message was treated as a duplicate")
	}
	if !channel.duplicate("message-1") {
		t.Fatal("duplicate message was not ignored")
	}
	if channel.duplicate("") {
		t.Fatal("empty message id was treated as a duplicate")
	}
}

func TestStartRetriesConnectionFailure(t *testing.T) {
	bot := &retryBot{failures: 3, done: make(chan struct{})}
	channel := &Channel{bot: bot, retries: 3, delay: 0}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := channel.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-bot.done
	if bot.attempts != 4 {
		t.Fatalf("Start attempts = %d, want 4", bot.attempts)
	}
}

func TestSendRetriesTemporaryFailure(t *testing.T) {
	client := &retryAPIClient{failures: 3}
	channel := &Channel{client: client, retries: 3, delay: 0}
	if err := channel.Send(context.Background(), channels.OutboundMessage{ChatID: "user", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if client.attempts != 4 {
		t.Fatalf("Post attempts = %d, want 4", client.attempts)
	}
}

func TestSendOpenIDDiscoveryResponse(t *testing.T) {
	client := &retryAPIClient{}
	channel := &Channel{client: client, retries: 0}
	if err := channel.sendOpenID(context.Background(), "user/openid"); err != nil {
		t.Fatal(err)
	}
	if client.path != "/v2/users/user%2Fopenid/messages" {
		t.Fatalf("Post path = %q, want escaped user message path", client.path)
	}
	body, ok := client.body.(*message.PrivateSendMessage)
	if !ok {
		t.Fatalf("Post body type = %T, want *message.PrivateSendMessage", client.body)
	}
	if body.Markdown == nil || body.Markdown.Content != `{"user_openid":"user/openid"}` {
		t.Fatalf("discovery response = %+v", body.Markdown)
	}
}

func TestDownloadAttachmentRetriesTemporaryFailure(t *testing.T) {
	client := &retryHTTPClient{failures: 3}
	channel := &Channel{http: client, retries: 3, delay: 0}
	path, err := channel.downloadAttachment(context.Background(), "https://example.test/file", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if client.attempts != 4 {
		t.Fatalf("download attempts = %d, want 4", client.attempts)
	}
}
