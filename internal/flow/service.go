package flow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/backendfactory"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/provider"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	"github.com/chenxuan520/agentbot/internal/session"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

type providerFactory func(config.Config, string) (providerapi.Client, error)

type backendFactoryFunc func(config.Config, workspace.Settings) (backend.Client, error)

// remoteRouter is the subset of the remote-agent hub the flow layer needs to
// decide routing and relay messages. It is satisfied by *remoteagent.Hub and
// injected in main, so flow does not import the hub package directly.
type remoteRouter interface {
	RouteIsLocal(ref conversation.Ref) bool
	Connected(ref conversation.Ref) bool
	Deliver(ctx context.Context, ref conversation.Ref, text string) (bool, error)
	NoteForwarded(ref conversation.Ref, messageID, reactionID string)
	ForceBot(ref conversation.Ref)
	ForceLocal(ref conversation.Ref) error
	Disconnect(ref conversation.Ref) bool
}

type queuedPrompt struct {
	input        TextInput
	reactionID   string
	queuedAtTime time.Time
}

type conversationRunState struct {
	running bool
	pending []queuedPrompt
}

type Service struct {
	cfg        config.Config
	sessions   *session.Service
	control    *control.Service
	access     *accesstoken.Service
	providers  providerFactory
	backends   backendFactoryFunc
	beforeMsg  beforeMessageHookRunner
	onReaction onReactionHookRunner
	afterReply afterReplyHookRunner
	remote     remoteRouter
	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	states     map[string]*conversationRunState
}

type TextInput struct {
	Provider         string
	ConversationID   string
	ConversationType string
	MessageType      string
	MessageID        string
	RootMessageID    string
	ParentMessageID  string
	ThreadID         string
	SenderType       string
	SenderID         string
	EventAction      string
	ReactionEmoji    string
	SystemText       string
	Text             string
	Attachments      []backend.Attachment
	AddReaction      bool
}

type PromptResult struct {
	Backend   string
	SessionID string
	ReplyText string
}

func NewService(cfg config.Config, sessions *session.Service, controlService *control.Service, accessServices ...*accesstoken.Service) *Service {
	var accessService *accesstoken.Service
	if len(accessServices) > 0 {
		accessService = accessServices[0]
	}
	return &Service{
		cfg:        cfg,
		sessions:   sessions,
		control:    controlService,
		access:     accessService,
		providers:  provider.FromConfig,
		backends:   backendfactory.FromSettings,
		beforeMsg:  runBeforeMessageHook,
		onReaction: runOnReactionHook,
		afterReply: runAfterReplyHook,
		locks:      map[string]*sync.Mutex{},
		states:     map[string]*conversationRunState{},
	}
}

// SetRemoteRouter wires the remote-agent hub. When set, conversations that opt
// in via settings.remoteEnabled and have a connected plugin route inbound
// messages to the plugin instead of the default backend.
func (s *Service) SetRemoteRouter(router remoteRouter) {
	s.remote = router
}

// SendAgentMessageToProvider pushes a plain text message into the conversation
// via its IM provider. The remote-agent hub uses it to relay local-agent output
// and switch notices.
func (s *Service) SendAgentMessageToProvider(ctx context.Context, ref conversation.Ref, text string) error {
	client, err := s.providers(s.cfg, ref.Provider)
	if err != nil {
		return err
	}
	return client.SendTextToChat(ctx, ref.ConversationID, text, " ")
}

// DeleteReactionForProvider removes a reaction from a message via the IM
// provider. The remote-agent hub uses it to clear the forward reaction once the
// local agent replies (or disconnects).
func (s *Service) DeleteReactionForProvider(ctx context.Context, ref conversation.Ref, messageID, reactionID string) {
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(reactionID) == "" {
		return
	}
	client, err := s.providers(s.cfg, ref.Provider)
	if err != nil {
		return
	}
	_ = client.DeleteReaction(ctx, messageID, reactionID)
}

func (s *Service) PromptConversation(ctx context.Context, ref conversation.Ref, text string) (PromptResult, error) {
	return s.promptConversation(ctx, ref, text, nil, backend.PromptOptions{})
}

func (s *Service) PromptConversationBTW(ctx context.Context, ref conversation.Ref, senderID, text string) (PromptResult, error) {
	return s.promptConversationBTW(ctx, ref, senderID, text, nil, backend.PromptOptions{})
}

