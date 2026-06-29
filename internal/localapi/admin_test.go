package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	"github.com/chenxuan520/agentbot/internal/session"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
	"github.com/chenxuan520/agentbot/internal/workspace"
	"github.com/gin-gonic/gin"
)

type fakeAdminTranscriptBackend struct {
	messages        []backend.SessionMessage
	sessionMessages map[string][]backend.SessionMessage
}

type fakeAdminDisplayProvider struct {
	infos map[string]providerapi.ChatDisplayInfo
}

func (f fakeAdminTranscriptBackend) Name() string                   { return "fake-transcript" }
func (f fakeAdminTranscriptBackend) Health(_ context.Context) error { return nil }
func (f fakeAdminTranscriptBackend) CreateSession(context.Context, string) (string, error) {
	return "", nil
}
func (f fakeAdminTranscriptBackend) AbortSession(context.Context, string) error { return nil }
func (f fakeAdminTranscriptBackend) Prompt(context.Context, string, string, string, []backend.Attachment, backend.PromptOptions) (backend.PromptResult, error) {
	return backend.PromptResult{}, nil
}
func (f fakeAdminTranscriptBackend) GetSessionMessages(_ context.Context, sessionID string) ([]backend.SessionMessage, error) {
	if f.sessionMessages != nil {
		return append([]backend.SessionMessage(nil), f.sessionMessages[strings.TrimSpace(sessionID)]...), nil
	}
	return append([]backend.SessionMessage(nil), f.messages...), nil
}

type fakeAdminModelsBackend struct {
	catalog backend.ModelCatalog
}

func (f fakeAdminModelsBackend) Name() string                   { return "fake-models" }
func (f fakeAdminModelsBackend) Health(_ context.Context) error { return nil }
func (f fakeAdminModelsBackend) CreateSession(context.Context, string) (string, error) {
	return "", nil
}
func (f fakeAdminModelsBackend) AbortSession(context.Context, string) error { return nil }
func (f fakeAdminModelsBackend) Prompt(context.Context, string, string, string, []backend.Attachment, backend.PromptOptions) (backend.PromptResult, error) {
	return backend.PromptResult{}, nil
}
func (f fakeAdminModelsBackend) ListModels(context.Context, string) (backend.ModelCatalog, error) {
	return f.catalog, nil
}
func (f fakeAdminModelsBackend) CurrentModel(context.Context, string) (string, error) {
	return f.catalog.Current, nil
}

func (f fakeAdminDisplayProvider) Name() string                 { return "fake-provider" }
func (f fakeAdminDisplayProvider) Health(context.Context) error { return nil }
func (f fakeAdminDisplayProvider) AddHandlingReaction(context.Context, string) (string, error) {
	return "", nil
}
func (f fakeAdminDisplayProvider) AddRemoteHandlingReaction(context.Context, string) (string, error) {
	return "", nil
}
func (f fakeAdminDisplayProvider) AddBlockedReaction(context.Context, string) error     { return nil }
func (f fakeAdminDisplayProvider) AddReaction(context.Context, string, string) error    { return nil }
func (f fakeAdminDisplayProvider) DeleteReaction(context.Context, string, string) error { return nil }
func (f fakeAdminDisplayProvider) SendTextToChat(context.Context, string, string, string) error {
	return nil
}
func (f fakeAdminDisplayProvider) ReplyTextToMessage(context.Context, string, string, string, providerapi.ReplyOptions) error {
	return nil
}
func (f fakeAdminDisplayProvider) GetChatDisplayInfo(_ context.Context, chatID string) (providerapi.ChatDisplayInfo, error) {
	if info, ok := f.infos[chatID]; ok {
		return info, nil
	}
	return providerapi.ChatDisplayInfo{}, nil
}

