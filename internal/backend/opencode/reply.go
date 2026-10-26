package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/chenxuan520/agentbot/internal/backend"
)

type sessionMessagePayload struct {
	Info struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
		ParentID  string `json:"parentID"`
		Role      string `json:"role"`
		Time      struct {
			Created   int64 `json:"created"`
			Completed int64 `json:"completed"`
		} `json:"time"`
		Summary json.RawMessage `json:"summary"`
		Finish  string          `json:"finish"`
		Error   json.RawMessage `json:"error"`
		Tokens  struct {
			Total     float64 `json:"total"`
			Input     float64 `json:"input"`
			Output    float64 `json:"output"`
			Reasoning float64 `json:"reasoning"`
			Cache     struct {
				Read  float64 `json:"read"`
				Write float64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	} `json:"info"`
	Parts []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		Reason    string `json:"reason"`
		Tool      string `json:"tool"`
		Synthetic bool   `json:"synthetic"`
		Ignored   bool   `json:"ignored"`
		Metadata  struct {
			ProviderExecuted   bool `json:"providerExecuted"`
			CompactionContinue bool `json:"compaction_continue"`
		} `json:"metadata"`
		State struct {
			Status   string         `json:"status"`
			Input    map[string]any `json:"input"`
			Output   string         `json:"output"`
			Error    string         `json:"error"`
			Metadata struct {
				SessionID   string `json:"sessionId"`
				Background  bool   `json:"background"`
				Interrupted bool   `json:"interrupted"`
			} `json:"metadata"`
		} `json:"state"`
	} `json:"parts"`
}

func (item sessionMessagePayload) message() backend.SessionMessage {
	message := backend.SessionMessage{
		ID:          item.Info.ID,
		SessionID:   item.Info.SessionID,
		ParentID:    item.Info.ParentID,
		Role:        item.Info.Role,
		CreatedAt:   item.Info.Time.Created,
		CompletedAt: item.Info.Time.Completed,
		Summary:     item.Info.Role == "assistant" && strings.TrimSpace(string(item.Info.Summary)) == "true",
		Finish:      item.Info.Finish,
		HasError:    len(item.Info.Error) > 0 && strings.TrimSpace(string(item.Info.Error)) != "null",
		Tokens: backend.TokenUsage{
			Total:      int(item.Info.Tokens.Total),
			Input:      int(item.Info.Tokens.Input),
			Output:     int(item.Info.Tokens.Output),
			Reasoning:  int(item.Info.Tokens.Reasoning),
			CacheRead:  int(item.Info.Tokens.Cache.Read),
			CacheWrite: int(item.Info.Tokens.Cache.Write),
		},
		Parts: make([]backend.SessionMessagePart, 0, len(item.Parts)),
	}
	for _, part := range item.Parts {
		message.Parts = append(message.Parts, backend.SessionMessagePart{
			Type:               part.Type,
			Text:               part.Text,
			Reason:             part.Reason,
			Tool:               part.Tool,
			ToolStatus:         part.State.Status,
			ToolInputSummary:   summarizeToolInput(part.Tool, part.State.Input),
			Synthetic:          part.Synthetic,
			Ignored:            part.Ignored,
			ToolSessionID:      part.State.Metadata.SessionID,
			ToolBackground:     part.State.Metadata.Background,
			ToolUpdated:        part.Tool == "task" && strings.Contains(part.State.Output, "<summary>Background task updated</summary>"),
			ToolCancelled:      part.Tool == "task" && part.State.Error == "Task cancelled",
			ProviderExecuted:   part.Metadata.ProviderExecuted,
			Interrupted:        part.State.Metadata.Interrupted,
			CompactionContinue: part.Metadata.CompactionContinue,
		})
	}
	for i := len(message.Parts) - 1; i >= 0; i-- {
		if part := message.Parts[i]; part.Type == "text" && !part.Synthetic && !part.Ignored && strings.TrimSpace(part.Text) != "" {
			message.ReplySuppressed = isRetryStatusOnly(part.Text)
			break
		}
	}
	return message
}

func (c *Client) GetSessionReplyMessages(ctx context.Context, workspacePath, sessionID string) ([]backend.SessionMessage, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("opencode get session reply messages requires session id")
	}
	v2, err := c.useV2(ctx)
	if err != nil {
		return nil, err
	}
	if v2 {
		return c.v2GetSessionMessages(ctx, workspacePath, sessionID)
	}

	query := url.Values{}
	query.Set("directory", workspacePath)
	query.Set("limit", "100")
	messages := make([]backend.SessionMessage, 0)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/session/"+url.PathEscape(sessionID)+"/message?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("opencode get session reply messages status: %s", resp.Status)
		}
		var payload []sessionMessagePayload
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, item := range payload {
			messages = append(messages, item.message())
		}
		cursor := resp.Header.Get("X-Next-Cursor")
		if cursor == "" {
			break
		}
		query.Set("before", cursor)
	}
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].CreatedAt == messages[j].CreatedAt {
			return messages[i].ID < messages[j].ID
		}
		return messages[i].CreatedAt < messages[j].CreatedAt
	})
	return messages, nil
}

func (c *Client) GetSessionReplyMessage(ctx context.Context, workspacePath, sessionID, messageID string) (backend.SessionMessage, error) {
	if strings.TrimSpace(sessionID) == "" {
		return backend.SessionMessage{}, fmt.Errorf("opencode get session reply message requires session id")
	}
	if strings.TrimSpace(messageID) == "" {
		return backend.SessionMessage{}, fmt.Errorf("opencode get session reply message requires message id")
	}
	v2, err := c.useV2(ctx)
	if err != nil {
		return backend.SessionMessage{}, err
	}
	if v2 {
		return c.v2GetSessionMessage(ctx, workspacePath, sessionID, messageID)
	}

	query := url.Values{}
	query.Set("directory", workspacePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/session/"+url.PathEscape(sessionID)+"/message/"+url.PathEscape(messageID)+"?"+query.Encode(), nil)
	if err != nil {
		return backend.SessionMessage{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return backend.SessionMessage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return backend.SessionMessage{}, fmt.Errorf("opencode get session reply message status: %s", resp.Status)
	}
	var payload sessionMessagePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return backend.SessionMessage{}, err
	}
	return payload.message(), nil
}
