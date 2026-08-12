package registry

import (
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openSyncTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("HEPHAESTUS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("HEPHAESTUS_TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&Identity{}, &Impression{}, &ToolGroup{}, &Concierge{}, &Workflow{}, &Job{}, &TemplateState{}); err != nil {
		t.Fatalf("migrate registry tables: %v", err)
	}
	return db
}

func TestIntegration_SyncTemplatesLifecycle(t *testing.T) {
	db := openSyncTestDB(t)
	name := "sync-test-identity-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		db.Where("kind = ? AND name = ?", KindIdentity, name).Delete(&TemplateState{})
		db.Where("name = ?", name).Delete(&Identity{})
	})

	baseTime := time.Now().UTC().Add(-2 * time.Hour)
	value := &Identity{Name: name, Description: "template one", ContextWindowTokens: 1024}
	hash, err := templateHash(value)
	if err != nil {
		t.Fatalf("hash initial template: %v", err)
	}
	template := Template{Kind: KindIdentity, Name: name, Path: "identity-" + name + ".toml", ModifiedAt: baseTime, Hash: hash, Value: value}

	reg, result, err := SyncTemplates(db, []Template{template}, nil, nil)
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if result.Created != 1 || reg.Identities[name].Description != "template one" {
		t.Fatalf("unexpected initial sync: result=%+v identity=%+v", result, reg.Identities[name])
	}

	var created Identity
	if err := db.First(&created, "name = ?", name).Error; err != nil {
		t.Fatalf("load created identity: %v", err)
	}
	createdAt := created.CreatedAt
	created.Description = "database edit"
	if err := replaceRecord(db, KindIdentity, &created); err != nil {
		t.Fatalf("edit database identity: %v", err)
	}
	var edited Identity
	if err := db.First(&edited, "name = ?", name).Error; err != nil {
		t.Fatalf("reload edited identity: %v", err)
	}
	if !edited.CreatedAt.Equal(createdAt) || !edited.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("unexpected edit timestamps: before=%+v after=%+v", created.RecordTimestamps, edited.RecordTimestamps)
	}

	olderValue := &Identity{Name: name, Description: "older template", ContextWindowTokens: 1024}
	olderHash, err := templateHash(olderValue)
	if err != nil {
		t.Fatalf("hash older template: %v", err)
	}
	template.Value = olderValue
	template.Hash = olderHash
	template.ModifiedAt = edited.UpdatedAt.Add(-time.Minute)
	reg, result, err = SyncTemplates(db, []Template{template}, nil, nil)
	if err != nil {
		t.Fatalf("sync older template: %v", err)
	}
	if result.Preserved != 1 || reg.Identities[name].Description != "database edit" {
		t.Fatalf("older template replaced database edit: result=%+v identity=%+v", result, reg.Identities[name])
	}

	newerValue := &Identity{Name: name, Description: "newer template", ContextWindowTokens: 1024}
	newerHash, err := templateHash(newerValue)
	if err != nil {
		t.Fatalf("hash newer template: %v", err)
	}
	template.Value = newerValue
	template.Hash = newerHash
	template.ModifiedAt = time.Now().UTC().Add(time.Hour)
	reg, result, err = SyncTemplates(db, []Template{template}, nil, nil)
	if err != nil {
		t.Fatalf("sync newer template: %v", err)
	}
	if result.Updated != 1 || reg.Identities[name].Description != "newer template" {
		t.Fatalf("newer template was not applied: result=%+v identity=%+v", result, reg.Identities[name])
	}

	if err := db.Where("name = ?", name).Delete(&Identity{}).Error; err != nil {
		t.Fatalf("delete identity: %v", err)
	}
	reg, result, err = SyncTemplates(db, []Template{template}, nil, nil)
	if err != nil {
		t.Fatalf("restore deleted identity: %v", err)
	}
	if result.Created != 1 || reg.Identities[name].Description != "newer template" {
		t.Fatalf("deleted template record was not restored: result=%+v identity=%+v", result, reg.Identities[name])
	}
}

func TestIntegration_SyncTemplatesPreservesLegacyRecord(t *testing.T) {
	db := openSyncTestDB(t)
	name := "sync-test-legacy-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		db.Where("kind = ? AND name = ?", KindIdentity, name).Delete(&TemplateState{})
		db.Where("name = ?", name).Delete(&Identity{})
	})

	legacy := Identity{Name: name, Description: "legacy database value", ContextWindowTokens: 1024}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy identity: %v", err)
	}
	templateValue := &Identity{Name: name, Description: "template value", ContextWindowTokens: 1024}
	hash, err := templateHash(templateValue)
	if err != nil {
		t.Fatalf("hash template: %v", err)
	}
	template := Template{Kind: KindIdentity, Name: name, Path: "identity-" + name + ".toml", ModifiedAt: time.Now().UTC().Add(time.Hour), Hash: hash, Value: templateValue}

	reg, result, err := SyncTemplates(db, []Template{template}, nil, nil)
	if err != nil {
		t.Fatalf("sync legacy record: %v", err)
	}
	if result.Preserved != 1 || reg.Identities[name].Description != "legacy database value" {
		t.Fatalf("first sync replaced legacy record: result=%+v identity=%+v", result, reg.Identities[name])
	}
	var state TemplateState
	if err := db.First(&state, "kind = ? AND name = ?", KindIdentity, name).Error; err != nil {
		t.Fatalf("load template baseline: %v", err)
	}
	if state.ContentHash != hash {
		t.Fatalf("unexpected template baseline hash: %q", state.ContentHash)
	}
}

func TestIntegration_SyncTemplatesRollsBackInvalidRegistry(t *testing.T) {
	db := openSyncTestDB(t)
	name := "sync-test-invalid-group-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		db.Where("kind = ? AND name = ?", KindToolGroup, name).Delete(&TemplateState{})
		db.Where("name = ?", name).Delete(&ToolGroup{})
	})

	value := &ToolGroup{Name: name, Tools: []string{"unknown-sync-test-tool"}}
	hash, err := templateHash(value)
	if err != nil {
		t.Fatalf("hash invalid template: %v", err)
	}
	template := Template{Kind: KindToolGroup, Name: name, Path: "toolgroup-" + name + ".yaml", ModifiedAt: time.Now().UTC(), Hash: hash, Value: value}

	if _, _, err := SyncTemplates(db, []Template{template}, nil, nil); err == nil {
		t.Fatal("expected invalid registry synchronization to fail")
	}
	var recordCount int64
	if err := db.Model(&ToolGroup{}).Where("name = ?", name).Count(&recordCount).Error; err != nil {
		t.Fatalf("count rolled-back tool group: %v", err)
	}
	var stateCount int64
	if err := db.Model(&TemplateState{}).Where("kind = ? AND name = ?", KindToolGroup, name).Count(&stateCount).Error; err != nil {
		t.Fatalf("count rolled-back template state: %v", err)
	}
	if recordCount != 0 || stateCount != 0 {
		t.Fatalf("invalid sync was not fully rolled back: records=%d states=%d", recordCount, stateCount)
	}
}
