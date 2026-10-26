package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/backendfactory"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/observability"
	appstore "github.com/chenxuan520/agentbot/internal/store"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

const replyReconnectDelay = 5 * time.Second

var backgroundResultID = regexp.MustCompile(`(?s)^<task id="([^"]+)" state="(?:completed|error)">.*<(?:task_result|task_error)>`)

type replyRoute struct {
	Input     TextInput
	ReplyMode string
}

type replyRouteKey struct{}

func withReplyRoute(ctx context.Context, input TextInput, replyMode string) context.Context {
	input.Attachments = nil
	return context.WithValue(ctx, replyRouteKey{}, replyRoute{Input: input, ReplyMode: replyMode})
}

func (s *Service) promptMessageRecorder(ctx context.Context, workspacePath, sessionID string, previous func(string) error) func(string) error {
	route, ok := ctx.Value(replyRouteKey{}).(replyRoute)
	if !ok || s.replies == nil || route.Input.MessageID == "" && route.Input.ConversationType != conversation.TypeDirect {
		return previous
	}
	return func(messageID string) error {
		if previous != nil {
			if err := previous(messageID); err != nil {
				return err
			}
		}
		data, err := json.Marshal(route)
		if err != nil {
			return err
		}
		return s.replies.UpsertReplySource(appstore.ReplySourceRecord{
			SessionID: sessionID, MessageID: messageID,
			Provider: route.Input.Provider, ConversationID: route.Input.ConversationID,
			WorkspacePath: workspacePath, InputJSON: string(data),
		})
	}
}

func (s *Service) replyRouteFor(ctx context.Context, workspacePath string, result PromptResult) (*replyRoute, error) {
	if s.replies == nil || result.SessionID == "" || result.ParentID == "" && !result.Native {
		return nil, nil
	}
	lock := s.replyLock("routes:" + result.SessionID)
	if err := lockReplyContext(ctx, lock); err != nil {
		return nil, err
	}
	defer lock.Unlock()
	sources, err := s.replies.ListReplySources(result.SessionID)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 && !result.Native {
		return nil, nil
	}
	if result.Native {
		routes := map[string]appstore.ReplySourceRecord{}
		for _, source := range sources {
			routes[source.MessageID] = source
		}
		source, err := nativeReplySource(routes, result.SourceMessageIDs)
		if err != nil {
			return nil, err
		}
		if source == nil {
			return nil, fmt.Errorf("opencode V2 reply %s has no provable input route", result.MessageID)
		}
		var route replyRoute
		if err := json.Unmarshal([]byte(source.InputJSON), &route); err != nil {
			return nil, err
		}
		return &route, nil
	}
	for _, source := range sources {
		if source.MessageID == result.ParentID {
			var route replyRoute
			if err := json.Unmarshal([]byte(source.InputJSON), &route); err != nil {
				return nil, err
			}
			return &route, nil
		}
	}
	settings, err := workspace.LoadSettings(workspacePath)
	if err != nil {
		return nil, err
	}
	client, err := s.backends(s.cfg, settings)
	if err != nil {
		return nil, err
	}
	lookup, ok := client.(backend.SessionReplyLookup)
	if !ok {
		return nil, nil
	}
	messages, err := lookup.GetSessionReplyMessages(ctx, workspacePath, result.SessionID)
	if err != nil {
		return nil, err
	}
	routes, err := s.replyRoutes(result.SessionID, sources, messages)
	if err != nil {
		return nil, err
	}
	source, ok := routes[result.ParentID]
	if !ok {
		return nil, nil
	}
	var route replyRoute
	if err := json.Unmarshal([]byte(source.InputJSON), &route); err != nil {
		return nil, err
	}
	return &route, nil
}

func nativeReplySource(routes map[string]appstore.ReplySourceRecord, ids []string) (*appstore.ReplySourceRecord, error) {
	var source *appstore.ReplySourceRecord
	missing := false
	for _, id := range ids {
		candidate, ok := routes[id]
		if !ok {
			missing = true
			continue
		}
		if source == nil {
			source = &candidate
		} else if source.Provider != candidate.Provider || source.ConversationID != candidate.ConversationID || source.WorkspacePath != candidate.WorkspacePath || source.InputJSON != candidate.InputJSON {
			return nil, fmt.Errorf("opencode V2 reply has ambiguous input routes")
		}
	}
	if source != nil && missing {
		return nil, fmt.Errorf("opencode V2 reply includes an unregistered input route")
	}
	return source, nil
}