func (s *Service) promptConversation(ctx context.Context, ref conversation.Ref, text string, attachments []backend.Attachment, options backend.PromptOptions) (PromptResult, error) {
	return s.promptConversationSlot(ctx, ref, session.SessionSlotMain, text, attachments, options)
}

func (s *Service) promptConversationBTW(ctx context.Context, ref conversation.Ref, senderID, text string, attachments []backend.Attachment, options backend.PromptOptions) (PromptResult, error) {
	lock := s.lock(ref)
	lock.Lock()
	defer lock.Unlock()

	prepared, err := s.sessions.PrepareBTW(ref, senderID, time.Now().UTC())
	if err != nil {
		return PromptResult{}, err
	}

	client, err := s.backends(s.cfg, prepared.Workspace.Settings)
	if err != nil {
		return PromptResult{}, err
	}

	sessionID := prepared.ActiveSessionID
	if prepared.NeedNewSession {
		sessionID, err = client.CreateSession(ctx, prepared.Workspace.Path)
		if err != nil {
			return PromptResult{}, err
		}
		if err := s.sessions.BindBTW(ref, senderID, prepared.AgentBackend, sessionID, time.Now().UTC()); err != nil {
			return PromptResult{}, err
		}
	}

	result, err := client.Prompt(ctx, prepared.Workspace.Path, sessionID, text, attachments, options)
	if err != nil {
		return PromptResult{}, err
	}

	if err := s.sessions.BindBTW(ref, senderID, prepared.AgentBackend, result.SessionID, time.Now().UTC()); err != nil {
		return PromptResult{}, err
	}

	return PromptResult{
		Backend:   prepared.AgentBackend,
		SessionID: result.SessionID,
		ReplyText: result.ReplyText,
	}, nil
}

func (s *Service) promptConversationSlot(ctx context.Context, ref conversation.Ref, slot session.SessionSlot, text string, attachments []backend.Attachment, options backend.PromptOptions) (PromptResult, error) {
	lock := s.lock(ref)
	lock.Lock()
	defer lock.Unlock()

	prepared, err := s.sessions.PrepareSlot(ref, slot, time.Now().UTC())
	if err != nil {
		return PromptResult{}, err
	}

	client, err := s.backends(s.cfg, prepared.Workspace.Settings)
	if err != nil {
		return PromptResult{}, err
	}

	sessionID := prepared.ActiveSessionID
	if prepared.NeedNewSession {
		sessionID, err = client.CreateSession(ctx, prepared.Workspace.Path)
		if err != nil {
			return PromptResult{}, err
		}
		if err := s.sessions.BindSlot(ref, slot, prepared.AgentBackend, sessionID, time.Now().UTC()); err != nil {
			return PromptResult{}, err
		}
	}

	result, err := client.Prompt(ctx, prepared.Workspace.Path, sessionID, text, attachments, options)
	if err != nil {
		return PromptResult{}, err
	}

	if err := s.sessions.BindSlot(ref, slot, prepared.AgentBackend, result.SessionID, time.Now().UTC()); err != nil {
		return PromptResult{}, err
	}

	return PromptResult{
		Backend:   prepared.AgentBackend,
		SessionID: result.SessionID,
		ReplyText: result.ReplyText,
	}, nil
}

// promptConversationTopic runs a prompt against the per-topic session keyed by
// topicKey. An empty topicKey falls back to the shared main session, so callers
// can route uniformly through here in topic-session reply mode.
func (s *Service) promptConversationTopic(ctx context.Context, ref conversation.Ref, topicKey, text string, attachments []backend.Attachment, options backend.PromptOptions) (PromptResult, error) {
	lock := s.lock(ref)
	lock.Lock()
	defer lock.Unlock()

	prepared, err := s.sessions.PrepareTopic(ref, topicKey, time.Now().UTC())
	if err != nil {
		return PromptResult{}, err
	}

	client, err := s.backends(s.cfg, prepared.Workspace.Settings)
	if err != nil {
		return PromptResult{}, err
	}

	sessionID := prepared.ActiveSessionID
	if prepared.NeedNewSession {
		sessionID, err = client.CreateSession(ctx, prepared.Workspace.Path)
		if err != nil {
			return PromptResult{}, err
		}
		if err := s.sessions.BindTopic(ref, topicKey, prepared.AgentBackend, sessionID, time.Now().UTC()); err != nil {
			return PromptResult{}, err
		}
	}

	result, err := client.Prompt(ctx, prepared.Workspace.Path, sessionID, text, attachments, options)
	if err != nil {
		return PromptResult{}, err
	}

	if err := s.sessions.BindTopic(ref, topicKey, prepared.AgentBackend, result.SessionID, time.Now().UTC()); err != nil {
		return PromptResult{}, err
	}

	return PromptResult{
		Backend:   prepared.AgentBackend,
		SessionID: result.SessionID,
		ReplyText: result.ReplyText,
	}, nil
}

