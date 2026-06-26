package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
)

const defaultHTTPTimeout = 5 * time.Minute

type Options struct {
	HTTPTimeout time.Duration
}

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func New(baseURL string) *Client {
	return NewWithOptions(baseURL, Options{})
}

func NewWithOptions(baseURL string, options Options) *Client {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	timeout := options.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &Client{
		baseURL:    trimmed,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *Client) Name() string {
	return "opencode"
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/global/health", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode health status: %s", resp.Status)
	}

	var payload struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if !payload.Healthy {
		return fmt.Errorf("opencode is not healthy")
	}
	return nil
}

func (c *Client) webAvailable(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode >= 200 && resp.StatusCode < 500
}

func (c *Client) CreateSession(ctx context.Context, workspacePath string) (string, error) {
	_ = c.disposeInstance(ctx, workspacePath)

	query := url.Values{}
	query.Set("directory", workspacePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session?"+query.Encode(), strings.NewReader(`{}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("opencode create session status: %s", resp.Status)
	}

	var payload struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.ID == "" {
		return "", fmt.Errorf("opencode create session returned empty id")
	}
	return payload.ID, nil
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (backend.SessionInfo, error) {
	if strings.TrimSpace(sessionID) == "" {
		return backend.SessionInfo{}, fmt.Errorf("opencode get session requires session id")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/session/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return backend.SessionInfo{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return backend.SessionInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyText, _ := io.ReadAll(resp.Body)
		return backend.SessionInfo{}, fmt.Errorf("opencode get session status: %s body=%s", resp.Status, strings.TrimSpace(string(bodyText)))
	}

	var payload struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return backend.SessionInfo{}, err
	}
	if payload.ID == "" {
		return backend.SessionInfo{}, fmt.Errorf("opencode get session returned empty id")
	}
	return backend.SessionInfo{ID: payload.ID, Directory: payload.Directory}, nil
}

func (c *Client) GetSessionMessages(ctx context.Context, sessionID string) ([]backend.SessionMessage, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("opencode get session messages requires session id")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/session/"+url.PathEscape(sessionID)+"/message", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyText, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("opencode get session messages status: %s body=%s", resp.Status, strings.TrimSpace(string(bodyText)))
	}

	var payload []struct {
		Info struct {
			ID   string `json:"id"`
			Role string `json:"role"`
			Time struct {
				Created int64 `json:"created"`
			} `json:"time"`
		} `json:"info"`
		Parts []struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Reason string `json:"reason"`
			Tool   string `json:"tool"`
			State  struct {
				Status string         `json:"status"`
				Input  map[string]any `json:"input"`
			} `json:"state"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	messages := make([]backend.SessionMessage, 0, len(payload))
	for _, item := range payload {
		message := backend.SessionMessage{
			ID:        item.Info.ID,
			Role:      item.Info.Role,
			CreatedAt: item.Info.Time.Created,
			Parts:     make([]backend.SessionMessagePart, 0, len(item.Parts)),
		}
		for _, part := range item.Parts {
			message.Parts = append(message.Parts, backend.SessionMessagePart{
				Type:             part.Type,
				Text:             part.Text,
				Reason:           part.Reason,
				Tool:             part.Tool,
				ToolStatus:       part.State.Status,
				ToolInputSummary: summarizeToolInput(part.Tool, part.State.Input),
			})
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func summarizeToolInput(tool string, input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash":
		return compactToolInputSummary(toolInputField(input, "command"))
	case "read":
		return compactToolInputSummary(toolInputField(input, "filePath"))
	case "grep", "glob":
		return compactToolInputSummary(toolInputField(input, "pattern"))
	case "webfetch":
		return compactToolInputSummary(toolInputField(input, "url"))
	default:
		if summary := compactToolInputSummary(toolInputField(input, "description")); summary != "" {
			return summary
		}
		return compactToolInputSummary(toolInputField(input, "header"))
	}
}

func toolInputField(input map[string]any, key string) string {
	raw, ok := input[key]
	if !ok || raw == nil {
		return ""
	}
	if value, ok := raw.(string); ok {
		return value
	}
	return fmt.Sprint(raw)
}

func compactToolInputSummary(value string) string {
	trimmed := strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if trimmed == "" {
		return ""
	}
	const maxLen = 120
	if len(trimmed) <= maxLen {
		return trimmed
	}
	return trimmed[:maxLen-3] + "..."
}

func (c *Client) AbortSession(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("opencode abort session requires session id")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session/"+url.PathEscape(sessionID)+"/abort", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("opencode abort session status: %s", resp.Status)
	}
	return nil
}

func (c *Client) disposeInstance(ctx context.Context, workspacePath string) error {
	query := url.Values{}
	query.Set("directory", workspacePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/instance/dispose?"+query.Encode(), nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("opencode dispose instance status: %s", resp.Status)
	}
	return nil
}
