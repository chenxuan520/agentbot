package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/chenxuan520/agentbot/internal/backend"
)

type v2SessionData struct {
	ID       string `json:"id"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
}

type v2CatalogModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Limit      struct {
		Context int `json:"context"`
	} `json:"limit"`
}

type v2ModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

func (c *Client) v2Health(ctx context.Context) error {
	var info struct {
		Version string `json:"version"`
	}
	if err := c.v2Request(ctx, http.MethodGet, "/api/info", nil, nil, &info); err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimPrefix(strings.TrimSpace(info.Version), "v"), "2.") {
		return fmt.Errorf("opencode V2 health returned an unsupported version")
	}
	return nil
}

func (c *Client) v2CreateSession(ctx context.Context, workspacePath string) (string, error) {
	body := map[string]any{
		"location": map[string]string{"directory": workspacePath},
		"permissions": []map[string]string{
			{"action": "question", "resource": "*", "effect": "deny"},
		},
	}
	var response struct {
		Data v2SessionData `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodPost, "/api/session", nil, body, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.Data.ID) == "" {
		return "", fmt.Errorf("opencode V2 create session returned empty id")
	}
	return response.Data.ID, nil
}

func (c *Client) v2GetSession(ctx context.Context, sessionID string) (backend.SessionInfo, error) {
	if strings.TrimSpace(sessionID) == "" {
		return backend.SessionInfo{}, fmt.Errorf("opencode V2 get session requires session id")
	}
	var response struct {
		Data v2SessionData `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID), nil, nil, &response); err != nil {
		return backend.SessionInfo{}, err
	}
	if strings.TrimSpace(response.Data.ID) == "" {
		return backend.SessionInfo{}, fmt.Errorf("opencode V2 get session returned empty id")
	}
	return backend.SessionInfo{ID: response.Data.ID, Directory: response.Data.Location.Directory}, nil
}

func (c *Client) v2AbortSession(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("opencode V2 abort session requires session id")
	}
	ctx, cancel := c.v2OperationContext(ctx)
	defer cancel()
	var response struct {
		Interrupted bool `json:"interrupted"`
	}
	if err := c.v2Request(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/interrupt", nil, nil, &response); err != nil {
		return err
	}
	pending, pendingErr := c.v2PendingNoReplyIDs(ctx, sessionID)
	if err := c.v2WaitSession(ctx, sessionID); err != nil {
		return errors.Join(pendingErr, err)
	}
	if pendingErr != nil {
		return pendingErr
	}
	return c.v2CleanupAbortedSystem(ctx, sessionID, response.Interrupted, pending)
}

func (c *Client) v2CompactSession(ctx context.Context, workspacePath, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("opencode V2 compact requires session id")
	}
	ctx, cancel := c.v2OperationContext(ctx)
	defer cancel()
	messageID, err := newPromptMessageID()
	if err != nil {
		return err
	}
	var admitted struct {
		Data struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Type      string `json:"type"`
		} `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(sessionID)
	body := map[string]string{"id": messageID, "delivery": "steer"}
	if err := c.v2Request(ctx, http.MethodPost, path+"/compact", nil, body, &admitted); err != nil {
		return err
	}
	if admitted.Data.ID == "" || admitted.Data.Type != "compaction" || admitted.Data.SessionID != sessionID {
		return fmt.Errorf("opencode V2 compact returned an invalid admission")
	}
	if err := c.v2WaitSession(ctx, sessionID); err != nil {
		return err
	}
	var response struct {
		Data struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodGet, path+"/message/"+url.PathEscape(admitted.Data.ID), nil, nil, &response); err != nil {
		return err
	}
	if response.Data.ID != admitted.Data.ID || response.Data.Type != "compaction" || response.Data.Status != "completed" {
		return fmt.Errorf("opencode V2 compaction did not complete (status %q)", response.Data.Status)
	}
	return nil
}

func (c *Client) v2CurrentModel(ctx context.Context, workspacePath string) (string, error) {
	var response struct {
		Data *v2CatalogModel `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodGet, "/api/model/default", v2LocationQuery(workspacePath), nil, &response); err != nil {
		return "", err
	}
	model := response.Data
	if model == nil || !model.Enabled || strings.TrimSpace(model.ID) == "" || strings.TrimSpace(model.ProviderID) == "" {
		return "", fmt.Errorf("opencode V2 has no usable default model")
	}
	return model.ProviderID + "/" + model.ID, nil
}

func (c *Client) v2ListModels(ctx context.Context, workspacePath string) (backend.ModelCatalog, error) {
	query := v2LocationQuery(workspacePath)
	var models struct {
		Data []v2CatalogModel `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodGet, "/api/model", query, nil, &models); err != nil {
		return backend.ModelCatalog{}, err
	}
	var providers struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodGet, "/api/provider", query, nil, &providers); err != nil {
		return backend.ModelCatalog{}, err
	}
	catalog := backend.ModelCatalog{Providers: make([]backend.ModelProvider, 0)}
	catalog.Current, _ = c.v2CurrentModel(ctx, workspacePath)
	currentProvider, currentID, _ := strings.Cut(catalog.Current, "/")
	names := make(map[string]string, len(providers.Data))
	for _, provider := range providers.Data {
		names[provider.ID] = strings.TrimSpace(provider.Name)
	}
	groups := make(map[string]*backend.ModelProvider)
	for _, model := range models.Data {
		if !model.Enabled || strings.TrimSpace(model.ProviderID) == "" || strings.TrimSpace(model.ID) == "" {
			continue
		}
		group := groups[model.ProviderID]
		if group == nil {
			name := names[model.ProviderID]
			if name == "" {
				name = model.ProviderID
			}
			group = &backend.ModelProvider{ID: model.ProviderID, Name: name, Models: make([]backend.ModelInfo, 0)}
			groups[model.ProviderID] = group
		}
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = model.ID
		}
		group.Models = append(group.Models, backend.ModelInfo{ID: model.ID, Name: name, ContextLimit: model.Limit.Context})
		if model.ProviderID == currentProvider && model.ID == currentID {
			group.Default = model.ID
		}
	}
	for _, group := range groups {
		sort.Slice(group.Models, func(i, j int) bool { return group.Models[i].ID < group.Models[j].ID })
		catalog.Providers = append(catalog.Providers, *group)
	}
	sort.Slice(catalog.Providers, func(i, j int) bool { return catalog.Providers[i].ID < catalog.Providers[j].ID })
	return catalog, nil
}

func v2ParseModelRef(value string) (v2ModelRef, error) {
	providerID, modelID, ok := splitModel(value)
	id, variant, hasVariant := strings.Cut(modelID, "#")
	if !ok || strings.Contains(providerID, "#") || id == "" || (hasVariant && (variant == "" || strings.Contains(variant, "#"))) {
		return v2ModelRef{}, fmt.Errorf("opencode V2 requires a provider/model reference with an optional variant")
	}
	return v2ModelRef{ID: id, ProviderID: providerID, Variant: variant}, nil
}

func (c *Client) v2OperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.httpClient.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func (c *Client) v2WaitSession(ctx context.Context, sessionID string) error {
	return c.v2Request(ctx, http.MethodPost, "/api/experimental/session/"+url.PathEscape(sessionID)+"/wait", nil, nil, nil)
}