func TestAdminProjectTokenListsSessions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	server.providers = func(config.Config, string) (providerapi.Client, error) {
		return fakeAdminDisplayProvider{infos: map[string]providerapi.ChatDisplayInfo{
			"chat-a": {DisplayName: "群A", ChatMode: "group"},
			"chat-b": {DisplayName: "张三", ChatMode: "p2p"},
		}}, nil
	}
	router := server.router()

	refA := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	refB := conversation.Ref{Provider: "feishu", ConversationID: "chat-b"}
	if _, err := sessions.Current(refA); err != nil {
		t.Fatalf("prepare session a: %v", err)
	}
	if _, err := sessions.Current(refB); err != nil {
		t.Fatalf("prepare session b: %v", err)
	}
	if err := sessions.Bind(refA, "opencode", "session-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind session a: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Items []struct {
			ConversationID string `json:"conversationId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(body.Items))
	}

	displayReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/sessions/display-names", strings.NewReader(`{"sessions":[{"provider":"feishu","conversationId":"chat-a"},{"provider":"feishu","conversationId":"chat-b"}]}`))
	displayReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	displayReq.Header.Set("Content-Type", "application/json")
	displayResp := httptest.NewRecorder()
	router.ServeHTTP(displayResp, displayReq)
	if displayResp.Code != http.StatusOK {
		t.Fatalf("display status = %d, body=%s", displayResp.Code, displayResp.Body.String())
	}
	var displayBody struct {
		Items []struct {
			ConversationID string `json:"conversationId"`
			DisplayName    string `json:"displayName"`
			ChatMode       string `json:"chatMode"`
		} `json:"items"`
	}
	if err := json.Unmarshal(displayResp.Body.Bytes(), &displayBody); err != nil {
		t.Fatalf("decode display response: %v", err)
	}
	if len(displayBody.Items) != 2 {
		t.Fatalf("display items = %d, want 2", len(displayBody.Items))
	}
	if displayBody.Items[0].DisplayName != "群A" || displayBody.Items[0].ChatMode != "group" {
		t.Fatalf("unexpected first display item: %+v", displayBody.Items[0])
	}
	if displayBody.Items[1].DisplayName != "张三" || displayBody.Items[1].ChatMode != "p2p" {
		t.Fatalf("unexpected second display item: %+v", displayBody.Items[1])
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-a", nil)
	detailReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	detailResp := httptest.NewRecorder()
	router.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detailResp.Code, detailResp.Body.String())
	}
	if !json.Valid(detailResp.Body.Bytes()) {
		t.Fatalf("invalid detail payload: %s", detailResp.Body.String())
	}
	if !containsJSONField(detailResp.Body.Bytes(), "sessionToken") {
		t.Fatalf("missing sessionToken in detail payload: %s", detailResp.Body.String())
	}
	if !containsJSONField(detailResp.Body.Bytes(), "chatMode") {
		t.Fatalf("missing chatMode in detail payload: %s", detailResp.Body.String())
	}
}

func TestAdminProjectTokenCanDeleteSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-delete"}
	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if err := sessions.Bind(ref, "opencode", "session-old", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/sessions/feishu/chat-delete", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.Code, resp.Body.String())
	}
	record, err := store.Get(ref)
	if err != nil {
		t.Fatalf("get record after delete: %v", err)
	}
	if record != nil {
		t.Fatal("expected session record to be deleted")
	}
	if _, err := os.Stat(current.Workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace path err = %v, want not exist", err)
	}
}

func TestAdminSessionTranscriptSupportsSnapshotAndIncrementalPolling(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	server.backends = func(config.Config, workspace.Settings) (backend.Client, error) {
		return fakeAdminTranscriptBackend{messages: []backend.SessionMessage{
			{ID: "msg-1", Role: "user", CreatedAt: 1779785200000, Parts: []backend.SessionMessagePart{{Type: "text", Text: "hello"}}},
			{ID: "msg-2", Role: "assistant", CreatedAt: 1779785210000, Parts: []backend.SessionMessagePart{{Type: "reasoning", Text: "thinking"}, {Type: "tool", Tool: "grep", ToolStatus: "completed", ToolInputSummary: "session"}, {Type: "text", Text: "done"}}},
			{ID: "msg-3", Role: "assistant", CreatedAt: 1779785220000, Tokens: backend.TokenUsage{Total: 12345, Input: 12000, Output: 300, Reasoning: 45}, Parts: []backend.SessionMessagePart{{Type: "step-finish", Reason: "stop"}}},
		}}, nil
	}
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if err := sessions.Bind(ref, "opencode", "session-main", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	snapshotReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-a/transcript", nil)
	snapshotReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	snapshotResp := httptest.NewRecorder()
	router.ServeHTTP(snapshotResp, snapshotReq)
	if snapshotResp.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, body=%s", snapshotResp.Code, snapshotResp.Body.String())
	}
	var snapshotBody adminTranscriptResponse
	if err := json.Unmarshal(snapshotResp.Body.Bytes(), &snapshotBody); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snapshotBody.Reset {
		t.Fatal("expected initial transcript response to reset")
	}
	if snapshotBody.SessionID != "session-main" {
		t.Fatalf("session id = %q", snapshotBody.SessionID)
	}
	if len(snapshotBody.AvailableSessions) != 1 || snapshotBody.AvailableSessions[0].SessionID != "session-main" {
		t.Fatalf("unexpected available sessions: %+v", snapshotBody.AvailableSessions)
	}
	if snapshotBody.TotalMessages != 3 || snapshotBody.LatestMessageID != "msg-3" {
		t.Fatalf("unexpected transcript meta: %+v", snapshotBody)
	}
	if snapshotBody.ContextTokens != 12345 || snapshotBody.ContextInputTokens != 12000 {
		t.Fatalf("unexpected context tokens: total=%d input=%d", snapshotBody.ContextTokens, snapshotBody.ContextInputTokens)
	}
	if len(snapshotBody.Messages) != 3 {
		t.Fatalf("snapshot messages = %d, want 3", len(snapshotBody.Messages))
	}
	if snapshotBody.Messages[1].Parts[0].Type != "reasoning" || snapshotBody.Messages[1].Parts[0].Text != "thinking" {
		t.Fatalf("unexpected reasoning part: %+v", snapshotBody.Messages[1].Parts[0])
	}
	if snapshotBody.Messages[1].Parts[1].ToolInputSummary != "session" {
		t.Fatalf("unexpected tool input summary: %+v", snapshotBody.Messages[1].Parts[1])
	}

	incrementalReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-a/transcript?sessionId=session-main&afterMessageId=msg-2", nil)
	incrementalReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	incrementalResp := httptest.NewRecorder()
	router.ServeHTTP(incrementalResp, incrementalReq)
	if incrementalResp.Code != http.StatusOK {
		t.Fatalf("incremental status = %d, body=%s", incrementalResp.Code, incrementalResp.Body.String())
	}
	var incrementalBody adminTranscriptResponse
	if err := json.Unmarshal(incrementalResp.Body.Bytes(), &incrementalBody); err != nil {
		t.Fatalf("decode incremental: %v", err)
	}
	if incrementalBody.Reset {
		t.Fatal("expected incremental transcript response")
	}
	if len(incrementalBody.Messages) != 2 || incrementalBody.Messages[0].ID != "msg-2" || incrementalBody.Messages[1].ID != "msg-3" {
		t.Fatalf("unexpected incremental messages: %+v", incrementalBody.Messages)
	}
}

func TestAdminSessionModelsListsCatalog(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	server.backends = func(config.Config, workspace.Settings) (backend.Client, error) {
		return fakeAdminModelsBackend{catalog: backend.ModelCatalog{
			Current: "openai/gpt-5.4",
			Providers: []backend.ModelProvider{{
				ID:      "openai",
				Name:    "OpenAI",
				Default: "gpt-5.5",
				Models: []backend.ModelInfo{
					{ID: "gpt-5.4", Name: "GPT-5.4", ContextLimit: 1050000},
					{ID: "gpt-5.5", Name: "GPT-5.5", ContextLimit: 400000},
				},
			}},
		}}, nil
	}
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-a/models", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.Code, resp.Body.String())
	}
	var body adminModelsResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if body.Current != "openai/gpt-5.4" {
		t.Fatalf("current = %q, want openai/gpt-5.4", body.Current)
	}
	if len(body.Providers) != 1 || body.Providers[0].ID != "openai" || body.Providers[0].Default != "gpt-5.5" {
		t.Fatalf("unexpected providers: %+v", body.Providers)
	}
	if len(body.Providers[0].Models) != 2 || body.Providers[0].Models[0].ID != "gpt-5.4" || body.Providers[0].Models[0].ContextLimit != 1050000 {
		t.Fatalf("unexpected models: %+v", body.Providers[0].Models)
	}
}

func TestAdminSessionTranscriptSupportsSelectingTopicSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	server.backends = func(config.Config, workspace.Settings) (backend.Client, error) {
		return fakeAdminTranscriptBackend{sessionMessages: map[string][]backend.SessionMessage{
			"topic-sess-1": {
				{ID: "topic-a-1", Role: "user", CreatedAt: 1, Parts: []backend.SessionMessagePart{{Type: "text", Text: "topic a"}}},
			},
			"topic-sess-2": {
				{ID: "topic-b-1", Role: "user", CreatedAt: 2, Parts: []backend.SessionMessagePart{{Type: "text", Text: "topic b"}}},
				{ID: "topic-b-2", Role: "assistant", CreatedAt: 3, Parts: []backend.SessionMessagePart{{Type: "text", Text: "done"}}},
			},
		}}, nil
	}
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-topic"}
	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	settings := current.Workspace.Settings
	settings.Settings.ReplyMode = workspace.ReplyModeTopicSession
	if err := workspace.SaveSettings(current.Workspace.Path, settings); err != nil {
		t.Fatalf("save topic-session mode: %v", err)
	}
	if err := sessions.BindTopic(ref, "om-root-a", "opencode", "topic-sess-1", time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatalf("bind topic session a: %v", err)
	}
	if err := sessions.BindTopic(ref, "om-root-b", "opencode", "topic-sess-2", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic session b: %v", err)
	}

	defaultReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-topic/transcript", nil)
	defaultReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	defaultResp := httptest.NewRecorder()
	router.ServeHTTP(defaultResp, defaultReq)
	if defaultResp.Code != http.StatusOK {
		t.Fatalf("default status = %d, body=%s", defaultResp.Code, defaultResp.Body.String())
	}
	var defaultBody adminTranscriptResponse
	if err := json.Unmarshal(defaultResp.Body.Bytes(), &defaultBody); err != nil {
		t.Fatalf("decode default transcript: %v", err)
	}
	if defaultBody.SessionID != "topic-sess-2" {
		t.Fatalf("default session id = %q, want latest topic session", defaultBody.SessionID)
	}
	if len(defaultBody.AvailableSessions) != 2 {
		t.Fatalf("available sessions = %+v, want two topic sessions", defaultBody.AvailableSessions)
	}
	if defaultBody.AvailableSessions[0].SessionID != "topic-sess-2" || defaultBody.AvailableSessions[0].Kind != "topic" {
		t.Fatalf("unexpected first available session: %+v", defaultBody.AvailableSessions[0])
	}

	selectedReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-topic/transcript?sessionId=topic-sess-1", nil)
	selectedReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	selectedResp := httptest.NewRecorder()
	router.ServeHTTP(selectedResp, selectedReq)
	if selectedResp.Code != http.StatusOK {
		t.Fatalf("selected status = %d, body=%s", selectedResp.Code, selectedResp.Body.String())
	}
	var selectedBody adminTranscriptResponse
	if err := json.Unmarshal(selectedResp.Body.Bytes(), &selectedBody); err != nil {
		t.Fatalf("decode selected transcript: %v", err)
	}
	if selectedBody.SessionID != "topic-sess-1" {
		t.Fatalf("selected session id = %q, want requested topic session", selectedBody.SessionID)
	}
	if len(selectedBody.Messages) != 1 || selectedBody.Messages[0].ID != "topic-a-1" {
		t.Fatalf("unexpected selected transcript messages: %+v", selectedBody.Messages)
	}
}

func TestBuildTranscriptResponseSnapshotStartsFromLatestUser(t *testing.T) {
	t.Parallel()

	messages := []backend.SessionMessage{
		{ID: "msg-1", Role: "assistant", CreatedAt: 1, Parts: []backend.SessionMessagePart{{Type: "text", Text: "old-a"}}},
		{ID: "msg-2", Role: "assistant", CreatedAt: 2, Parts: []backend.SessionMessagePart{{Type: "text", Text: "old-b"}}},
		{ID: "msg-3", Role: "user", CreatedAt: 3, Parts: []backend.SessionMessagePart{{Type: "text", Text: "latest user"}}},
		{ID: "msg-4", Role: "assistant", CreatedAt: 4, Parts: []backend.SessionMessagePart{{Type: "text", Text: "after user"}}},
	}

	resp := buildTranscriptResponse("session-main", "", "", messages, nil)
	if !resp.Reset {
		t.Fatal("expected reset snapshot")
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("snapshot messages = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].ID != "msg-3" || resp.Messages[1].ID != "msg-4" {
		t.Fatalf("unexpected snapshot ids: %+v", resp.Messages)
	}
}

func TestBuildTranscriptResponseSnapshotCapsLatestUserSegmentToWindowSize(t *testing.T) {
	t.Parallel()

	messages := make([]backend.SessionMessage, 0, 56)
	messages = append(messages, backend.SessionMessage{ID: "msg-user", Role: "user", CreatedAt: 1, Parts: []backend.SessionMessagePart{{Type: "text", Text: "latest user"}}})
	for i := 1; i <= 55; i++ {
		messages = append(messages, backend.SessionMessage{
			ID:        fmt.Sprintf("msg-%02d", i),
			Role:      "assistant",
			CreatedAt: int64(i + 1),
			Parts:     []backend.SessionMessagePart{{Type: "text", Text: fmt.Sprintf("step %02d", i)}},
		})
	}

	resp := buildTranscriptResponse("session-main", "", "", messages, nil)
	if !resp.Reset {
		t.Fatal("expected reset snapshot")
	}
	if len(resp.Messages) != 50 {
		t.Fatalf("snapshot messages = %d, want 50", len(resp.Messages))
	}
	if resp.Messages[0].ID != "msg-06" || resp.Messages[len(resp.Messages)-1].ID != "msg-55" {
		t.Fatalf("unexpected capped snapshot range: first=%s last=%s", resp.Messages[0].ID, resp.Messages[len(resp.Messages)-1].ID)
	}
}

func TestBuildTranscriptResponseIncrementalIncludesUpdatedLatestMessage(t *testing.T) {
	t.Parallel()

	messages := []backend.SessionMessage{
		{ID: "msg-1", Role: "user", CreatedAt: 1, Parts: []backend.SessionMessagePart{{Type: "text", Text: "hello"}}},
		{ID: "msg-2", Role: "assistant", CreatedAt: 2, Parts: []backend.SessionMessagePart{{Type: "tool", Tool: "grep", ToolStatus: "running"}, {Type: "text", Text: "partial"}}},
	}

	resp := buildTranscriptResponse("session-main", "session-main", "msg-2", messages, nil)
	if resp.Reset {
		t.Fatal("expected incremental response")
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("incremental messages = %d, want 1", len(resp.Messages))
	}
	if resp.Messages[0].ID != "msg-2" {
		t.Fatalf("unexpected incremental id: %+v", resp.Messages[0])
	}
	if len(resp.Messages[0].Parts) != 2 || resp.Messages[0].Parts[0].ToolStatus != "running" {
		t.Fatalf("unexpected incremental parts: %+v", resp.Messages[0].Parts)
	}
}

func TestAdminSessionAgentsReturnsReadOnlyContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-a/agents", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Path         string `json:"path"`
		ResolvedPath string `json:"resolvedPath"`
		Content      string `json:"content"`
		Mode         string `json:"mode"`
		ReadOnly     bool   `json:"readOnly"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Path != "AGENTS.md" {
		t.Fatalf("path = %q", body.Path)
	}
	if !body.ReadOnly {
		t.Fatal("expected readonly agents payload")
	}
	if body.Mode != workspace.AgentsModeTemplate {
		t.Fatalf("mode = %q", body.Mode)
	}
	if body.Content != "default-agent\n" {
		t.Fatalf("content = %q", body.Content)
	}
	if body.ResolvedPath == "" {
		t.Fatal("expected resolved path")
	}
}