func (s *Service) FinalizeReply(ctx context.Context, ref conversation.Ref, input TextInput, replyText string, options providerapi.ReplyOptions) (string, providerapi.ReplyOptions, error) {
	current, err := s.sessions.Current(ref)
	if err != nil {
		return "", options, err
	}
	if len(options.MentionUserIDs) == 0 && strings.TrimSpace(options.MentionUserID) == "" {
		if mentionUserID := defaultMentionUserID(input); mentionUserID != "" {
			options.MentionUserID = mentionUserID
		}
	}
	afterReplyResult, err := s.afterReply(ctx, current.Workspace.Path, input, replyText)
	if err != nil {
		return "", options, err
	}
	if strings.TrimSpace(afterReplyResult.ReplyText) != "" {
		replyText = afterReplyResult.ReplyText
	}
	if len(afterReplyResult.MentionUserIDs) > 0 {
		options.MentionUserID = ""
		options.MentionUserIDs = append([]string(nil), afterReplyResult.MentionUserIDs...)
	} else if strings.TrimSpace(afterReplyResult.MentionUserID) != "" {
		options.MentionUserID = afterReplyResult.MentionUserID
		options.MentionUserIDs = nil
	}
	options.InThread = shouldReplyInThread(input.ConversationType, current.Workspace.Settings.Settings.ReplyMode)
	return replyText, options, nil
}

func (s *Service) ProcessText(ctx context.Context, input TextInput) (PromptResult, error) {
	input.ConversationType = conversation.NormalizeType(input.ConversationType)
	ref := conversation.Ref{Provider: input.Provider, ConversationID: input.ConversationID}
	client, err := s.providers(s.cfg, input.Provider)
	if err != nil {
		return PromptResult{}, err
	}

	commandResult, err := s.handleCommand(ctx, ref, input)
	if err != nil {
		return PromptResult{}, err
	}
	if commandResult.handled {
		current, err := s.sessions.Current(ref)
		if err != nil {
			return PromptResult{}, err
		}
		processingReactionID := s.addProcessingReaction(ctx, client, input)
		defer s.deleteProcessingReaction(input, client, processingReactionID)
		replyText := commandResult.replyText
		replyOptions := defaultReplyOptions(input)
		if commandResult.viaAgent {
			replyText, replyOptions, err = s.FinalizeReply(ctx, ref, input, replyText, replyOptions)
			if err != nil {
				hookReply := fmt.Sprintf("after_reply.py 执行失败: %s", err)
				if err := s.replyText(ctx, client, input, current.Workspace.Settings.Settings.ReplyMode, hookReply, defaultReplyOptions(input)); err != nil {
					return PromptResult{}, err
				}
				return PromptResult{ReplyText: hookReply}, nil
			}
		}
		result := PromptResult{ReplyText: replyText}
		if err := s.replyText(ctx, client, input, current.Workspace.Settings.Settings.ReplyMode, replyText, replyOptions); err != nil {
			return PromptResult{}, err
		}
		return result, nil
	}

	refused, err := s.isRefused(ref, time.Now().UTC())
	if err != nil {
		return PromptResult{}, err
	}
	if refused {
		if input.MessageID != "" {
			_ = client.AddBlockedReaction(ctx, input.MessageID)
		}
		return PromptResult{}, nil
	}

	current, err := s.sessions.Current(ref)
	if err != nil {
		return PromptResult{}, err
	}
	hookResult, err := s.beforeMsg(ctx, current.Workspace.Path, input)
	if err != nil {
		hookReply := fmt.Sprintf("before_message.py 执行失败: %s", err)
		if err := s.replyText(ctx, client, input, current.Workspace.Settings.Settings.ReplyMode, hookReply, defaultReplyOptions(input)); err != nil {
			return PromptResult{}, err
		}
		return PromptResult{ReplyText: hookReply}, nil
	}
	if strings.TrimSpace(hookResult.Text) != "" {
		input.Text = hookResult.Text
	}
	if strings.TrimSpace(hookResult.SystemText) != "" {
		input.SystemText = hookResult.SystemText
	}
	if hookResult.Drop {
		result := PromptResult{ReplyText: hookResult.ReplyText}
		if strings.TrimSpace(hookResult.ReactionEmoji) != "" && input.MessageID != "" {
			if err := client.AddReaction(ctx, input.MessageID, hookResult.ReactionEmoji); err != nil {
				return PromptResult{}, err
			}
		}
		if strings.TrimSpace(hookResult.ReplyText) != "" {
			if err := s.replyText(ctx, client, input, current.Workspace.Settings.Settings.ReplyMode, hookResult.ReplyText, defaultReplyOptions(input)); err != nil {
				return PromptResult{}, err
			}
		}
		return result, nil
	}
	if s.remote != nil && current.Workspace.Settings.Settings.RemoteEnabled && s.remote.RouteIsLocal(ref) {
		delivered, err := s.remote.Deliver(ctx, ref, input.Text)
		if err != nil {
			return PromptResult{}, err
		}
		if delivered {
			// Mark the message as picked up by the local agent and let the hub
			// clear the reaction when the local agent replies. This is the only
			// place the remote forward reaction is used; the bot path is
			// untouched.
			if reactionID := s.addRemoteForwardReaction(ctx, client, input); reactionID != "" {
				s.remote.NoteForwarded(ref, input.MessageID, reactionID)
			}
			return PromptResult{}, nil
		}
		// The plugin dropped between the route check and delivery; fall back to
		// the default backend below.
	}
	processingReactionID := s.addProcessingReaction(ctx, client, input)
	queued := queuedPrompt{input: input, reactionID: processingReactionID, queuedAtTime: time.Now().UTC()}
	if !s.startOrQueuePrompt(ref, queued) {
		return PromptResult{}, nil
	}
	return s.processQueuedPrompts(ctx, ref, queued)
}

