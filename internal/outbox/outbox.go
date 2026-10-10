// Package outbox implements the durable, leased notification outbox shared by
// subagent completion events and late tool completion events: rows are
// claimed with SKIP LOCKED under a lease, then acknowledged in the turn's
// commit transaction or released when the turn fails.
package outbox

import (
	"time"

	"github.com/Cyvadra/hephaestus/internal/agent"
	"github.com/Cyvadra/hephaestus/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Lease bounds how long a claimed event stays hidden from other claimers when
// its turn never acknowledges or releases it.
const Lease = 5 * time.Minute

// Table is one outbox table. Model is a pointer to its row type, which must
// have id, payload, claimed_at and consumed_at columns; SessionColumn names
// the column holding the delivery session; Prefix leads each notification's
// text, followed by the JSON payload.
type Table struct {
	DB            *gorm.DB
	Model         any
	SessionColumn string
	Prefix        string
}

type row struct {
	ID      uint
	Payload datatypes.JSON
}

// ClaimNotifications leases unconsumed events for sessionID in durable order.
func (t Table) ClaimNotifications(sessionID uint) ([]agent.Notification, error) {
	var rows []row
	if err := t.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := store.ForUpdate(tx.Model(t.Model), "SKIP LOCKED").
			Select("id", "payload").
			Where(t.SessionColumn+" = ? AND consumed_at IS NULL AND (claimed_at IS NULL OR claimed_at < ?)", sessionID, now.Add(-Lease)).
			Order("id").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]uint, len(rows))
		for i := range rows {
			ids[i] = rows[i].ID
		}
		return tx.Model(t.Model).Where("id IN ? AND consumed_at IS NULL", ids).Update("claimed_at", &now).Error
	}); err != nil {
		return nil, err
	}
	out := make([]agent.Notification, len(rows))
	for i := range rows {
		out[i] = agent.Notification{ID: rows[i].ID, Text: t.Prefix + string(rows[i].Payload)}
	}
	return out, nil
}

// AcknowledgeNotificationsTx marks ids consumed inside tx.
func (t Table) AcknowledgeNotificationsTx(tx *gorm.DB, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	return tx.Model(t.Model).Where("id IN ? AND consumed_at IS NULL", ids).Updates(map[string]any{"consumed_at": &now, "claimed_at": nil}).Error
}

// ReleaseNotifications returns leased ids to the unclaimed pool.
func (t Table) ReleaseNotifications(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return t.DB.Model(t.Model).Where("id IN ? AND consumed_at IS NULL", ids).Update("claimed_at", nil).Error
}

// PendingSessionIDs lists sessions with unconsumed events.
func (t Table) PendingSessionIDs() ([]uint, error) {
	var ids []uint
	err := t.DB.Model(t.Model).Where("consumed_at IS NULL").Distinct().Pluck(t.SessionColumn, &ids).Error
	return ids, err
}
