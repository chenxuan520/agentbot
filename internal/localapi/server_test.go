package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/flow"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	"github.com/chenxuan520/agentbot/internal/session"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
	"github.com/chenxuan520/agentbot/internal/workspace"
	"github.com/gin-gonic/gin"
)

func TestHandleProviderChatMembers(t *testing.T) {
	t.Parallel()

	server := &Server{cfg: config.Config{ServerHost: "127.0.0.1", ServerPort: 8080}}
	server.providers = func(config.Config, string) (providerapi.Client, error) {
		return fakeProviderClient{}, nil
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.GET("/api/v1/provider/chat-members", server.handleProviderChatMembers)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/provider/chat-members?provider=feishu&conversationId=oc_test", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	var body struct {
		Provider       string                   `json:"provider"`
		ConversationID string                   `json:"conversationId"`
		Items          []providerapi.ChatMember `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Provider != "feishu" {
		t.Fatalf("provider = %q", body.Provider)
	}
	if body.ConversationID != "oc_test" {
		t.Fatalf("conversationId = %q", body.ConversationID)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items count = %d, want 1", len(body.Items))
	}
	if body.Items[0].MemberID != "ou-duty" || body.Items[0].Name != "李佳奇" {
		t.Fatalf("unexpected item: %+v", body.Items[0])
	}
}

func TestHandleProviderChatMessages(t *testing.T) {
	t.Parallel()

	server := &Server{cfg: config.Config{ServerHost: "127.0.0.1", ServerPort: 8080}}
	server.providers = func(config.Config, string) (providerapi.Client, error) {
		return fakeProviderClient{}, nil
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.GET("/api/v1/provider/chat-messages", server.handleProviderChatMessages)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/provider/chat-messages?provider=feishu&conversationId=oc_test&pageSize=5&cardMsgContentType=user_card_content", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte(`"messageId":"om-card"`)) {
		t.Fatalf("unexpected response: %s", resp.Body.String())
	}
}

func TestHandleProviderMockMessage(t *testing.T) {
	t.Parallel()

	server := &Server{cfg: config.Config{ServerHost: "127.0.0.1", ServerPort: 8080}}
	server.process = func(_ context.Context, input flow.TextInput) (flow.PromptResult, error) {
		if input.MessageID != "om-card" {
			t.Fatalf("message id = %q", input.MessageID)
		}
		if input.MessageType != "interactive" {
			t.Fatalf("message type = %q", input.MessageType)
		}
		return flow.PromptResult{ReplyText: "processed"}, nil
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.POST("/api/v1/provider/mock-message", server.handleProviderMockMessage)

	body := []byte(`{"provider":"feishu","conversationId":"oc_test","conversationType":"group","messageType":"interactive","messageId":"om-card","senderType":"app","senderId":"cli_x","text":"{\"schema\":\"2.0\"}","async":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider/mock-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte(`"ReplyText":"processed"`)) {
		t.Fatalf("unexpected response: %s", resp.Body.String())
	}
}

func TestHandleProviderRecallMessage(t *testing.T) {
	t.Parallel()

	server := &Server{cfg: config.Config{ServerHost: "127.0.0.1", ServerPort: 8080}}
	server.providers = func(config.Config, string) (providerapi.Client, error) {
		return fakeProviderClient{}, nil
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.POST("/api/v1/provider/recall-message", server.handleProviderRecallMessage)

	body := []byte(`{"provider":"feishu","conversationId":"oc_test","messageId":"om_test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provider/recall-message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte(`"messageId":"om_test"`)) {
		t.Fatalf("unexpected response: %s", resp.Body.String())
	}
}

func TestSessionSettingsEndpoints(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	server := New(cfg, nil, nil, sessions, nil)
	router := server.router()
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-1"}

	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if err := sessions.Bind(ref, "opencode", "session-old", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/session/settings?provider=feishu&conversationId=chat-1", nil)
	getResp := httptest.NewRecorder()
	router.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getResp.Code, getResp.Body.String())
	}
	if !bytes.Contains(getResp.Body.Bytes(), []byte(`"conversationId":"chat-1"`)) {
		t.Fatalf("unexpected get body: %s", getResp.Body.String())
	}

	settings := current.Workspace.Settings
	settings.Settings.ReplyMode = "topic"
	settings.Settings.AcceptInteractiveCardMessages = true
	settings.Agent.OpencodeConfig = map[string]any{"model": "gpt-5"}
	settings.Agent.OpencodeHTTPTimeoutSeconds = 900
	body := map[string]any{
		"provider":       ref.Provider,
		"conversationId": ref.ConversationID,
		"settings":       settings,
		"rebuild":        true,
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/session/settings", bytes.NewReader(data))
	postReq.Header.Set("Content-Type", "application/json")
	postResp := httptest.NewRecorder()
	router.ServeHTTP(postResp, postReq)
	if postResp.Code != http.StatusOK {
		t.Fatalf("post status = %d, body = %s", postResp.Code, postResp.Body.String())
	}
	if !bytes.Contains(postResp.Body.Bytes(), []byte(`"rebuilt":true`)) {
		t.Fatalf("unexpected post body: %s", postResp.Body.String())
	}

	updated, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("current updated session: %v", err)
	}
	if updated.Workspace.Settings.Settings.ReplyMode != "topic" {
		t.Fatalf("reply mode = %q", updated.Workspace.Settings.Settings.ReplyMode)
	}
	if !updated.Workspace.Settings.Settings.AcceptInteractiveCardMessages {
		t.Fatal("expected interactive cards to be enabled")
	}
	if updated.Workspace.Settings.Agent.OpencodeConfig["model"] != "gpt-5" {
		t.Fatalf("unexpected opencode config: %+v", updated.Workspace.Settings.Agent.OpencodeConfig)
	}
	if updated.Workspace.Settings.Agent.OpencodeHTTPTimeoutSeconds != 900 {
		t.Fatalf("timeout seconds = %d, want 900", updated.Workspace.Settings.Agent.OpencodeHTTPTimeoutSeconds)
	}
	if updated.ActiveSessionID != "" {
		t.Fatalf("active session = %q, want cleared after rebuild", updated.ActiveSessionID)
	}
}

type fakeProviderClient struct{}

func (fakeProviderClient) Name() string                   { return "fake" }
func (fakeProviderClient) Health(_ context.Context) error { return nil }
func (fakeProviderClient) AddHandlingReaction(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (fakeProviderClient) AddRemoteHandlingReaction(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (fakeProviderClient) AddBlockedReaction(_ context.Context, _ string) error { return nil }
func (fakeProviderClient) AddReaction(_ context.Context, _, _ string) error     { return nil }
func (fakeProviderClient) DeleteReaction(_ context.Context, _, _ string) error  { return nil }
func (fakeProviderClient) RecallMessage(_ context.Context, messageID string) error {
	if messageID != "om_test" {
		return nil
	}
	return nil
}
func (fakeProviderClient) SendTextToChat(_ context.Context, _, _, _ string) error { return nil }
func (fakeProviderClient) ReplyTextToMessage(_ context.Context, _, _, _ string, _ providerapi.ReplyOptions) error {
	return nil
}
func (fakeProviderClient) ListChatMembers(_ context.Context, chatID string) ([]providerapi.ChatMember, error) {
	if chatID != "oc_test" {
		return nil, nil
	}
	return []providerapi.ChatMember{{MemberID: "ou-duty", MemberIDType: "open_id", Name: "李佳奇"}}, nil
}

func (fakeProviderClient) ListChatMessages(_ context.Context, chatID string, _ providerapi.ChatMessageListOptions) (providerapi.ChatMessageListResult, error) {
	if chatID != "oc_test" {
		return providerapi.ChatMessageListResult{}, nil
	}
	return providerapi.ChatMessageListResult{
		Items: []providerapi.ChatMessage{{
			MessageID:  "om-card",
			MsgType:    "interactive",
			CreateTime: "1778585404827",
			UpdateTime: "1778585457118",
			ChatID:     chatID,
			Sender: providerapi.ChatMessageSender{
				ID:         "cli_card",
				IDType:     "app_id",
				SenderType: "app",
			},
			Body: providerapi.ChatMessageBody{Content: `{"schema":"2.0"}`},
		}},
	}, nil
}

func writeTemplate(t *testing.T, root string) {
	t.Helper()

	templateDir := filepath.Join(root, "templates", "default")
	if err := os.MkdirAll(filepath.Join(templateDir, ".agents", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir template dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "AGENTS.md"), []byte("default-agent\n"), 0o644); err != nil {
		t.Fatalf("write agents file: %v", err)
	}
	settings := workspace.DefaultSettings()
	if err := workspace.SaveSettings(templateDir, settings); err != nil {
		t.Fatalf("write settings file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, ".agents", "hooks", "before_message.py"), []byte("pass\n"), 0o644); err != nil {
		t.Fatalf("write hook file: %v", err)
	}
}
