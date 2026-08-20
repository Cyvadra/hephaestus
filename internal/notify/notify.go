// Package notify provides global warning/error reporting to a WeCom webhook
// plus an in-memory ring buffer of recent warnings for the /status command.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	retryInterval = 15 * time.Second
	maxRetries    = 6
	ringSize      = 10
	queueSize     = 32
)

// Entry is a single recorded warning or error.
type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Notifier pushes Warn/Error messages to a WeCom webhook and keeps a
// bounded in-memory history for later inspection (e.g. /status).
type Notifier struct {
	webhookURL string
	httpClient *http.Client
	queue      chan Entry
	cancel     context.CancelFunc
	workers    sync.WaitGroup
	stopOnce   sync.Once
	queueMu    sync.Mutex
	stopped    bool

	mu   sync.Mutex
	ring []Entry
}

// New creates a Notifier. webhookURL may be empty, in which case messages
// are only logged locally and kept in the ring buffer.
func New(webhookURL string) *Notifier {
	n := &Notifier{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
	if webhookURL != "" {
		ctx, cancel := context.WithCancel(context.Background())
		n.cancel = cancel
		n.queue = make(chan Entry, queueSize)
		n.workers.Add(1)
		go n.run(ctx)
	}
	return n
}

// Warn records a warning-level message.
func (n *Notifier) Warn(format string, args ...any) {
	n.record("WARN", fmt.Sprintf(format, args...))
}

// Error records an error-level message.
func (n *Notifier) Error(format string, args ...any) {
	n.record("ERROR", fmt.Sprintf(format, args...))
}

func (n *Notifier) record(level, message string) {
	entry := Entry{Time: time.Now(), Level: level, Message: message}
	log.Printf("[%s] %s", level, message)

	n.mu.Lock()
	n.ring = append(n.ring, entry)
	if len(n.ring) > ringSize {
		n.ring = n.ring[len(n.ring)-ringSize:]
	}
	n.mu.Unlock()

	if n.webhookURL != "" {
		n.queueMu.Lock()
		defer n.queueMu.Unlock()
		if n.stopped {
			return
		}
		select {
		case n.queue <- entry:
		default:
			log.Printf("[notify] dropped WeCom notification: queue full")
		}
	}
}

// Shutdown stops webhook delivery and waits for the worker to exit.
func (n *Notifier) Shutdown(ctx context.Context) error {
	if n.cancel == nil {
		return nil
	}
	n.stopOnce.Do(func() {
		n.queueMu.Lock()
		n.stopped = true
		close(n.queue)
		n.queueMu.Unlock()
	})
	done := make(chan struct{})
	go func() {
		n.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		n.cancel()
		<-done
		return nil
	}
}

func (n *Notifier) run(ctx context.Context) {
	defer n.workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-n.queue:
			if !ok {
				return
			}
			n.send(ctx, entry)
		}
	}
}

// Recent returns and clears up to ringSize most recent entries, oldest first.
func (n *Notifier) Recent() []Entry {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Entry, len(n.ring))
	copy(out, n.ring)
	n.ring = nil
	return out
}

type wecomTextPayload struct {
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
}

func (n *Notifier) send(ctx context.Context, entry Entry) {
	payload := wecomTextPayload{MsgType: "text"}
	payload.Text.Content = fmt.Sprintf("[%s] %s\n%s", entry.Level, entry.Time.Format(time.RFC3339), entry.Message)
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryInterval):
			case <-ctx.Done():
				return
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := n.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return
		}
		lastErr = fmt.Errorf("wecom webhook returned status %d", resp.StatusCode)
	}
	// Webhook delivery failed after all retries; only local log remains.
	log.Printf("[notify] failed to deliver WeCom notification after %d retries: %v", maxRetries, lastErr)
}
