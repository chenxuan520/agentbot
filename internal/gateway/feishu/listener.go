package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/flow"
	"github.com/chenxuan520/agentbot/internal/observability"
	"github.com/chenxuan520/agentbot/internal/progress"
	providerfeishu "github.com/chenxuan520/agentbot/internal/provider/feishu"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	"github.com/chenxuan520/agentbot/internal/session"
)

type groupMessagePolicyFunc func(conversation.Ref) (session.GroupMessagePolicy, error)
type acceptInteractiveCardsFunc func(conversation.Ref) (bool, error)

const interactiveEventDumpFileName = "last-interactive-event.json"
const processFailureReplyText = "刚才处理失败了，请再发一次。"

type interactiveEventDump struct {
	ReceivedAt string                     `json:"receivedAt"`
	Event      *larkim.P2MessageReceiveV1 `json:"event"`
}

type providerClient interface {
	providerapi.Client
	providerapi.MessageGetter
	PrepareImageAttachments(ctx context.Context, messageID string, imageKeys []string) ([]backend.Attachment, func(), error)
	BotIdentity(ctx context.Context) (providerfeishu.BotIdentity, error)
}

type botIdentity struct {
	OpenID  string
	AppName string
}

type Listener struct {
	cfg                config.Config
	flow               *flow.Service
	progress           *progress.Service
	client             providerClient
	groupMessagePolicy groupMessagePolicyFunc
	acceptInteractive  acceptInteractiveCardsFunc
	botPoller          *botMessagePoller
	botIdentityMu      sync.Mutex
	botIdentity        botIdentity
}

func NewListener(cfg config.Config, flowService *flow.Service, progressService *progress.Service, sessionService *session.Service) *Listener {
	client := providerfeishu.New(cfg.FeishuAppID, cfg.FeishuAppSecret, cfg.FeishuAckEmoji, cfg.FeishuRemoteAckEmoji)
	listener := &Listener{cfg: cfg, flow: flowService, progress: progressService, client: client}
	if sessionService != nil {
		listener.groupMessagePolicy = sessionService.GroupMessagePolicy
		listener.acceptInteractive = sessionService.AcceptInteractiveCardMessages
		// Feishu does not push bot-to-bot messages over the realtime stream, so
		// other bots' messages are observed by polling chat history. Reuse the
		// listener's client as the message lister and run it alongside Listen.
		listener.botPoller = &botMessagePoller{
			chatRootDir:       cfg.ChatRootDir,
			selfAppID:         strings.TrimSpace(cfg.FeishuAppID),
			lister:            client,
			enabledRefs:       sessionService.EnabledOtherBotMessageRefs,
			process:           flowService.ProcessText,
			acceptInteractive: sessionService.AcceptInteractiveCardMessages,
		}
	}
	return listener
}

func (l *Listener) Listen(ctx context.Context) error {
	if l.cfg.FeishuAppID == "" || l.cfg.FeishuAppSecret == "" {
		return errors.New("missing feishu app credentials")
	}

	// Run the other-bot poller as part of Feishu inbound, tied to the realtime
	// stream's lifecycle: it starts here and stops when Listen returns.
	pollCtx, stopPoller := context.WithCancel(ctx)
	defer stopPoller()
	if l.botPoller != nil {
		go l.botPoller.loop(pollCtx, botPollInterval)
	}

	dispatch := dispatcher.NewEventDispatcher("", "")
	dispatch.OnP2MessageReceiveV1(l.onMessage)
	dispatch.OnP2MessageReactionCreatedV1(l.onReactionCreated)
	dispatch.OnP2MessageReactionDeletedV1(l.onReactionDeleted)

	client := ws.NewClient(
		l.cfg.FeishuAppID,
		l.cfg.FeishuAppSecret,
		ws.WithEventHandler(dispatch),
		ws.WithAutoReconnect(true),
	)

	result := make(chan error, 1)
	go func() {
		result <- client.Start(ctx)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-result:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
}

func (l *Listener) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	incoming := parseIncoming(event)
	if incoming == nil {
		return nil
	}
	ref := conversation.Ref{Provider: "feishu", ConversationID: incoming.ChatID}
	if incoming.Mentioned {
		incoming.Mentioned = l.isBotMention(ctx, *incoming)
	}
	allowed, err := l.shouldHandleIncoming(ref, *incoming)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	if incoming.IsInteractiveMessage() {
		if err := l.dumpInteractiveEvent(ref, event); err != nil {
			return err
		}
	}
	if l.progress != nil {
		accepted, err := l.progress.Accept(
			ref,
			incoming.MessageID,
			incoming.CreateTimeMS,
		)
		if err != nil || !accepted {
			return err
		}
	}
	go func(message IncomingMessage) {
		attachments, cleanup, err := l.client.PrepareImageAttachments(context.Background(), message.MessageID, message.ImageKeys)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			observability.RecordError("listener", "feishu", message.ChatID, "prepare image attachments failed", err)
			l.replyProcessingFailure(context.Background(), message)
			return
		}
		if _, err := l.flow.ProcessText(context.Background(), flow.TextInput{
			Provider:         "feishu",
			ConversationID:   message.ChatID,
			ConversationType: message.ConversationType,
			MessageType:      message.MessageType,
			MessageID:        message.MessageID,
			RootMessageID:    message.RootMessageID,
			ParentMessageID:  message.ParentMessageID,
			ThreadID:         message.ThreadID,
			SenderType:       message.SenderType,
			SenderID:         message.SenderID,
			Text:             message.Text,
			Attachments:      attachments,
			AddReaction:      true,
		}); err != nil {
			observability.RecordError("backend", "feishu", message.ChatID, "process message failed", err)
			l.replyProcessingFailure(context.Background(), message)
		}
	}(*incoming)

	return nil
}

