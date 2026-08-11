// Package command implements the platform's slash-command surface
// (/help, /status, /list, /switch, ...). Command input never enters the
// LLM context, and template responses are never persisted to chat history.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/toolkit"
	"github.com/Cyvadra/hephaestus/internal/transform"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Kind is one of the config/runtime concepts addressable via /list, /detail,
// /switch, /activate and /deactivate.
type Kind string

const (
	KindIdentity   Kind = "identity"
	KindImpression Kind = "impression"
	KindToolGroup  Kind = "toolgroup"
	KindPlugin     Kind = "plugin"
	KindConcierge  Kind = "concierge"
	KindSession    Kind = "session"
	KindJob        Kind = "job"
	KindWorkflow   Kind = "workflow"
	KindProject    Kind = "project"
)

// Service dispatches slash commands against the platform's static registry
// and runtime store.
type Service struct {
	registries   *registry.Store
	toolReg      *toolkit.Registry
	pluginReg    *plugin.Registry
	sessions     *session.Service
	notifier     *notify.Notifier
	db           *gorm.DB
	projects     *project.Service
	interactions *interaction.Manager

	mu       sync.Mutex
	lastList map[uint]map[Kind][]string
	cancels  map[uint]cancelRegistration
	nextTurn uint64
}

type cancelRegistration struct {
	id     uint64
	cancel context.CancelFunc
}

// NewService wires the command dispatcher to its dependencies.
func NewService(registries *registry.Store, toolReg *toolkit.Registry, pluginReg *plugin.Registry, sessions *session.Service, notifier *notify.Notifier, db *gorm.DB, projects *project.Service, interactions *interaction.Manager) *Service {
	return &Service{
		registries:   registries,
		toolReg:      toolReg,
		pluginReg:    pluginReg,
		sessions:     sessions,
		notifier:     notifier,
		db:           db,
		projects:     projects,
		interactions: interactions,
		lastList:     map[uint]map[Kind][]string{},
		cancels:      map[uint]cancelRegistration{},
	}
}

func (s *Service) currentRegistry() *registry.Registry {
	return s.registries.Current()
}

// IsCommand reports whether text is a slash command rather than a chat
// message.
func IsCommand(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}

// RegisterCancel records cancel as the way to interrupt sessionID's
// in-flight turn, so a later /stop can invoke it. Callers must call
// UnregisterCancel once the turn finishes.
func (s *Service) RegisterCancel(sessionID uint, cancel context.CancelFunc) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextTurn++
	s.cancels[sessionID] = cancelRegistration{id: s.nextTurn, cancel: cancel}
	return s.nextTurn
}

// UnregisterCancel removes sessionID's cancel func once its turn is done.
func (s *Service) UnregisterCancel(sessionID uint, registrationID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if registration, ok := s.cancels[sessionID]; ok && registration.id == registrationID {
		delete(s.cancels, sessionID)
	}
}

// Execute parses and runs a slash command, returning its template response.
// The response must never be persisted or fed back into the LLM context.
func (s *Service) Execute(sessionID uint, text string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", fmt.Errorf("command: empty input")
	}
	name := fields[0]
	args := fields[1:]

	switch name {
	case "/help":
		return helpText, nil
	case "/ping":
		return "pong", nil
	case "/stop":
		return s.stop(sessionID), nil
	case "/status":
		return s.status(sessionID)
	case "/list":
		return s.list(sessionID, args)
	case "/detail":
		return s.detail(sessionID, args)
	case "/switch":
		return s.switchTo(sessionID, args)
	case "/activate":
		return s.setActive(sessionID, args, true)
	case "/deactivate":
		return s.setActive(sessionID, args, false)
	case "/clear":
		return s.clear(sessionID)
	case "/new":
		return s.new(sessionID)
	case "/interact":
		return s.interact(sessionID, args)
	default:
		return "", fmt.Errorf("command: unknown command %q", name)
	}
}

func (s *Service) interact(sessionID uint, args []string) (string, error) {
	if len(args) != 1 || (args[0] != "approve" && args[0] != "deny") {
		return "", fmt.Errorf("command: usage: /interact <approve|deny>")
	}
	if s.interactions == nil {
		return "", fmt.Errorf("command: interactions are not configured")
	}
	if err := s.interactions.Respond(sessionID, args[0] == "approve"); err != nil {
		if errors.Is(err, interaction.ErrNoPending) {
			return "", fmt.Errorf("command: no interaction is awaiting a response")
		}
		return "", err
	}
	if args[0] == "approve" {
		return "Permission approved. Continuing the task.", nil
	}
	return "Permission denied. The requested operation will not run.", nil
}

