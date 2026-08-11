package command

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/registry"
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
	service.lastList[7] = map[Kind][]string{KindProject: {"first", "second"}}

	got, err := service.resolveName(7, KindProject, "#2")
	if err != nil || got != "second" {
		t.Fatalf("resolve #2: got %q, err %v", got, err)
	}
	got, err = service.resolveName(7, KindProject, "123")
	if err != nil || got != "123" {
		t.Fatalf("numeric literal name: got %q, err %v", got, err)
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
