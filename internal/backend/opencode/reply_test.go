package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func TestFinalReplyText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*backend.SessionMessage)
		want   string
		wantOK bool
	}{
		{name: "final text", want: "second", wantOK: true},
		{name: "user", change: func(message *backend.SessionMessage) { message.Role = "user" }},
		{name: "missing role", change: func(message *backend.SessionMessage) { message.Role = "" }},
		{name: "not completed", change: func(message *backend.SessionMessage) { message.CompletedAt = 0 }},
		{name: "negative completion", change: func(message *backend.SessionMessage) { message.CompletedAt = -1 }},
		{name: "summary", change: func(message *backend.SessionMessage) { message.Summary = true }},
		{name: "error", change: func(message *backend.SessionMessage) { message.HasError = true }},
		{name: "suppressed", change: func(message *backend.SessionMessage) { message.ReplySuppressed = true }},
		{name: "missing finish", change: func(message *backend.SessionMessage) { message.Finish = "" }},
		{name: "tool finish", change: func(message *backend.SessionMessage) { message.Finish = "tool-calls" }},
		{name: "length finish", change: func(message *backend.SessionMessage) { message.Finish = "length" }},
		{name: "other finish", change: func(message *backend.SessionMessage) { message.Finish = "other" }},
		{name: "completed local tool", change: func(message *backend.SessionMessage) {
			message.Parts = append(message.Parts, backend.SessionMessagePart{Type: "tool", Tool: "task", ToolStatus: "completed"})
		}},
		{name: "running local tool", change: func(message *backend.SessionMessage) {
			message.Parts = append(message.Parts, backend.SessionMessagePart{Type: "tool", Tool: "bash", ToolStatus: "running"})
		}},
		{name: "failed local tool", change: func(message *backend.SessionMessage) {
			message.Parts = append(message.Parts, backend.SessionMessagePart{Type: "tool", Tool: "bash", ToolStatus: "error"})
		}},
		{name: "ignored local tool", change: func(message *backend.SessionMessage) {
			message.Parts = append(message.Parts, backend.SessionMessagePart{Type: "tool", Synthetic: true, Ignored: true})
		}},
		{name: "provider tool", change: func(message *backend.SessionMessage) {
			message.Parts = append(message.Parts, backend.SessionMessagePart{Type: "tool", ProviderExecuted: true})
		}, want: "second", wantOK: true},
		{name: "interrupted tool", change: func(message *backend.SessionMessage) {
			message.Parts = append(message.Parts, backend.SessionMessagePart{Type: "tool", Interrupted: true})
		}, want: "second", wantOK: true},
		{name: "provider and local tools", change: func(message *backend.SessionMessage) {
			message.Parts = append(message.Parts,
				backend.SessionMessagePart{Type: "tool", ProviderExecuted: true},
				backend.SessionMessagePart{Type: "tool", Tool: "read"},
			)
		}},
		{name: "no parts", change: func(message *backend.SessionMessage) { message.Parts = nil }},
		{name: "reasoning only", change: func(message *backend.SessionMessage) {
			message.Parts = []backend.SessionMessagePart{{Type: "reasoning", Text: "private"}}
		}},
		{name: "synthetic only", change: func(message *backend.SessionMessage) {
			message.Parts = []backend.SessionMessagePart{{Type: "text", Text: "synthetic", Synthetic: true}}
		}},
		{name: "ignored only", change: func(message *backend.SessionMessage) {
			message.Parts = []backend.SessionMessagePart{{Type: "text", Text: "ignored", Ignored: true}}
		}},
		{name: "blank only", change: func(message *backend.SessionMessage) {
			message.Parts = []backend.SessionMessagePart{{Type: "text", Text: " \n\t "}, {Type: "text"}}
		}},
		{name: "non text", change: func(message *backend.SessionMessage) {
			message.Parts = []backend.SessionMessagePart{{Type: "step-finish", Text: "private", Reason: "stop"}}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			message := backend.SessionMessage{
				Role:        "assistant",
				CompletedAt: 42,
				Finish:      "stop",
				Parts: []backend.SessionMessagePart{
					{Type: "text", Text: "  first  "},
					{Type: "reasoning", Text: "private"},
					{Type: "text", Text: "synthetic", Synthetic: true},
					{Type: "text", Text: "ignored", Ignored: true},
					{Type: "text", Text: " \n\t "},
					{Type: "text"},
					{Type: "text", Text: "second"},
					{Type: "step-finish", Reason: "stop"},
				},
			}
			if tt.change != nil {
				tt.change(&message)
			}
			text, ok := backend.FinalReplyText(message)
			if text != tt.want || ok != tt.wantOK {
				t.Fatalf("FinalReplyText() = (%q, %v), want (%q, %v)", text, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestReplyHistoryAcceptsUserSummary(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("before") {
		case "":
			w.Header().Set("X-Next-Cursor", "older")
			_, _ = w.Write([]byte(`[{"info":{"id":"msg_answer","role":"assistant","time":{"created":2,"completed":3},"summary":false,"finish":"stop"},"parts":[{"type":"text","text":"answer"}]}]`))
		default:
			_, _ = w.Write([]byte(`[{"info":{"id":"msg_user","role":"user","time":{"created":1},"summary":{"title":"request","body":"summary","diffs":[]}},"parts":[{"type":"text","text":"request"}]}]`))
		}
	}))
	defer server.Close()
	messages, err := New(server.URL).GetSessionReplyMessages(context.Background(), "/tmp/workspace", "ses_test")
	if err != nil {
		t.Fatalf("decode normal user summary: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[0].Summary || messages[1].Summary {
		t.Fatalf("history = %+v", messages)
	}
	if text, ok := backend.FinalReplyText(messages[1]); !ok || text != "answer" {
		t.Fatalf("final reply = %q, %v", text, ok)
	}
}

func TestFinalReplyTextIgnoresRetriedDraft(t *testing.T) {
	t.Parallel()
	message := backend.SessionMessage{
		Role: "assistant", CompletedAt: 1, Finish: "stop",
		Parts: []backend.SessionMessagePart{
			{Type: "text", Text: "abandoned partial attempt"},
			{Type: "reasoning", Text: "private"},
			{Type: "text", Text: "correct final answer"},
			{Type: "text", Text: "internal", Synthetic: true},
			{Type: "text", Text: "ignored", Ignored: true},
		},
	}
	if text, ok := backend.FinalReplyText(message); !ok || text != "correct final answer" {
		t.Fatalf("reply contains failed draft: %q, %v", text, ok)
	}
}

func TestGetSessionReplyMessageParsesFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		errorField string
		hasError   bool
	}{
		{name: "absent error"},
		{name: "null error", errorField: `,"error":null`},
		{name: "object error", errorField: `,"error":{"name":"APIError"}`, hasError: true},
		{name: "empty error", errorField: `,"error":{}`, hasError: true},
		{name: "false error", errorField: `,"error":false`, hasError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const workspacePath = "/tmp/workspace with spaces/?#&世界"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/session/ses_test/message/msg_assistant" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if query := r.URL.Query(); query.Get("directory") != workspacePath || len(query) != 1 {
					t.Errorf("query = %v, want only workspace directory", query)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"info":{"id":"msg_assistant","sessionID":"ses_test","parentID":"msg_user","role":"assistant","time":{"created":1779785210000,"completed":1779785211000},"summary":true,"finish":"stop","tokens":{"total":14983,"input":14952,"output":7,"reasoning":24,"cache":{"read":3,"write":1}}` + tt.errorField + `},
					"parts":[
						{"type":"text","text":"public"},
						{"type":"text","text":"synthetic","synthetic":true,"metadata":{"compaction_continue":true}},
						{"type":"text","text":"ignored","ignored":true},
						{"type":"reasoning","text":"private"},
						{"type":"tool","tool":"task","metadata":{"providerExecuted":true},"state":{"status":"completed","input":{"description":" child  task "},"metadata":{"sessionId":"ses_child","interrupted":true,"background":true}}},
						{"type":"tool","tool":"bash","metadata":{"sessionId":"wrong","interrupted":true,"background":true},"state":{"status":"running","input":{"command":"pwd"},"metadata":{"providerExecuted":true,"compaction_continue":true}}},
						{"type":"compaction"},
						{"type":"step-finish","reason":"stop"}
					]
				}`))
			}))
			defer server.Close()

			client := New(server.URL)
			client.httpClient = server.Client()
			var lookup backend.SessionReplyLookup = client
			message, err := lookup.GetSessionReplyMessage(context.Background(), workspacePath, "ses_test", "msg_assistant")
			if err != nil {
				t.Fatalf("get session reply message: %v", err)
			}
			want := backend.SessionMessage{
				ID:          "msg_assistant",
				SessionID:   "ses_test",
				ParentID:    "msg_user",
				Role:        "assistant",
				CreatedAt:   1779785210000,
				CompletedAt: 1779785211000,
				Summary:     true,
				Finish:      "stop",
				HasError:    tt.hasError,
				Tokens:      backend.TokenUsage{Total: 14983, Input: 14952, Output: 7, Reasoning: 24, CacheRead: 3, CacheWrite: 1},
				Parts: []backend.SessionMessagePart{
					{Type: "text", Text: "public"},
					{Type: "text", Text: "synthetic", Synthetic: true, CompactionContinue: true},
					{Type: "text", Text: "ignored", Ignored: true},
					{Type: "reasoning", Text: "private"},
					{Type: "tool", Tool: "task", ToolStatus: "completed", ToolInputSummary: "child task", ToolSessionID: "ses_child", ToolBackground: true, ProviderExecuted: true, Interrupted: true},
					{Type: "tool", Tool: "bash", ToolStatus: "running", ToolInputSummary: "pwd"},
					{Type: "compaction"},
					{Type: "step-finish", Reason: "stop"},
				},
			}
			if !reflect.DeepEqual(message, want) {
				t.Fatalf("message = %+v, want %+v", message, want)
			}
		})
	}
}

func TestReplyLookupSuppressesRetryStatusOnly(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"id":"msg_retry","role":"assistant","time":{"completed":1},"finish":"stop"},"parts":[{"type":"text","text":"已从失败处重试并核实完成。"}]}`))
	}))
	defer server.Close()
	message, err := New(server.URL).GetSessionReplyMessage(context.Background(), "/tmp/workspace", "ses_test", "msg_retry")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.FinalReplyText(message); ok || !message.ReplySuppressed {
		t.Fatalf("retry-only message was eligible for replay: %+v", message)
	}
}