func TestAdminSessionAgentsCanSwitchToCustomAndPersistContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if err := sessions.Bind(ref, "opencode", "session-old", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/sessions/feishu/chat-a/agents", strings.NewReader(`{"mode":"custom","content":"# custom agents\n"}`))
	updateReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}
	var body struct {
		Mode     string `json:"mode"`
		Content  string `json:"content"`
		ReadOnly bool   `json:"readOnly"`
	}
	if err := json.Unmarshal(updateResp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if body.Mode != workspace.AgentsModeCustom {
		t.Fatalf("mode = %q", body.Mode)
	}
	if body.ReadOnly {
		t.Fatal("expected custom agents payload to be writable")
	}
	if body.Content != "# custom agents\n" {
		t.Fatalf("content = %q", body.Content)
	}

	updated, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if updated.Workspace.Settings.Agent.AgentsMode != workspace.AgentsModeCustom {
		t.Fatalf("agents mode = %q", updated.Workspace.Settings.Agent.AgentsMode)
	}
	if updated.ActiveSessionID != "" {
		t.Fatalf("active session = %q, want cleared after agents update", updated.ActiveSessionID)
	}
	agentsInfo, err := os.Lstat(filepath.Join(updated.Workspace.Path, workspace.AgentsFileName))
	if err != nil {
		t.Fatalf("stat custom agents: %v", err)
	}
	if agentsInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected custom AGENTS.md to be a regular file")
	}
	agentsData, err := os.ReadFile(filepath.Join(updated.Workspace.Path, workspace.AgentsFileName))
	if err != nil {
		t.Fatalf("read custom agents: %v", err)
	}
	if string(agentsData) != "# custom agents\n" {
		t.Fatalf("agents file content = %q", string(agentsData))
	}
}