func lockReplyContext(ctx context.Context, lock *sync.Mutex) error {
	for !lock.TryLock() {
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		lock.Unlock()
		return err
	}
	return nil
}

func (s *Service) replyLock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.replyLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.replyLocks[key] = lock
	}
	return lock
}

func (s *Service) lockReply(ctx context.Context, sessionID, messageID string) (func(), bool, error) {
	if s.replies == nil || sessionID == "" || messageID == "" {
		return func() {}, false, nil
	}
	lock := s.replyLock("session:" + sessionID)
	if err := lockReplyContext(ctx, lock); err != nil {
		return func() {}, false, err
	}
	delivered, err := s.replies.ReplyDelivered(sessionID, messageID)
	if err != nil {
		lock.Unlock()
		return func() {}, false, err
	}
	return lock.Unlock, delivered, nil
}

func (s *Service) markReplyDelivered(sessionID, messageID string) error {
	if s.replies == nil || sessionID == "" || messageID == "" {
		return nil
	}
	return s.replies.MarkReplyDelivered(sessionID, messageID)
}

func (s *Service) ListenReplies(ctx context.Context) error {
	client, err := backendfactory.FromConfig(s.cfg, "opencode")
	if err != nil {
		return err
	}
	events, ok := client.(backend.SessionEventSource)
	if !ok {
		return fmt.Errorf("opencode backend does not support reply events")
	}
	lookup, ok := client.(backend.SessionReplyLookup)
	if !ok {
		return fmt.Errorf("opencode backend does not support reply lookup")
	}
	return s.listenReplies(ctx, events, lookup, replyReconnectDelay)
}

