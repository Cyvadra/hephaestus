package store

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockSession loads a session while serializing lifecycle changes that can
// create or remove work owned by it.
func LockSession(tx *gorm.DB, sessionID uint) (*Session, error) {
	var session Session
	query := tx
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&session, sessionID).Error; err != nil {
		return nil, err
	}
	return &session, nil
}