func TestAdminSessionTokenIsScopedToOwnSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := gin.New()
	router = server.router()

	refA := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	refB := conversation.Ref{Provider: "feishu", ConversationID: "chat-b"}
	if _, err := sessions.Current(refA); err != nil {
		t.Fatalf("prepare session a: %v", err)
	}
	if _, err := sessions.Current(refB); err != nil {
		t.Fatalf("prepare session b: %v", err)
	}
	token, err := access.EnsureSessionToken(refA)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+token)
	meResp := httptest.NewRecorder()
	router.ServeHTTP(meResp, meReq)
	if meResp.Code != http.StatusOK {
		t.Fatalf("me status = %d, body=%s", meResp.Code, meResp.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listResp := httptest.NewRecorder()
	router.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusForbidden {
		t.Fatalf("list status = %d, body=%s", listResp.Code, listResp.Body.String())
	}

	forbiddenReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-b", nil)
	forbiddenReq.Header.Set("Authorization", "Bearer "+token)
	forbiddenResp := httptest.NewRecorder()
	router.ServeHTTP(forbiddenResp, forbiddenReq)
	if forbiddenResp.Code != http.StatusForbidden {
		t.Fatalf("detail status = %d, body=%s", forbiddenResp.Code, forbiddenResp.Body.String())
	}

	allowedReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-a", nil)
	allowedReq.Header.Set("Authorization", "Bearer "+token)
	allowedResp := httptest.NewRecorder()
	router.ServeHTTP(allowedResp, allowedReq)
	if allowedResp.Code != http.StatusOK {
		t.Fatalf("allowed detail status = %d, body=%s", allowedResp.Code, allowedResp.Body.String())
	}
}

func TestAdminProjectTokenManagesRoles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles", nil)
	listReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	listResp := httptest.NewRecorder()
	router.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list roles status = %d, body=%s", listResp.Code, listResp.Body.String())
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/roles", strings.NewReader(`{"name":"ops","copyFrom":"default"}`))
	createReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	router.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create role status = %d, body=%s", createResp.Code, createResp.Body.String())
	}
	var created struct {
		ID         string             `json:"id"`
		Settings   workspace.Settings `json:"settings"`
		AgentsFile struct {
			Content string `json:"content"`
		} `json:"agentsFile"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create role: %v", err)
	}
	if created.ID != "ops" {
		t.Fatalf("role id = %q", created.ID)
	}
	if created.Settings.Template != "ops" {
		t.Fatalf("role template = %q", created.Settings.Template)
	}
	if created.AgentsFile.Content != "default-agent\n" {
		t.Fatalf("created agents content = %q", created.AgentsFile.Content)
	}

	updatedSettings := created.Settings
	updatedSettings.Settings.ReplyMode = "thread"
	updatedSettings.Settings.HistoryTTLHours = 72
	updatedSettings.Mounts.SkillIDs = []string{"skill-a", "skill-b"}
	updateBody, err := json.Marshal(struct {
		Settings      workspace.Settings `json:"settings"`
		AgentsContent string             `json:"agentsContent"`
	}{
		Settings:      updatedSettings,
		AgentsContent: "# ops role\n",
	})
	if err != nil {
		t.Fatalf("marshal update body: %v", err)
	}
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/roles/ops", strings.NewReader(string(updateBody)))
	updateReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update role status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}
	var updated struct {
		Settings   workspace.Settings `json:"settings"`
		AgentsFile struct {
			Content string `json:"content"`
		} `json:"agentsFile"`
	}
	if err := json.Unmarshal(updateResp.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update role: %v", err)
	}
	if updated.Settings.Template != "ops" {
		t.Fatalf("updated template = %q", updated.Settings.Template)
	}
	if updated.Settings.Settings.ReplyMode != "thread" {
		t.Fatalf("updated reply mode = %q", updated.Settings.Settings.ReplyMode)
	}
	if updated.Settings.Settings.HistoryTTLHours != 72 {
		t.Fatalf("updated ttl = %d", updated.Settings.Settings.HistoryTTLHours)
	}
	if len(updated.Settings.Mounts.SkillIDs) != 2 {
		t.Fatalf("updated skills = %+v", updated.Settings.Mounts.SkillIDs)
	}
	if updated.AgentsFile.Content != "# ops role\n" {
		t.Fatalf("updated agents content = %q", updated.AgentsFile.Content)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles/ops", nil)
	getReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	getResp := httptest.NewRecorder()
	router.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get role status = %d, body=%s", getResp.Code, getResp.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/roles/ops", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	deleteResp := httptest.NewRecorder()
	router.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete role status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles/ops", nil)
	missingReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	missingResp := httptest.NewRecorder()
	router.ServeHTTP(missingResp, missingReq)
	if missingResp.Code != http.StatusNotFound {
		t.Fatalf("missing role status = %d, body=%s", missingResp.Code, missingResp.Body.String())
	}
}

func TestAdminRoleDeleteProtectsDefaultAndInUseTemplates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	if _, err := sessions.CreateTemplate("ops", "default"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if _, err := sessions.SwitchTemplate(ref, "ops"); err != nil {
		t.Fatalf("switch template: %v", err)
	}

	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	defaultReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/roles/default", nil)
	defaultReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	defaultResp := httptest.NewRecorder()
	router.ServeHTTP(defaultResp, defaultReq)
	if defaultResp.Code != http.StatusConflict {
		t.Fatalf("delete default status = %d, body=%s", defaultResp.Code, defaultResp.Body.String())
	}

	inUseReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/roles/ops", nil)
	inUseReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	inUseResp := httptest.NewRecorder()
	router.ServeHTTP(inUseResp, inUseReq)
	if inUseResp.Code != http.StatusConflict {
		t.Fatalf("delete in-use role status = %d, body=%s", inUseResp.Code, inUseResp.Body.String())
	}
}

func TestAdminSessionTokenCannotManageRoles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("session token roles status = %d, body=%s", resp.Code, resp.Body.String())
	}
}

func TestAdminProjectTokenManagesSkills(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)
	writeSkill(t, root, "skill-a", "Skill A", map[string]string{
		"SKILL.md":         "# Skill A\n\nfirst\n",
		"refs/guide.md":    "guide\n",
		"bin/script.sh":    "#!/usr/bin/env bash\n",
		"config/test.yaml": "name: test\n",
	})

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/skills/skill-a", nil)
	detailReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	detailResp := httptest.NewRecorder()
	router.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("skill detail status = %d, body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detail struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		ReadOnly bool   `json:"readOnly"`
	}
	if err := json.Unmarshal(detailResp.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode skill detail: %v", err)
	}
	if detail.ID != "skill-a" || detail.Title != "Skill A" || detail.ReadOnly {
		t.Fatalf("unexpected skill detail: %+v", detail)
	}

	filesReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/skills/skill-a/files", nil)
	filesReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	filesResp := httptest.NewRecorder()
	router.ServeHTTP(filesResp, filesReq)
	if filesResp.Code != http.StatusOK {
		t.Fatalf("skill files status = %d, body=%s", filesResp.Code, filesResp.Body.String())
	}
	var filesBody struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	if err := json.Unmarshal(filesResp.Body.Bytes(), &filesBody); err != nil {
		t.Fatalf("decode skill files: %v", err)
	}
	if len(filesBody.Items) != 4 {
		t.Fatalf("skill file count = %d, want 4", len(filesBody.Items))
	}

	contentReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/skills/skill-a/files/content?path=SKILL.md", nil)
	contentReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	contentResp := httptest.NewRecorder()
	router.ServeHTTP(contentResp, contentReq)
	if contentResp.Code != http.StatusOK {
		t.Fatalf("skill content status = %d, body=%s", contentResp.Code, contentResp.Body.String())
	}
	var contentBody struct {
		Content  string `json:"content"`
		ReadOnly bool   `json:"readOnly"`
	}
	if err := json.Unmarshal(contentResp.Body.Bytes(), &contentBody); err != nil {
		t.Fatalf("decode skill content: %v", err)
	}
	if contentBody.Content != "# Skill A\n\nfirst\n" || contentBody.ReadOnly {
		t.Fatalf("unexpected skill content payload: %+v", contentBody)
	}

	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/skills/skill-a/files/content", strings.NewReader(`{"path":"SKILL.md","content":"# Skill A\n\nupdated\n"}`))
	updateReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("skill update status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}
	updatedData, err := os.ReadFile(filepath.Join(root, "agents", "skills", "skill-a", "SKILL.md"))
	if err != nil {
		t.Fatalf("read updated skill file: %v", err)
	}
	if string(updatedData) != "# Skill A\n\nupdated\n" {
		t.Fatalf("updated skill file = %q", string(updatedData))
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/skills/skill-a", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	deleteResp := httptest.NewRecorder()
	router.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("skill delete status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "skills", "skill-a")); !os.IsNotExist(err) {
		t.Fatalf("expected skill directory removed, err=%v", err)
	}
}

func TestAdminProjectTokenCanCreateSkill(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/skills", strings.NewReader(`{"id":"skill-new","content":"# Skill New\n"}`))
	createReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	router.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create skill status = %d, body=%s", createResp.Code, createResp.Body.String())
	}

	createdData, err := os.ReadFile(filepath.Join(root, "agents", "skills", "skill-new", "SKILL.md"))
	if err != nil {
		t.Fatalf("read created skill file: %v", err)
	}
	if string(createdData) != "# Skill New\n" {
		t.Fatalf("created skill file = %q", string(createdData))
	}

	defaultReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/skills", strings.NewReader(`{"id":"skill-default","content":""}`))
	defaultReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	defaultReq.Header.Set("Content-Type", "application/json")
	defaultResp := httptest.NewRecorder()
	router.ServeHTTP(defaultResp, defaultReq)
	if defaultResp.Code != http.StatusOK {
		t.Fatalf("create default skill status = %d, body=%s", defaultResp.Code, defaultResp.Body.String())
	}
	defaultData, err := os.ReadFile(filepath.Join(root, "agents", "skills", "skill-default", "SKILL.md"))
	if err != nil {
		t.Fatalf("read default skill file: %v", err)
	}
	defaultContent := string(defaultData)
	if !strings.Contains(defaultContent, "name: skill-default") || !strings.Contains(defaultContent, "description:") {
		t.Fatalf("default skill file missing frontmatter: %q", defaultContent)
	}
}

func TestAdminSkillDeleteProtectsReferencedSkills(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)
	opsTemplateDir := filepath.Join(root, "templates", "ops")
	if err := os.MkdirAll(filepath.Join(opsTemplateDir, ".agents", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir ops template dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opsTemplateDir, "AGENTS.md"), []byte("ops-agent\n"), 0o644); err != nil {
		t.Fatalf("write ops agents: %v", err)
	}
	opsSettings := workspace.DefaultSettings()
	opsSettings.Template = "ops"
	if err := workspace.SaveSettings(opsTemplateDir, opsSettings); err != nil {
		t.Fatalf("write ops settings: %v", err)
	}
	writeSkill(t, root, "skill-a", "Skill A", map[string]string{"SKILL.md": "# Skill A\n"})

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	opsSettings, err = workspace.LoadSettings(filepath.Join(root, "templates", "ops"))
	if err != nil {
		t.Fatalf("load ops settings: %v", err)
	}
	opsSettings.Mounts.SkillIDs = []string{"skill-a"}
	if err := workspace.SaveSettings(filepath.Join(root, "templates", "ops"), opsSettings); err != nil {
		t.Fatalf("save ops settings: %v", err)
	}

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	settings := current.Workspace.Settings
	settings.Mounts.SkillIDs = []string{"skill-a"}
	if err := workspace.SaveSettings(current.Workspace.Path, settings); err != nil {
		t.Fatalf("save workspace settings: %v", err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/skills/skill-a", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	deleteResp := httptest.NewRecorder()
	router.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusConflict {
		t.Fatalf("skill delete status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	if !strings.Contains(deleteResp.Body.String(), "sessions=[feishu:chat-a]") {
		t.Fatalf("missing session usage in error: %s", deleteResp.Body.String())
	}
	if !strings.Contains(deleteResp.Body.String(), "templates=[ops]") {
		t.Fatalf("missing template usage in error: %s", deleteResp.Body.String())
	}
}

func TestAdminSessionTokenCanViewButNotMutateSkills(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)
	writeSkill(t, root, "skill-a", "Skill A", map[string]string{"SKILL.md": "# Skill A\n"})

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/skills", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listResp := httptest.NewRecorder()
	router.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("session skill list status = %d, body=%s", listResp.Code, listResp.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/skills/skill-a", nil)
	detailReq.Header.Set("Authorization", "Bearer "+token)
	detailResp := httptest.NewRecorder()
	router.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("session skill detail status = %d, body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detail struct {
		ReadOnly bool `json:"readOnly"`
	}
	if err := json.Unmarshal(detailResp.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode session skill detail: %v", err)
	}
	if !detail.ReadOnly {
		t.Fatal("expected session token skill detail to be readonly")
	}

	contentReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/skills/skill-a/files/content?path=SKILL.md", nil)
	contentReq.Header.Set("Authorization", "Bearer "+token)
	contentResp := httptest.NewRecorder()
	router.ServeHTTP(contentResp, contentReq)
	if contentResp.Code != http.StatusOK {
		t.Fatalf("session skill content status = %d, body=%s", contentResp.Code, contentResp.Body.String())
	}

	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/skills/skill-a/files/content", strings.NewReader(`{"path":"SKILL.md","content":"# changed\n"}`))
	updateReq.Header.Set("Authorization", "Bearer "+token)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusForbidden {
		t.Fatalf("session skill update status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/skills", strings.NewReader(`{"id":"skill-new","content":"# Skill New\n"}`))
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	router.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusForbidden {
		t.Fatalf("session skill create status = %d, body=%s", createResp.Code, createResp.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/skills/skill-a", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleteResp := httptest.NewRecorder()
	router.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusForbidden {
		t.Fatalf("session skill delete status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}
}

func TestAdminTokensCanListSubagents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)
	writeSubagentFile(t, root, "code-reviewer", "---\nname: Reviewer\ndescription: review code\nmode: subagent\n---\n\n# Reviewer\n")
	writeSubagentFile(t, root, "code-writer", "# Writer\n")

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	projectReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/subagents", nil)
	projectReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	projectResp := httptest.NewRecorder()
	router.ServeHTTP(projectResp, projectReq)
	if projectResp.Code != http.StatusOK {
		t.Fatalf("project subagent list status = %d, body=%s", projectResp.Code, projectResp.Body.String())
	}
	var body struct {
		Items []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Mode        string `json:"mode"`
		} `json:"items"`
	}
	if err := json.Unmarshal(projectResp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode subagent list: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("subagent count = %d, want 2", len(body.Items))
	}
	if body.Items[0].ID != "code-reviewer" || body.Items[0].Title != "Reviewer" || body.Items[0].Mode != "subagent" {
		t.Fatalf("unexpected first subagent: %+v", body.Items[0])
	}

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}
	sessionReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/subagents", nil)
	sessionReq.Header.Set("Authorization", "Bearer "+token)
	sessionResp := httptest.NewRecorder()
	router.ServeHTTP(sessionResp, sessionReq)
	if sessionResp.Code != http.StatusOK {
		t.Fatalf("session subagent list status = %d, body=%s", sessionResp.Code, sessionResp.Body.String())
	}
}

func TestAdminProjectTokenManagesSubagents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)
	writeSubagentFile(t, root, "code-reviewer", "---\nname: Reviewer\ndescription: review code\nmode: subagent\n---\n\n# Reviewer\n")

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/subagents", strings.NewReader(`{"id":"code-writer","content":"---\nname: Writer\ndescription: write code\nmode: subagent\n---\n\n# Writer\n"}`))
	createReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	router.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create subagent status = %d, body=%s", createResp.Code, createResp.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/subagents/code-writer", nil)
	detailReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	detailResp := httptest.NewRecorder()
	router.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("subagent detail status = %d, body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detail struct {
		ID       string `json:"id"`
		ReadOnly bool   `json:"readOnly"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(detailResp.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode subagent detail: %v", err)
	}
	if detail.ID != "code-writer" || detail.ReadOnly || !strings.Contains(detail.Content, "# Writer") {
		t.Fatalf("unexpected subagent detail: %+v", detail)
	}

	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/subagents/code-writer", strings.NewReader(`{"content":"---\nname: Writer\ndescription: updated\nmode: subagent\n---\n\n# Writer\n"}`))
	updateReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update subagent status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}
	updatedData, err := os.ReadFile(filepath.Join(root, "agents", "subagents", "code-writer.md"))
	if err != nil {
		t.Fatalf("read updated subagent: %v", err)
	}
	if !strings.Contains(string(updatedData), "description: updated") {
		t.Fatalf("unexpected updated subagent content: %q", string(updatedData))
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/subagents/code-writer", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	deleteResp := httptest.NewRecorder()
	router.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete subagent status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "subagents", "code-writer.md")); !os.IsNotExist(err) {
		t.Fatalf("expected subagent file removed, err=%v", err)
	}
}

