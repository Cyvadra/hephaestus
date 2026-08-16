// Package qq implements the channels.Channel contract for QQ C2C messages.
package qq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/pkg/channels"
	"github.com/ProgramCX/GoQQBot/pkg/api"
	"github.com/ProgramCX/GoQQBot/pkg/api/c2c"
	"github.com/ProgramCX/GoQQBot/pkg/goqqrobot"
	"github.com/ProgramCX/GoQQBot/pkg/message"
	messagetypes "github.com/ProgramCX/GoQQBot/pkg/message/types"
	sharedtypes "github.com/ProgramCX/GoQQBot/shared/types"
)

// Config configures the QQ bot and restricts inbound traffic to one user.
type Config struct {
	AppID      string
	AppSecret  string
	UserOpenID string
}

type bot interface {
	Start() error
	Stop() error
	OnCommand(string, message.EventId, goqqrobot.Handler)
}

type apiClient interface {
	Post(context.Context, string, interface{}) ([]byte, error)
}

type httpClient interface {
	Do(*http.Request) (*http.Response, error)
}

const (
	defaultRetryCount = 3
	defaultRetryDelay = 5 * time.Second
)

// Channel is a QQ C2C channel.
type Channel struct {
	config  Config
	client  apiClient
	http    httpClient
	bot     bot
	handler channels.Handler
	running atomic.Bool
	retries int
	delay   time.Duration
	mu      sync.RWMutex
}

func init() {
	channels.RegisterFactory("qq", func(config any) (channels.Channel, error) {
		qqConfig, ok := config.(Config)
		if !ok {
			return nil, fmt.Errorf("qq channel: expected qq.Config, got %T", config)
		}
		return New(qqConfig)
	})
}

// New creates a QQ channel. Start establishes the WebSocket connection.
func New(config Config) (*Channel, error) {
	config.AppID = strings.TrimSpace(config.AppID)
	config.AppSecret = strings.TrimSpace(config.AppSecret)
	config.UserOpenID = strings.TrimSpace(config.UserOpenID)
	if config.AppID == "" || config.AppSecret == "" {
		return nil, errors.New("qq channel: app id and app secret are required")
	}
	channel := &Channel{
		config: config, client: api.NewClient(config.AppID, config.AppSecret),
		http: &http.Client{Timeout: 30 * time.Second}, retries: defaultRetryCount, delay: defaultRetryDelay,
	}
	created, err := goqqrobot.Init(config.AppID, config.AppSecret, channel.receive)
	if err != nil {
		return nil, fmt.Errorf("qq channel: initialize bot: %w", err)
	}
	for _, commandName := range command.Names() {
		created.OnCommand(commandName, message.C2C_MESSAGE_CREATE, channel.receive)
	}
	channel.bot = created
	return channel, nil
}

func (*Channel) Name() string { return "qq" }

func (c *Channel) SetHandler(handler channels.Handler) {
	c.mu.Lock()
	c.handler = handler
	c.mu.Unlock()
}

func (c *Channel) Start(ctx context.Context) error {
	if !c.running.CompareAndSwap(false, true) {
		return nil
	}
	go c.run(ctx)
	go func() {
		<-ctx.Done()
		_ = c.Stop(context.Background())
	}()
	return nil
}

func (c *Channel) Stop(context.Context) error {
	if !c.running.CompareAndSwap(true, false) {
		return nil
	}
	return c.bot.Stop()
}

func (c *Channel) run(ctx context.Context) {
	defer c.running.Store(false)
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil || !c.running.Load() {
			return
		}
		err := c.bot.Start()
		if err == nil || ctx.Err() != nil || !c.running.Load() {
			return
		}
		if attempt >= c.retries {
			log.Printf("qq channel: connection failed after %d retries: %v", c.retries, err)
			return
		}
		log.Printf("qq channel: connection failed: %v; retrying in %s (%d/%d)", err, c.delay, attempt+1, c.retries)
		if err := waitRetry(ctx, c.delay); err != nil {
			return
		}
	}
}

func (c *Channel) Send(ctx context.Context, outbound channels.OutboundMessage) error {
	body := &message.PrivateSendMessage{CommonSendMessage: message.CommonSendMessage{
		MsgType:  message.MARKDOWN,
		Markdown: &messagetypes.Markdown{Content: outbound.Content},
	}}
	path := fmt.Sprintf(api.C2C_SEND_MESSAGE, url.PathEscape(outbound.ChatID))
	if _, err := retryValue(ctx, c.retries, c.delay, func() ([]byte, error) {
		return c.client.Post(ctx, path, body)
	}); err != nil {
		return fmt.Errorf("qq channel: send message: %w", err)
	}
	return nil
}

