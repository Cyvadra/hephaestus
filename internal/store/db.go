package store

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Cyvadra/hephaestus/internal/registry"
)

// Open connects to Postgres and migrates runtime and persisted configuration
// models.
func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("store: connect postgres: %w", err)
	}

	if err := db.AutoMigrate(
		&Project{}, &ChatMessage{}, &MessageAttachment{}, &Compression{}, &PluginState{}, &ChannelBinding{}, &ToolAudit{}, &ChatRun{}, &ChatRunEvent{},
		&WorkflowRun{}, &WorkflowStepRun{}, &JobRun{}, &JobState{},
		&registry.Identity{}, &registry.Impression{}, &registry.ToolGroup{},
		&registry.Concierge{}, &registry.Workflow{}, &registry.Job{},
		&registry.Constant{},
		&registry.TemplateState{},
	); err != nil {
		return nil, fmt.Errorf("store: automigrate: %w", err)
	}
	for _, model := range []any{
		&registry.Identity{}, &registry.Impression{}, &registry.ToolGroup{},
		&registry.Concierge{}, &registry.Workflow{}, &registry.Job{},
		&registry.Constant{},
	} {
		if err := db.Model(model).
			Where("created_at IS NULL OR updated_at IS NULL").
			Updates(map[string]any{
				"created_at": gorm.Expr("COALESCE(created_at, CURRENT_TIMESTAMP)"),
				"updated_at": gorm.Expr("COALESCE(updated_at, CURRENT_TIMESTAMP)"),
			}).Error; err != nil {
			return nil, fmt.Errorf("store: backfill registry timestamps: %w", err)
		}
	}
	if err := db.Model(&registry.Concierge{}).
		Where("nickname IS NULL OR nickname = ?", "").
		Update("nickname", gorm.Expr("name")).Error; err != nil {
		return nil, fmt.Errorf("store: backfill concierge nicknames: %w", err)
	}
	defaultProject, err := ensureDefaultProject(db)
	if err != nil {
		return nil, err
	}
	if err := migrateSessions(db, defaultProject.ID); err != nil {
		return nil, err
	}

	return db, nil
}

func ensureDefaultProject(db *gorm.DB) (*Project, error) {
	var project Project
	err := db.Where("name = ?", DefaultProjectName).First(&project).Error
	if err == nil {
		return &project, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("store: load default project: %w", err)
	}
	project = Project{
		Name:        DefaultProjectName,
		Description: "System default workspace for agent file operations.",
	}
	if err := db.Create(&project).Error; err != nil {
		return nil, fmt.Errorf("store: create default project: %w", err)
	}
	return &project, nil
}

// legacySessionProjectID maps to sessions while allowing the new column to
// remain nullable until existing rows have been assigned a Project.
type legacySessionProjectID struct {
	ProjectID *uint `gorm:"column:project_id"`
}

func (legacySessionProjectID) TableName() string { return "sessions" }

// legacySessionLastMessageTime adds the activity column as nullable so
// existing rows can be backfilled before Session's non-null constraint is
// applied.
type legacySessionLastMessageTime struct {
	LastMessageTime *time.Time `gorm:"column:last_message_time"`
}

func (legacySessionLastMessageTime) TableName() string { return "sessions" }

type legacySession struct {
	ID        uint
	ProjectID *uint
	Settings  datatypes.JSONType[SessionSettings] `gorm:"type:jsonb"`
}

func (legacySession) TableName() string { return "sessions" }

func migrateSessions(db *gorm.DB, defaultProjectID uint) error {
	if !db.Migrator().HasTable(&Session{}) {
		if err := db.AutoMigrate(&Session{}); err != nil {
			return fmt.Errorf("store: create sessions table: %w", err)
		}
		return nil
	}
	if !db.Migrator().HasColumn(&Session{}, "ProjectID") {
		if err := db.Migrator().AddColumn(&legacySessionProjectID{}, "ProjectID"); err != nil {
			return fmt.Errorf("store: add nullable sessions.project_id: %w", err)
		}
	}
	if !db.Migrator().HasColumn(&Session{}, "LastMessageTime") {
		if err := db.Migrator().AddColumn(&legacySessionLastMessageTime{}, "LastMessageTime"); err != nil {
			return fmt.Errorf("store: add nullable sessions.last_message_time: %w", err)
		}
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		var sessions []legacySession
		if err := tx.Where("project_id IS NULL").Find(&sessions).Error; err != nil {
			return fmt.Errorf("list legacy sessions: %w", err)
		}
		for _, sess := range sessions {
			projectID := defaultProjectID
			legacyProject := sess.Settings.Data().Project
			if legacyProject != "" {
				var project Project
				if err := tx.Where("name = ?", legacyProject).First(&project).Error; err == nil {
					projectID = project.ID
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("load legacy project %q: %w", legacyProject, err)
				}
			}
			if err := tx.Model(&legacySession{}).Where("id = ?", sess.ID).Update("project_id", projectID).Error; err != nil {
				return fmt.Errorf("bind project for session %d: %w", sess.ID, err)
			}
		}
		if err := tx.Exec(`
			UPDATE sessions
			SET last_message_time = COALESCE(
				(SELECT timestamp FROM chat_messages WHERE id = sessions.active_leaf_message_id),
				created_at,
				CURRENT_TIMESTAMP
			)
			WHERE last_message_time IS NULL
		`).Error; err != nil {
			return fmt.Errorf("backfill session last message times: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := db.AutoMigrate(&Session{}); err != nil {
		return fmt.Errorf("store: finalize sessions migration: %w", err)
	}
	return nil
}
