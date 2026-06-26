// Package remoteagent bridges a conversation to a local agent plugin over a
// WebSocket long connection.
//
// A local agent (the user's computer) runs a plugin that connects out to
// agent-bot using the conversation's session token. While connected (and the
// conversation has opted in via settings.remoteEnabled), inbound messages are
// relayed to the plugin instead of the default backend, and the plugin's output
// is pushed back into the conversation. The local session stays the source of
// truth; agent-bot is a thin relay.
//
// The route is a runtime overlay, not a backend: the conversation's backend and
// opencode session are never disturbed, so toggling local on/off resumes the
// bot session cleanly.
package remoteagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

// ErrNoPluginConnected is returned when a manual switch to local is requested
// but no plugin is currently connected for the conversation.
var ErrNoPluginConnected = errors.New("no local agent plugin connected")

const (
	connectNotice    = "本地 agent 已连接，后续消息将转发给本地 agent 处理。"
	disconnectNotice = "本地 agent 已断开，已切回 bot。"
	localAgentFooter = "\n\n— 来自 local agent"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 1 << 20
	sendQueueSize  = 32
)

// SendFunc pushes a plain text message into a conversation. The hub uses it for
// both relayed local-agent output (with a footer) and switch notices.
type SendFunc func(ctx context.Context, ref conversation.Ref, text string) error

// AckClearer removes a reaction previously added to an inbound message. The hub
// calls it to clear the "forwarded to local agent" reaction once the plugin
// replies (or the connection drops). It is best effort.
type AckClearer func(ctx context.Context, ref conversation.Ref, messageID, reactionID string)

// pendingAck is a forward reaction waiting to be cleared when the local agent
// next produces output for the conversation.
type pendingAck struct {
	messageID  string
	reactionID string
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Auth is enforced before the upgrade via the session token, so origin is
	// not used as a security boundary here.
	CheckOrigin: func(r *http.Request) bool { return true },
}

var promptSeq atomic.Uint64

// Hub tracks connected local agent plugins keyed by conversation and exposes the
// routing decisions consumed by the flow layer.
type Hub struct {
	send SendFunc

	mu          sync.Mutex
	conns       map[string]*conn
	forceBot    map[string]bool
	clearAck    AckClearer
	pendingAcks map[string][]pendingAck
}

// NewHub builds a hub. send is used to push messages and notices into the
// conversation; in production it is wired to the flow layer's provider send.
func NewHub(send SendFunc) *Hub {
	return &Hub{
		send:        send,
		conns:       map[string]*conn{},
		forceBot:    map[string]bool{},
		pendingAcks: map[string][]pendingAck{},
	}
}

// SetAckClearer wires the callback used to remove the forward reaction once the
// plugin replies. Optional: without it, forward reactions are simply left in
// place. Set once at startup before any connection is served.
func (h *Hub) SetAckClearer(fn AckClearer) {
	h.mu.Lock()
	h.clearAck = fn
	h.mu.Unlock()
}

// NoteForwarded records a reaction added to messageID after the message was
// forwarded to the plugin, so the hub can clear it when the plugin next replies
// (or disconnects). Empty messageID/reactionID is a no-op.
func (h *Hub) NoteForwarded(ref conversation.Ref, messageID, reactionID string) {
	if trimmed(messageID) == "" || trimmed(reactionID) == "" {
		return
	}
	k := key(ref)
	h.mu.Lock()
	h.pendingAcks[k] = append(h.pendingAcks[k], pendingAck{messageID: messageID, reactionID: reactionID})
	h.mu.Unlock()
}

// clearAcks pops and clears all pending forward reactions for ref. It runs the
// deletions off the caller goroutine so it never blocks the read pump.
func (h *Hub) clearAcks(ref conversation.Ref) {
	k := key(ref)
	h.mu.Lock()
	acks := h.pendingAcks[k]
	delete(h.pendingAcks, k)
	fn := h.clearAck
	h.mu.Unlock()
	if fn == nil || len(acks) == 0 {
		return
	}
	go func() {
		for _, a := range acks {
			fn(context.Background(), ref, a.messageID, a.reactionID)
		}
	}()
}

// Serve upgrades an authenticated request to a WebSocket and serves it until the
// connection closes. ref is the conversation resolved from the session token by
// the caller. It blocks for the lifetime of the connection.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, ref conversation.Ref) error {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}
	c := &conn{
		hub:    h,
		ref:    ref,
		ws:     ws,
		sendCh: make(chan []byte, sendQueueSize),
		done:   make(chan struct{}),
	}
	h.register(c)
	go c.writePump()
	c.readPump()
	return nil
}

