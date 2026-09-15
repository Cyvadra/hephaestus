package store

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ForUpdate adds a row lock to the query on dialects that support one.
// SQLite serializes writers at the connection level, so no clause is needed.
func ForUpdate(tx *gorm.DB, options ...string) *gorm.DB {
	if tx.Dialector.Name() != "postgres" {
		return tx
	}
	locking := clause.Locking{Strength: "UPDATE"}
	if len(options) > 0 {
		locking.Options = options[0]
	}
	return tx.Clauses(locking)
}

// LockSession loads a session while serializing lifecycle changes that can
// create or remove work owned by it.
func LockSession(tx *gorm.DB, sessionID uint) (*Session, error) {
	var session Session
	if err := ForUpdate(tx).First(&session, sessionID).Error; err != nil {
		return nil, err
	}
	return &session, nil
}