func TestAdminProjectTokenManagesLegacySubagents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)
	writeLegacySubagentFile(t, root, "code-reviewer", "---\nname: Reviewer\ndescription: legacy\nmode: subagent\n---\n\n# Reviewer\n")

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/subagents", nil)
	listReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	listResp := httptest.NewRecorder()
	router.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("legacy subagent list status = %d, body=%s", listResp.Code, listResp.Body.String())
	}
	var listBody struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode legacy subagent list: %v", err)
	}
	if len(listBody.Items) != 1 || listBody.Items[0].ID != "code-reviewer" {
		t.Fatalf("unexpected legacy subagent items: %+v", listBody.Items)
	}

	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/subagents/code-reviewer", strings.NewReader(`{"content":"---\nname: Reviewer\ndescription: updated legacy\nmode: subagent\n---\n\n# Reviewer\n"}`))
	updateReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("legacy subagent update status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}
	legacyPath := filepath.Join(root, "subagents", "code-reviewer.md")
	legacyData, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy subagent: %v", err)
	}
	if !strings.Contains(string(legacyData), "description: updated legacy") {
		t.Fatalf("unexpected legacy subagent content: %q", string(legacyData))
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/subagents/code-reviewer", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	deleteResp := httptest.NewRecorder()
	router.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("legacy subagent delete status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy subagent file removed, err=%v", err)
	}
}