func (c *Channel) SendFile(ctx context.Context, chatID string, attachment channels.Attachment) error {
	data, err := os.ReadFile(attachment.Path)
	if err != nil {
		return fmt.Errorf("qq channel: read file: %w", err)
	}
	upload := struct {
		FileType uint64 `json:"file_type"`
		FileData string `json:"file_data"`
		FileName string `json:"file_name"`
	}{FileType: qqFileType(attachment.MIME), FileData: base64.StdEncoding.EncodeToString(data), FileName: attachment.Name}
	uploadPath := fmt.Sprintf(api.C2C_FILE_MESSAGE, url.PathEscape(chatID))
	encoded, err := retryValue(ctx, c.retries, c.delay, func() ([]byte, error) {
		return c.client.Post(ctx, uploadPath, &upload)
	})
	if err != nil {
		return fmt.Errorf("qq channel: upload file: %w", err)
	}
	var uploaded message.RichMediaResponse
	if err := json.Unmarshal(encoded, &uploaded); err != nil {
		return fmt.Errorf("qq channel: decode file upload: %w", err)
	}
	body := &message.PrivateSendMessage{CommonSendMessage: message.CommonSendMessage{
		MsgType: message.MEDIA,
		Media:   &message.MediaInfo{FileInfo: uploaded.FileInfo},
	}}
	messagePath := fmt.Sprintf(api.C2C_SEND_MESSAGE, url.PathEscape(chatID))
	if _, err := retryValue(ctx, c.retries, c.delay, func() ([]byte, error) {
		return c.client.Post(ctx, messagePath, body)
	}); err != nil {
		return fmt.Errorf("qq channel: send file: %w", err)
	}
	return nil
}

func (c *Channel) receive(ctx *sharedtypes.Context) error {
	inbound, err := c2c.GetPrivateReceiveMessage(*ctx.Payload)
	if err != nil {
		return err
	}
	if c.config.UserOpenID == "" {
		return c.sendOpenID(context.Background(), inbound.Author.UserOpenid)
	}
	if inbound.Author.UserOpenid != c.config.UserOpenID {
		return nil
	}
	attachments := make([]channels.Attachment, 0, len(inbound.Attachments))
	for _, source := range inbound.Attachments {
		path, err := c.downloadAttachment(context.Background(), source.Url, source.Filename)
		if err != nil {
			log.Printf("qq channel: download attachment %q: %v", source.Filename, err)
			continue
		}
		attachments = append(attachments, channels.Attachment{
			Path: path, Name: source.Filename, MIME: string(source.ContentType),
		})
	}
	c.mu.RLock()
	handler := c.handler
	c.mu.RUnlock()
	if handler != nil {
		handler(context.Background(), channels.InboundMessage{
			Channel: "qq", ChatID: inbound.Author.UserOpenid,
			SenderID: inbound.Author.UserOpenid, MessageID: inbound.Id,
			Content: strings.TrimSpace(inbound.Content), Attachments: attachments,
		})
	}
	return nil
}

func (c *Channel) sendOpenID(ctx context.Context, userOpenID string) error {
	content, err := json.Marshal(struct {
		UserOpenID string `json:"user_openid"`
	}{UserOpenID: userOpenID})
	if err != nil {
		return fmt.Errorf("qq channel: encode discovery response: %w", err)
	}
	return c.Send(ctx, channels.OutboundMessage{ChatID: userOpenID, Content: string(content)})
}

func (c *Channel) downloadAttachment(ctx context.Context, rawURL, name string) (string, error) {
	return retryValue(ctx, c.retries, c.delay, func() (string, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", err
		}
		response, err := c.http.Do(request)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return "", fmt.Errorf("download returned %s", response.Status)
		}
		extension := filepath.Ext(filepath.Base(name))
		file, err := os.CreateTemp("", "hephaestus-qq-*"+extension)
		if err != nil {
			return "", err
		}
		defer file.Close()
		if _, err := file.ReadFrom(response.Body); err != nil {
			_ = os.Remove(file.Name())
			return "", err
		}
		return file.Name(), nil
	})
}

func retryValue[T any](ctx context.Context, retries int, delay time.Duration, operation func() (T, error)) (T, error) {
	var zero T
	var err error
	for attempt := 0; attempt <= retries; attempt++ {
		if value, operationErr := operation(); operationErr == nil {
			return value, nil
		} else {
			err = operationErr
		}
		if attempt < retries {
			if waitErr := waitRetry(ctx, delay); waitErr != nil {
				return zero, waitErr
			}
		}
	}
	return zero, err
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func qqFileType(mime string) uint64 {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return 1
	case strings.HasPrefix(mime, "video/"):
		return 2
	case strings.HasPrefix(mime, "audio/"):
		return 3
	default:
		return 4
	}
}
