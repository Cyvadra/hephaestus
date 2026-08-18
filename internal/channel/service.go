// Package channel bridges external channels to the chat and command services.
package channel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cyvadra/hephaestus/internal/chat"
	"github.com/Cyvadra/hephaestus/internal/chatrun"
	"github.com/Cyvadra/hephaestus/internal/command"
	"github.com/Cyvadra/hephaestus/internal/interaction"
	"github.com/Cyvadra/hephaestus/internal/project"
	"github.com/Cyvadra/hephaestus/internal/registry"
	"github.com/Cyvadra/hephaestus/internal/session"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/internal/tools"
	"github.com/Cyvadra/hephaestus/pkg/channels"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultApprovalTimeout = 2 * time.Minute

// Service owns external channels and routes each chat through one serialized
// message stack. Approval responses bypass the stack because the active turn
// is blocked waiting for them.
type Service struct {
	db           *gorm.DB
	registries   *registry.Store
	sessions     *session.Service
	pipeline     *chat.Pipeline
	chatRuns     *chatrun.Service
	commands     *command.Service
	projects     *project.Service
	interactions *interaction.Manager
	channels     []channels.Channel
	timeout      time.Duration

	mu       sync.Mutex
	queues   map[string]chan channels.InboundMessage
	pending  map[string]pendingApproval
	stopped  bool
	workers  sync.WaitGroup
	stopOnce sync.Once
}

type pendingApproval struct {
	sessionID uint
	requestID uint64
}

// New creates an external-channel runtime.
func New(db *gorm.DB, registries *registry.Store, sessions *session.Service, pipeline *chat.Pipeline, chatRuns *chatrun.Service, commands *command.Service, projects *project.Service, interactions *interaction.Manager, configured ...channels.Channel) *Service {
	return &Service{
		db: db, registries: registries, sessions: sessions, pipeline: pipeline,
		chatRuns: chatRuns,
		commands: commands, projects: projects, interactions: interactions,
		channels: configured, timeout: defaultApprovalTimeout,
		queues: map[string]chan channels.InboundMessage{}, pending: map[string]pendingApproval{},
	}
}

// Start starts all configured channels. A channel failure stops those already
// started and is returned to the caller.
func (s *Service) Start(ctx context.Context) error {
	for index, external := range s.channels {
		external.SetHandler(s.handleInbound)
		if err := external.Start(ctx); err != nil {
			for previous := index - 1; previous >= 0; previous-- {
				_ = s.channels[previous].Stop(context.Background())
			}
			return fmt.Errorf("channel %s: start: %w", external.Name(), err)
		}
	}
	return nil
}

// Stop stops accepting channel traffic and waits for message stacks to drain.
func (s *Service) Stop(ctx context.Context) error {
	var joined error
	s.stopOnce.Do(func() {
		for _, external := range s.channels {
			joined = errors.Join(joined, external.Stop(ctx))
		}
		s.mu.Lock()
		s.stopped = true
		for key, queue := range s.queues {
			close(queue)
			delete(s.queues, key)
		}
		s.pending = map[string]pendingApproval{}
		s.mu.Unlock()
		s.workers.Wait()
	})
	return joined
}

func (s *Service) handleInbound(ctx context.Context, message channels.InboundMessage) {
	key := bindingKey(message.Channel, message.ChatID)
	s.mu.Lock()
	if pending, ok := s.pending[key]; ok {
		delete(s.pending, key)
		s.mu.Unlock()
		_ = s.interactions.Respond(pending.sessionID, isApproval(message.Content))
		return
	}
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if isStopCommand(message.Content) {
		s.mu.Unlock()
		s.processStop(ctx, message)
		return
	}
	queue := s.queues[key]
	if queue == nil {
		queue = make(chan channels.InboundMessage, 16)
		s.queues[key] = queue
		s.workers.Add(1)
		go s.runStack(ctx, queue)
	}
	s.mu.Unlock()
	select {
	case queue <- message:
	case <-ctx.Done():
	}
}

func isStopCommand(text string) bool {
	fields := strings.Fields(text)
	return len(fields) > 0 && fields[0] == "/stop"
}