func TestAdminSessionTokenCanViewButNotMutateSubagents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)
	writeSubagentFile(t, root, "code-reviewer", "---\nname: Reviewer\ndescription: review code\nmode: subagent\n---\n\n# Reviewer\n")

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/subagents/code-reviewer", nil)
	detailReq.Header.Set("Authorization", "Bearer "+token)
	detailResp := httptest.NewRecorder()
	router.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("session subagent detail status = %d, body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detail struct {
		ReadOnly bool `json:"readOnly"`
	}
	if err := json.Unmarshal(detailResp.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode session subagent detail: %v", err)
	}
	if !detail.ReadOnly {
		t.Fatal("expected session token subagent detail to be readonly")
	}

	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/subagents/code-reviewer", strings.NewReader(`{"content":"# changed\n"}`))
	updateReq.Header.Set("Authorization", "Bearer "+token)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusForbidden {
		t.Fatalf("session subagent update status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/subagents/code-reviewer", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleteResp := httptest.NewRecorder()
	router.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusForbidden {
		t.Fatalf("session subagent delete status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}
}

func containsJSONField(data []byte, field string) bool {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	_, ok := payload[field]
	return ok
}

func writeSkill(t *testing.T, root, skillID, _ string, files map[string]string) {
	t.Helper()

	skillDir := filepath.Join(root, "agents", "skills", skillID)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	for path, content := range files {
		resolved := filepath.Join(skillDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			t.Fatalf("mkdir skill file dir: %v", err)
		}
		if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
			t.Fatalf("write skill file %s: %v", path, err)
		}
	}
}

func writeSubagentFile(t *testing.T, root, subagentID, content string) {
	t.Helper()

	subagentDir := filepath.Join(root, "agents", "subagents")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatalf("mkdir subagent dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subagentDir, subagentID+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write subagent file: %v", err)
	}
}

func writeLegacySubagentFile(t *testing.T, root, subagentID, content string) {
	t.Helper()

	legacyDir := filepath.Join(root, "subagents")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy subagent dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, subagentID+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write legacy subagent file: %v", err)
	}
}