func TestReplyLookupRetryOnlyIgnoresHiddenText(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		parts string
		want  string
	}{
		{name: "ignored retry after answer", parts: `{"type":"text","text":"answer"},{"type":"text","text":"已重试成功。","ignored":true}`, want: "answer"},
		{name: "synthetic retry after answer", parts: `{"type":"text","text":"answer"},{"type":"text","text":"已重试成功。","synthetic":true}`, want: "answer"},
		{name: "ignored answer after retry", parts: `{"type":"text","text":"已重试成功。"},{"type":"text","text":"answer","ignored":true}`},
		{name: "synthetic answer after retry", parts: `{"type":"text","text":"已重试成功。"},{"type":"text","text":"answer","synthetic":true}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"info":{"id":"msg_test","role":"assistant","time":{"completed":1},"finish":"stop"},"parts":[` + tt.parts + `]}`))
			}))
			defer server.Close()
			message, err := New(server.URL).GetSessionReplyMessage(context.Background(), "/tmp/workspace", "ses_test", "msg_test")
			if err != nil {
				t.Fatal(err)
			}
			if text, ok := backend.FinalReplyText(message); text != tt.want || ok != (tt.want != "") {
				t.Fatalf("visible reply = %q, %v; want %q", text, ok, tt.want)
			}
		})
	}
}