// processStop bypasses the per-chat message queue so it can interrupt the
// turn currently occupying that queue.
func (s *Service) processStop(ctx context.Context, message channels.InboundMessage) {
	external := s.findChannel(message.Channel)
	if external == nil {
		return
	}
	sessionID, err := s.boundSession(message.Channel, message.ChatID)
	if err != nil {
		_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: err.Error()})
		return
	}
	response, err := s.commands.Execute(sessionID, "/stop")
	if err != nil {
		response = err.Error()
	}
	_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: response})
}

func (s *Service) runStack(ctx context.Context, queue <-chan channels.InboundMessage) {
	defer s.workers.Done()
	for message := range queue {
		s.process(ctx, message)
	}
}

func (s *Service) process(ctx context.Context, message channels.InboundMessage) {
	external := s.findChannel(message.Channel)
	if external == nil {
		return
	}
	sessionID, err := s.boundSession(message.Channel, message.ChatID)
	if err != nil {
		_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: err.Error()})
		return
	}
	message.Attachments = s.persistInboundAttachments(sessionID, message.Attachments)

	text := message.Content
	for _, attachment := range message.Attachments {
		text += fmt.Sprintf("\n[received file: %s, path: %s]", attachment.Name, attachment.Path)
	}
	if command.IsCommand(text) {
		result, executeErr := s.commands.ExecuteResult(sessionID, text)
		response := result.Response
		if executeErr == nil && result.SessionTarget != nil {
			executeErr = s.saveBinding(message.Channel, message.ChatID, result.SessionTarget.ID)
		}
		if executeErr != nil {
			response = executeErr.Error()
		}
		_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: response})
		return
	}

	sessionRow, err := s.sessions.Get(sessionID)
	if err != nil {
		_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: fmt.Sprintf("channel: load active session: %v", err)})
		return
	}
	done := make(chan struct{})
	_, err = s.chatRuns.Start(sessionID, sessionRow.ProjectID, store.ChatRunMessage, map[string]any{"text": text, "channel": message.Channel}, func(turnCtx context.Context, onDelta func(chat.StreamEvent)) (*chatrun.Result, error) {
		defer close(done)
		result, runErr := s.pipeline.Run(turnCtx, sessionID, text, channelTurnOptions(sessionRow.ActiveLeafMessageID, func(event chat.StreamEvent) {
			onDelta(event)
			if event.Type == interaction.EventAskPermission && event.Interaction != nil {
				s.beginApproval(ctx, external, message, *event.Interaction)
			}
			// Reasoning, deltas and tool events intentionally become "_" at the
			// external-channel boundary and are not sent.
		}))
		if runErr != nil {
			if !errors.Is(runErr, context.Canceled) {
				_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: runErr.Error()})
			}
			return nil, runErr
		}
		if result == nil || result.Message == nil {
			return nil, nil
		}
		_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: result.Message.Content})
		s.sendAttachments(ctx, external, message.ChatID, sessionID, result.Message.ID)
		return &chatrun.Result{FinalMessageID: &result.Message.ID, Response: map[string]any{"content": result.Message.Content}}, nil
	})
	if err != nil {
		response := err.Error()
		if errors.Is(err, chatrun.ErrRunActive) {
			response = "A task is already running for this session."
		}
		_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: response})
		return
	}
	<-done
}

func channelTurnOptions(expectedLeaf *uint, onDelta func(chat.StreamEvent)) chat.TurnOptions {
	return chat.TurnOptions{ExpectedLeaf: expectedLeaf, OnDelta: onDelta}
}

func (s *Service) persistInboundAttachments(sessionID uint, attachments []channels.Attachment) []channels.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	sessionRow, err := s.sessions.Get(sessionID)
	if err != nil {
		return nil
	}
	projectRow, err := s.projects.Get(sessionRow.ProjectID)
	if err != nil {
		return nil
	}
	directory := filepath.Join(s.projects.Path(*projectRow), "uploads", time.Now().Format("2006-01-02"))
	if os.MkdirAll(directory, 0o755) != nil {
		return nil
	}
	persisted := make([]channels.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		name := filepath.Base(attachment.Name)
		if name == "." || name == "" {
			name = filepath.Base(attachment.Path)
		}
		target := availablePath(directory, name)
		if err := copyFile(attachment.Path, target); err != nil {
			continue
		}
		_ = os.Remove(attachment.Path)
		attachment.Path = filepath.ToSlash(filepath.Join("uploads", filepath.Base(directory), filepath.Base(target)))
		attachment.Name = filepath.Base(target)
		persisted = append(persisted, attachment)
	}
	return persisted
}

