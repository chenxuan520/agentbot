package localapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/remoteagent"
	"github.com/chenxuan520/agentbot/internal/session"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

type fakeRemoteServer struct {
	status remoteagent.Status
}

func (f fakeRemoteServer) Serve(http.ResponseWriter, *http.Request, conversation.Ref) error {
	return nil
}

func (f fakeRemoteServer) Status(conversation.Ref) remoteagent.Status {
	return f.status
}

func remoteStatusTestServer(t *testing.T, remote RemoteAgentServer) (*session.Service, *Server, config.Config) {
	t.Helper()
	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	if remote != nil {
		server.SetRemoteAgentServer(remote)
	}
	return sessions, server, cfg
}

func enableWorkspaceRemote(t *testing.T, sessions *session.Service, ref conversation.Ref) {
	t.Helper()
	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	settings, err := workspace.LoadSettings(current.Workspace.Path)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	settings.Settings.RemoteEnabled = true
	if err := workspace.SaveSettings(current.Workspace.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}

func getRemoteStatus(t *testing.T, server *Server, cfg config.Config, ref conversation.Ref) map[string]any {
	t.Helper()
	router := server.router()
	url := "/api/v1/admin/sessions/" + ref.Provider + "/" + ref.ConversationID + "/remote-status"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestAdminRemoteStatusConnectedLocal(t *testing.T) {
	t.Parallel()

	sessions, server, cfg := remoteStatusTestServer(t, fakeRemoteServer{status: remoteagent.Status{
		Connected:  true,
		RouteLocal: true,
		AgentID:    "macbook",
		SessionID:  "local-1",
		Title:      "repo: agent-bot",
	}})
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-remote"}
	enableWorkspaceRemote(t, sessions, ref)

	body := getRemoteStatus(t, server, cfg, ref)
	if body["enabled"] != true || body["connected"] != true || body["route"] != "local" {
		t.Fatalf("unexpected status: %+v", body)
	}
	if body["agentId"] != "macbook" || body["sessionId"] != "local-1" || body["title"] != "repo: agent-bot" {
		t.Fatalf("unexpected fields: %+v", body)
	}
}

func TestAdminRemoteStatusDisabledWhenSettingOff(t *testing.T) {
	t.Parallel()

	// Plugin reports connected, but the conversation has not opted in.
	sessions, server, cfg := remoteStatusTestServer(t, fakeRemoteServer{status: remoteagent.Status{Connected: true, RouteLocal: true}})
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-off"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}

	body := getRemoteStatus(t, server, cfg, ref)
	if body["enabled"] != false || body["route"] != "disabled" {
		t.Fatalf("disabled conversation should report disabled: %+v", body)
	}
}

func TestAdminRemoteStatusBotWhenEnabledButOffline(t *testing.T) {
	t.Parallel()

	// Enabled but no plugin wired (remote nil) => falls back to bot route.
	sessions, server, cfg := remoteStatusTestServer(t, nil)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-bot"}
	enableWorkspaceRemote(t, sessions, ref)

	body := getRemoteStatus(t, server, cfg, ref)
	if body["enabled"] != true || body["connected"] != false || body["route"] != "bot" {
		t.Fatalf("enabled+offline should report bot: %+v", body)
	}
}