func TestAdminListsRepos(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"

	gitDir := filepath.Join(cfg.RepoRootDir, "service-a", ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir repo git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.RepoRootDir, "plain-dir"), 0o755); err != nil {
		t.Fatalf("mkdir plain repo: %v", err)
	}

	// symlinked repo: agents/repos/<id> -> external real git checkout must be
	// listed (os.Stat follows the symlink), not skipped like a plain symlink.
	externalRepo := filepath.Join(root, "external", "service-link")
	externalGit := filepath.Join(externalRepo, ".git")
	if err := os.MkdirAll(externalGit, 0o755); err != nil {
		t.Fatalf("mkdir external git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalGit, "HEAD"), []byte("ref: refs/heads/linkbranch\n"), 0o644); err != nil {
		t.Fatalf("write external HEAD: %v", err)
	}
	if err := os.Symlink(externalRepo, filepath.Join(cfg.RepoRootDir, "service-link")); err != nil {
		t.Fatalf("symlink repo: %v", err)
	}
	// dangling symlink (target missing) must be skipped, not error.
	if err := os.Symlink(filepath.Join(root, "nope"), filepath.Join(cfg.RepoRootDir, "dangling")); err != nil {
		t.Fatalf("symlink dangling: %v", err)
	}

	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/repos", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("repo list status = %d, body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Items []struct {
			ID     string `json:"id"`
			Branch string `json:"branch"`
			HasGit bool   `json:"hasGit"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode repo list: %v", err)
	}
	if len(body.Items) != 3 {
		t.Fatalf("repo count = %d, want 3: %+v", len(body.Items), body.Items)
	}
	byID := map[string]struct {
		Branch string
		HasGit bool
	}{}
	for _, item := range body.Items {
		byID[item.ID] = struct {
			Branch string
			HasGit bool
		}{item.Branch, item.HasGit}
	}
	if got := byID["service-a"]; !got.HasGit || got.Branch != "main" {
		t.Fatalf("service-a = %+v, want hasGit=true branch=main", got)
	}
	if got := byID["plain-dir"]; got.HasGit || got.Branch != "" {
		t.Fatalf("plain-dir = %+v, want hasGit=false branch empty", got)
	}
	if got := byID["service-link"]; !got.HasGit || got.Branch != "linkbranch" {
		t.Fatalf("service-link = %+v, want hasGit=true branch=linkbranch", got)
	}
	if _, ok := byID["dangling"]; ok {
		t.Fatalf("dangling symlink should be skipped: %+v", body.Items)
	}
}

func TestValidateRepoCloneURL(t *testing.T) {
	t.Parallel()

	ok := []string{
		"https://github.com/org/repo.git",
		"http://internal.mirror/org/repo.git",
		"ssh://git@host/org/repo.git",
		"git://host/org/repo.git",
		"git@github.com:org/repo.git",
	}
	for _, url := range ok {
		if err := validateRepoCloneURL(url); err != nil {
			t.Fatalf("validateRepoCloneURL(%q) = %v, want nil", url, err)
		}
	}

	bad := []string{
		"",
		"-x",
		"--upload-pack=touch /tmp/x",
		"ext::sh -c whoami",
		"ext::sh -c 'rm -rf /'",
		"file:///etc/passwd",
		"fd::17/foo",
		"git clone ; rm -rf /",
		"https://host/repo\n--upload-pack=evil",
	}
	for _, url := range bad {
		if err := validateRepoCloneURL(url); err == nil {
			t.Fatalf("validateRepoCloneURL(%q) = nil, want error", url)
		}
	}
}

func TestInferRepoIDFromURL(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"https://github.com/org/repo.git": "repo",
		"git@github.com:org/repo.git":     "repo",
		"https://host/a/b/":               "b",
		"ssh://git@host/org/service-a":    "service-a",
	}
	for url, want := range cases {
		if got := inferRepoIDFromURL(url); got != want {
			t.Fatalf("inferRepoIDFromURL(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestAdminRepoCloneRejectsSessionToken(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/repos", strings.NewReader(`{"url":"https://github.com/org/repo.git"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("session token clone status = %d, body=%s", resp.Code, resp.Body.String())
	}
}

func TestAdminRepoCloneRejectsDangerousURL(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	for _, payload := range []string{
		`{"url":"ext::sh -c whoami"}`,
		`{"url":"file:///etc/passwd"}`,
		`{"url":"https://host/repo.git","id":"../escape"}`,
		`{"url":"https://host/repo.git","id":"AGENTS.md"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/repos", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("clone payload %s status = %d, want 400, body=%s", payload, resp.Code, resp.Body.String())
		}
	}
}

func TestCloneRepoUsesShallowCloneFlags(t *testing.T) {
	repoRoot := t.TempDir()
	fakeBin := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "git-args.txt")

	script := filepath.Join(fakeBin, "git")
	scriptContent := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$TEST_GIT_ARGS_FILE\"\n" +
		"for last do target=$last; done\n" +
		"mkdir -p \"$target/.git\"\n" +
		"printf 'ref: refs/heads/main\\n' > \"$target/.git/HEAD\"\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_GIT_ARGS_FILE", argsPath)

	branch, err := cloneRepo(context.Background(), repoRoot, "svc", "https://github.com/org/repo.git")
	if err != nil {
		t.Fatalf("cloneRepo: %v", err)
	}
	if branch != "main" {
		t.Fatalf("branch=%q want main", branch)
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := strings.Fields(string(argsData))
	if len(args) != 6 {
		t.Fatalf("git args len=%d want 6 args=%v", len(args), args)
	}
	if args[0] != "clone" || args[1] != "--depth=1" || args[2] != "--no-single-branch" || args[3] != "--" || args[4] != "https://github.com/org/repo.git" {
		t.Fatalf("unexpected git args: %v", args)
	}
	if !strings.HasPrefix(args[5], filepath.Join(repoRoot, ".repo-clone-")) {
		t.Fatalf("clone target=%q want temp dir under %q", args[5], repoRoot)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "svc", ".git", "HEAD")); err != nil {
		t.Fatalf("cloned repo missing HEAD: %v", err)
	}
}

func TestValidateGitBranchName(t *testing.T) {
	t.Parallel()

	ok := []string{"main", "master", "feature/foo", "release-1.2", "a_b.c", "dev/x+y", "i18n_main"}
	for _, b := range ok {
		if err := validateGitBranchName(b); err != nil {
			t.Fatalf("validateGitBranchName(%q) unexpected err: %v", b, err)
		}
	}
	bad := []string{"", "-x", "--force", "/lead", "trail/", "a..b", "a//b", "ref@{0}", "x.lock", "has space", "ctrl\x01", "semi;rm -rf", "tilde~1", "co:lon"}
	for _, b := range bad {
		if err := validateGitBranchName(b); err == nil {
			t.Fatalf("validateGitBranchName(%q) expected error", b)
		}
	}
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestAdminRepoBranchesAndCheckout(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"

	repoDir := filepath.Join(cfg.RepoRootDir, "svc")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGitForTest(t, repoDir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitForTest(t, repoDir, "add", "-A")
	runGitForTest(t, repoDir, "commit", "-q", "-m", "init")
	runGitForTest(t, repoDir, "branch", "feature/x")

	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	has := func(list []string, want string) bool {
		for _, b := range list {
			if b == want {
				return true
			}
		}
		return false
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/repos/svc/branches", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("branches status=%d body=%s", resp.Code, resp.Body.String())
	}
	var bl struct {
		Current  string   `json:"current"`
		Branches []string `json:"branches"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &bl); err != nil {
		t.Fatalf("decode branches: %v", err)
	}
	if bl.Current != "main" {
		t.Fatalf("current=%q want main", bl.Current)
	}
	if !has(bl.Branches, "main") || !has(bl.Branches, "feature/x") {
		t.Fatalf("branches=%v want main + feature/x", bl.Branches)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/repos/svc/checkout", strings.NewReader(`{"branch":"feature/x"}`))
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("checkout status=%d body=%s", resp.Code, resp.Body.String())
	}
	var co struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &co); err != nil {
		t.Fatalf("decode checkout: %v", err)
	}
	if co.Branch != "feature/x" {
		t.Fatalf("checked-out branch=%q want feature/x", co.Branch)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/repos/svc/checkout", strings.NewReader(`{"branch":"-evil"}`))
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("evil branch status=%d, want 400 body=%s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/repos/ghost/branches", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("unknown repo status=%d, want 404", resp.Code)
	}
}

func TestAdminRepoMutationsRejectSessionToken(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-repo"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	// Scope check runs before repo lookup, so a session token is rejected with
	// 403 regardless of whether the repo exists.
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/v1/admin/repos/svc/pull", ""},
		{http.MethodPost, "/api/v1/admin/repos/svc/checkout", `{"branch":"main"}`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("%s %s with session token status=%d, want 403 body=%s", tc.method, tc.path, resp.Code, resp.Body.String())
		}
	}
}
