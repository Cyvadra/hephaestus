package registry

import "testing"

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