// RouteIsLocal reports whether inbound messages for ref should currently go to
// the local agent: a plugin is connected and the route has not been manually
// forced back to the bot.
func (h *Hub) RouteIsLocal(ref conversation.Ref) bool {
	k := key(ref)
	h.mu.Lock()
	defer h.mu.Unlock()
	_, connected := h.conns[k]
	return connected && !h.forceBot[k]
}

// Connected reports whether a plugin is connected for ref.
func (h *Hub) Connected(ref conversation.Ref) bool {
	k := key(ref)
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[k]
	return ok
}

// Status is a snapshot of a conversation's live remote-agent connection, used
// by the web console to show whether a plugin is connected and which route is
// active. It does not include settings.remoteEnabled (the caller adds that).
type Status struct {
	Connected  bool   `json:"connected"`
	RouteLocal bool   `json:"routeLocal"`
	AgentID    string `json:"agentId"`
	SessionID  string `json:"sessionId"`
	Title      string `json:"title"`
}

// Status returns a snapshot of the live connection for ref.
func (h *Hub) Status(ref conversation.Ref) Status {
	k := key(ref)
	h.mu.Lock()
	c, connected := h.conns[k]
	forceBot := h.forceBot[k]
	h.mu.Unlock()
	if !connected {
		return Status{}
	}
	agentID, sessionID, title := c.attrs()
	return Status{
		Connected:  true,
		RouteLocal: !forceBot,
		AgentID:    agentID,
		SessionID:  sessionID,
		Title:      title,
	}
}

// ForceBot manually routes ref back to the bot even while a plugin stays
// connected. A later connect/disconnect event or ForceLocal overrides it.
func (h *Hub) ForceBot(ref conversation.Ref) {
	k := key(ref)
	h.mu.Lock()
	h.forceBot[k] = true
	h.mu.Unlock()
}

// ForceLocal manually routes ref back to the connected plugin, undoing a prior
// ForceBot. It errors when no plugin is connected.
func (h *Hub) ForceLocal(ref conversation.Ref) error {
	k := key(ref)
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[k]; !ok {
		return ErrNoPluginConnected
	}
	h.forceBot[k] = false
	return nil
}

// Disconnect force-closes the plugin connection for ref, if any, and reports
// whether one was connected. Closing triggers the normal teardown on the
// connection goroutine (route reverts to bot, pending acks cleared, disconnect
// notice sent). A plugin that auto-reconnects will simply bind again.
func (h *Hub) Disconnect(ref conversation.Ref) bool {
	k := key(ref)
	h.mu.Lock()
	c := h.conns[k]
	h.mu.Unlock()
	if c == nil {
		return false
	}
	c.close()
	return true
}

// Deliver relays text to the plugin connected for ref. It returns false (without
// error) when no plugin is connected, so callers can fall back to the bot.
func (h *Hub) Deliver(ctx context.Context, ref conversation.Ref, text string) (bool, error) {
	k := key(ref)
	h.mu.Lock()
	c := h.conns[k]
	h.mu.Unlock()
	if c == nil {
		return false, nil
	}

	env := outboundEnvelope{
		Type:      "prompt",
		PromptID:  newPromptID(),
		SessionID: c.sessionID(),
		Text:      text,
	}
	data, err := env.marshal()
	if err != nil {
		return false, err
	}
	if !c.enqueue(data) {
		return false, nil
	}
	return true, nil
}

func (h *Hub) register(c *conn) {
	k := key(c.ref)
	h.mu.Lock()
	old := h.conns[k]
	h.conns[k] = c
	h.forceBot[k] = false
	h.mu.Unlock()

	if old != nil {
		// Takeover (e.g. switched computers): replace silently, no re-notice.
		old.close()
		return
	}
	h.notify(c.ref, connectNotice)
}

func (h *Hub) unregister(c *conn) {
	k := key(c.ref)
	h.mu.Lock()
	cur, ok := h.conns[k]
	isCurrent := ok && cur == c
	if isCurrent {
		delete(h.conns, k)
		delete(h.forceBot, k)
	}
	h.mu.Unlock()

	c.close()
	if isCurrent {
		// The plugin is gone; clear any forward reactions still pending so they
		// don't linger on messages that will now never get a local reply.
		h.clearAcks(c.ref)
		h.notify(c.ref, disconnectNotice)
	}
}

// notify pushes a system message without blocking the caller (register/
// unregister run on the connection goroutine).
func (h *Hub) notify(ref conversation.Ref, text string) {
	if h.send == nil {
		return
	}
	go func() { _ = h.send(context.Background(), ref, text) }()
}

func key(ref conversation.Ref) string {
	return ref.Provider + ":" + ref.ConversationID
}

func newPromptID() string {
	return fmt.Sprintf("p-%d-%d", time.Now().UnixNano(), promptSeq.Add(1))
}

func trimmed(value string) string { return strings.TrimSpace(value) }
