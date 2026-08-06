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

	mu       sync.Mutex
	lastList map[uint]map[Kind][]string  // sessionID -> kind -> ordered names, for /detail /switch /activate /deactivate by id
	cancels  map[uint]context.CancelFunc // sessionID -> cancel for the turn currently in flight
}

// NewService wires the command dispatcher to its dependencies.
func NewService(reg *registry.Registry, toolReg *tools.Registry, pluginReg *plugin.Registry, sessions *session.Service, notifier *notify.Notifier, db *gorm.DB) *Service {
	return &Service{
		reg:       reg,
		toolReg:   toolReg,
		pluginReg: pluginReg,
		sessions:  sessions,
		notifier:  notifier,
		db:        db,
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
/switch <identity|concierge|session> <id|name> - switch
/activate <impression|toolgroup|plugin> <id[,id...]> - enable
/deactivate <impression|toolgroup|plugin> <id[,id...]> - disable
/clear - archive this session and start a fresh one with the same settings
/new - archive this session and start a fresh one from its source concierge`

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

	var names []string
	switch kind {
	case KindIdentity:
		names = keysOf(s.reg.Identities)
	case KindImpression:
		names = keysOf(s.reg.Impressions)
	case KindToolGroup:
		names = keysOf(s.reg.ToolGroups)
	case KindPlugin:
		names = keysOfBool(s.pluginReg.KnownNames())
	case KindConcierge:
		names = keysOf(s.reg.Concierges)
	case KindWorkflow:
		names = keysOf(s.reg.Workflows)
	case KindJob:
		names = keysOf(s.reg.Jobs)
	case KindSession:
		var sessions []store.Session
		if err := s.db.Where("flag_archived = ?", false).Find(&sessions).Error; err != nil {
			return "", err
		}
		for _, sess := range sessions {
			names = append(names, strconv.Itoa(int(sess.ID)))
		}
	case KindProject:
		return "Projects are not implemented yet.", nil
	default:
		return "", fmt.Errorf("command: unknown kind %q", kind)
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

	var v any
	switch kind {
	case KindIdentity:
		v = s.reg.Identities[name]
	case KindImpression:
		v = s.reg.Impressions[name]
	case KindToolGroup:
		v = s.reg.ToolGroups[name]
	case KindConcierge:
		v = s.reg.Concierges[name]
	case KindWorkflow:
		v = s.reg.Workflows[name]
	case KindJob:
		v = s.reg.Jobs[name]
	case KindSession:
		id, err := strconv.Atoi(name)
		if err != nil {
			return "", fmt.Errorf("command: invalid session id %q", name)
		}
		sess, err := s.loadSession(uint(id))
		if err != nil {
			return "", err
		}
		v = sess
	default:
		return "", fmt.Errorf("command: /detail does not support kind %q", kind)
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) switchTo(sessionID uint, args []string) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("command: usage: /switch <identity|concierge|session> <id|name>")
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
	if err := s.db.Model(sess).Update("flag_archived", true).Error; err != nil {
		return "", err
	}

	newSess := &store.Session{
		SourceConcierge: sess.SourceConcierge,
		Settings:        datatypes.NewJSONType(settings),
	}
	if err := s.db.Create(newSess).Error; err != nil {
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
	if err := s.db.Model(sess).Update("flag_archived", true).Error; err != nil {
		return "", err
	}

	newSess, err := s.sessions.CreateFromConcierge(c)
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
