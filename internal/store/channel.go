package store

import "time"

// ChannelBinding persists the last session used by one external chat.
type ChannelBinding struct {
	ID uint `gorm:"primaryKey;autoIncrement"`

	Channel   string `gorm:"size:64;uniqueIndex:idx_channel_chat"`
	ChatID    string `gorm:"size:512;uniqueIndex:idx_channel_chat"`
	SessionID uint   `gorm:"not null;index"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
