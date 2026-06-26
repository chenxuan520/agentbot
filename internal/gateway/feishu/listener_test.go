package feishu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/flow"
	providerfeishu "github.com/chenxuan520/agentbot/internal/provider/feishu"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	"github.com/chenxuan520/agentbot/internal/session"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestShouldHandleIncomingSkipsUnmentionedGroupByDefault(t *testing.T) {
	t.Parallel()

	listener := &Listener{}
	allowed, err := listener.shouldHandleIncoming(conversation.Ref{Provider: "feishu", ConversationID: "chat-1"}, IncomingMessage{
		ConversationType: conversation.TypeGroup,
		SenderType:       senderTypeUser,
		Mentioned:        false,
	})
	if err != nil {
		t.Fatalf("shouldHandleIncoming: %v", err)
	}
	if allowed {
		t.Fatal("expected unmentioned group message to be ignored by default")
	}
}

func TestShouldHandleIncomingAllowsMentionedHumanGroupByDefault(t *testing.T) {
	t.Parallel()

	listener := &Listener{botIdentity: botIdentity{OpenID: "ou_bot"}}
	allowed, err := listener.shouldHandleIncoming(conversation.Ref{Provider: "feishu", ConversationID: "chat-2"}, IncomingMessage{
		ConversationType: conversation.TypeGroup,
		SenderType:       senderTypeUser,
		Mentioned:        true,
		MentionOpenIDs:   []string{"ou_bot"},
	})
	if err != nil {
		t.Fatalf("shouldHandleIncoming: %v", err)
	}
	if !allowed {
		t.Fatal("expected mentioned human group message to be allowed")
	}
}

func TestShouldHandleIncomingSkipsMentionOfOtherUserByDefault(t *testing.T) {
	t.Parallel()

	listener := &Listener{botIdentity: botIdentity{OpenID: "ou_bot", AppName: "bot-name"}}
	if listener.isBotMention(t.Context(), IncomingMessage{
		MentionOpenIDs: []string{"ou_other"},
		MentionNames:   []string{"other-user"},
	}) {
		t.Fatal("expected mention of other user not to match current bot")
	}
	allowed, err := listener.shouldHandleIncoming(conversation.Ref{Provider: "feishu", ConversationID: "chat-2b"}, IncomingMessage{
		ConversationType: conversation.TypeGroup,
		SenderType:       senderTypeUser,
		Mentioned:        false,
		MentionOpenIDs:   []string{"ou_other"},
		MentionNames:     []string{"other-user"},
	})
	if err != nil {
		t.Fatalf("shouldHandleIncoming: %v", err)
	}
	if allowed {
		t.Fatal("expected mention of other user to stay ignored by default")
	}
}

func TestOnMessageSkipsNonBotMentionWithoutAnyProcessing(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeListenerProviderClient{}
	listener := &Listener{
		cfg:         config.Default(t.TempDir()),
		client:      fakeClient,
		botIdentity: botIdentity{OpenID: "ou_bot"},
	}
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderType: strPtr("user")},
			Message: &larkim.EventMessage{
				ChatId:      strPtr("oc_group"),
				MessageId:   strPtr("om_other_mention"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 看这个"}`),
				Mentions: []*larkim.MentionEvent{{
					Id:   &larkim.UserId{OpenId: strPtr("ou_other")},
					Name: strPtr("other-user"),
				}},
			},
		},
	}

	if err := listener.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if fakeClient.prepareCalls != 0 {
		t.Fatalf("prepare calls = %d, want 0", fakeClient.prepareCalls)
	}
}

func TestIsBotMentionMatchesOpenIDOnly(t *testing.T) {
	t.Parallel()

	listener := &Listener{botIdentity: botIdentity{OpenID: "ou_bot", AppName: "bot-name"}}
	if !listener.isBotMention(t.Context(), IncomingMessage{MentionOpenIDs: []string{"ou_bot"}}) {
		t.Fatal("expected bot open_id mention to match")
	}
	if listener.isBotMention(t.Context(), IncomingMessage{MentionNames: []string{"bot-name"}}) {
		t.Fatal("expected bot name alone not to match")
	}
}

type fakeListenerProviderClient struct {
	prepareCalls    int
	getMessageCalls int
}

func (f *fakeListenerProviderClient) Name() string {
	return "fake-listener"
}

func (f *fakeListenerProviderClient) Health(context.Context) error {
	return nil
}

func (f *fakeListenerProviderClient) AddHandlingReaction(context.Context, string) (string, error) {
	return "", nil
}

func (f *fakeListenerProviderClient) AddRemoteHandlingReaction(context.Context, string) (string, error) {
	return "", nil
}

func (f *fakeListenerProviderClient) AddBlockedReaction(context.Context, string) error {
	return nil
}

func (f *fakeListenerProviderClient) AddReaction(context.Context, string, string) error {
	return nil
}

func (f *fakeListenerProviderClient) DeleteReaction(context.Context, string, string) error {
	return nil
}

func (f *fakeListenerProviderClient) GetMessage(context.Context, string) (providerapi.ChatMessage, error) {
	f.getMessageCalls++
	return providerapi.ChatMessage{ChatID: "oc_group"}, nil
}

func (f *fakeListenerProviderClient) SendTextToChat(context.Context, string, string, string) error {
	return nil
}

func (f *fakeListenerProviderClient) ReplyTextToMessage(context.Context, string, string, string, providerapi.ReplyOptions) error {
	return nil
}

