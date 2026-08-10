package store

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LoadPluginState unmarshals sessionID/pluginName's persisted state into
// out. It returns (false, nil) if no state has been saved yet.
func LoadPluginState(db *gorm.DB, sessionID uint, pluginName string, out any) (bool, error) {
	var row PluginState
	result := db.Where("session_id = ? AND plugin_name = ?", sessionID, pluginName).Limit(1).Find(&row)
	if result.Error != nil {
		return false, fmt.Errorf("store: load plugin state (session=%d, plugin=%s): %w", sessionID, pluginName, result.Error)
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	if err := json.Unmarshal(row.Data, out); err != nil {
		return false, fmt.Errorf("store: unmarshal plugin state (session=%d, plugin=%s): %w", sessionID, pluginName, err)
	}
	return true, nil
}

// SavePluginState upserts sessionID/pluginName's persisted state from data.
func SavePluginState(db *gorm.DB, sessionID uint, pluginName string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("store: marshal plugin state (session=%d, plugin=%s): %w", sessionID, pluginName, err)
	}

	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "session_id"}, {Name: "plugin_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"data", "updated_at"}),
	}).Create(&PluginState{
		SessionID:  sessionID,
		PluginName: pluginName,
		Data:       encoded,
	}).Error
}
