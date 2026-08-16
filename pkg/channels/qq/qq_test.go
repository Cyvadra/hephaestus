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

	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/pkg/channels"
	"github.com/ProgramCX/GoQQBot/pkg/goqqrobot"
	"github.com/ProgramCX/GoQQBot/pkg/message"
)

type retryAPIClient struct {
	mu       sync.Mutex
	attempts int
	failures int
}

func (c *retryAPIClient) Post(context.Context, string, interface{}) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
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
		"switch", "activate", "deactivate", "clear", "new", "interact",
	}
	if !slices.Equal(command.Names(), want) {
		t.Fatalf("command.Names() = %v, want %v", command.Names(), want)
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
