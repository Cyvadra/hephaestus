package store

import (
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open connects to Postgres and runs AutoMigrate for every runtime model.
func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("store: connect postgres: %w", err)
	}

	if err := db.AutoMigrate(&Session{}, &ChatMessage{}, &Compression{}, &PluginState{}, &Project{}); err != nil {
		return nil, fmt.Errorf("store: automigrate: %w", err)
	}

	return db, nil
}