func (s *Service) ProcessReaction(ctx context.Context, input TextInput) (PromptResult, error) {
	input.ConversationType = conversation.NormalizeType(input.ConversationType)
	ref := conversation.Ref{Provider: input.Provider, ConversationID: input.ConversationID}
	client, err := s.providers(s.cfg, input.Provider)
	if err != nil {
		return PromptResult{}, err
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		return PromptResult{}, err
	}
	hookResult, err := s.onReaction(ctx, current.Workspace.Path, input)
	if err != nil {
		hookReply := fmt.Sprintf("on_reaction.py 执行失败: %s", err)
		if err := s.replyText(ctx, client, input, current.Workspace.Settings.Settings.ReplyMode, hookReply, defaultReplyOptions(input)); err != nil {
			return PromptResult{}, err
		}
		return PromptResult{ReplyText: hookReply}, nil
	}
	if strings.TrimSpace(hookResult.Text) != "" {
		input.Text = hookResult.Text
	}
	if strings.TrimSpace(hookResult.SystemText) != "" {
		input.SystemText = hookResult.SystemText
	}
	if hookResult.Drop || strings.TrimSpace(input.Text) == "" {
		result := PromptResult{ReplyText: hookResult.ReplyText}
		if strings.TrimSpace(hookResult.ReactionEmoji) != "" && input.MessageID != "" {
			if err := client.AddReaction(ctx, input.MessageID, hookResult.ReactionEmoji); err != nil {
				return PromptResult{}, err
			}
		}
		if strings.TrimSpace(hookResult.ReplyText) != "" {
			if err := s.replyText(ctx, client, input, current.Workspace.Settings.Settings.ReplyMode, hookResult.ReplyText, defaultReplyOptions(input)); err != nil {
				return PromptResult{}, err
			}
		}
		return result, nil
	}
	return s.ProcessText(ctx, input)
}

func (s *Service) processQueuedPrompts(ctx context.Context, ref conversation.Ref, first queuedPrompt) (PromptResult, error) {
	batch := []queuedPrompt{first}
	var firstResult PromptResult
	var firstErr error
	processedFirst := false

	for len(batch) > 0 {
		result, err := s.processPromptBatch(ctx, ref, batch)
		if !processedFirst {
			firstResult = result
			firstErr = err
			processedFirst = true
		}
		batch = s.nextPromptBatchOrRelease(ref)
	}

	return firstResult, firstErr
}

