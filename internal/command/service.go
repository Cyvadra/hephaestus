// Package command implements the platform's slash-command surface
// (/help, /status, /list, /switch, ...). Command input never enters the
// LLM context, and template responses are never persisted to chat history.
package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cyvadra/hephaestus/internal/compress"
	"github.com/Cyvadra/hephaestus/internal/notify"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/tools"
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
	reg       *registry.Registry
	toolReg   *tools.Registry
	pluginReg *plugin.Registry
	sessions  *session.Service
	notifier  *notify.Notifier
	db        *gorm.DB
	projects  *project.Service

	mu       sync.Mutex
	lastList map[uint]map[Kind][]string  // sessionID -> kind -> ordered names, for /detail /switch /activate /deactivate by id
	cancels  map[uint]context.CancelFunc // sessionID -> cancel for the turn currently in flight
}

// NewService wires the command dispatcher to its dependencies.
func NewService(reg *registry.Registry, toolReg *tools.Registry, pluginReg *plugin.Registry, sessions *session.Service, notifier *notify.Notifier, db *gorm.DB, projects *project.Service) *Service {
	return &Service{
		reg:       reg,
		toolReg:   toolReg,
		pluginReg: pluginReg,
		sessions:  sessions,
		notifier:  notifier,
		db:        db,
		projects:  projects,
		lastList:  map[uint]map[Kind][]string{},
		cancels:   map[uint]context.CancelFunc{},
	}
}

// IsCommand reports whether text is a slash command rather than a chat
// message.
func IsCommand(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}

// RegisterCancel records cancel as the way to interrupt sessionID's
// in-flight turn, so a later /stop can invoke it. Callers must call
// UnregisterCancel once the turn finishes.
func (s *Service) RegisterCancel(sessionID uint, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels[sessionID] = cancel
}

// UnregisterCancel removes sessionID's cancel func once its turn is done.
func (s *Service) UnregisterCancel(sessionID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, sessionID)
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
	default:
		return "", fmt.Errorf("command: unknown command %q", name)
	}
}

const helpText = `Available commands:
/help - show this help
/ping - platform health
/stop - stop the current task
/status - session info, context usage, recent warnings
/list <kind> - list available options (identity|impression|toolgroup|plugin|concierge|session|job|workflow|project)
/detail <kind> <id> - show details of one option
/switch <identity|concierge|session|project> <id|name> - switch
/activate <impression|toolgroup|plugin> <id[,id...]> - enable
/deactivate <impression|toolgroup|plugin> <id[,id...]> - disable
/clear - archive this session and start a fresh one with the same settings
/new - archive this session and start a fresh one from its source concierge`

// kindDescriptor bundles the per-Kind lookups /list and /detail need, so
// adding a new Kind means adding one table entry instead of extending two
// parallel switch statements. Either func may be nil where that operation
// isn't supported for the Kind (e.g. /detail on a plugin).
type kindDescriptor struct {
	names  func(s *Service) ([]string, error)
	detail func(s *Service, name string) (any, error)
}

var kindDescriptors = map[Kind]kindDescriptor{
	KindIdentity: {
		names:  func(s *Service) ([]string, error) { return keysOf(s.reg.Identities), nil },
		detail: func(s *Service, name string) (any, error) { return s.reg.Identities[name], nil },
	},
	KindImpression: {
		names:  func(s *Service) ([]string, error) { return keysOf(s.reg.Impressions), nil },
		detail: func(s *Service, name string) (any, error) { return s.reg.Impressions[name], nil },
	},
	KindToolGroup: {
		names:  func(s *Service) ([]string, error) { return keysOf(s.reg.ToolGroups), nil },
		detail: func(s *Service, name string) (any, error) { return s.reg.ToolGroups[name], nil },
	},
	KindPlugin: {
		names: func(s *Service) ([]string, error) { return keysOfBool(s.pluginReg.KnownNames()), nil },
	},
	KindConcierge: {
		names:  func(s *Service) ([]string, error) { return keysOf(s.reg.Concierges), nil },
		detail: func(s *Service, name string) (any, error) { return s.reg.Concierges[name], nil },
	},
	KindWorkflow: {
		names:  func(s *Service) ([]string, error) { return keysOf(s.reg.Workflows), nil },
		detail: func(s *Service, name string) (any, error) { return s.reg.Workflows[name], nil },
	},
	KindJob: {
		names:  func(s *Service) ([]string, error) { return keysOf(s.reg.Jobs), nil },
		detail: func(s *Service, name string) (any, error) { return s.reg.Jobs[name], nil },
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
	},
}

