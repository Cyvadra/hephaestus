// Package qq exposes the application's entry point to GoQQBot.
package qq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/ProgramCX/GoQQBot/pkg/api"
	"github.com/ProgramCX/GoQQBot/pkg/goqqrobot"
	"github.com/ProgramCX/GoQQBot/pkg/message"
	messagetypes "github.com/ProgramCX/GoQQBot/pkg/message/types"
)

// Config configures the QQ bot connection and notification recipient.
type Config struct {
	AppID      string
	AppSecret  string
	UserOpenID string
}

// Message is the server response for a delivered C2C message.
type Message struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

type apiClient interface {
	Post(context.Context, string, interface{}) ([]byte, error)
}

type bot interface {
	Start() error
	Stop() error
}

// Client is the application-facing entry point for GoQQBot.
type Client struct {
	appID      string
	appSecret  string
	userOpenID string
	apiClient  apiClient
	bot        bot
	initErr    error
	startOnce  sync.Once
	stopOnce   sync.Once
}

// New creates a GoQQBot-backed client. Call Start during application startup
// to establish the WebSocket connection.
func New(config Config) *Client {
	appID := strings.TrimSpace(config.AppID)
	appSecret := strings.TrimSpace(config.AppSecret)
	client := &Client{
		appID:      appID,
		appSecret:  appSecret,
		userOpenID: strings.TrimSpace(config.UserOpenID),
	}
	if appID == "" || appSecret == "" {
		return client
	}
	client.apiClient = api.NewClient(appID, appSecret)
	return client
}

// Configured reports whether all credentials needed for notifications exist.
func (c *Client) Configured() bool {
	return c != nil && c.appID != "" && c.appSecret != "" && c.userOpenID != "" && c.initErr == nil
}

// Start starts the GoQQBot WebSocket connection and keeps it alive until the
// context is canceled. Connection and reconnect errors are returned.
func (c *Client) Start(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	if c == nil || c.appID == "" || c.appSecret == "" {
		done <- errors.New("QQ credentials are not initialized")
		close(done)
		return done
	}

	started := false
	c.startOnce.Do(func() {
		started = true
		if c.bot == nil {
			c.bot, c.initErr = goqqrobot.Init(c.appID, c.appSecret, nil)
		}
		if c.initErr != nil {
			done <- c.initErr
			close(done)
			return
		}
		go func() {
			defer close(done)
			botDone := make(chan error, 1)
			go func() {
				botDone <- c.bot.Start()
			}()
			select {
			case err := <-botDone:
				done <- err
			case <-ctx.Done():
				if err := c.Close(); err != nil {
					done <- err
					return
				}
				done <- <-botDone
			}
		}()
	})
	if !started {
		close(done)
	}
	return done
}

// Close shuts down the GoQQBot WebSocket connection.
func (c *Client) Close() error {
	if c == nil || c.bot == nil {
		return nil
	}
	var err error
	c.stopOnce.Do(func() { err = c.bot.Stop() })
	return err
}

// SendMarkdown sends a proactive C2C Markdown notification through GoQQBot.
func (c *Client) SendMarkdown(ctx context.Context, content string) (Message, error) {
	var response Message
	if !c.Configured() || c.apiClient == nil {
		return response, errors.New("QQ credentials are not initialized")
	}
	body := &message.PrivateSendMessage{CommonSendMessage: message.CommonSendMessage{
		MsgType:  message.MARKDOWN,
		Markdown: &messagetypes.Markdown{Content: content},
	}}
	path := fmt.Sprintf(api.C2C_SEND_MESSAGE, url.PathEscape(c.userOpenID))
	data, err := c.apiClient.Post(ctx, path, body)
	if err != nil {
		return response, fmt.Errorf("send QQ markdown message: %w", err)
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return response, fmt.Errorf("decode QQ message response: %w", err)
	}
	return response, nil
}