func (f *fakeListenerProviderClient) PrepareImageAttachments(context.Context, string, []string) ([]backend.Attachment, func(), error) {
	f.prepareCalls++
	return nil, func() {}, nil
}

func (f *fakeListenerProviderClient) BotIdentity(context.Context) (providerfeishu.BotIdentity, error) {
	return providerfeishu.BotIdentity{OpenID: "ou_bot", AppName: "bot-name"}, nil
}

func TestShouldHandleIncomingAllowsConfiguredUnmentionedThread(t *testing.T) {
	t.Parallel()

	var gotRef conversation.Ref
	listener := &Listener{
		groupMessagePolicy: func(ref conversation.Ref) (session.GroupMessagePolicy, error) {
			gotRef = ref
			return session.GroupMessagePolicy{AcceptHumanMessagesWithoutMention: true}, nil
		},
	}
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-3"}
	allowed, err := listener.shouldHandleIncoming(ref, IncomingMessage{
		ConversationType: conversation.TypeThread,
		SenderType:       senderTypeUser,
		Mentioned:        false,
	})
	if err != nil {
		t.Fatalf("shouldHandleIncoming: %v", err)
	}
	if !allowed {
		t.Fatal("expected configured thread message to be allowed")
	}
	if gotRef != ref {
		t.Fatalf("ref = %+v, want %+v", gotRef, ref)
	}
}

func TestShouldHandleIncomingSkipsRobotGroupByDefault(t *testing.T) {
	t.Parallel()

	listener := &Listener{}
	allowed, err := listener.shouldHandleIncoming(conversation.Ref{Provider: "feishu", ConversationID: "chat-4"}, IncomingMessage{
		ConversationType: conversation.TypeGroup,
		SenderType:       "app",
	})
	if err != nil {
		t.Fatalf("shouldHandleIncoming: %v", err)
	}
	if allowed {
		t.Fatal("expected robot group message to be ignored by default")
	}
}

func TestShouldHandleIncomingSkipsDirectRobotMessage(t *testing.T) {
	t.Parallel()

	listener := &Listener{}
	allowed, err := listener.shouldHandleIncoming(conversation.Ref{Provider: "feishu", ConversationID: "chat-6"}, IncomingMessage{
		ConversationType: conversation.TypeDirect,
		SenderType:       "app",
	})
	if err != nil {
		t.Fatalf("shouldHandleIncoming: %v", err)
	}
	if allowed {
		t.Fatal("expected direct robot message to stay ignored")
	}
}

func TestShouldHandleIncomingSkipsInteractiveByDefault(t *testing.T) {
	t.Parallel()

	listener := &Listener{}
	allowed, err := listener.shouldHandleIncoming(conversation.Ref{Provider: "feishu", ConversationID: "chat-7"}, IncomingMessage{
		ConversationType: conversation.TypeGroup,
		MessageType:      "interactive",
		SenderType:       "app",
	})
	if err != nil {
		t.Fatalf("shouldHandleIncoming: %v", err)
	}
	if allowed {
		t.Fatal("expected interactive message to be ignored by default")
	}
}

func TestShouldHandleIncomingAllowsConfiguredInteractive(t *testing.T) {
	t.Parallel()

	var gotRef conversation.Ref
	listener := &Listener{
		acceptInteractive: func(ref conversation.Ref) (bool, error) {
			gotRef = ref
			return true, nil
		},
	}
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-8"}
	allowed, err := listener.shouldHandleIncoming(ref, IncomingMessage{
		ConversationType: conversation.TypeGroup,
		MessageType:      "interactive",
		SenderType:       "app",
	})
	if err != nil {
		t.Fatalf("shouldHandleIncoming: %v", err)
	}
	if !allowed {
		t.Fatal("expected configured interactive message to be allowed")
	}
	if gotRef != ref {
		t.Fatalf("ref = %+v, want %+v", gotRef, ref)
	}
}

func TestOnReactionEventSkipsAppGeneratedReaction(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeListenerProviderClient{}
	listener := &Listener{
		client: fakeClient,
		flow:   &flow.Service{},
	}

	err := listener.onReactionEvent(context.Background(), &IncomingMessage{
		MessageType:   "reaction",
		MessageID:     "om-reaction-app",
		SenderType:    "app",
		SenderID:      "cli_bot",
		EventAction:   "created",
		ReactionEmoji: "OK",
	})
	if err != nil {
		t.Fatalf("onReactionEvent: %v", err)
	}
	if fakeClient.getMessageCalls != 0 {
		t.Fatalf("get message calls = %d, want 0", fakeClient.getMessageCalls)
	}
}

func TestDumpInteractiveEventWritesRuntimeFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	listener := &Listener{cfg: config.Default(root)}
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-dump"}
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderType: strPtr("user")},
			Message: &larkim.EventMessage{
				ChatId:      strPtr("oc_dump"),
				MessageId:   strPtr("om_dump"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("interactive"),
				Content:     strPtr(`{"text":"hello"}`),
			},
		},
	}

	if err := listener.dumpInteractiveEvent(ref, event); err != nil {
		t.Fatalf("dumpInteractiveEvent: %v", err)
	}

	path := filepath.Join(ref.WorkspacePath(listener.cfg.ChatRootDir), ".agents", "runtime", interactiveEventDumpFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dump file: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "om_dump") {
		t.Fatalf("dump file missing message id: %s", text)
	}
	if !strings.Contains(text, "interactive") {
		t.Fatalf("dump file missing message type: %s", text)
	}
}