const helpText = `Available commands:
/help - show this help
/ping - platform health
/stop - stop the current task
/status - session info, context usage, recent warnings
/list <kind> - list available options (identity|impression|toolgroup|plugin|concierge|session|job|workflow|project)
/detail <kind> <id> - show details of one option
/switch <identity|concierge> <#id|name> - switch
/activate <impression|toolgroup|plugin> <#id[,#id...]|name[,name...]> - enable
/deactivate <impression|toolgroup|plugin> <#id[,#id...]|name[,name...]> - disable
/clear - archive this session and start a fresh one with the same settings
/new - archive this session and start a fresh one from its source concierge
/interact <approve|deny> - respond to a pending runtime request`

// kindDescriptor bundles the per-Kind lookups /list and /detail need, so
// adding a new Kind means adding one table entry instead of extending two
// parallel switch statements. Either func may be nil where that operation
// isn't supported for the Kind (e.g. /detail on a plugin).
type kindDescriptor struct {
	names    func(s *Service) ([]string, error)
	detail   func(s *Service, name string) (any, error)
	validate func(s *Service, name string) error
}

func knownName[T any](kind Kind, values func(*Service) map[string]T) func(*Service, string) error {
	return func(s *Service, name string) error {
		if _, ok := values(s)[name]; !ok {
			return fmt.Errorf("command: unknown %s %q", kind, name)
		}
		return nil
	}
}

var kindDescriptors = map[Kind]kindDescriptor{
	KindIdentity: {
		names:    func(s *Service) ([]string, error) { return keysOf(s.currentRegistry().Identities), nil },
		detail:   func(s *Service, name string) (any, error) { return s.currentRegistry().Identities[name], nil },
		validate: knownName(KindIdentity, func(s *Service) map[string]registry.Identity { return s.currentRegistry().Identities }),
	},
	KindImpression: {
		names:    func(s *Service) ([]string, error) { return keysOf(s.currentRegistry().Impressions), nil },
		detail:   func(s *Service, name string) (any, error) { return s.currentRegistry().Impressions[name], nil },
		validate: knownName(KindImpression, func(s *Service) map[string]registry.Impression { return s.currentRegistry().Impressions }),
	},
	KindToolGroup: {
		names:    func(s *Service) ([]string, error) { return keysOf(s.currentRegistry().ToolGroups), nil },
		detail:   func(s *Service, name string) (any, error) { return s.currentRegistry().ToolGroups[name], nil },
		validate: knownName(KindToolGroup, func(s *Service) map[string]registry.ToolGroup { return s.currentRegistry().ToolGroups }),
	},
	KindPlugin: {
		names: func(s *Service) ([]string, error) { return keysOf(s.pluginReg.KnownNames()), nil },
		validate: func(s *Service, name string) error {
			if !s.pluginReg.Has(name) {
				return fmt.Errorf("command: unknown plugin %q", name)
			}
			return nil
		},
	},
	KindConcierge: {
		names:    func(s *Service) ([]string, error) { return keysOf(s.currentRegistry().Concierges), nil },
		detail:   func(s *Service, name string) (any, error) { return s.currentRegistry().Concierges[name], nil },
		validate: knownName(KindConcierge, func(s *Service) map[string]registry.Concierge { return s.currentRegistry().Concierges }),
	},
	KindWorkflow: {
		names:  func(s *Service) ([]string, error) { return keysOf(s.currentRegistry().Workflows), nil },
		detail: func(s *Service, name string) (any, error) { return s.currentRegistry().Workflows[name], nil },
	},
	KindJob: {
		names:  func(s *Service) ([]string, error) { return keysOf(s.currentRegistry().Jobs), nil },
		detail: func(s *Service, name string) (any, error) { return s.currentRegistry().Jobs[name], nil },
	},
	KindSession: {
		names: func(s *Service) ([]string, error) {
			var sessions []store.Session
			if err := s.db.Where("flag_archived = ?", false).Find(&sessions).Error; err != nil {
				return nil, err
			}
			names := make([]string, 0, len(sessions))
			for _, sess := range sessions {
				names = append(names, strconv.Itoa(int(sess.ID)))
			}
			return names, nil
		},
		detail: func(s *Service, name string) (any, error) {
			id, err := strconv.Atoi(name)
			if err != nil {
				return nil, fmt.Errorf("command: invalid session id %q", name)
			}
			return s.loadSession(uint(id))
		},
	},
	KindProject: {
		names: func(s *Service) ([]string, error) {
			projects, err := s.projects.List()
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(projects))
			for _, p := range projects {
				names = append(names, p.Name)
			}
			return names, nil
		},
		detail: func(s *Service, name string) (any, error) { return s.projects.GetByName(name) },
		validate: func(s *Service, name string) error {
			if _, err := s.projects.GetByName(name); err != nil {
				return fmt.Errorf("command: unknown project %q", name)
			}
			return nil
		},
	},
}

