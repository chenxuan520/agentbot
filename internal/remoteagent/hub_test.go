package remoteagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

type recordingSender struct {
	mu   sync.Mutex
	msgs []string
}

func (s *recordingSender) send(_ context.Context, _ conversation.Ref, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, text)
	return nil
}

func (s *recordingSender) count(text string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.msgs {
		if m == text {
			n++
		}
	}
	return n
}

func (s *recordingSender) lastContains(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func newHubServer(t *testing.T, h *Hub, ref conversation.Ref) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = h.Serve(w, r, ref)
	}))
	t.Cleanup(srv.Close)
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

func TestHubConnectAndDisconnectDriveRouteAndNotices(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-connect"}
	_, wsURL := newHubServer(t, hub, ref)

	c := dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "connected")
	if !hub.RouteIsLocal(ref) {
		t.Fatalf("route should be local while connected")
	}
	waitUntil(t, func() bool { return sender.count(connectNotice) == 1 }, "connect notice")

	_ = c.Close()
	waitUntil(t, func() bool { return !hub.Connected(ref) }, "disconnected")
	if hub.RouteIsLocal(ref) {
		t.Fatalf("route should not be local after disconnect")
	}
	waitUntil(t, func() bool { return sender.count(disconnectNotice) == 1 }, "disconnect notice")
}

func TestHubDeliverOnlineAndOffline(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-deliver"}

	// Offline: nothing connected yet.
	delivered, err := hub.Deliver(context.Background(), ref, "hi")
	if err != nil {
		t.Fatalf("deliver offline: %v", err)
	}
	if delivered {
		t.Fatalf("deliver should report false when offline")
	}

	_, wsURL := newHubServer(t, hub, ref)
	c := dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "connected")

	delivered, err = hub.Deliver(context.Background(), ref, "do the thing")
	if err != nil {
		t.Fatalf("deliver online: %v", err)
	}
	if !delivered {
		t.Fatalf("deliver should report true when online")
	}

	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	var env outboundEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal prompt: %v", err)
	}
	if env.Type != "prompt" || env.Text != "do the thing" {
		t.Fatalf("unexpected prompt envelope: %+v", env)
	}
	if strings.TrimSpace(env.PromptID) == "" {
		t.Fatalf("prompt envelope missing promptId: %+v", env)
	}
}

func TestHubRelaysPluginMessageWithFooter(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-relay"}
	_, wsURL := newHubServer(t, hub, ref)

	c := dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "connected")

	if err := c.WriteJSON(inboundEnvelope{Type: "message", Text: "本地结果"}); err != nil {
		t.Fatalf("write message: %v", err)
	}
	waitUntil(t, func() bool { return sender.lastContains("本地结果" + localAgentFooter) }, "relayed message with footer")
}

type recordingAckClearer struct {
	mu      sync.Mutex
	cleared []string
}

func (r *recordingAckClearer) clear(_ context.Context, _ conversation.Ref, messageID, reactionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleared = append(r.cleared, messageID+"/"+reactionID)
}

func (r *recordingAckClearer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cleared)
}

func TestHubClearsForwardAcksOnPluginReply(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	clearer := &recordingAckClearer{}
	hub.SetAckClearer(clearer.clear)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-ack-reply"}
	_, wsURL := newHubServer(t, hub, ref)

	c := dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "connected")

	hub.NoteForwarded(ref, "om-1", "rid-1")
	hub.NoteForwarded(ref, "om-2", "rid-2")
	// Empty ids are ignored.
	hub.NoteForwarded(ref, "", "rid-x")

	if err := c.WriteJSON(inboundEnvelope{Type: "message", Text: "本地结果"}); err != nil {
		t.Fatalf("write message: %v", err)
	}
	waitUntil(t, func() bool { return clearer.count() == 2 }, "both forward acks cleared on reply")

	// A second reply with nothing pending clears nothing more.
	if err := c.WriteJSON(inboundEnvelope{Type: "message", Text: "再来一条"}); err != nil {
		t.Fatalf("write message 2: %v", err)
	}
	waitUntil(t, func() bool { return sender.count("再来一条"+localAgentFooter) == 1 }, "second message relayed")
	if clearer.count() != 2 {
		t.Fatalf("cleared = %d, want 2 (no new acks pending)", clearer.count())
	}
}

