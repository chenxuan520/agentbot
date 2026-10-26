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

const maxSessionEventSize = 8 << 20

func (c *Client) WatchSessionEvents(ctx context.Context, handle func(backend.SessionEvent) error) error {
	v2, err := c.useV2(ctx)
	if err != nil {
		return err
	}
	if v2 {
		return c.v2WatchSessionEvents(ctx, handle)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/global/event", nil)
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
		return fmt.Errorf("opencode global event status: %s", resp.Status)
	}
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), maxSessionEventSize)
	var data strings.Builder
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if line == "" {
			event, keep, err := parseSessionEvent(data.String())
			data.Reset()
			if err != nil {
				return err
			}
			if keep {
				if err := handle(event); err != nil {
					return err
				}
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		if field != "data" {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		if data.Len()+len(value)+1 > maxSessionEventSize {
			return fmt.Errorf("opencode global event exceeds %d bytes", maxSessionEventSize)
		}
		data.WriteString(value)
		data.WriteByte('\n')
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

func parseSessionEvent(data string) (backend.SessionEvent, bool, error) {
	if strings.TrimSpace(data) == "" {
		return backend.SessionEvent{}, false, nil
	}
	var envelope struct {
		Payload struct {
			Type       string          `json:"type"`
			Properties json.RawMessage `json:"properties"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return backend.SessionEvent{}, false, fmt.Errorf("decode opencode global event: %w", err)
	}
	event := backend.SessionEvent{Type: envelope.Payload.Type}
	switch event.Type {
	case "server.connected":
		return event, true, nil
	case "message.updated", "session.idle", "session.status":
	default:
		return backend.SessionEvent{}, false, nil
	}
	var properties struct {
		SessionID string `json:"sessionID"`
		Info      struct {
			SessionID string `json:"sessionID"`
			ID        string `json:"id"`
			Role      string `json:"role"`
			Time      struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		} `json:"info"`
		Status struct {
			Type string `json:"type"`
		} `json:"status"`
	}
	if len(envelope.Payload.Properties) > 0 {
		if err := json.Unmarshal(envelope.Payload.Properties, &properties); err != nil {
			return backend.SessionEvent{}, false, fmt.Errorf("decode opencode global event properties: %w", err)
		}
	}
	event.SessionID = properties.SessionID
	switch event.Type {
	case "message.updated":
		if properties.Info.SessionID != "" {
			event.SessionID = properties.Info.SessionID
		}
		event.MessageID = properties.Info.ID
		event.Role = properties.Info.Role
		event.CompletedAt = properties.Info.Time.Completed
	case "session.status":
		if properties.Status.Type != "idle" {
			return backend.SessionEvent{}, false, nil
		}
		event.Type = "session.idle"
	}
	return event, true, nil
}