func (s *Service) stop(sessionID uint) string {
	s.mu.Lock()
	registration, ok := s.cancels[sessionID]
	s.mu.Unlock()
	if !ok {
		return "Nothing is currently running for this session."
	}
	registration.cancel()
	return "Stopping current task."
}

func (s *Service) status(sessionID uint) (string, error) {
	sess, err := s.loadSession(sessionID)
	if err != nil {
		return "", err
	}
	settings := sess.Settings.Data()

	activePath, err := s.sessions.ActivePath(*sess)
	if err != nil {
		return "", err
	}
	total := 0
	for _, m := range activePath {
		total += transform.EstimateLength(m.Content)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "session: %d (concierge: %s, archived: %v)\n", sess.ID, sess.SourceConcierge, sess.FlagArchived)
	fmt.Fprintf(&b, "identity: %s\n", settings.Identity)
	fmt.Fprintf(&b, "impressions: %s\n", strings.Join(settings.Impressions, ", "))
	fmt.Fprintf(&b, "tool_groups: %s\n", strings.Join(settings.ToolGroups, ", "))
	fmt.Fprintf(&b, "plugins: %s\n", strings.Join(settings.Plugins, ", "))
	boundProject, err := s.projects.Get(sess.ProjectID)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "project: %s\n", boundProject.Name)
	fmt.Fprintf(&b, "context usage (estimated units): %d\n", total)
	if sess.CompressionID != nil {
		fmt.Fprintf(&b, "compression: #%d (up to message %d)\n", *sess.CompressionID, *sess.CompressionLastMessageID)
	}

	b.WriteString("\nrecent warnings:\n")
	for _, e := range s.notifier.Recent() {
		fmt.Fprintf(&b, "[%s] %s: %s\n", e.Time.Format(time.RFC3339), e.Level, e.Message)
	}

	return b.String(), nil
}

func (s *Service) list(sessionID uint, args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("command: usage: /list <kind>")
	}
	kind := Kind(args[0])
	desc, ok := kindDescriptors[kind]
	if !ok || desc.names == nil {
		return "", fmt.Errorf("command: unknown kind %q", kind)
	}
	names, err := desc.names(s)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	if s.lastList[sessionID] == nil {
		s.lastList[sessionID] = map[Kind][]string{}
	}
	s.lastList[sessionID][kind] = names
	s.mu.Unlock()

	var b strings.Builder
	for i, name := range names {
		fmt.Fprintf(&b, "%d. %s\n", i+1, name)
	}
	if b.Len() == 0 {
		return fmt.Sprintf("No %s configured.", kind), nil
	}
	return b.String(), nil
}

func (s *Service) detail(sessionID uint, args []string) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("command: usage: /detail <kind> <id>")
	}
	kind := Kind(args[0])
	name, err := s.resolveName(sessionID, kind, args[1])
	if err != nil {
		return "", err
	}

	desc, ok := kindDescriptors[kind]
	if !ok || desc.detail == nil {
		return "", fmt.Errorf("command: /detail does not support kind %q", kind)
	}
	v, err := desc.detail(s, name)
	if err != nil {
		return "", err
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) switchTo(sessionID uint, args []string) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("command: usage: /switch <identity|concierge|project> <#id|name>")
	}
	kind, target := Kind(args[0]), args[1]

	sess, err := s.loadSession(sessionID)
	if err != nil {
		return "", err
	}
	settings := sess.Settings.Data()

	switch kind {
	case KindIdentity:
		name, err := s.resolveName(sessionID, KindIdentity, target)
		if err != nil {
			return "", err
		}
		if err := validateKindName(s, KindIdentity, name); err != nil {
			return "", err
		}
		settings.Identity = name
		if err := s.saveSettings(sess, settings); err != nil {
			return "", err
		}
		return fmt.Sprintf("Switched identity to %q.", name), nil

	case KindConcierge:
		name, err := s.resolveName(sessionID, KindConcierge, target)
		if err != nil {
			return "", err
		}
		if err := validateKindName(s, KindConcierge, name); err != nil {
			return "", err
		}
		c := s.currentRegistry().Concierges[name]
		nextSettings := session.SettingsFromConcierge(c)
		settings = nextSettings
		sess.SourceConcierge = c.Name
		if err := s.saveSettings(sess, settings); err != nil {
			return "", err
		}
		return fmt.Sprintf("Switched to concierge %q (identity now %q).", name, c.Identity), nil

	default:
		return "", fmt.Errorf("command: /switch does not support kind %q", kind)
	}
}