func TestGetSessionReplyMessageToolBackground(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata string
		want     bool
	}{
		{name: "background", metadata: `,"metadata":{"background":true}`, want: true},
		{name: "foreground", metadata: `,"metadata":{"background":false}`},
		{name: "missing metadata"},
		{name: "null metadata", metadata: `,"metadata":null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"info":{"id":"msg_assistant"},"parts":[{"type":"tool","tool":"task","metadata":{"background":true},"state":{"status":"completed"` + tt.metadata + `}}]}`))
			}))
			defer server.Close()

			client := New(server.URL)
			client.httpClient = server.Client()
			message, err := client.GetSessionReplyMessage(context.Background(), "/tmp/workspace", "ses_test", "msg_assistant")
			if err != nil {
				t.Fatalf("get session reply message: %v", err)
			}
			if len(message.Parts) != 1 || message.Parts[0].ToolBackground != tt.want {
				t.Fatalf("parts = %+v, want ToolBackground %v from state metadata only", message.Parts, tt.want)
			}
		})
	}
}

func TestReplyLookupParsesTaskUpdateAndCancellation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		state     string
		updated   bool
		cancelled bool
	}{
		{name: "running update", state: `"status":"completed","output":"<task><summary>Background task updated</summary></task>"`, updated: true},
		{name: "cancelled task", state: `"status":"error","error":"Task cancelled"`, cancelled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"info":{"id":"msg_test"},"parts":[{"type":"tool","tool":"task","state":{` + tt.state + `}}]}`))
			}))
			defer server.Close()
			message, err := New(server.URL).GetSessionReplyMessage(context.Background(), "/tmp/workspace", "ses_test", "msg_test")
			if err != nil {
				t.Fatal(err)
			}
			if len(message.Parts) != 1 || message.Parts[0].ToolUpdated != tt.updated || message.Parts[0].ToolCancelled != tt.cancelled {
				t.Fatalf("task metadata = %+v", message.Parts)
			}
		})
	}
}

func TestGetSessionReplyMessagesPaginatesAndSorts(t *testing.T) {
	t.Parallel()

	const workspacePath = "/tmp/workspace with spaces"
	const firstCursor = "cursor newer+/?=#"
	const secondCursor = "cursor older"
	var linkRequests atomic.Int32
	linkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		linkRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer linkServer.Close()

	var mu sync.Mutex
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/session/ses_test/message" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		query := r.URL.Query()
		if query.Get("directory") != workspacePath || query.Get("limit") != "100" {
			t.Errorf("unexpected query: %v", query)
		}
		before := query.Get("before")
		mu.Lock()
		cursors = append(cursors, before)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", "<"+linkServer.URL+"/arbitrary>; rel=\"next\"")
		switch before {
		case "":
			if query.Has("before") || len(query) != 2 {
				t.Errorf("first page query = %v, want directory and limit only", query)
			}
			w.Header().Set("X-Next-Cursor", firstCursor)
			_, _ = w.Write([]byte(`[{"info":{"id":"msg_d","sessionID":"ses_test","time":{"created":300}}},{"info":{"id":"msg_c","sessionID":"ses_test","time":{"created":200}}}]`))
		case firstCursor:
			w.Header().Set("X-Next-Cursor", secondCursor)
			_, _ = w.Write([]byte(`[{"info":{"id":"msg_b","sessionID":"ses_test","time":{"created":200}}},{"info":{"id":"msg_a","sessionID":"ses_test","time":{"created":100}}}]`))
		case secondCursor:
			_, _ = w.Write([]byte(`[{"info":{"id":"msg_0","sessionID":"ses_test","time":{"created":10}}}]`))
		default:
			t.Errorf("unexpected cursor: %q", before)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	messages, err := client.GetSessionReplyMessages(context.Background(), workspacePath, "ses_test")
	if err != nil {
		t.Fatalf("get session reply messages: %v", err)
	}
	var ids []string
	for _, message := range messages {
		ids = append(ids, message.ID)
		if message.SessionID != "ses_test" {
			t.Errorf("session id = %q, want ses_test", message.SessionID)
		}
	}
	if want := []string{"msg_0", "msg_a", "msg_b", "msg_c", "msg_d"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("message ids = %v, want %v", ids, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"", firstCursor, secondCursor}; !reflect.DeepEqual(cursors, want) {
		t.Fatalf("cursors = %v, want %v", cursors, want)
	}
	if got := linkRequests.Load(); got != 0 {
		t.Fatalf("Link requests = %d, want 0", got)
	}
}

func TestGetSessionReplyMessagesWithoutCursor(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if query := r.URL.Query(); query.Get("directory") != "/tmp/workspace" || query.Get("limit") != "100" || query.Has("before") {
			t.Errorf("unexpected query: %v", query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"info":{"id":"msg_new","time":{"created":2}}},{"info":{"id":"msg_old","time":{"created":1}}}]`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	messages, err := client.GetSessionReplyMessages(context.Background(), "/tmp/workspace", "ses_test")
	if err != nil {
		t.Fatalf("get session reply messages: %v", err)
	}
	if len(messages) != 2 || messages[0].ID != "msg_old" || messages[1].ID != "msg_new" {
		t.Fatalf("messages = %+v, want both messages oldest first", messages)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestSessionReplyLookupsRejectMissingIDs(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	if _, err := client.GetSessionReplyMessages(context.Background(), "/tmp/workspace", " "); err == nil {
		t.Fatal("expected missing session id error")
	}
	if _, err := client.GetSessionReplyMessage(context.Background(), "/tmp/workspace", " ", "msg_test"); err == nil {
		t.Fatal("expected missing session id error")
	}
	if _, err := client.GetSessionReplyMessage(context.Background(), "/tmp/workspace", "ses_test", " "); err == nil {
		t.Fatal("expected missing message id error")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestSessionReplyLookupsHTTPError(t *testing.T) {
	t.Parallel()

	const secret = "private-response-body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(strings.Repeat(secret, 4096)))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	_, listErr := client.GetSessionReplyMessages(context.Background(), "/tmp/workspace", "ses_test")
	_, messageErr := client.GetSessionReplyMessage(context.Background(), "/tmp/workspace", "ses_test", "msg_test")
	for _, err := range []error{listErr, messageErr} {
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("error = %v, want HTTP 403", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposes response body: %v", err)
		}
	}
}
