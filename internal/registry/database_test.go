package registry

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStorePublishesCompleteSnapshots(t *testing.T) {
	first := &Registry{Identities: map[string]Identity{"first": {Name: "first"}}}
	second := &Registry{Identities: map[string]Identity{"second": {Name: "second"}}}
	store := NewStore(first)

	if store.Current() != first {
		t.Fatal("expected initial registry snapshot")
	}
	store.Publish(second)
	if store.Current() != second {
		t.Fatal("expected published registry snapshot")
	}
}

func TestStoreConcurrentReadAndPublish(t *testing.T) {
	first := &Registry{Identities: map[string]Identity{"first": {Name: "first"}}}
	second := &Registry{Identities: map[string]Identity{"second": {Name: "second"}}}
	store := NewStore(first)

	var wait sync.WaitGroup
	for range 10 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				current := store.Current()
				if current != first && current != second {
					t.Errorf("read partial or unknown snapshot: %p", current)
				}
			}
		}()
	}
	for range 100 {
		store.Publish(second)
		store.Publish(first)
	}
	wait.Wait()
}

func TestClone_MapsAreIndependent(t *testing.T) {
	original := &Registry{
		Identities:  map[string]Identity{"default": {Name: "default"}},
		Impressions: map[string]Impression{},
		ToolGroups:  map[string]ToolGroup{},
		Concierges:  map[string]Concierge{},
		Workflows:   map[string]Workflow{},
		Jobs:        map[string]Job{},
	}

	clone := original.Clone()
	clone.Identities["database"] = Identity{Name: "database"}
	delete(clone.Identities, "default")

	if _, ok := original.Identities["default"]; !ok {
		t.Fatal("clone mutation removed original value")
	}
	if _, ok := original.Identities["database"]; ok {
		t.Fatal("clone mutation added original value")
	}
}

func TestNormalizeIdentity_AppliesDefaultPrompt(t *testing.T) {
	identity := Identity{Name: "default", ContextWindowTokens: 1}
	if err := normalizeIdentity(&identity); err != nil {
		t.Fatalf("normalizeIdentity: %v", err)
	}
	if identity.SystemPrompt != DefaultSystemPrompt {
		t.Fatalf("expected default prompt %q, got %q", DefaultSystemPrompt, identity.SystemPrompt)
	}
	if identity.InjectedMessages == nil {
		t.Fatal("expected nil injected messages to normalize to an empty slice")
	}
}

func TestNormalizeImpression_InitializesMessages(t *testing.T) {
	impression := Impression{Name: "default"}
	normalizeImpression(&impression)
	if impression.Messages == nil {
		t.Fatal("expected nil messages to normalize to an empty slice")
	}
}

func TestNormalizeConcierge_DefaultsNicknameToName(t *testing.T) {
	concierge := Concierge{Name: "default", ToolGroups: []string{"basic"}, Plugins: []string{"audit"}}
	if err := normalizeConcierge(&concierge); err != nil {
		t.Fatalf("normalizeConcierge: %v", err)
	}
	if concierge.Nickname != concierge.Name {
		t.Fatalf("expected nickname %q, got %q", concierge.Name, concierge.Nickname)
	}
	if len(concierge.DefaultToolGroups) != 1 || concierge.DefaultToolGroups[0] != "basic" {
		t.Fatalf("expected omitted defaults to inherit tool groups, got %v", concierge.DefaultToolGroups)
	}
	if len(concierge.DefaultPlugins) != 1 || concierge.DefaultPlugins[0] != "audit" {
		t.Fatalf("expected omitted defaults to inherit plugins, got %v", concierge.DefaultPlugins)
	}
}

func TestNormalizeConcierge_PreservesExplicitEmptyDefaults(t *testing.T) {
	concierge := Concierge{
		Name:              "default",
		ToolGroups:        []string{"basic"},
		DefaultToolGroups: []string{},
		Plugins:           []string{"audit"},
		DefaultPlugins:    []string{},
	}
	if err := normalizeConcierge(&concierge); err != nil {
		t.Fatalf("normalizeConcierge: %v", err)
	}
	if concierge.DefaultToolGroups == nil || len(concierge.DefaultToolGroups) != 0 {
		t.Fatalf("expected explicit empty default tool groups, got %v", concierge.DefaultToolGroups)
	}
	if concierge.DefaultPlugins == nil || len(concierge.DefaultPlugins) != 0 {
		t.Fatalf("expected explicit empty default plugins, got %v", concierge.DefaultPlugins)
	}
}

func TestNormalizeConcierge_LimitsNicknameByUnicodeCharacters(t *testing.T) {
	concierge := Concierge{Name: "default", Nickname: strings.Repeat("助", 20)}
	if err := normalizeConcierge(&concierge); err != nil {
		t.Fatalf("normalize 20-character nickname: %v", err)
	}

	concierge.Nickname = strings.Repeat("助", 21)
	if err := normalizeConcierge(&concierge); err == nil {
		t.Fatal("expected 21-character nickname to be rejected")
	}
}

func TestValidatePersistedName(t *testing.T) {
	if err := validatePersistedName(KindJob, ""); err == nil {
		t.Fatal("expected empty persisted name to be rejected")
	}
	if err := validatePersistedName(KindJob, "morning-brief"); err != nil {
		t.Fatalf("validate non-empty persisted name: %v", err)
	}
}

func TestCatalogNameHelpers_SortAndFilter(t *testing.T) {
	mapNames := sortedMapKeys(map[string]Identity{
		"zeta":  {Name: "zeta"},
		"alpha": {Name: "alpha"},
	})
	if len(mapNames) != 2 || mapNames[0] != "alpha" || mapNames[1] != "zeta" {
		t.Fatalf("unexpected sorted map names: %v", mapNames)
	}

	boolNames := sortedBoolKeys(map[string]bool{
		"web_search": true,
		"disabled":   false,
		"shell":      true,
	})
	if len(boolNames) != 2 || boolNames[0] != "shell" || boolNames[1] != "web_search" {
		t.Fatalf("unexpected sorted boolean names: %v", boolNames)
	}
}

func TestRecordTimestamps_AreHiddenFromConfigurationJSON(t *testing.T) {
	identity := Identity{
		RecordTimestamps: RecordTimestamps{
			CreatedAt: time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC),
		},
		Name: "default",
	}
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "created_at") || strings.Contains(text, "updated_at") || strings.Contains(text, "CreatedAt") || strings.Contains(text, "UpdatedAt") {
		t.Fatalf("configuration JSON exposed internal timestamps: %s", text)
	}
}
