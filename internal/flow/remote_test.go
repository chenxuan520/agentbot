package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

type forwardedAck struct {
	messageID  string
	reactionID string
}

type fakeRemoteRouter struct {
	local        bool
	connected    bool
	deliverOK    bool
	disconnectOK bool
	delivered    []string
	forwarded    []forwardedAck
	forcedBot    int
	disconnected int
}

func (f *fakeRemoteRouter) RouteIsLocal(conversation.Ref) bool { return f.local }
func (f *fakeRemoteRouter) Connected(conversation.Ref) bool    { return f.connected }
func (f *fakeRemoteRouter) Deliver(_ context.Context, _ conversation.Ref, text string) (bool, error) {
	f.delivered = append(f.delivered, text)
	return f.deliverOK, nil
}
func (f *fakeRemoteRouter) NoteForwarded(_ conversation.Ref, messageID, reactionID string) {
	f.forwarded = append(f.forwarded, forwardedAck{messageID: messageID, reactionID: reactionID})
}
func (f *fakeRemoteRouter) ForceBot(conversation.Ref)         { f.forcedBot++ }
func (f *fakeRemoteRouter) ForceLocal(conversation.Ref) error { return nil }
func (f *fakeRemoteRouter) Disconnect(conversation.Ref) bool {
	f.disconnected++
	return f.disconnectOK
}

func enableRemote(t *testing.T, service *Service, ref conversation.Ref) {
	t.Helper()
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("materialize workspace: %v", err)
	}
	settings, err := workspace.LoadSettings(current.Workspace.Path)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	settings.Settings.RemoteEnabled = true
	if err := workspace.SaveSettings(current.Workspace.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}

func TestProcessTextRoutesToRemoteAgentWhenLocal(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	enableRemote(t, service, ref)
	router := &fakeRemoteRouter{local: true, connected: true, deliverOK: true}
	service.SetRemoteRouter(router)

	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-remote-1",
		SenderID:         "ou-user",
		Text:             "hello local",
		AddReaction:      true,
	}); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(router.delivered) != 1 || router.delivered[0] != "hello local" {
		t.Fatalf("delivered = %+v, want [hello local]", router.delivered)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("backend prompt calls = %d, want 0", fakeBackend.promptCalls)
	}
	if len(fakeProvider.replies) != 0 || len(fakeProvider.sentChats) != 0 {
		t.Fatalf("unexpected outbound: replies=%d sends=%d", len(fakeProvider.replies), len(fakeProvider.sentChats))
	}
	// The forward reaction (configurable remote emoji) is added and registered
	// so the hub can clear it when the local agent replies.
	if len(fakeProvider.reactions) != 1 || fakeProvider.reactions[0].messageID != "om-remote-1" || fakeProvider.reactions[0].kind != "remote-handling" {
		t.Fatalf("forward reaction = %+v, want one remote-handling on om-remote-1", fakeProvider.reactions)
	}
	if len(router.forwarded) != 1 || router.forwarded[0].messageID != "om-remote-1" || router.forwarded[0].reactionID != "reaction-id" {
		t.Fatalf("forwarded acks = %+v, want [om-remote-1/reaction-id]", router.forwarded)
	}
}

func TestProcessTextFallsBackToBotWhenRemoteNotLocal(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	enableRemote(t, service, ref)
	router := &fakeRemoteRouter{local: false, connected: false}
	service.SetRemoteRouter(router)

	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-remote-2",
		SenderID:         "ou-user",
		Text:             "hello bot",
	}); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(router.delivered) != 0 {
		t.Fatalf("delivered = %+v, want none", router.delivered)
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("backend prompt calls = %d, want 1", fakeBackend.promptCalls)
	}
}

func TestProcessTextIgnoresRemoteWhenDisabled(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	// remoteEnabled stays false (default template), so the local route is gated off.
	router := &fakeRemoteRouter{local: true, connected: true, deliverOK: true}
	service.SetRemoteRouter(router)

	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-remote-3",
		SenderID:         "ou-user",
		Text:             "hello",
	}); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(router.delivered) != 0 {
		t.Fatalf("delivered = %+v, want none (disabled)", router.delivered)
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("backend prompt calls = %d, want 1", fakeBackend.promptCalls)
	}
}

func TestProcessTextFallsBackWhenDeliveryFails(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	enableRemote(t, service, ref)
	// Plugin reported local but the delivery races with a disconnect.
	router := &fakeRemoteRouter{local: true, connected: true, deliverOK: false}
	service.SetRemoteRouter(router)

	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-remote-4",
		SenderID:         "ou-user",
		Text:             "hello",
	}); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(router.delivered) != 1 {
		t.Fatalf("delivered attempts = %d, want 1", len(router.delivered))
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("backend prompt calls = %d, want 1 (fell back)", fakeBackend.promptCalls)
	}
}

func TestProcessTextDisconnectLocalCommand(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	router := &fakeRemoteRouter{connected: true, disconnectOK: true}
	service.SetRemoteRouter(router)

	res, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-disc",
		SenderID:         "ou-user",
		Text:             "/disconnect-local",
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if router.disconnected != 1 {
		t.Fatalf("disconnect calls = %d, want 1", router.disconnected)
	}
	if !strings.Contains(res.ReplyText, "已强制断开本地 agent 连接") {
		t.Fatalf("reply = %q, want force-disconnect ack", res.ReplyText)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("backend prompt calls = %d, want 0 (command only)", fakeBackend.promptCalls)
	}
}

func TestProcessTextDisconnectLocalWhenNotConnected(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, ref := newTestFlowService(t)
	router := &fakeRemoteRouter{connected: false, disconnectOK: false}
	service.SetRemoteRouter(router)

	res, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-disc-none",
		SenderID:         "ou-user",
		Text:             "/disconnect-local",
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if router.disconnected != 1 {
		t.Fatalf("disconnect calls = %d, want 1 (still attempted)", router.disconnected)
	}
	if !strings.Contains(res.ReplyText, "未连接") {
		t.Fatalf("reply = %q, want not-connected notice", res.ReplyText)
	}
}