func (l *Listener) onReactionCreated(ctx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
	return l.onReactionEvent(ctx, parseReactionCreated(event))
}

func (l *Listener) onReactionDeleted(ctx context.Context, event *larkim.P2MessageReactionDeletedV1) error {
	return l.onReactionEvent(ctx, parseReactionDeleted(event))
}

func (l *Listener) onReactionEvent(ctx context.Context, incoming *IncomingMessage) error {
	if incoming == nil {
		return nil
	}
	// Ignore app-generated reactions so the bot's own ack/blocked/hook reactions do not loop back into on_reaction.py.
	if strings.EqualFold(strings.TrimSpace(incoming.SenderType), "app") {
		return nil
	}
	if l.flow == nil {
		return nil
	}
	message, err := l.client.GetMessage(ctx, incoming.MessageID)
	if err != nil {
		return err
	}
	incoming.ChatID = message.ChatID
	incoming.RootMessageID = message.RootID
	incoming.ParentMessageID = message.ParentID
	incoming.ThreadID = message.ThreadID
	incoming.ConversationType = normalizeConversationType(messageTypeToChatType(message))
	go func(message IncomingMessage) {
		_, _ = l.flow.ProcessReaction(context.Background(), flow.TextInput{
			Provider:         "feishu",
			ConversationID:   message.ChatID,
			ConversationType: message.ConversationType,
			MessageType:      message.MessageType,
			MessageID:        message.MessageID,
			RootMessageID:    message.RootMessageID,
			ParentMessageID:  message.ParentMessageID,
			ThreadID:         message.ThreadID,
			SenderType:       message.SenderType,
			SenderID:         message.SenderID,
			EventAction:      message.EventAction,
			ReactionEmoji:    message.ReactionEmoji,
			Text:             message.Text,
			AddReaction:      false,
		})
	}(*incoming)
	return nil
}

func messageTypeToChatType(message providerapi.ChatMessage) string {
	if strings.TrimSpace(message.ThreadID) != "" {
		return "topic_group"
	}
	return "group"
}

func (l *Listener) replyProcessingFailure(ctx context.Context, message IncomingMessage) {
	if l.client == nil {
		return
	}
	if message.ConversationType != conversation.TypeDirect && message.MessageID != "" {
		_ = l.client.ReplyTextToMessage(ctx, message.MessageID, processFailureReplyText, " ", providerapi.ReplyOptions{
			InThread: conversation.NormalizeType(message.ConversationType) == conversation.TypeThread,
		})
		return
	}
	if message.ChatID != "" {
		_ = l.client.SendTextToChat(ctx, message.ChatID, processFailureReplyText, " ")
	}
}

func (l *Listener) isBotMention(ctx context.Context, incoming IncomingMessage) bool {
	if len(incoming.MentionOpenIDs) == 0 && len(incoming.MentionNames) == 0 {
		return false
	}
	identity, err := l.loadBotIdentity(ctx)
	if err != nil {
		return false
	}
	for _, openID := range incoming.MentionOpenIDs {
		if openID != "" && openID == identity.OpenID {
			return true
		}
	}
	return false
}

func (l *Listener) loadBotIdentity(ctx context.Context) (botIdentity, error) {
	l.botIdentityMu.Lock()
	defer l.botIdentityMu.Unlock()
	if l.botIdentity.OpenID != "" || l.botIdentity.AppName != "" {
		return l.botIdentity, nil
	}
	if l.client == nil {
		return botIdentity{}, errors.New("missing provider client")
	}
	identity, err := l.client.BotIdentity(ctx)
	if err != nil {
		return botIdentity{}, err
	}
	l.botIdentity = botIdentity{OpenID: strings.TrimSpace(identity.OpenID), AppName: strings.TrimSpace(identity.AppName)}
	return l.botIdentity, nil
}

func (l *Listener) shouldHandleIncoming(ref conversation.Ref, incoming IncomingMessage) (bool, error) {
	if incoming.IsInteractiveMessage() {
		if l.acceptInteractive == nil {
			return false, nil
		}
		return l.acceptInteractive(ref)
	}
	if incoming.ConversationType != conversation.TypeGroup && incoming.ConversationType != conversation.TypeThread {
		return incoming.IsUserMessage(), nil
	}
	if incoming.IsUserMessage() {
		if incoming.Mentioned {
			return true, nil
		}
		policy, err := l.loadGroupMessagePolicy(ref)
		if err != nil {
			return false, err
		}
		return policy.AcceptHumanMessagesWithoutMention, nil
	}
	return false, nil
}

func (l *Listener) loadGroupMessagePolicy(ref conversation.Ref) (session.GroupMessagePolicy, error) {
	if l.groupMessagePolicy == nil {
		return session.GroupMessagePolicy{}, nil
	}
	return l.groupMessagePolicy(ref)
}

func (l *Listener) dumpInteractiveEvent(ref conversation.Ref, event *larkim.P2MessageReceiveV1) error {
	runtimeDir := filepath.Join(ref.WorkspacePath(l.cfg.ChatRootDir), ".agents", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	payload := interactiveEventDump{
		ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Event:      event,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(runtimeDir, interactiveEventDumpFileName), data, 0o644)
}
