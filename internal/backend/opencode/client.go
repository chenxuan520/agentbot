package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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
			Tokens struct {
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

// CompactSession compacts the session's context using opencode's summarize
// endpoint. Sending "/compact" as a normal message does NOT compact anything
// (opencode's message endpoint never parses slash commands), so this hits the
// dedicated POST /session/{id}/summarize operation instead. The session id is
// preserved; opencode appends a compaction marker plus a summary message.
func (c *Client) CompactSession(ctx context.Context, workspacePath, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("opencode compact requires session id")
	}

	providerID, modelID, err := c.defaultModel(ctx, workspacePath)
	if err != nil {
		return err
	}

	data, err := json.Marshal(map[string]string{
		"providerID": providerID,
		"modelID":    modelID,
	})
	if err != nil {
		return err
	}

	query := url.Values{}
	query.Set("directory", workspacePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session/"+url.PathEscape(sessionID)+"/summarize?"+query.Encode(), strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyText, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("opencode compact status: %s body=%s", resp.Status, strings.TrimSpace(string(bodyText)))
	}
	return nil
}

// CurrentModel returns the workspace's effective model as "providerID/modelID".
func (c *Client) CurrentModel(ctx context.Context, workspacePath string) (string, error) {
	provider, model, err := c.defaultModel(ctx, workspacePath)
	if err != nil {
		return "", err
	}
	return provider + "/" + model, nil
}

// ListModels reports the models opencode can run for the workspace, grouped by
// connected provider, plus the currently effective model. It reads opencode's
// /config/providers (the filtered, configured set) rather than /provider (the
// full catalog of everything opencode knows).
func (c *Client) ListModels(ctx context.Context, workspacePath string) (backend.ModelCatalog, error) {
	query := url.Values{}
	query.Set("directory", workspacePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/config/providers?"+query.Encode(), nil)
	if err != nil {
		return backend.ModelCatalog{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return backend.ModelCatalog{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyText, _ := io.ReadAll(resp.Body)
		return backend.ModelCatalog{}, fmt.Errorf("opencode config providers status: %s body=%s", resp.Status, strings.TrimSpace(string(bodyText)))
	}

	var payload struct {
		Providers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Models map[string]struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Limit struct {
					Context float64 `json:"context"`
				} `json:"limit"`
			} `json:"models"`
		} `json:"providers"`
		Default map[string]string `json:"default"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return backend.ModelCatalog{}, err
	}

	catalog := backend.ModelCatalog{Providers: make([]backend.ModelProvider, 0, len(payload.Providers))}
	for _, provider := range payload.Providers {
		entry := backend.ModelProvider{
			ID:      provider.ID,
			Name:    provider.Name,
			Default: payload.Default[provider.ID],
			Models:  make([]backend.ModelInfo, 0, len(provider.Models)),
		}
		for id, model := range provider.Models {
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				modelID = id
			}
			name := strings.TrimSpace(model.Name)
			if name == "" {
				name = modelID
			}
			entry.Models = append(entry.Models, backend.ModelInfo{
				ID:           modelID,
				Name:         name,
				ContextLimit: int(model.Limit.Context),
			})
		}
		// Map iteration order is random; sort for stable output.
		sort.Slice(entry.Models, func(i, j int) bool {
			return entry.Models[i].ID < entry.Models[j].ID
		})
		catalog.Providers = append(catalog.Providers, entry)
	}

	// Current model is best-effort: a missing default should not fail the list.
	if current, err := c.CurrentModel(ctx, workspacePath); err == nil {
		catalog.Current = current
	}
	return catalog, nil
}

// defaultModel resolves the workspace's effective opencode model, returned by
// /config as "providerID/modelID". summarize requires both ids explicitly.
func (c *Client) defaultModel(ctx context.Context, workspacePath string) (string, string, error) {
	query := url.Values{}
	query.Set("directory", workspacePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/config?"+query.Encode(), nil)
	if err != nil {
		return "", "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyText, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("opencode config status: %s body=%s", resp.Status, strings.TrimSpace(string(bodyText)))
	}

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", "", err
	}

	provider, model, ok := strings.Cut(strings.TrimSpace(payload.Model), "/")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return "", "", fmt.Errorf("opencode config has no usable model (got %q)", payload.Model)
	}
	return provider, model, nil
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
