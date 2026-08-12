package store

import (
	"time"

	"gorm.io/gorm"
)

// NormalizePagination clamps limit to [1,200] (defaulting to 50) and offset
// to >= 0. It is shared by every list endpoint so the pagination contract is
// defined in one place.
func NormalizePagination(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// ListRuns returns run rows newest first, optionally filtered by a
// code-constant name column, with bounded pagination. Shared by the job and
// workflow run listings.
func ListRuns[T any](db *gorm.DB, nameColumn, name string, limit, offset int) ([]T, error) {
	limit, offset = NormalizePagination(limit, offset)
	query := db.Order("id DESC").Limit(limit).Offset(offset)
	if name != "" {
		query = query.Where(nameColumn+" = ?", name)
	}
	var runs []T
	if err := query.Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

// MarkInterrupted flags stale pending/running rows of model as interrupted;
// used by the startup reconcile of both executors.
func MarkInterrupted(db *gorm.DB, model any, pending []string) error {
	now := time.Now()
	return db.Model(model).
		Where("status IN ?", pending).
		Updates(map[string]any{"status": "interrupted", "finished_at": &now, "error": "interrupted by process restart"}).Error
}
