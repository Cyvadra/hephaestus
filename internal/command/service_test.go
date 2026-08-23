package command

import (
	"context"
	"strings"
	"testing"

	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
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

func TestSaveConciergeSettingsPersistsConciergeAndSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&store.Project{}, &store.Session{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	project := store.Project{Name: "default"}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	sess := store.Session{
		ProjectID:       project.ID,
		SourceConcierge: "default",
		Settings:        datatypes.NewJSONType(store.SessionSettings{Identity: "Default"}),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}

	service := &Service{db: db}
	wantSettings := store.SessionSettings{Identity: "Rose"}
	if err := service.saveConciergeSettings(&sess, "rose-initial", wantSettings); err != nil {
		t.Fatalf("save concierge settings: %v", err)
	}

	var reloaded store.Session
	if err := db.First(&reloaded, sess.ID).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.SourceConcierge != "rose-initial" {
		t.Fatalf("source concierge = %q, want %q", reloaded.SourceConcierge, "rose-initial")
	}
	if reloaded.Settings.Data().Identity != "Rose" {
		t.Fatalf("identity = %q, want %q", reloaded.Settings.Data().Identity, "Rose")
	}
}

func TestFirstAvailableConciergeUsesProjectOrderAndSkipsMissing(t *testing.T) {
	project := store.Project{AvailableConciergeList: []string{"deleted", "rose-initial", "default"}}
	concierges := map[string]registry.Concierge{
		"default":      {Name: "default"},
		"rose-initial": {Name: "rose-initial", Identity: "Rose"},
	}

	got, ok := firstAvailableConcierge(project, concierges)
	if !ok || got.Name != "rose-initial" {
		t.Fatalf("firstAvailableConcierge() = (%q, %v), want (%q, true)", got.Name, ok, "rose-initial")
	}
}

func TestFirstAvailableConciergeRejectsProjectWithoutRegisteredConcierge(t *testing.T) {
	project := store.Project{AvailableConciergeList: []string{"deleted"}}
	if _, ok := firstAvailableConcierge(project, map[string]registry.Concierge{}); ok {
		t.Fatal("expected no available registered concierge")
	}
}

func TestSwitchProjectCanApproveConciergeSwitch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&store.Project{}, &store.Session{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	currentProject := store.Project{Name: "current", AvailableConciergeList: []string{"default"}}
	targetProject := store.Project{Name: "lifespace", AvailableConciergeList: []string{"rose-initial"}}
	if err := db.Create(&currentProject).Error; err != nil {
		t.Fatalf("create current project: %v", err)
	}
	if err := db.Create(&targetProject).Error; err != nil {
		t.Fatalf("create target project: %v", err)
	}
	sess := store.Session{
		ProjectID:       currentProject.ID,
		SourceConcierge: "default",
		Settings:        datatypes.NewJSONType(store.SessionSettings{Identity: "Default"}),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	projects, err := project.New(db, t.TempDir())
	if err != nil {
		t.Fatalf("create project service: %v", err)
	}
	interactions := interaction.NewManager()
	service := &Service{
		db: db, projects: projects, interactions: interactions,
		registries: registry.NewStore(&registry.Registry{Concierges: map[string]registry.Concierge{
			"default":      {Name: "default", Identity: "Default"},
			"rose-initial": {Name: "rose-initial", Identity: "Rose"},
		}}),
		lastList: map[uint]map[Kind][]string{}, cancels: map[uint]cancelRegistration{},
	}
	events := make(chan interaction.Event, 1)
	ctx := interaction.WithReporter(context.Background(), func(event interaction.Event) { events <- event })

	type commandResult struct {
		result Result
		err    error
	}
	done := make(chan commandResult, 1)
	go func() {
		result, executeErr := service.ExecuteResultContext(ctx, sess.ID, "/switch project lifespace")
		done <- commandResult{result: result, err: executeErr}
	}()

	event := <-events
	if !strings.Contains(event.Request.Details, `"rose-initial"`) {
		t.Fatalf("approval details = %q, want rose-initial", event.Request.Details)
	}
	if err := interactions.Respond(sess.ID, true); err != nil {
		t.Fatalf("approve switch: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("switch project: %v", got.err)
	}
	if !strings.Contains(got.result.Response, `project "lifespace"`) {
		t.Fatalf("response = %q", got.result.Response)
	}

	var reloaded store.Session
	if err := db.First(&reloaded, sess.ID).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.ProjectID != targetProject.ID || reloaded.SourceConcierge != "rose-initial" || reloaded.Settings.Data().Identity != "Rose" {
		t.Fatalf("session after switch = project %d, concierge %q, identity %q", reloaded.ProjectID, reloaded.SourceConcierge, reloaded.Settings.Data().Identity)
	}
}

func TestFirstUnavailableRequiresConciergeCapability(t *testing.T) {
	if got := firstUnavailable([]string{"basic", "optional"}, []string{"basic"}); got != "optional" {
		t.Fatalf("firstUnavailable() = %q, want optional", got)
	}
	if got := firstUnavailable([]string{"basic"}, []string{"basic", "optional"}); got != "" {
		t.Fatalf("firstUnavailable() rejected available capability %q", got)
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

func TestCommandNamesAndHelpShareDefinitions(t *testing.T) {
	for _, name := range Names() {
		if !strings.Contains(helpText, "/"+name) {
			t.Fatalf("help omits registered command %q", name)
		}
	}
}

func TestParseArchiveArgumentDefaultsToFalse(t *testing.T) {
	for _, test := range []struct {
		args    []string
		want    bool
		wantErr bool
	}{
		{args: nil, want: false},
		{args: []string{"false"}, want: false},
		{args: []string{"true"}, want: true},
		{args: []string{"TRUE"}, wantErr: true},
		{args: []string{"1"}, wantErr: true},
		{args: []string{"true", "false"}, wantErr: true},
	} {
		got, err := parseArchiveArgument("new", test.args)
		if (err != nil) != test.wantErr {
			t.Fatalf("parseArchiveArgument(%v) error = %v, wantErr %v", test.args, err, test.wantErr)
		}
		if err == nil && got != test.want {
			t.Fatalf("parseArchiveArgument(%v) = %v, want %v", test.args, got, test.want)
		}
	}
}

func TestParseHistoryCount(t *testing.T) {
	for _, test := range []struct {
		args    []string
		want    int
		wantErr bool
	}{
		{args: nil, want: 1},
		{args: []string{"3"}, want: 3},
		{args: []string{"0"}, wantErr: true},
		{args: []string{"nope"}, wantErr: true},
		{args: []string{"1", "2"}, wantErr: true},
		{args: []string{"21"}, wantErr: true},
	} {
		got, err := parseHistoryCount("last", test.args)
		if (err != nil) != test.wantErr || (err == nil && got != test.want) {
			t.Fatalf("parseHistoryCount(%v) = %d, %v; want %d, wantErr %v", test.args, got, err, test.want, test.wantErr)
		}
	}
}

func TestReverseReplayedPreservesChronologicalOrder(t *testing.T) {
	messages := []ReplayedMessage{{Content: "newest"}, {Content: "older"}}
	reverseReplayed(messages)
	if messages[0].Content != "older" || messages[1].Content != "newest" {
		t.Fatalf("unexpected replay order: %#v", messages)
	}
}

func TestInteractAutomaticApprovalCommands(t *testing.T) {
	service := testService()
	service.interactions = interaction.NewManager()

	if _, err := service.Execute(7, "/interact "+interactionAutoApprove); err != nil {
		t.Fatalf("enable automatic approval: %v", err)
	}
	if !service.AutoApprove(7) {
		t.Fatal("automatic approval should be enabled")
	}
	if service.AutoApprove(8) {
		t.Fatal("automatic approval should remain scoped to its session")
	}
	if _, err := service.Execute(7, "/interact "+interactionCancelAutoApprove); err != nil {
		t.Fatalf("cancel automatic approval: %v", err)
	}
	if service.AutoApprove(7) {
		t.Fatal("automatic approval should be disabled")
	}
	if _, err := service.Execute(7, "/interact auto-deny"); err == nil {
		t.Fatal("deprecated automatic approval command should be rejected")
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
