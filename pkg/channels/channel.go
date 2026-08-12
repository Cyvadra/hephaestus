// Package channels connects external messaging systems to Hephaestus.
// Its interface is adapted from PicoClaw's pkg/channels package.
package channels

import "context"

// InboundMessage is one complete message received from an external channel.
type InboundMessage struct {
	Channel     string
	ChatID      string
	SenderID    string
	MessageID   string
	Content     string
	Attachments []Attachment
}

// OutboundMessage is one complete message sent to an external channel.
// External channels intentionally receive only finalized messages.
type OutboundMessage struct {
	ChatID  string
	Content string
}

// Attachment represents a received or delivered file.
type Attachment struct {
	Path string
	Name string
	MIME string
}

// Handler receives normalized messages from a Channel.
type Handler func(context.Context, InboundMessage)

// Channel is the common lifecycle and message contract for external chat
// systems. Implementations call SetHandler before Start begins delivering.
type Channel interface {
	Name() string
	Start(context.Context) error
	Stop(context.Context) error
	Send(context.Context, OutboundMessage) error
	SetHandler(Handler)
}

// FileChannel is implemented by channels that support file transfer.
type FileChannel interface {
	Channel
	SendFile(context.Context, string, Attachment) error
}