func TestHubClearsForwardAcksOnDisconnect(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	clearer := &recordingAckClearer{}
	hub.SetAckClearer(clearer.clear)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-ack-disc"}
	_, wsURL := newHubServer(t, hub, ref)

	c := dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "connected")
	hub.NoteForwarded(ref, "om-9", "rid-9")

	_ = c.Close()
	waitUntil(t, func() bool { return !hub.Connected(ref) }, "disconnected")
	waitUntil(t, func() bool { return clearer.count() == 1 }, "pending ack cleared on disconnect")
}

func TestHubDisconnectForceClosesConnection(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-force-disc"}

	// No plugin connected: returns false, no notice.
	if hub.Disconnect(ref) {
		t.Fatal("disconnect with no plugin should return false")
	}

	_, wsURL := newHubServer(t, hub, ref)
	_ = dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "connected")

	if !hub.Disconnect(ref) {
		t.Fatal("disconnect should return true when a plugin is connected")
	}
	waitUntil(t, func() bool { return !hub.Connected(ref) }, "connection closed after Disconnect")
	if hub.RouteIsLocal(ref) {
		t.Fatal("route should not be local after force disconnect")
	}
	waitUntil(t, func() bool { return sender.count(disconnectNotice) == 1 }, "disconnect notice after force close")
}

func TestHubForceBotAndForceLocal(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-force"}

	// ForceLocal without a plugin is an error.
	if err := hub.ForceLocal(ref); err != ErrNoPluginConnected {
		t.Fatalf("force local offline err = %v, want ErrNoPluginConnected", err)
	}

	_, wsURL := newHubServer(t, hub, ref)
	_ = dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "connected")

	if !hub.RouteIsLocal(ref) {
		t.Fatalf("route should be local after connect")
	}
	hub.ForceBot(ref)
	if hub.RouteIsLocal(ref) {
		t.Fatalf("route should be bot after ForceBot")
	}
	if !hub.Connected(ref) {
		t.Fatalf("plugin should still be connected after ForceBot")
	}
	if err := hub.ForceLocal(ref); err != nil {
		t.Fatalf("force local: %v", err)
	}
	if !hub.RouteIsLocal(ref) {
		t.Fatalf("route should be local again after ForceLocal")
	}
}

func TestHubStatusReflectsAttachAndForceBot(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-status"}

	if st := hub.Status(ref); st.Connected {
		t.Fatalf("offline status should be disconnected: %+v", st)
	}

	_, wsURL := newHubServer(t, hub, ref)
	c := dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "connected")

	if err := c.WriteJSON(inboundEnvelope{Type: "attach", AgentID: "macbook", SessionID: "local-1", Title: "demo"}); err != nil {
		t.Fatalf("write attach: %v", err)
	}
	waitUntil(t, func() bool { return hub.Status(ref).AgentID == "macbook" }, "attach propagated")

	st := hub.Status(ref)
	if !st.Connected || !st.RouteLocal || st.SessionID != "local-1" || st.Title != "demo" {
		t.Fatalf("unexpected status after attach: %+v", st)
	}

	hub.ForceBot(ref)
	if st := hub.Status(ref); !st.Connected || st.RouteLocal {
		t.Fatalf("status after ForceBot should be connected but not local: %+v", st)
	}
}

func TestHubTakeoverReplacesOldConnection(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub(sender.send)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-takeover"}
	_, wsURL := newHubServer(t, hub, ref)

	_ = dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "first connected")
	waitUntil(t, func() bool { return sender.count(connectNotice) == 1 }, "first connect notice")

	// Second plugin connects for the same conversation: it takes over silently.
	_ = dial(t, wsURL)
	waitUntil(t, func() bool { return hub.Connected(ref) }, "still connected after takeover")

	// Give the replaced connection time to be torn down.
	time.Sleep(50 * time.Millisecond)
	if got := sender.count(connectNotice); got != 1 {
		t.Fatalf("connect notices = %d, want 1 (takeover is silent)", got)
	}
	if got := sender.count(disconnectNotice); got != 0 {
		t.Fatalf("disconnect notices = %d, want 0 (takeover should not notify)", got)
	}
	if !hub.RouteIsLocal(ref) {
		t.Fatalf("route should still be local after takeover")
	}
}