func (s *Service) listenReplies(ctx context.Context, events backend.SessionEventSource, lookup backend.SessionReplyLookup, retryDelay time.Duration) error {
	if s.replies == nil {
		return nil
	}
	for ctx.Err() == nil {
		err := events.WatchSessionEvents(ctx, func(event backend.SessionEvent) error {
			if event.Type == "server.connected" {
				return s.reconcileReplies(ctx, lookup, "")
			}
			if event.SessionID != "" && (event.Type == "session.idle" || event.Type == "message.updated" && event.Role == "assistant" && event.CompletedAt > 0) {
				return s.reconcileReplies(ctx, lookup, event.SessionID)
			}
			return nil
		})
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			observability.RecordError("backend", "", "", "opencode reply event stream disconnected", err)
			log.Printf("opencode reply event stream disconnected: %v", err)
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (s *Service) reconcileReplies(ctx context.Context, lookup backend.SessionReplyLookup, sessionID string) error {
	sources, err := s.replies.ListReplySources(sessionID)
	if err != nil {
		observability.RecordError("backend", "", "", "list reply sources failed", err)
		return err
	}
	seen := map[string]bool{}
	var failures error
	for _, source := range sources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if seen[source.SessionID] {
			continue
		}
		seen[source.SessionID] = true
		if err := s.reconcileSessionReplies(ctx, lookup, source.SessionID); err != nil {
			observability.RecordError("backend", source.Provider, source.ConversationID, "forward session reply failed", err)
			log.Printf("forward session reply failed: session=%s err=%v", source.SessionID, err)
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

func (s *Service) reconcileSessionReplies(ctx context.Context, lookup backend.SessionReplyLookup, sessionID string) error {
	sources, err := s.replies.ListReplySources(sessionID)
	if err != nil || len(sources) == 0 {
		return err
	}
	ref := conversation.Ref{Provider: sources[0].Provider, ConversationID: sources[0].ConversationID}
	gate := s.replyLock("prompt:" + conversationKey(ref))
	if err := lockReplyContext(ctx, gate); err != nil {
		return err
	}
	defer gate.Unlock()
	lock := s.lock(ref)
	if err := lockReplyContext(ctx, lock); err != nil {
		return err
	}
	defer lock.Unlock()
	routeLock := s.replyLock("routes:" + sessionID)
	if err := lockReplyContext(ctx, routeLock); err != nil {
		return err
	}
	defer routeLock.Unlock()
	if _, err := os.Stat(sources[0].WorkspacePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	messages, err := lookup.GetSessionReplyMessages(ctx, sources[0].WorkspacePath, sessionID)
	if err != nil {
		return err
	}
	routes, err := s.replyRoutes(sessionID, sources, messages)
	if err != nil {
		return err
	}
	var failures error
	for _, message := range messages {
		if _, ok := backend.FinalReplyText(message); !ok {
			continue
		}
		source, ok := routes[message.ParentID]
		if message.Native {
			candidate, err := nativeReplySource(routes, message.SourceMessageIDs)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			ok = candidate != nil
			if ok {
				source = *candidate
			}
		}
		if !ok {
			continue
		}
		if message.SessionID != "" && message.SessionID != sessionID {
			continue
		}
		delivered, err := s.replies.ReplyDelivered(sessionID, message.ID)
		if err != nil {
			return err
		}
		if delivered {
			continue
		}
		latest, err := lookup.GetSessionReplyMessage(ctx, source.WorkspacePath, sessionID, message.ID)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		text, ok := backend.FinalReplyText(latest)
		if !ok || latest.ID != message.ID || latest.SessionID != "" && latest.SessionID != sessionID || latest.ParentID != message.ParentID || latest.Native != message.Native {
			continue
		}
		if latest.Native {
			candidate, err := nativeReplySource(routes, latest.SourceMessageIDs)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			if candidate == nil || candidate.InputJSON != source.InputJSON {
				continue
			}
		}
		var route replyRoute
		if err := json.Unmarshal([]byte(source.InputJSON), &route); err != nil {
			return fmt.Errorf("decode reply route: %w", err)
		}
		client, err := s.providers(s.cfg, source.Provider)
		if err != nil {
			return err
		}
		if _, err := s.finalizeAgentReply(ctx, client, route.Input, route.ReplyMode, source.WorkspacePath, PromptResult{
			Backend: "opencode", SessionID: sessionID, MessageID: message.ID, ReplyText: text,
		}); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

func (s *Service) replyRoutes(sessionID string, sources []appstore.ReplySourceRecord, messages []backend.SessionMessage) (map[string]appstore.ReplySourceRecord, error) {
	routes := map[string]appstore.ReplySourceRecord{}
	for _, source := range sources {
		routes[source.MessageID] = source
	}
	tasks := map[string]appstore.ReplySourceRecord{}
	var previous *appstore.ReplySourceRecord
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].Native && messages[j].Native {
			return messages[i].Sequence < messages[j].Sequence
		}
		if messages[i].CreatedAt == messages[j].CreatedAt {
			return messages[i].ID < messages[j].ID
		}
		return messages[i].CreatedAt < messages[j].CreatedAt
	})
	for _, message := range messages {
		if message.Native {
			continue
		}
		if message.SessionID != "" && message.SessionID != sessionID {
			continue
		}
		if message.Role == "assistant" {
			source, registered := routes[message.ParentID]
			for _, part := range message.Parts {
				if part.Type == "tool" && part.Tool == "task" && part.ToolSessionID != "" {
					if part.ToolCancelled {
						delete(tasks, part.ToolSessionID)
					} else if part.ToolBackground && !part.ToolUpdated {
						if registered {
							tasks[part.ToolSessionID] = source
						} else {
							delete(tasks, part.ToolSessionID)
						}
					}
				}
			}
			continue
		}
		if message.Role != "user" {
			continue
		}
		var inherited *appstore.ReplySourceRecord
		if source, ok := routes[message.ID]; ok {
			inherited = &source
		} else {
			for _, part := range message.Parts {
				if part.Type == "compaction" || part.CompactionContinue {
					inherited = previous
					break
				}
			}
		}
		for _, part := range message.Parts {
			if part.Type != "text" || !part.Synthetic || part.Ignored {
				continue
			}
			match := backgroundResultID.FindStringSubmatch(strings.TrimSpace(part.Text))
			if len(match) != 2 {
				continue
			}
			source, ok := tasks[match[1]]
			if !ok {
				continue
			}
			inherited = &source
			delete(tasks, match[1])
			break
		}
		previous = inherited
		if inherited != nil {
			source := *inherited
			source.SessionID = sessionID
			source.MessageID = message.ID
			if existing, ok := routes[message.ID]; !ok || existing.InputJSON != source.InputJSON {
				if err := s.replies.UpsertReplySource(source); err != nil {
					return nil, err
				}
			}
			routes[message.ID] = source
		}
	}
	return routes, nil
}
