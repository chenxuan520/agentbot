package flow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/flow"
	"github.com/chenxuan520/agentbot/internal/session"
	appstore "github.com/chenxuan520/agentbot/internal/store"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

type replyIntegrationTransport struct {
	base http.RoundTripper
	url  string
}

func (t replyIntegrationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host == "open.feishu.cn" {
		forward := request.Clone(request.Context())
		url := *request.URL
		forward.URL = &url
		forward.URL.Scheme = "http"
		forward.URL.Host = t.url
		return t.base.RoundTrip(forward)
	}
	return t.base.RoundTrip(request)
}

func TestReplyListenerWithSQLiteAndHTTPEvents(t *testing.T) {
	root := t.TempDir()
	template := filepath.Join(root, "templates", "default")
	if err := os.MkdirAll(template, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := workspace.DefaultSettings()
	settings.Settings.ReplyMode = "thread"
	if err := workspace.SaveSettings(template, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, workspace.AgentsFileName), []byte("test agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(root)
	cfg.FeishuAppID = "test-app"
	cfg.FeishuAppSecret = "test-secret"
	db, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions := session.NewService(db, workspace.NewManager(cfg, db))
	ref := conversation.Ref{Provider: "feishu", ConversationID: "test-chat"}
	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatal(err)
	}
	input := flow.TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-original", RootMessageID: "om-root", SenderID: "user-original"}
	route, err := json.Marshal(struct {
		Input     flow.TextInput
		ReplyMode string
	}{Input: input, ReplyMode: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertReplySource(appstore.ReplySourceRecord{
		SessionID: "ses-main", MessageID: "msg-user", Provider: ref.Provider, ConversationID: ref.ConversationID,
		WorkspacePath: current.Workspace.Path, InputJSON: string(route),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkReplyDelivered("ses-main", "msg-started"); err != nil {
		t.Fatal(err)
	}
	const final = `{"info":{"id":"msg-final","sessionID":"ses-main","parentID":"msg-notice","role":"assistant","time":{"created":4,"completed":5},"finish":"stop"},"parts":[{"type":"reasoning","text":"private"},{"type":"text","text":"background result"}]}`
	const history = `[
		{"info":{"id":"msg-user","sessionID":"ses-main","role":"user","time":{"created":1}},"parts":[{"type":"text","text":"start"}]},
		{"info":{"id":"msg-tool","sessionID":"ses-main","parentID":"msg-user","role":"assistant","time":{"created":2,"completed":2},"finish":"tool-calls"},"parts":[{"type":"tool","tool":"task","state":{"status":"completed","metadata":{"sessionId":"ses-child","background":true}}}]},
		{"info":{"id":"msg-started","sessionID":"ses-main","parentID":"msg-user","role":"assistant","time":{"created":2,"completed":2},"finish":"stop"},"parts":[{"type":"text","text":"started"}]},
		{"info":{"id":"msg-notice","sessionID":"ses-main","role":"user","time":{"created":3}},"parts":[{"type":"text","synthetic":true,"text":"<task id=\"ses-child\" state=\"completed\"><task_result>done</task_result></task>"}]},` + final + `]`
	var mu sync.Mutex
	var targets []string
	var texts []string
	var threaded []bool
	replied := make(chan struct{}, 4)
	eventSeen := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/info":
			w.WriteHeader(http.StatusNotFound)
		case "/global/event":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"payload\":{\"type\":\"server.connected\",\"properties\":{}}}\n\n")
			w.(http.Flusher).Flush()
			eventSeen <- struct{}{}
			<-r.Context().Done()
		case "/session/ses-main/message":
			if r.URL.Query().Get("directory") != current.Workspace.Path {
				t.Error("reply history missing workspace directory")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, history)
		case "/session/ses-main/message/msg-final":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, final)
		case "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
		case "/open-apis/im/v1/messages/om-original/reply":
			var body struct {
				Content       string `json:"content"`
				ReplyInThread bool   `json:"reply_in_thread"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			mu.Lock()
			targets = append(targets, r.URL.Path)
			texts = append(texts, body.Content)
			threaded = append(threaded, body.ReplyInThread)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"data":{"message_id":"om-sent"}}`)
			replied <- struct{}{}
		default:
			t.Errorf("unexpected HTTP request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = replyIntegrationTransport{base: transport, url: server.Listener.Addr().String()}
	defer func() { http.DefaultTransport = transport }()
	cfg.BackendBaseURLs["opencode"] = server.URL
	service := flow.NewService(cfg, sessions, control.NewService(db))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.ListenReplies(ctx) }()
	select {
	case <-replied:
	case <-ctx.Done():
		t.Fatal("no background reply")
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		delivered, err := db.ReplyDelivered("ses-main", "msg-final")
		if err != nil {
			t.Fatal(err)
		}
		if delivered {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reply was not recorded as delivered")
		case <-ticker.C:
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-eventSeen
	delivered, err := db.ReplyDelivered("ses-main", "msg-final")
	if err != nil || !delivered {
		t.Fatalf("SQLite delivery = %v, err = %v", delivered, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions = session.NewService(db, workspace.NewManager(cfg, db))
	service = flow.NewService(cfg, sessions, control.NewService(db))
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { done <- service.ListenReplies(ctx) }()
	select {
	case <-eventSeen:
	case <-ctx.Done():
		t.Fatal("restart did not subscribe")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(targets) != 1 || !threaded[0] {
		t.Fatalf("targets = %v, threaded = %v", targets, threaded)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(texts[0]), &content); err != nil {
		t.Fatal(err)
	}
	if !containsReplyText(content, "background result") || containsReplyText(content, "private") {
		t.Fatalf("unexpected reply content: %s", texts[0])
	}
}

func containsReplyText(value any, text string) bool {
	switch item := value.(type) {
	case string:
		return strings.Contains(item, text)
	case []any:
		for _, value := range item {
			if containsReplyText(value, text) {
				return true
			}
		}
	case map[string]any:
		for _, value := range item {
			if containsReplyText(value, text) {
				return true
			}
		}
	}
	return false
}