func (s *Service) setActive(sessionID uint, args []string, active bool) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("command: usage: /activate|/deactivate <impression|toolgroup|plugin> <id[,id...]>")
	}
	kind := Kind(args[0])
	ids := strings.Split(args[1], ",")

	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name, err := s.resolveName(sessionID, kind, strings.TrimSpace(id))
		if err != nil {
			return "", err
		}
		names = append(names, name)
	}
	for _, name := range names {
		if err := validateKindName(s, kind, name); err != nil {
			return "", err
		}
	}

	sess, err := s.loadSession(sessionID)
	if err != nil {
		return "", err
	}
	settings := sess.Settings.Data()

	var target *[]string
	switch kind {
	case KindImpression:
		target = &settings.Impressions
	case KindToolGroup:
		target = &settings.ToolGroups
	case KindPlugin:
		if !active {
			for _, name := range names {
				if s.pluginReg.IsFixed(name) {
					return "", fmt.Errorf("command: plugin %q is fixed and cannot be deactivated", name)
				}
			}
		}
		target = &settings.Plugins
	default:
		return "", fmt.Errorf("command: /activate and /deactivate do not support kind %q", kind)
	}

	if active {
		*target = unionAppend(*target, names)
	} else {
		*target = removeAll(*target, names)
	}

	if err := s.saveSettings(sess, settings); err != nil {
		return "", err
	}
	verb := "Activated"
	if !active {
		verb = "Deactivated"
	}
	return fmt.Sprintf("%s %s: %s.", verb, kind, strings.Join(names, ", ")), nil
}

func (s *Service) clear(sessionID uint) (string, error) {
	sess, err := s.loadSession(sessionID)
	if err != nil {
		return "", err
	}
	settings := sess.Settings.Data()
	newSess, err := s.sessions.Replace(sess.ID, sess.SourceConcierge, settings)
	if err != nil {
		return "", err
	}
	s.forgetSession(sess.ID)
	return fmt.Sprintf("Archived session %d. New session: %d.", sess.ID, newSess.ID), nil
}

func (s *Service) new(sessionID uint) (string, error) {
	sess, err := s.loadSession(sessionID)
	if err != nil {
		return "", err
	}
	c, ok := s.currentRegistry().Concierges[sess.SourceConcierge]
	if !ok {
		return "", fmt.Errorf("command: source concierge %q no longer exists", sess.SourceConcierge)
	}
	newSess, err := s.sessions.Replace(sess.ID, c.Name, session.SettingsFromConcierge(c))
	if err != nil {
		return "", err
	}
	s.forgetSession(sess.ID)
	return fmt.Sprintf("Archived session %d. New session: %d (from concierge %q).", sess.ID, newSess.ID, c.Name), nil
}

func (s *Service) loadSession(id uint) (*store.Session, error) {
	var sess store.Session
	if err := s.db.First(&sess, id).Error; err != nil {
		return nil, fmt.Errorf("command: load session %d: %w", id, err)
	}
	return &sess, nil
}

func (s *Service) saveSettings(sess *store.Session, settings store.SessionSettings) error {
	return s.db.Model(sess).Update("settings", datatypes.NewJSONType(settings)).Error
}

// resolveName accepts a #id from the most recent list or a literal name.
func (s *Service) resolveName(sessionID uint, kind Kind, ref string) (string, error) {
	if strings.HasPrefix(ref, "#") {
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "#"))
		if err != nil {
			return "", fmt.Errorf("command: invalid list reference %q", ref)
		}
		s.mu.Lock()
		names := s.lastList[sessionID][kind]
		s.mu.Unlock()
		if id < 1 || id > len(names) {
			return "", fmt.Errorf("command: id %d out of range; run /list %s first", id, kind)
		}
		return names[id-1], nil
	}
	return ref, nil
}

func validateKindName(s *Service, kind Kind, name string) error {
	desc, ok := kindDescriptors[kind]
	if !ok || desc.validate == nil {
		return fmt.Errorf("command: unknown kind %q", kind)
	}
	return desc.validate(s, name)
}

func (s *Service) forgetSession(sessionID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lastList, sessionID)
}

func keysOf[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func unionAppend(existing, add []string) []string {
	set := map[string]bool{}
	for _, v := range existing {
		set[v] = true
	}
	out := append([]string(nil), existing...)
	for _, v := range add {
		if !set[v] {
			out = append(out, v)
			set[v] = true
		}
	}
	return out
}

func removeAll(existing, remove []string) []string {
	drop := map[string]bool{}
	for _, v := range remove {
		drop[v] = true
	}
	var out []string
	for _, v := range existing {
		if !drop[v] {
			out = append(out, v)
		}
	}
	return out
}
