package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func (c *Client) v2WatchSessionEvents(ctx context.Context, handle func(backend.SessionEvent) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if handle == nil {
		return fmt.Errorf("opencode V2 event callback is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/event", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	httpClient := *c.httpClient
	httpClient.Timeout = 0
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode V2 event status: %d", resp.StatusCode)
	}
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := handle(backend.SessionEvent{Type: "server.connected"}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), maxSessionEventSize)
	var data strings.Builder
	var name string
	dispatch := func() error {
		value, eventName := data.String(), name
		data.Reset()
		name = ""
		if eventName == "error" && strings.TrimSpace(value) != "" {
			return fmt.Errorf("opencode V2 event stream error")
		}
		event, keep, err := parseV2SessionEvent(value)
		if err != nil {
			return err
		}
		if keep && event.Type != "server.connected" {
			return handle(event)
		}
		return nil
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			if data.Len()+len(value)+1 > maxSessionEventSize {
				return fmt.Errorf("opencode V2 event exceeds %d bytes", maxSessionEventSize)
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := dispatch(); err != nil {
		return err
	}
	return io.EOF
}

func parseV2SessionEvent(data string) (backend.SessionEvent, bool, error) {
	if strings.TrimSpace(data) == "" {
		return backend.SessionEvent{}, false, nil
	}
	var envelope struct {
		Type    string          `json:"type"`
		Created float64         `json:"created"`
		Data    json.RawMessage `json:"data"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return backend.SessionEvent{}, false, fmt.Errorf("decode opencode V2 event: invalid JSON envelope")
	}
	if envelope.Type == "error" || envelope.Type == "server.error" || v2HasValue(envelope.Error) {
		return backend.SessionEvent{}, false, fmt.Errorf("opencode V2 event stream error")
	}
	if envelope.Type == "server.connected" {
		return backend.SessionEvent{Type: "server.connected"}, true, nil
	}
	switch envelope.Type {
	case "session.step.started", "session.step.streamed", "session.step.ended", "session.step.failed", "session.retry.scheduled",
		"session.execution.succeeded", "session.execution.failed", "session.execution.interrupted", "":
	default:
		return backend.SessionEvent{}, false, nil
	}
	var payload struct {
		SessionID          string          `json:"sessionID"`
		AssistantMessageID string          `json:"assistantMessageID"`
		Error              json.RawMessage `json:"error"`
	}
	if len(envelope.Data) != 0 {
		if err := json.Unmarshal(envelope.Data, &payload); err != nil {
			return backend.SessionEvent{}, false, fmt.Errorf("decode opencode V2 event: invalid JSON data")
		}
	}
	if envelope.Type == "" {
		if v2HasValue(payload.Error) {
			return backend.SessionEvent{}, false, fmt.Errorf("opencode V2 event stream error")
		}
		return backend.SessionEvent{}, false, nil
	}
	if strings.TrimSpace(payload.SessionID) == "" {
		return backend.SessionEvent{}, false, nil
	}
	event := backend.SessionEvent{SessionID: payload.SessionID}
	switch envelope.Type {
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		event.Type = "session.idle"
	default:
		if strings.TrimSpace(payload.AssistantMessageID) == "" {
			return backend.SessionEvent{}, false, nil
		}
		event.Type = "message.updated"
		event.MessageID = payload.AssistantMessageID
		event.Role = "assistant"
		if envelope.Type == "session.step.ended" || envelope.Type == "session.step.failed" {
			event.CompletedAt = int64(envelope.Created)
			if event.CompletedAt <= 0 {
				event.CompletedAt = 1
			}
		}
	}
	return event, true, nil
}
