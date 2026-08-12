// Package qq implements the channels.Channel contract for QQ C2C messages.
package qq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

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

// Channel is a QQ C2C channel.
type Channel struct {
	config  Config
	client  apiClient
	bot     bot
	handler channels.Handler
	running atomic.Bool
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
	if config.AppID == "" || config.AppSecret == "" || config.UserOpenID == "" {
		return nil, errors.New("qq channel: app id, app secret and user openid are required")
	}
	channel := &Channel{config: config, client: api.NewClient(config.AppID, config.AppSecret)}
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
	go func() {
		if err := c.bot.Start(); err != nil {
			c.running.Store(false)
		}
	}()
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

func (c *Channel) Send(ctx context.Context, outbound channels.OutboundMessage) error {
	body := &message.PrivateSendMessage{CommonSendMessage: message.CommonSendMessage{
		MsgType:  message.MARKDOWN,
		Markdown: &messagetypes.Markdown{Content: outbound.Content},
	}}
	path := fmt.Sprintf(api.C2C_SEND_MESSAGE, url.PathEscape(outbound.ChatID))
	if _, err := c.client.Post(ctx, path, body); err != nil {
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
	encoded, err := c.client.Post(ctx, uploadPath, &upload)
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
	if _, err := c.client.Post(ctx, messagePath, body); err != nil {
		return fmt.Errorf("qq channel: send file: %w", err)
	}
	return nil
}

func (c *Channel) receive(ctx *sharedtypes.Context) error {
	inbound, err := c2c.GetPrivateReceiveMessage(*ctx.Payload)
	if err != nil {
		return err
	}
	if inbound.Author.UserOpenid != c.config.UserOpenID {
		return nil
	}
	attachments := make([]channels.Attachment, 0, len(inbound.Attachments))
	for _, source := range inbound.Attachments {
		path, err := downloadAttachment(source.Url, source.Filename)
		if err != nil {
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

func downloadAttachment(rawURL, name string) (string, error) {
	response, err := http.Get(rawURL)
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
