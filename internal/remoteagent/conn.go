package remoteagent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

// inboundEnvelope is a message from the plugin to agent-bot.
type inboundEnvelope struct {
	Type      string `json:"type"`
	AgentID   string `json:"agentId"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Text      string `json:"text"`
}

// outboundEnvelope is a message from agent-bot to the plugin.
type outboundEnvelope struct {
	Type      string `json:"type"`
	PromptID  string `json:"promptId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Text      string `json:"text,omitempty"`
}

func (e outboundEnvelope) marshal() ([]byte, error) {
	return json.Marshal(e)
}

// conn is a single plugin WebSocket connection bound to one conversation.
type conn struct {
	hub *Hub
	ref conversation.Ref
	ws  *websocket.Conn

	sendCh    chan []byte
	done      chan struct{}
	closeOnce sync.Once

	attrMu     sync.Mutex
	agentID    string
	curSession string
	title      string
}

func (c *conn) sessionID() string {
	c.attrMu.Lock()
	defer c.attrMu.Unlock()
	return c.curSession
}

func (c *conn) attrs() (agentID, sessionID, title string) {
	c.attrMu.Lock()
	defer c.attrMu.Unlock()
	return c.agentID, c.curSession, c.title
}

func (c *conn) setAttach(agentID, sessionID, title string) {
	c.attrMu.Lock()
	defer c.attrMu.Unlock()
	c.agentID = agentID
	c.curSession = sessionID
	c.title = title
}

// enqueue hands data to the write pump. It returns false if the connection is
// closing, so the caller can treat the message as undelivered.
func (c *conn) enqueue(data []byte) bool {
	select {
	case c.sendCh <- data:
		return true
	case <-c.done:
		return false
	}
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.Close()
	})
}

func (c *conn) readPump() {
	defer c.hub.unregister(c)
	c.ws.SetReadLimit(maxMessageSize)
	_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		c.handleMessage(data)
	}
}

func (c *conn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case data := <-c.sendCh:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.TextMessage, data); err != nil {
				// Tear down so readPump unblocks immediately instead of waiting
				// out the read deadline, and a dead conn stops routing local.
				c.close()
				return
			}
		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *conn) handleMessage(data []byte) {
	var env inboundEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	switch trimmed(env.Type) {
	case "attach":
		c.setAttach(trimmed(env.AgentID), trimmed(env.SessionID), trimmed(env.Title))
	case "message":
		text := trimmed(env.Text)
		if text == "" || c.hub.send == nil {
			return
		}
		// Relay local output as a new message, tagged so the user can tell it
		// came from the local agent. Sending inline preserves ordering.
		_ = c.hub.send(context.Background(), c.ref, text+localAgentFooter)
		// The local agent produced output, so clear any pending "forwarded"
		// reactions on this conversation's inbound messages.
		c.hub.clearAcks(c.ref)
	}
}