func (s *Service) processPromptBatch(ctx context.Context, ref conversation.Ref, prompts []queuedPrompt) (PromptResult, error) {
	if len(prompts) == 0 {
		return PromptResult{}, nil
	}

	client, err := s.providers(s.cfg, prompts[len(prompts)-1].input.Provider)
	if err != nil {
		return PromptResult{}, err
	}
	defer s.deleteBatchReactions(prompts, client)
	current, err := s.sessions.Current(ref)
	if err != nil {
		return PromptResult{}, err
	}
	replyMode := current.Workspace.Settings.Settings.ReplyMode

	// Topic-session mode keys each message to its own topic session, so a batch
	// may span multiple topics. Coalescing non-final replies (as the shared path
	// below does) would silently swallow other topics' answers, so process every
	// queued prompt independently and reply to each.
	if workspace.IsTopicSessionReplyMode(replyMode) {
		var firstResult PromptResult
		for i, prompt := range prompts {
			topicKey := topicKeyForMessage(prompt.input, replyMode)
			result, err := s.promptConversationTopic(ctx, ref, topicKey, prompt.input.Text, prompt.input.Attachments, backend.PromptOptions{
				System: prompt.input.SystemText,
			})
			if err != nil {
				return PromptResult{}, err
			}
			sent, err := s.finalizeAgentReply(ctx, client, prompt.input, replyMode, current.Workspace.Path, result)
			if err != nil {
				return PromptResult{}, err
			}
			if i == 0 {
				firstResult = sent
			}
		}
		return firstResult, nil
	}

	for _, prompt := range prompts[:len(prompts)-1] {
		if _, err := s.promptConversation(ctx, ref, prompt.input.Text, prompt.input.Attachments, backend.PromptOptions{
			NoReply: true,
			System:  prompt.input.SystemText,
		}); err != nil {
			return PromptResult{}, err
		}
	}

	finalInput := prompts[len(prompts)-1].input
	result, err := s.promptConversation(ctx, ref, finalInput.Text, finalInput.Attachments, backend.PromptOptions{
		System: finalInput.SystemText,
	})
	if err != nil {
		return PromptResult{}, err
	}

	return s.finalizeAgentReply(ctx, client, finalInput, replyMode, current.Workspace.Path, result)
}

// finalizeAgentReply runs after_reply.py over an agent result and sends the
// (possibly rewritten) reply, applying any mention overrides the hook returns.
func (s *Service) finalizeAgentReply(ctx context.Context, client providerapi.Client, input TextInput, replyMode, workspacePath string, result PromptResult) (PromptResult, error) {
	replyOptions := defaultReplyOptions(input)
	afterReplyResult, err := s.afterReply(ctx, workspacePath, input, result.ReplyText)
	if err != nil {
		hookReply := fmt.Sprintf("after_reply.py 执行失败: %s", err)
		if err := s.replyText(ctx, client, input, replyMode, hookReply, defaultReplyOptions(input)); err != nil {
			return PromptResult{}, err
		}
		return PromptResult{ReplyText: hookReply}, nil
	}
	if strings.TrimSpace(afterReplyResult.ReplyText) != "" {
		result.ReplyText = afterReplyResult.ReplyText
	}
	if len(afterReplyResult.MentionUserIDs) > 0 {
		replyOptions.MentionUserID = ""
		replyOptions.MentionUserIDs = append([]string(nil), afterReplyResult.MentionUserIDs...)
	} else if strings.TrimSpace(afterReplyResult.MentionUserID) != "" {
		replyOptions.MentionUserID = afterReplyResult.MentionUserID
		replyOptions.MentionUserIDs = nil
	}
	if err := s.replyText(ctx, client, input, replyMode, result.ReplyText, replyOptions); err != nil {
		return PromptResult{}, err
	}
	return result, nil
}

func (s *Service) deleteBatchReactions(prompts []queuedPrompt, client providerapi.Client) {
	for _, prompt := range prompts {
		s.deleteProcessingReaction(prompt.input, client, prompt.reactionID)
	}
}

func (s *Service) replyText(ctx context.Context, client providerapi.Client, input TextInput, replyMode, text string, options providerapi.ReplyOptions) error {
	if input.ConversationType != conversation.TypeDirect {
		options.InThread = shouldReplyInThread(input.ConversationType, replyMode)
		if err := client.ReplyTextToMessage(ctx, input.MessageID, text, " ", options); err != nil {
			return err
		}
		return nil
	}

	if err := client.SendTextToChat(ctx, input.ConversationID, text, " "); err != nil {
		return err
	}
	return nil
}