func (s *Service) stop(sessionID uint) string {
	s.mu.Lock()
	cancel, ok := s.cancels[sessionID]
	s.mu.Unlock()
	if !ok {
		return "Nothing is currently running for this session."
	}
	cancel()
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
		total += compress.EstimateLength(m.Content)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "session: %d (concierge: %s, archived: %v)\n", sess.ID, sess.SourceConcierge, sess.FlagArchived)
	fmt.Fprintf(&b, "identity: %s\n", settings.Identity)
	fmt.Fprintf(&b, "impressions: %s\n", strings.Join(settings.Impressions, ", "))
	fmt.Fprintf(&b, "tool_groups: %s\n", strings.Join(settings.ToolGroups, ", "))
	fmt.Fprintf(&b, "plugins: %s\n", strings.Join(settings.Plugins, ", "))
	if settings.Project != "" {
		fmt.Fprintf(&b, "project: %s\n", settings.Project)
	} else {
		b.WriteString("project: (none)\n")
	}
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
		return "", fmt.Errorf("command: usage: /switch <identity|concierge|session|project> <id|name>")
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
		if _, ok := s.reg.Identities[name]; !ok {
			return "", fmt.Errorf("command: unknown identity %q", name)
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
		c, ok := s.reg.Concierges[name]
		if !ok {
			return "", fmt.Errorf("command: unknown concierge %q", name)
		}
		settings = store.SessionSettings{
			Identity:    c.Identity,
			Impressions: append([]string(nil), c.Impressions...),
			ToolGroups:  append([]string(nil), c.ToolGroups...),
			Plugins:     append([]string(nil), c.Plugins...),
		}
		sess.SourceConcierge = c.Name
		if err := s.saveSettings(sess, settings); err != nil {
			return "", err
		}
		return fmt.Sprintf("Switched to concierge %q (identity now %q).", name, c.Identity), nil

	case KindSession:
		id, err := strconv.Atoi(target)
		if err != nil {
			return "", fmt.Errorf("command: invalid session id %q", target)
		}
		if _, err := s.loadSession(uint(id)); err != nil {
			return "", err
		}
		return fmt.Sprintf("Switched to session %d.", id), nil

	case KindProject:
		name, err := s.resolveName(sessionID, KindProject, target)
		if err != nil {
			return "", err
		}
		if _, err := s.projects.GetByName(name); err != nil {
			return "", fmt.Errorf("command: unknown project %q", name)
		}
		settings.Project = name
		if err := s.saveSettings(sess, settings); err != nil {
			return "", err
		}
		return fmt.Sprintf("Switched project to %q.", name), nil

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
	return fmt.Sprintf("Archived session %d. New session: %d.", sess.ID, newSess.ID), nil
}

func (s *Service) new(sessionID uint) (string, error) {
	sess, err := s.loadSession(sessionID)
	if err != nil {
		return "", err
	}
	c, ok := s.reg.Concierges[sess.SourceConcierge]
	if !ok {
		return "", fmt.Errorf("command: source concierge %q no longer exists", sess.SourceConcierge)
	}
	newSess, err := s.sessions.Replace(sess.ID, c.Name, store.SessionSettings{
		Identity:    c.Identity,
		Impressions: append([]string(nil), c.Impressions...),
		ToolGroups:  append([]string(nil), c.ToolGroups...),
		Plugins:     append([]string(nil), c.Plugins...),
	})
	if err != nil {
		return "", err
	}
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

// resolveName accepts either a session-temporary id (from the most recent
// /list of the same kind) or a literal name, and returns the literal name.
func (s *Service) resolveName(sessionID uint, kind Kind, ref string) (string, error) {
	if id, err := strconv.Atoi(ref); err == nil {
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

func keysOf[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfBool(m map[string]bool) []string {
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
