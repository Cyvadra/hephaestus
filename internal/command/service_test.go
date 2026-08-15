package command

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
)

func testService() *Service {
	reg := &registry.Registry{
		Identities:  map[string]registry.Identity{"default": {Name: "default"}},
		Impressions: map[string]registry.Impression{"concise": {Name: "concise"}},
		ToolGroups:  map[string]registry.ToolGroup{"basic": {Name: "basic"}},
		Concierges:  map[string]registry.Concierge{"default": {Name: "default"}},
	}
	return &Service{
		registries: registry.NewStore(reg),
		lastList:   map[uint]map[Kind][]string{},
		cancels:    map[uint]cancelRegistration{},
	}
}

func TestValidateKindNameRejectsUnknownConfiguredName(t *testing.T) {
	service := testService()
	for _, kind := range []Kind{KindIdentity, KindImpression, KindToolGroup, KindConcierge} {
		if err := validateKindName(service, kind, "missing"); err == nil {
			t.Fatalf("expected unknown %s to be rejected", kind)
		}
	}
}

func TestValidateKindNameUsesPublishedRegistry(t *testing.T) {
	service := testService()
	if err := validateKindName(service, KindIdentity, "updated"); err == nil {
		t.Fatal("expected unpublished identity to be rejected")
	}
	service.registries.Publish(&registry.Registry{
		Identities: map[string]registry.Identity{"updated": {Name: "updated"}},
	})
	if err := validateKindName(service, KindIdentity, "updated"); err != nil {
		t.Fatalf("expected published identity to be accepted: %v", err)
	}
}

func TestResolveNameUsesExplicitListReference(t *testing.T) {
	service := testService()
	service.lastList[7] = map[Kind][]string{KindProject: {"first", "second"}, KindConcierge: {"default"}}

	got, err := service.resolveName(7, KindProject, "#2")
	if err != nil || got != "second" {
		t.Fatalf("resolve #2: got %q, err %v", got, err)
	}
	got, err = service.resolveName(7, KindProject, "2")
	if err != nil || got != "second" {
		t.Fatalf("resolve bare 2: got %q, err %v", got, err)
	}
	got, err = service.resolveName(7, KindConcierge, "1")
	if err != nil || got != "default" {
		t.Fatalf("resolve bare 1: got %q, err %v", got, err)
	}
	got, err = service.resolveName(7, KindProject, "123")
	if err != nil || got != "123" {
		t.Fatalf("numeric literal name without matching list item: got %q, err %v", got, err)
	}
}

func TestResolveSessionIDDistinguishesOrdinalsFromStableIDs(t *testing.T) {
	service := testService()
	service.lastList[7] = map[Kind][]string{KindSession: {"42", "99"}}

	got, err := service.resolveSessionID(7, "2")
	if err != nil || got != 99 {
		t.Fatalf("resolve session ordinal: got %d, err %v", got, err)
	}
	got, err = service.resolveSessionID(7, "#42")
	if err != nil || got != 42 {
		t.Fatalf("resolve stable session ID: got %d, err %v", got, err)
	}
	if _, err := service.resolveSessionID(7, "42"); err == nil {
		t.Fatal("bare session ID must not bypass the latest session list")
	}
	if _, err := service.resolveSessionID(7, "3"); err == nil {
		t.Fatal("out-of-range session ordinal must be rejected")
	}
}

func TestSessionListItemsUseTitleLabelAndStableID(t *testing.T) {
	items := sessionListItems([]store.Session{
		{ID: 42, ProjectID: 1, Project: store.Project{Name: "alpha"}, Title: "Release checklist"},
		{ID: 43, ProjectID: 2, Project: store.Project{Name: "beta"}},
	}, 42)
	if got := items[0]; got.name != "42" || got.label != "* Release checklist (#42)" || got.group != "alpha" {
		t.Fatalf("unexpected titled session item: %#v", got)
	}
	if got := items[1]; got.name != "43" || got.label != "Session #43" || got.group != "beta" {
		t.Fatalf("unexpected untitled session item: %#v", got)
	}
}

func TestSessionListItemsGroupsProjectsByLatestSession(t *testing.T) {
	items := sessionListItems([]store.Session{
		{ID: 30, ProjectID: 2, Project: store.Project{Name: "beta"}},
		{ID: 20, ProjectID: 1, Project: store.Project{Name: "alpha"}},
		{ID: 10, ProjectID: 2, Project: store.Project{Name: "beta"}},
	}, 0)

	got := []string{items[0].name, items[1].name, items[2].name}
	want := []string{"30", "10", "20"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("grouped session order = %v, want %v", got, want)
	}

	wantOutput := "beta:\n1. Session #30\n2. Session #10\nalpha:\n3. Session #20\n"
	if gotOutput := formatList(KindSession, items); gotOutput != wantOutput {
		t.Fatalf("formatted session list = %q, want %q", gotOutput, wantOutput)
	}
}

func TestMarkActiveItemsMarksAllEnabledNamesWithoutChangingOrdinals(t *testing.T) {
	items := markActiveItems(namedItems([]string{"first", "second", "third"}), []string{"first", "third"})

	if got := formatList(KindPlugin, items); got != "1. * first\n2. second\n3. * third\n" {
		t.Fatalf("formatted active list = %q", got)
	}
	if items[0].name != "first" || items[2].name != "third" {
		t.Fatalf("active markers changed list names: %#v", items)
	}
}

func TestKeysOfSortsNamesForStableListReferences(t *testing.T) {
	got := keysOf(map[string]struct{}{"third": {}, "first": {}, "second": {}})
	want := []string{"first", "second", "third"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keysOf() = %v, want %v", got, want)
	}
}

func TestSwitchSessionIsNotAdvertisedOrSupported(t *testing.T) {
	if strings.Contains(helpText, "identity|concierge|session|project") {
		t.Fatal("help still advertises a server-side session switch")
	}
}

func TestCancelRegistrationCannotBeRemovedByOlderTurn(t *testing.T) {
	service := testService()
	firstID := service.RegisterCancel(4, func() {})
	secondCanceled := false
	secondID := service.RegisterCancel(4, func() { secondCanceled = true })

	service.UnregisterCancel(4, firstID)
	if got := service.stop(4); got != "Stopping current task." {
		t.Fatalf("unexpected stop response: %q", got)
	}
	if !secondCanceled {
		t.Fatal("older turn removed the current cancellation registration")
	}
	service.UnregisterCancel(4, secondID)
	if _, ok := service.cancels[4]; ok {
		t.Fatal("current cancellation registration was not removed")
	}
}

func TestRegisterCancelReturnsMonotonicIDs(t *testing.T) {
	service := testService()
	first := service.RegisterCancel(1, context.CancelFunc(func() {}))
	second := service.RegisterCancel(1, context.CancelFunc(func() {}))
	if second <= first {
		t.Fatalf("registration IDs are not monotonic: %d then %d", first, second)
	}
}