func defaultReplyOptions(input TextInput) providerapi.ReplyOptions {
	if mentionUserID := defaultMentionUserID(input); mentionUserID != "" {
		return providerapi.ReplyOptions{MentionUserID: mentionUserID}
	}
	return providerapi.ReplyOptions{}
}

func defaultMentionUserID(input TextInput) string {
	senderID := strings.TrimSpace(input.SenderID)
	if senderID == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(input.SenderType)) {
	case "", "user":
		return senderID
	default:
		return ""
	}
}

// topicKeyForMessage returns the topic-session key for a normal conversational
// message when the conversation runs in the topic-session reply mode. A message
// already inside a thread keys by its thread root; a fresh top-level message
// starts a new topic rooted at itself.
func topicKeyForMessage(input TextInput, replyMode string) string {
	if !workspace.IsTopicSessionReplyMode(replyMode) {
		return ""
	}
	if key := strings.TrimSpace(input.RootMessageID); key != "" {
		return key
	}
	if key := strings.TrimSpace(input.ThreadID); key != "" {
		return key
	}
	return strings.TrimSpace(input.MessageID)
}

// topicKeyForCommand mirrors topicKeyForMessage but never opens a new topic for a
// top-level command: commands are control-plane, so at top level they act on the
// baseline session and only bind to a topic when issued inside an existing one.
func topicKeyForCommand(input TextInput, replyMode string) string {
	if !workspace.IsTopicSessionReplyMode(replyMode) {
		return ""
	}
	if key := strings.TrimSpace(input.RootMessageID); key != "" {
		return key
	}
	return strings.TrimSpace(input.ThreadID)
}

func isThreadReplyMode(replyMode string) bool {
	switch replyMode {
	case workspace.ReplyModeThread, workspace.ReplyModeTopic, workspace.ReplyModeTopicSession:
		return true
	default:
		return false
	}
}

func shouldReplyInThread(conversationType, replyMode string) bool {
	if conversationType == conversation.TypeThread {
		return true
	}
	return isThreadReplyMode(replyMode)
}

func (s *Service) isRefused(ref conversation.Ref, now time.Time) (bool, error) {
	if s.control == nil {
		return false, nil
	}
	return s.control.HasActiveRefuse(ref, now)
}

func (s *Service) addProcessingReaction(ctx context.Context, client providerapi.Client, input TextInput) string {
	if !input.AddReaction || input.MessageID == "" {
		return ""
	}
	processingReactionID, _ := client.AddHandlingReaction(ctx, input.MessageID)
	return processingReactionID
}

// addRemoteForwardReaction marks an inbound message as forwarded to the local
// agent, using the provider's configurable remote-agent emoji (which defaults
// to the regular ack emoji). It only runs on the remote route.
func (s *Service) addRemoteForwardReaction(ctx context.Context, client providerapi.Client, input TextInput) string {
	if !input.AddReaction || input.MessageID == "" {
		return ""
	}
	reactionID, _ := client.AddRemoteHandlingReaction(ctx, input.MessageID)
	return reactionID
}

func (s *Service) deleteProcessingReaction(input TextInput, client providerapi.Client, reactionID string) {
	if reactionID == "" || input.MessageID == "" {
		return
	}
	_ = client.DeleteReaction(context.Background(), input.MessageID, reactionID)
}

func (s *Service) startOrQueuePrompt(ref conversation.Ref, prompt queuedPrompt) bool {
	key := conversationKey(ref)
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.conversationStateLocked(key)
	if state.running {
		state.pending = append(state.pending, prompt)
		return false
	}
	state.running = true
	return true
}

func (s *Service) nextPromptBatchOrRelease(ref conversation.Ref) []queuedPrompt {
	key := conversationKey(ref)
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[key]
	if !ok {
		return nil
	}
	if len(state.pending) == 0 {
		state.running = false
		delete(s.states, key)
		return nil
	}
	batch := append([]queuedPrompt(nil), state.pending...)
	state.pending = nil
	return batch
}

func (s *Service) conversationStateLocked(key string) *conversationRunState {
	if state, ok := s.states[key]; ok {
		return state
	}
	state := &conversationRunState{}
	s.states[key] = state
	return state
}

func (s *Service) lock(ref conversation.Ref) *sync.Mutex {
	key := conversationKey(ref)
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.locks[key]; ok {
		return existing
	}
	next := &sync.Mutex{}
	s.locks[key] = next
	return next
}

func conversationKey(ref conversation.Ref) string {
	return ref.Provider + ":" + ref.ConversationID
}
