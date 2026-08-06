// Package notify provides global warning/error reporting to a WeCom webhook
// plus an in-memory ring buffer of recent warnings for the /status command.
package notify

import (
	"bytes"
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

	mu   sync.Mutex
	ring []Entry
}

// New creates a Notifier. webhookURL may be empty, in which case messages
// are only logged locally and kept in the ring buffer.
func New(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
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
		go n.send(entry)
	}
}

// Recent returns up to ringSize most recent entries, oldest first.
func (n *Notifier) Recent() []Entry {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Entry, len(n.ring))
	copy(out, n.ring)
	return out
}

type wecomTextPayload struct {
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
}

func (n *Notifier) send(entry Entry) {
	payload := wecomTextPayload{MsgType: "text"}
	payload.Text.Content = fmt.Sprintf("[%s] %s\n%s", entry.Level, entry.Time.Format(time.RFC3339), entry.Message)
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(retryInterval)
		}
		req, err := http.NewRequest(http.MethodPost, n.webhookURL, bytes.NewReader(body))
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