func availablePath(directory, name string) string {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for index := 0; ; index++ {
		candidateName := name
		if index > 0 {
			candidateName = fmt.Sprintf("%s (%d)%s", stem, index, extension)
		}
		candidate := filepath.Join(directory, candidateName)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

func copyFile(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		_ = os.Remove(targetPath)
		return err
	}
	return target.Close()
}

func (s *Service) beginApproval(ctx context.Context, external channels.Channel, message channels.InboundMessage, request interaction.Request) {
	key := bindingKey(message.Channel, message.ChatID)
	s.mu.Lock()
	s.pending[key] = pendingApproval{sessionID: request.SessionID, requestID: request.ID}
	s.mu.Unlock()
	prompt := request.Title
	if request.Details != "" {
		prompt += "\n" + request.Details
	}
	prompt += "\n请回复“确认”同意，其他回复视为拒绝。超时将自动同意。"
	_ = external.Send(ctx, channels.OutboundMessage{ChatID: message.ChatID, Content: prompt})
	time.AfterFunc(s.timeout, func() {
		s.mu.Lock()
		pending, ok := s.pending[key]
		if ok && pending.requestID == request.ID {
			delete(s.pending, key)
		}
		s.mu.Unlock()
		if ok && pending.requestID == request.ID {
			_ = s.interactions.Respond(request.SessionID, true)
		}
	})
}

func (s *Service) boundSession(channelName, chatID string) (uint, error) {
	var binding store.ChannelBinding
	result := s.db.Where("channel = ? AND chat_id = ?", channelName, chatID).Limit(1).Find(&binding)
	if result.Error != nil {
		return 0, fmt.Errorf("channel: load session binding: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return binding.SessionID, nil
	}

	reg := s.registries.Current()
	names := make([]string, 0, len(reg.Concierges))
	for name := range reg.Concierges {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return 0, errors.New("channel: no concierge is configured")
	}
	defaultProject, err := s.projects.EnsureDefault()
	if err != nil {
		return 0, err
	}
	concierge := reg.Concierges[names[0]]
	identity := reg.Identities[concierge.Identity]
	created, err := s.sessions.CreateFromConcierge(concierge, defaultProject.ID, identity.ReasoningEffort)
	if err != nil {
		return 0, err
	}
	if err := s.saveBinding(channelName, chatID, created.ID); err != nil {
		return 0, err
	}
	return created.ID, nil
}

func (s *Service) saveBinding(channelName, chatID string, sessionID uint) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel"}, {Name: "chat_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"session_id", "updated_at"}),
	}).Create(&store.ChannelBinding{Channel: channelName, ChatID: chatID, SessionID: sessionID}).Error
}

func (s *Service) sendAttachments(ctx context.Context, external channels.Channel, chatID string, sessionID, messageID uint) {
	fileChannel, ok := external.(channels.FileChannel)
	if !ok {
		return
	}
	sessionRow, err := s.sessions.Get(sessionID)
	if err != nil {
		return
	}
	projectRow, err := s.projects.Get(sessionRow.ProjectID)
	if err != nil {
		return
	}
	attachments, err := s.sessions.MessageAttachments(messageID)
	if err != nil {
		return
	}
	for _, attachment := range attachments {
		resolved, delivery, err := tools.ResolveProjectFile(s.projects.Path(*projectRow), attachment.Path)
		if err == nil {
			_ = fileChannel.SendFile(ctx, chatID, channels.Attachment{Path: resolved, Name: delivery.Name, MIME: delivery.MIME})
		}
	}
}

func (s *Service) findChannel(name string) channels.Channel {
	for _, external := range s.channels {
		if external.Name() == name {
			return external
		}
	}
	return nil
}

func bindingKey(channelName, chatID string) string { return channelName + "\x00" + chatID }

func isApproval(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	switch normalized {
	case "确认", "yes", "y", "1":
		return true
	default:
		return false
	}
}
