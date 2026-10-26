package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func TestV2MessageNativeSchema(t *testing.T) {
	t.Parallel()
	var item v2NativeMessage
	const raw = `{
		"id":"msg_native","type":"assistant","agent":"build",
		"model":{"providerID":"provider","id":"model","variant":"high"},
		"metadata":{"source":7,"custom":{"kept":true}},
		"sessionID":"wrong","parentID":"fake_parent","role":"user",
		"finish":"stop","error":null,"retry":null,
		"time":{"created":100,"streamed":200,"completed":300},
		"tokens":{"input":100,"output":2,"reasoning":3,"cache":{"read":40,"write":5}},
		"content":[
			{"type":"text","text":"first","native":{"signature":"opaque"}},
			{"type":"reasoning","text":"private","time":{"created":110,"completed":120}},
			{"type":"tool","id":"call_cloud","name":"cloud_search","executed":true,
			 "state":{"status":"completed","input":{"query":"lookup"},"metadata":{"custom":true},
			 "content":[{"type":"text","text":"not a reply"},{"type":"file","uri":"file:///tmp/result","mime":"text/plain","name":"result"}]},
			 "time":{"created":130,"ran":140,"completed":150}},
			{"type":"text","text":"last"}
		]
	}`
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	if item.Agent != "build" || item.Model.ProviderID != "provider" || item.Model.ID != "model" || item.Model.Variant != "high" || item.Metadata["source"] != float64(7) {
		t.Fatalf("native fields = %+v", item)
	}
	if item.Time.Streamed != 200 || item.Content[2].ID != "call_cloud" || item.Content[2].Time.Ran != 140 || item.Content[2].State.Content[1].URI != "file:///tmp/result" {
		t.Fatalf("native content = %+v", item.Content)
	}
	message := item.message("ses_scope", 7)
	if message.Role != "assistant" || message.SessionID != "ses_scope" || message.ParentID != "" || !message.Native || message.Sequence != 7 || message.CreatedAt != 100 || message.CompletedAt != 300 {
		t.Fatalf("normalized message = %+v", message)
	}
	wantTokens := backend.TokenUsage{Total: 150, Input: 100, Output: 2, Reasoning: 3, CacheRead: 40, CacheWrite: 5}
	if message.Tokens != wantTokens || !message.Parts[2].ProviderExecuted || message.Parts[2].Tool != "cloud_search" {
		t.Fatalf("normalized fields = %+v", message)
	}
	if text, ok := backend.FinalReplyText(message); !ok || text != "last" {
		t.Fatalf("final text = %q, %v", text, ok)
	}
}

func TestV2MessageNativeToolStates(t *testing.T) {
	t.Parallel()
	for _, state := range []string{
		`{"status":"streaming","input":"{\"description\":"}`,
		`{"status":"running","input":{"description":" child  work "},"metadata":{"sessionID":"ses_child","status":"running"}}`,
		`{"status":"completed","input":{"description":" child  work ","background":false},"metadata":{"sessionID":"ses_child","status":"running"},"content":[{"type":"text","text":"background dispatch"}]}`,
		`{"status":"error","input":{},"error":{"type":"aborted","message":"private failure"},"metadata":{"sessionID":"ses_child","status":"cancelled"}}`,
	} {
		var item v2NativeMessage
		raw := `{"id":"msg_tool","type":"assistant","time":{"created":1,"completed":2},"finish":"stop","content":[{"type":"text","text":"draft"},{"type":"tool","id":"call","name":"subagent","executed":false,"state":` + state + `}]}`
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			t.Fatal(err)
		}
		message := item.message("ses_parent", 1)
		part := message.Parts[1]
		if part.ToolStatus != item.Content[1].State.Status || part.ProviderExecuted || part.Interrupted || part.ToolUpdated {
			t.Fatalf("tool normalization = %+v", part)
		}
		if part.ToolStatus == "completed" && (part.ToolSessionID != "ses_child" || !part.ToolBackground || part.ToolInputSummary != "child work") {
			t.Fatalf("background dispatch = %+v", part)
		}
		if part.ToolStatus == "error" && !part.ToolCancelled {
			t.Fatalf("cancellation = %+v", part)
		}
		if _, ok := backend.FinalReplyText(message); ok {
			t.Fatalf("local tool draft eligible: %+v", message)
		}
	}
}

func TestV2MessageFinalReplyConditions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*v2NativeMessage)
		want   bool
	}{
		{name: "visible last text", want: true},
		{name: "user", change: func(m *v2NativeMessage) { m.Type = "user"; m.Text = "user" }},
		{name: "synthetic", change: func(m *v2NativeMessage) { m.Type = "synthetic"; m.Text = "notice" }},
		{name: "system", change: func(m *v2NativeMessage) { m.Type = "system"; m.Text = "system" }},
		{name: "compaction", change: func(m *v2NativeMessage) { m.Type = "compaction"; m.Summary = json.RawMessage(`"summary"`) }},
		{name: "summary", change: func(m *v2NativeMessage) { m.Summary = json.RawMessage(`true`) }},
		{name: "streamed only", change: func(m *v2NativeMessage) { m.Time.Streamed = 3; m.Time.Completed = 0 }},
		{name: "error", change: func(m *v2NativeMessage) { m.Error = json.RawMessage(`{"type":"provider.unknown","message":"secret"}`) }},
		{name: "false error still present", change: func(m *v2NativeMessage) { m.Error = json.RawMessage(`false`) }},
		{name: "retry", change: func(m *v2NativeMessage) {
			m.Retry = json.RawMessage(`{"attempt":1,"at":4,"error":{"type":"provider.unknown","message":"retry"}}`)
		}},
		{name: "empty retry", change: func(m *v2NativeMessage) { m.Retry = json.RawMessage(`{}`) }},
		{name: "null retry and error", change: func(m *v2NativeMessage) { m.Retry = json.RawMessage(`null`); m.Error = json.RawMessage(`null`) }, want: true},
		{name: "length", change: func(m *v2NativeMessage) { m.Finish = "length" }},
		{name: "tool calls", change: func(m *v2NativeMessage) { m.Finish = "tool-calls" }},
		{name: "missing finish", change: func(m *v2NativeMessage) { m.Finish = "" }},
		{name: "hidden only", change: func(m *v2NativeMessage) { m.Content = m.Content[2:4] }},
		{name: "unknown content", change: func(m *v2NativeMessage) { m.Content = append(m.Content, v2NativePart{Type: "future-tool"}) }},
		{name: "local tool after final", change: func(m *v2NativeMessage) { m.Content = append(m.Content, v2NativePart{Type: "tool", Name: "read"}) }},
		{name: "local tool before final", change: func(m *v2NativeMessage) {
			m.Content = append([]v2NativePart{{Type: "tool", Name: "read"}}, m.Content...)
		}},
		{name: "provider tool", change: func(m *v2NativeMessage) {
			m.Content = append(m.Content, v2NativePart{Type: "tool", Name: "cloud_search", Executed: true})
		}, want: true},
		{name: "provider plus local", change: func(m *v2NativeMessage) {
			m.Content = append(m.Content, v2NativePart{Type: "tool", Executed: true}, v2NativePart{Type: "tool", Name: "read"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var item v2NativeMessage
			if err := json.Unmarshal([]byte(`{"id":"msg_final","type":"assistant","finish":"stop","time":{"created":1,"completed":2},"content":[{"type":"text","text":"draft"},{"type":"reasoning","text":"private"},{"type":"text","text":"hidden","synthetic":true},{"type":"text","text":"ignored","ignored":true},{"type":"text","text":"last"},{"type":"text","text":"  "},{"type":"text","text":"hidden tail","ignored":true}]}`), &item); err != nil {
				t.Fatal(err)
			}
			if tt.change != nil {
				tt.change(&item)
			}
			text, ok := backend.FinalReplyText(item.message("ses", 1))
			if ok != tt.want || tt.want && text != "last" || !tt.want && text != "" {
				t.Fatalf("FinalReplyText = %q, %v; want eligible %v", text, ok, tt.want)
			}
		})
	}
}

func TestV2MessageHistoryPaginationAndSingle(t *testing.T) {
	t.Parallel()
	const cursor1 = " opaque +/?=#&order=desc "
	const cursor2 = "second cursor 世界"
	var linkRequests atomic.Int32
	linkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		linkRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer linkServer.Close()
	pages := [][]string{
		{v2TestUser("msg_z_user", 900, "A"), v2TestAssistant("msg_a_dispatch", 1, 20, "tool-calls", v2TestSubagent("call_A", "ses_child", "running"))},
		{v2TestAssistant("msg_a_final", 2, 30, "stop", v2TestText("A final")), v2TestUser("msg_b_user", 300, "B"), v2TestAssistant("msg_b_final", 5, 310, "stop", v2TestText("B final"))},
		{v2TestIdle("msg_idle", 400), v2TestNotice("msg_notice", "ses_child", "completed", 500), v2TestAssistant("msg_answer", 3, 600, "stop", v2TestText("historical"))},
	}
	var mu sync.Mutex
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "test-user" || password != "test-password" {
			t.Error("missing transport authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", "<"+linkServer.URL+"/arbitrary>; rel=\"next\"")
		if r.URL.Path == "/api/session/ses_scope/message/msg_answer" {
			if r.URL.RawQuery != "" {
				t.Errorf("single location query = %s", r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"data":%s}`, v2TestAssistant("msg_answer", 3, 601, "stop", v2TestText("single verified")))
			return
		}
		if r.URL.Path != "/api/session/ses_scope/message" {
			t.Errorf("path = %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		query := r.URL.Query()
		cursor := query.Get("cursor")
		mu.Lock()
		cursors = append(cursors, cursor)
		mu.Unlock()
		page, next := 0, cursor1
		switch cursor {
		case "":
			if !reflect.DeepEqual(query, url.Values{"limit": {"100"}, "order": {"asc"}}) {
				t.Errorf("first query = %v", query)
			}
		case cursor1:
			page, next = 1, cursor2
		case cursor2:
			page, next = 2, ""
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
		if cursor != "" && !reflect.DeepEqual(query, url.Values{"cursor": {cursor}}) {
			t.Errorf("cursor query must contain only opaque cursor: %v", query)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"data":[%s],"cursor":{"previous":"not-followed","next":%q}}`, strings.Join(pages[page], ","), next)
	}))
	defer server.Close()
	newClient := func() *Client {
		return NewWithOptions(server.URL, Options{Username: "test-user", Password: "test-password"})
	}
	messages, err := newClient().v2GetSessionMessages(context.Background(), "/ignored/workspace", "ses_scope")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, message := range messages {
		ids = append(ids, message.ID)
		if !message.Native || message.ParentID != "" || message.SessionID != "ses_scope" || message.Sequence != int64(i+1) {
			t.Fatalf("history fields = %+v", message)
		}
	}
	wantIDs := []string{"msg_z_user", "msg_a_dispatch", "msg_a_final", "msg_b_user", "msg_b_final", "msg_idle", "msg_notice", "msg_answer"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("server sequence reordered: %v", ids)
	}
	v2TestSourceIDs(t, messages, "msg_answer", []string{"msg_z_user"})
	message, err := newClient().v2GetSessionMessage(context.Background(), "/ignored/workspace", "ses_scope", "msg_answer")
	if err != nil {
		t.Fatal(err)
	}
	if message.Sequence != messages[7].Sequence || !reflect.DeepEqual(message.SourceMessageIDs, messages[7].SourceMessageIDs) || message.CompletedAt != 601 {
		t.Fatalf("single/history source mismatch: %+v / %+v", message, messages[7])
	}
	if text, ok := backend.FinalReplyText(message); !ok || text != "single verified" {
		t.Fatalf("single content was not rechecked: %q, %v", text, ok)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"", cursor1, cursor2, "", cursor1, cursor2}; !reflect.DeepEqual(cursors, want) || linkRequests.Load() != 0 {
		t.Fatalf("cursor order = %v; Link requests = %d", cursors, linkRequests.Load())
	}
}

func TestV2MessageSingleRebuildsWithoutClientCache(t *testing.T) {
	t.Parallel()
	var generation atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assistant := v2TestAssistant("msg_answer", 2, 3, "stop", v2TestText("verified"))
		if strings.HasSuffix(r.URL.Path, "/msg_answer") {
			_, _ = fmt.Fprintf(w, `{"data":%s}`, assistant)
			return
		}
		user := "msg_A"
		if generation.Load() != 0 {
			user = "msg_B"
		}
		_, _ = fmt.Fprintf(w, `{"data":[%s,%s],"cursor":{}}`, v2TestUser(user, 1, "input"), assistant)
	}))
	defer server.Close()
	first, err := New(server.URL).v2GetSessionMessage(context.Background(), "workspace", "ses", "msg_answer")
	if err != nil {
		t.Fatal(err)
	}
	generation.Store(1)
	second, err := New(server.URL).v2GetSessionMessage(context.Background(), "workspace", "ses", "msg_answer")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.SourceMessageIDs, []string{"msg_A"}) || !reflect.DeepEqual(second.SourceMessageIDs, []string{"msg_B"}) {
		t.Fatalf("fresh history sources = %v / %v", first.SourceMessageIDs, second.SourceMessageIDs)
	}
}

func TestV2MessageRequestsUseSameContextAndEscapedScope(t *testing.T) {
	t.Parallel()
	const sessionID = "ses space/?#&"
	const messageID = "msg space/?#&"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := "/api/session/" + url.PathEscape(sessionID) + "/message"
		assistant := v2TestAssistant(messageID, 2, 3, "stop", v2TestText("verified"))
		switch r.URL.EscapedPath() {
		case path:
			_, _ = fmt.Fprintf(w, `{"data":[%s,%s],"cursor":{}}`, v2TestUser("msg_user", 1, "input"), assistant)
		case path + "/" + url.PathEscape(messageID):
			if r.URL.RawQuery != "" {
				t.Errorf("single query = %s", r.URL.RawQuery)
			}
			_, _ = fmt.Fprintf(w, `{"data":%s}`, assistant)
		default:
			t.Errorf("escaped path = %s", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := New(server.URL)
	client.httpClient = server.Client()
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "same-request")
	transport := client.httpClient.Transport
	var requests atomic.Int32
	client.httpClient.Transport = eventRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.Context().Value(contextKey{}) != "same-request" {
			t.Error("single and history must preserve request context")
		}
		return transport.RoundTrip(r)
	})
	message, err := client.v2GetSessionMessage(ctx, "not-in-query", sessionID, messageID)
	if err != nil || !reflect.DeepEqual(message.SourceMessageIDs, []string{"msg_user"}) || requests.Load() != 2 {
		t.Fatalf("message = %+v, err = %v, requests = %d", message, err, requests.Load())
	}
}

func TestV2MessageFailuresFailClosed(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status int
		list   string
		single string
	}{
		{name: "HTTP secret", status: http.StatusForbidden, list: "private-response-body", single: "private-response-body"},
		{name: "missing data", list: `{}`, single: `{"data":{"id":"msg_answer","type":"assistant"}}`},
		{name: "null data", list: `{"data":null}`, single: `{"data":{"id":"msg_answer","type":"assistant"}}`},
		{name: "missing identity", list: `{"data":[{"type":"user"}],"cursor":{}}`, single: `{"data":{"id":"msg_answer","type":"assistant"}}`},
		{name: "duplicate identity", list: `{"data":[{"id":"msg_same","type":"user"},{"id":"msg_same","type":"user"}],"cursor":{}}`, single: `{"data":{"id":"msg_answer","type":"assistant"}}`},
		{name: "single missing from history", list: `{"data":[],"cursor":{}}`, single: `{"data":{"id":"msg_answer","type":"assistant"}}`},
		{name: "single identity mismatch", list: `{"data":[],"cursor":{}}`, single: `{"data":{"id":"msg_other","type":"assistant"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				body := tt.list
				if strings.HasSuffix(r.URL.Path, "/msg_answer") {
					body = tt.single
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			_, err := New(server.URL).v2GetSessionMessage(context.Background(), "workspace", "ses", "msg_answer")
			if err == nil || strings.Contains(err.Error(), "private-response-body") {
				t.Fatalf("failure = %v", err)
			}
		})
	}
}

func TestV2MessageRepeatedCursorAndMissingIDs(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":[],"cursor":{"next":"same opaque cursor"}}`))
	}))
	defer server.Close()
	client := New(server.URL)
	if _, err := client.v2GetSessionMessages(context.Background(), "workspace", " "); err == nil {
		t.Fatal("missing session accepted")
	}
	for _, ids := range [][2]string{{" ", "msg"}, {"ses", " "}} {
		if _, err := client.v2GetSessionMessage(context.Background(), "workspace", ids[0], ids[1]); err == nil {
			t.Fatal("missing single identity accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.v2GetSessionMessages(ctx, "workspace", "ses"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid lookups sent %d requests", requests.Load())
	}
	if _, err := client.v2GetSessionMessages(context.Background(), "workspace", "ses"); err == nil || !strings.Contains(err.Error(), "repeated cursor") || requests.Load() != 2 {
		t.Fatalf("cursor cycle = %v, requests = %d", err, requests.Load())
	}
}

func TestV2SourceContinuationBoundaries(t *testing.T) {
	t.Parallel()
	localTool := `{"type":"tool","id":"call_read","name":"read","state":{"status":"completed","input":{},"content":[{"type":"text","text":"tool output"}]}}`
	for _, tt := range []struct {
		name  string
		items []string
		want  []string
	}{
		{name: "user assistant idle unique", items: []string{v2TestUser("msg_A", 100, "A"), v2TestAssistant("msg_old", 200, 300, "stop", v2TestText("old")), v2TestIdle("msg_idle", 400), v2TestUser("msg_B", 500, "B"), v2TestAssistant("msg_final", 600, 700, "stop", v2TestText("new"))}, want: []string{"msg_B"}},
		{name: "safe stop without idle", items: []string{v2TestUser("msg_A", 100, "A"), v2TestAssistant("msg_old", 200, 300, "stop", v2TestText("old")), v2TestUser("msg_B", 400, "B"), v2TestAssistant("msg_final", 500, 600, "stop", v2TestText("new"))}, want: []string{"msg_B"}},
		{name: "NoReply pending and continuation batch", items: []string{v2TestUser("msg_A", 100, "A"), v2TestUser("msg_B", 200, "B"), v2TestAssistant("msg_tools", 300, 400, "tool-calls", localTool), v2TestUser("msg_C", 500, "C"), v2TestAssistant("msg_final", 600, 700, "stop", v2TestText("batch"))}, want: []string{"msg_A", "msg_B", "msg_C"}},
		{name: "steered before stop completion", items: []string{v2TestUser("msg_A", 1, "A"), v2TestAssistant("msg_old", 2, 100, "stop", v2TestText("old")), v2TestUser("msg_B", 50, "B"), v2TestAssistant("msg_final", 60, 120, "stop", v2TestText("new"))}, want: []string{"msg_A", "msg_B"}},
		{name: "same millisecond not a proven stop boundary", items: []string{v2TestUser("msg_A", 1, "A"), v2TestAssistant("msg_old", 2, 100, "stop", v2TestText("old")), v2TestUser("msg_B", 100, "B"), v2TestAssistant("msg_final", 101, 120, "stop", v2TestText("new"))}, want: []string{"msg_A", "msg_B"}},
		{name: "completed local tool cannot reset", items: []string{v2TestUser("msg_A", 100, "A"), v2TestAssistant("msg_tools", 200, 300, "stop", v2TestText("draft")+","+localTool), v2TestUser("msg_B", 400, "B"), v2TestAssistant("msg_final", 500, 600, "stop", v2TestText("new"))}, want: []string{"msg_A", "msg_B"}},
		{name: "compaction and system do not become users or reset", items: []string{v2TestUser("msg_A", 100, "A"), v2TestAssistant("msg_tools", 200, 300, "tool-calls", localTool), `{"id":"msg_compact","type":"compaction","status":"completed","reason":"auto","summary":"not user input","time":{"created":350}}`, `{"id":"msg_system","type":"system","text":"system update","time":{"created":360}}`, v2TestUser("msg_B", 400, "B"), v2TestAssistant("msg_final", 500, 600, "stop", v2TestText("new"))}, want: []string{"msg_A", "msg_B"}},
		{name: "foreign user remains an unmapped candidate", items: []string{v2TestUser("msg_A", 100, "A"), v2TestAssistant("msg_tools", 200, 300, "tool-calls", localTool), v2TestUser("msg_foreign", 400, "external"), v2TestAssistant("msg_final", 500, 600, "stop", v2TestText("new"))}, want: []string{"msg_A", "msg_foreign"}},
		{name: "foreign synthetic cannot inherit registered user", items: []string{v2TestUser("msg_A", 100, "A"), v2TestAssistant("msg_tools", 200, 300, "tool-calls", localTool), `{"id":"msg_external_synthetic","type":"synthetic","text":"external input","time":{"created":400}}`, v2TestAssistant("msg_final", 500, 600, "stop", v2TestText("new"))}, want: []string{"msg_A", "msg_external_synthetic"}},
		{name: "orphan continuation preserves unknown", items: []string{v2TestAssistant("msg_orphan", 100, 200, "tool-calls", localTool), v2TestUser("msg_A", 300, "A"), v2TestAssistant("msg_final", 400, 500, "stop", v2TestText("new"))}, want: []string{v2UnknownSourcePrefix + "msg_orphan", "msg_A"}},
		{name: "idle resets failed unfinished continuation", items: []string{v2TestUser("msg_A", 100, "A"), v2TestAssistant("msg_unfinished", 200, 0, "", v2TestText("draft")), `{"id":"msg_idle","type":"idle","outcome":"failed","error":{"type":"provider.unknown","message":"private"},"time":{"created":300}}`, v2TestUser("msg_B", 400, "B"), v2TestAssistant("msg_final", 500, 600, "stop", v2TestText("new"))}, want: []string{"msg_B"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			messages := v2TestRebuild(t, tt.items...)
			v2TestSourceIDs(t, messages, "msg_final", tt.want)
			for _, message := range messages {
				if !message.Native || message.ParentID != "" || message.ID == "msg_compact" && (message.Role == "user" || !message.Summary) {
					t.Fatalf("unsafe normalized identity = %+v", message)
				}
			}
		})
	}
}

func TestV2SourceStopBoundaryChecksAllInputs(t *testing.T) {
	t.Parallel()
	messages := v2TestRebuild(t,
		v2TestUser("msg_A", 1, "A"),
		v2TestAssistant("msg_old", 2, 100, "stop", v2TestText("old")),
		v2TestUser("msg_B", 200, "B"),
		v2TestUser("msg_C", 50, "C"),
		v2TestAssistant("msg_final", 300, 400, "stop", v2TestText("batch")),
	)
	v2TestSourceIDs(t, messages, "msg_final", []string{"msg_A", "msg_B", "msg_C"})
}

func TestV2SourceBackgroundReturnsOriginalInput(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{"completed", "error", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()
			items := v2TestBackgroundA()
			items = append(items, v2TestUser("msg_B", 500, "B"), v2TestAssistant("msg_B_final", 600, 700, "stop", v2TestText("B")), v2TestIdle("msg_B_idle", 750), v2TestNotice("msg_notice", "ses_child", terminal, 800), v2TestAssistant("msg_final", 900, 1000, "stop", v2TestText("A result")))
			messages := v2TestRebuild(t, items...)
			v2TestSourceIDs(t, messages, "msg_notice", []string{"msg_A"})
			v2TestSourceIDs(t, messages, "msg_final", []string{"msg_A"})
			if !messages[7].Parts[0].Synthetic || messages[7].Role != "user" {
				t.Fatalf("synthetic notification lost its native hint: %+v", messages[7])
			}
		})
	}
}

func TestV2SourceInterleavedNotificationIsAmbiguous(t *testing.T) {
	t.Parallel()
	items := v2TestBackgroundA()
	items = append(items, v2TestUser("msg_B", 500, "B"), v2TestAssistant("msg_B_running", 600, 0, "", v2TestText("B draft")), v2TestNotice("msg_notice", "ses_child", "completed", 800), v2TestAssistant("msg_final", 900, 1000, "stop", v2TestText("mixed")))
	messages := v2TestRebuild(t, items...)
	v2TestSourceIDs(t, messages, "msg_notice", []string{"msg_A"})
	v2TestSourceIDs(t, messages, "msg_final", []string{"msg_B", "msg_A"})
}

func TestV2SourceChildCallsMergeAndTerminalClosesMapping(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"running", "completed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			items := v2TestBackgroundA()
			items = append(items, v2TestUser("msg_B", 500, "B"), v2TestAssistant("msg_dispatch_B", 600, 700, "tool-calls", v2TestSubagent("call_B", "ses_child", status)), v2TestAssistant("msg_B_final", 720, 730, "stop", v2TestText("B")), v2TestIdle("msg_B_idle", 750), v2TestNotice("msg_notice", "ses_child", "completed", 800), v2TestAssistant("msg_final", 900, 1000, "stop", v2TestText("result")), v2TestIdle("msg_result_idle", 1100), v2TestNotice("msg_second_notice", "ses_child", "completed", 1200), v2TestAssistant("msg_after_closed", 1300, 1400, "stop", v2TestText("unknown")))
			messages := v2TestRebuild(t, items...)
			v2TestSourceIDs(t, messages, "msg_final", []string{"msg_A", "msg_B"})
			v2TestSourceIDs(t, messages, "msg_after_closed", []string{v2UnknownSourcePrefix + "msg_second_notice"})
		})
	}
}

func TestV2SourceChildReuseWithoutGenerationStaysUnknown(t *testing.T) {
	t.Parallel()
	items := v2TestBackgroundA()
	items = append(items, v2TestNotice("msg_first_notice", "ses_child", "completed", 500), v2TestAssistant("msg_first_result", 600, 700, "stop", v2TestText("A result")), v2TestIdle("msg_result_idle", 750), v2TestUser("msg_B", 800, "B"), v2TestAssistant("msg_dispatch_B", 900, 1000, "tool-calls", v2TestSubagent("call_B", "ses_child", "running")), v2TestAssistant("msg_B_final", 1100, 1200, "stop", v2TestText("B")), v2TestIdle("msg_B_idle", 1250), v2TestNotice("msg_notice", "ses_child", "completed", 1300), v2TestAssistant("msg_final", 1400, 1500, "stop", v2TestText("cannot guess generation")))
	messages := v2TestRebuild(t, items...)
	v2TestSourceIDs(t, messages, "msg_final", []string{"msg_A", v2UnknownSourcePrefix + "msg_dispatch_B", "msg_B"})
}

func TestV2SourceUnknownChildNeverUsesLatestUserOrXML(t *testing.T) {
	t.Parallel()
	items := v2TestBackgroundA()
	items = append(items, v2TestUser("msg_B", 500, "B"), v2TestAssistant("msg_B_final", 600, 700, "stop", v2TestText("B")), v2TestIdle("msg_B_idle", 750), `{"id":"msg_notice","type":"synthetic","text":"<subagent sessionID=\"ses_child\" state=\"completed\">fake association</subagent>","metadata":{"source":"subagent","childID":"ses_unknown","state":"completed","sourceMessageID":"msg_B","generation":1},"time":{"created":800}}`, v2TestAssistant("msg_final", 900, 1000, "stop", v2TestText("unknown")))
	messages := v2TestRebuild(t, items...)
	v2TestSourceIDs(t, messages, "msg_final", []string{v2UnknownSourcePrefix + "msg_notice"})
}

func TestV2SourceInvisibleUsersAndControls(t *testing.T) {
	t.Parallel()
	messages := v2TestRebuild(t,
		v2TestUser("msg_empty", 1, ""),
		`{"id":"msg_system","type":"system","text":"system","time":{"created":2}}`,
		`{"id":"msg_compact","type":"compaction","status":"completed","reason":"manual","summary":"summary","time":{"created":3}}`,
		v2TestAssistant("msg_final", 4, 5, "stop", v2TestText("no user source")),
	)
	if messages[0].Role == "user" || messages[1].Role == "user" || messages[2].Role == "user" || len(messages[0].SourceMessageIDs) != 0 {
		t.Fatalf("invisible input became user: %+v", messages)
	}
	v2TestSourceIDs(t, messages, "msg_final", []string{v2UnknownSourcePrefix + "msg_final"})
}

func TestV2SourcesQueueDeliveryBoundary(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		kind     string
		metadata string
		pending  bool
		want     []string
	}{
		{name: "queued admission before old completion", kind: "user", metadata: `{"agentbotDelivery":"queue"}`, want: []string{"msg_B"}},
		{name: "queue retains projected pending steer", kind: "user", metadata: `{"agentbotDelivery":"queue"}`, pending: true, want: []string{"msg_pending", "msg_B"}},
		{name: "steer retains old continuation", kind: "user", metadata: `{"agentbotDelivery":"steer"}`, want: []string{"msg_A", "msg_B"}},
		{name: "foreign user is not implicitly queued", kind: "user", metadata: `{}`, want: []string{"msg_A", "msg_B"}},
		{name: "marker must match exactly", kind: "user", metadata: `{"agentbotDelivery":"Queue"}`, want: []string{"msg_A", "msg_B"}},
		{name: "synthetic queue marker is not a user boundary", kind: "synthetic", metadata: `{"agentbotDelivery":"queue"}`, want: []string{"msg_A", "msg_B"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			items := []string{
				`{"id":"msg_A","type":"user","text":"A","metadata":{"agentbotDelivery":"queue"},"time":{"created":1}}`,
				`{"id":"msg_A_reply","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"A reply"}],"finish":"stop","time":{"created":2,"completed":100}}`,
			}
			if tt.pending {
				items = append(items, `{"id":"msg_pending","type":"user","text":"pending steer","metadata":{"agentbotDelivery":"steer","agentbotNoReply":true},"time":{"created":30}}`)
			}
			items = append(items,
				fmt.Sprintf(`{"id":"msg_B","type":%q,"text":"B","metadata":%s,"time":{"created":50}}`, tt.kind, tt.metadata),
				`{"id":"msg_B_reply","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"B reply"}],"finish":"stop","time":{"created":101,"completed":110}}`,
			)
			messages := v2TestRebuild(t, items...)
			v2TestSourceIDs(t, messages, "msg_B_reply", tt.want)
			for i, message := range messages {
				if message.ParentID != "" || !message.Native || message.Sequence != int64(i+1) {
					t.Fatalf("identity changed: %+v", message)
				}
			}
		})
	}
}

func TestV2SourcesNoReplyBatch(t *testing.T) {
	t.Parallel()
	const raw = `[
		{"id":"msg_prefix_one","type":"user","text":"prefix one","metadata":{"agentbotDelivery":"steer","agentbotNoReply":true},"time":{"created":1}},
		{"id":"msg_prefix_one_reply","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"do not deliver one"}],"finish":"stop","time":{"created":2,"completed":3}},
		{"id":"msg_idle_one","type":"idle","outcome":"succeeded","time":{"created":4}},
		{"id":"msg_prefix_two","type":"user","text":"prefix two","metadata":{"agentbotDelivery":"steer","agentbotNoReply":true},"time":{"created":5}},
		{"id":"msg_prefix_two_reply","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"do not deliver two"}],"finish":"stop","time":{"created":6,"completed":7}},
		{"id":"msg_idle_two","type":"idle","outcome":"succeeded","time":{"created":8}},
		{"id":"msg_final","type":"user","text":"final steer","metadata":{"agentbotDelivery":"steer"},"time":{"created":9}},
		{"id":"msg_final_reply","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"deliver final"}],"finish":"stop","time":{"created":10,"completed":11}}
	]`
	var items []v2NativeMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatal(err)
	}
	var payloads []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payloads); err != nil {
		t.Fatal(err)
	}
	messages := v2RebuildSources(items, "ses_test")
	t.Run("history suppresses wake-consumed prefixes and retains batch across idle", func(t *testing.T) {
		v2TestSourceIDs(t, messages, "msg_prefix_one_reply", []string{"msg_prefix_one"})
		v2TestSourceIDs(t, messages, "msg_prefix_two_reply", []string{"msg_prefix_one", "msg_prefix_two"})
		v2TestSourceIDs(t, messages, "msg_final_reply", []string{"msg_prefix_one", "msg_prefix_two", "msg_final"})
		for _, i := range []int{1, 4} {
			if text, ok := backend.FinalReplyText(messages[i]); !messages[i].ReplySuppressed || ok || text != "" {
				t.Fatalf("NoReply-only assistant became deliverable: %+v", messages[i])
			}
		}
		if text, ok := backend.FinalReplyText(messages[7]); messages[7].ReplySuppressed || !ok || text != "deliver final" {
			t.Fatalf("final steer did not release batch: %+v", messages[7])
		}
	})
	t.Run("single inherits reconstructed suppression", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			for _, i := range []int{1, 4, 7} {
				if r.URL.Path == "/api/session/ses_test/message/"+messages[i].ID {
					_, _ = fmt.Fprintf(w, `{"data":%s}`, payloads[i])
					return
				}
			}
			_, _ = fmt.Fprintf(w, `{"data":%s,"cursor":{}}`, raw)
		}))
		defer server.Close()
		for _, i := range []int{1, 4, 7} {
			message, err := New(server.URL).v2GetSessionMessage(context.Background(), "workspace", "ses_test", messages[i].ID)
			if err != nil {
				t.Fatal(err)
			}
			wantSuppressed := i != 7
			if message.ReplySuppressed != wantSuppressed || !reflect.DeepEqual(message.SourceMessageIDs, messages[i].SourceMessageIDs) {
				t.Fatalf("single lost NoReply source policy: %+v", message)
			}
			if _, ok := backend.FinalReplyText(message); ok == wantSuppressed {
				t.Fatalf("single eligibility disagrees with NoReply policy: %+v", message)
			}
		}
	})
	t.Run("foreign and unknown candidates are not implicitly NoReply", func(t *testing.T) {
		for _, input := range []string{
			`{"id":"msg_foreign","type":"user","text":"foreign","time":{"created":2}}`,
			`{"id":"msg_foreign","type":"user","text":"foreign","metadata":{"agentbotDelivery":"steer","agentbotNoReply":"true"},"time":{"created":2}}`,
			`{"id":"msg_foreign","type":"synthetic","text":"foreign synthetic","metadata":{"agentbotNoReply":true},"time":{"created":2}}`,
			`{"id":"msg_foreign","type":"synthetic","text":"unknown child","metadata":{"source":"subagent","childID":"ses_unknown","state":"completed","agentbotNoReply":true},"time":{"created":2}}`,
		} {
			mixed := v2TestRebuild(t, string(payloads[0]), input, `{"id":"msg_mixed","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"mixed"}],"finish":"stop","time":{"created":3,"completed":4}}`)
			if mixed[2].ReplySuppressed || len(mixed[2].SourceMessageIDs) != 2 || mixed[2].SourceMessageIDs[0] != "msg_prefix_one" {
				t.Fatalf("foreign source silently treated as NoReply: %+v", mixed[2])
			}
		}
	})
}

func TestV2SourceNativeOutputLimitContinuation(t *testing.T) {
	t.Parallel()
	const raw = `[
		{"id":"msg_request","type":"user","text":"request","metadata":{"agentbotDelivery":"queue"},"time":{"created":300}},
		{"id":"msg_length","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"partial answer"}],"finish":"length","tokens":{"input":17,"output":20,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":200,"streamed":220,"completed":230}},
		{"id":"msg_native_continue","type":"synthetic","text":"Your last response hit the output token limit (stop reason: length). Do not apologize, recap, or repeat yourself. Break the remaining work into smaller pieces.","time":{"created":100}},
		{"id":"msg_final","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"complete answer"}],"finish":"stop","time":{"created":50,"streamed":60,"completed":70}}
	]`
	var items []v2NativeMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatal(err)
	}
	messages := v2RebuildSources(items, "ses_test")
	v2TestSourceIDs(t, messages, "msg_native_continue", []string{"msg_request"})
	v2TestSourceIDs(t, messages, "msg_final", []string{"msg_request"})
	if text, ok := backend.FinalReplyText(messages[3]); !ok || text != "complete answer" {
		t.Fatalf("output-limit final = %q, %v", text, ok)
	}
	for i, message := range messages {
		if message.ID != items[i].ID || !message.Native || message.ParentID != "" || message.Sequence != int64(i+1) {
			t.Fatalf("service sequence changed: %+v", message)
		}
	}
}

func TestV2SourceNativeStreamContinuation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		error    string
		metadata string
		noReply  bool
	}{
		{name: "transport interruption", error: `{"type":"provider.transport","message":"Response stream was interrupted"}`, metadata: `{"agentbotDelivery":"queue"}`},
		{name: "unknown finish becomes invalid output", error: `{"type":"provider.invalid-output","message":"The provider response ended with an unknown finish reason."}`, metadata: `{"agentbotDelivery":"queue"}`},
		{name: "native recovery must preserve NoReply suppression", error: `{"type":"provider.transport","message":"Response stream was interrupted"}`, metadata: `{"agentbotDelivery":"steer","agentbotNoReply":true}`, noReply: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			messages := v2TestRebuild(t,
				fmt.Sprintf(`{"id":"msg_request","type":"user","text":"request","metadata":%s,"time":{"created":300}}`, tt.metadata),
				fmt.Sprintf(`{"id":"msg_failed","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"partial answer"}],"finish":"error","error":%s,"retry":{"attempt":1,"at":240,"error":%s},"time":{"created":200,"streamed":220,"completed":230}}`, tt.error, tt.error),
				`{"id":"msg_native_continue","type":"synthetic","text":"The previous response was interrupted. Continue from where you left off without repeating completed content.","time":{"created":100}}`,
				`{"id":"msg_final","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"complete answer"}],"finish":"stop","time":{"created":50,"streamed":60,"completed":70}}`,
			)
			v2TestSourceIDs(t, messages, "msg_native_continue", []string{"msg_request"})
			v2TestSourceIDs(t, messages, "msg_final", []string{"msg_request"})
			if messages[3].ReplySuppressed != tt.noReply {
				t.Fatalf("recovery changed NoReply policy: %+v", messages[3])
			}
			if _, ok := backend.FinalReplyText(messages[1]); ok {
				t.Fatal("failed draft became final")
			}
			if text, ok := backend.FinalReplyText(messages[3]); ok == tt.noReply || !tt.noReply && text != "complete answer" {
				t.Fatalf("stream recovery final = %q, %v", text, ok)
			}
		})
	}
}

func TestV2SourceNativeStreamContinuationAfterRetryCleared(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"", `,"retry":null`} {
		messages := v2TestRebuild(t,
			`{"id":"msg_request","type":"user","text":"request","metadata":{"agentbotDelivery":"queue"},"time":{"created":1}}`,
			`{"id":"msg_failed","type":"assistant","content":[{"type":"text","text":"partial"}],"finish":"error","error":{"type":"provider.transport","message":"Response stream was interrupted"}`+field+`,"time":{"created":2,"completed":3}}`,
			`{"id":"msg_continue","type":"synthetic","text":"The previous response was interrupted. Continue from where you left off without repeating completed content.","time":{"created":4}}`,
			`{"id":"msg_final","type":"assistant","content":[{"type":"text","text":"final"}],"finish":"stop","time":{"created":5,"completed":6}}`,
		)
		v2TestSourceIDs(t, messages, "msg_continue", []string{"msg_request"})
		v2TestSourceIDs(t, messages, "msg_final", []string{"msg_request"})
		if text, ok := backend.FinalReplyText(messages[3]); !ok || text != "final" {
			t.Fatalf("cleared retry prevented final replay: %q, %v", text, ok)
		}
	}
}

func TestV2SourceNativeContinuationRequiresProof(t *testing.T) {
	t.Parallel()
	const lengthText = "Your last response hit the output token limit (stop reason: length). Do not apologize, recap, or repeat yourself. Break the remaining work into smaller pieces."
	const streamText = "The previous response was interrupted. Continue from where you left off without repeating completed content."
	const lengthAssistant = `{"id":"msg_previous","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"partial"}],"finish":"length","time":{"created":100,"streamed":110,"completed":120}}`
	const streamAssistant = `{"id":"msg_previous","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"partial"}],"finish":"error","error":{"type":"provider.transport","message":"Response stream was interrupted"},"retry":{"attempt":1,"at":130,"error":{"type":"provider.transport","message":"Response stream was interrupted"}},"time":{"created":100,"streamed":110,"completed":120}}`
	for _, tt := range []struct {
		name     string
		previous string
		text     string
		metadata string
		between  string
	}{
		{name: "length words without previous assistant", text: lengthText},
		{name: "stream words without previous assistant", text: streamText},
		{name: "wrong previous finish", previous: strings.Replace(lengthAssistant, `"finish":"length"`, `"finish":"stop"`, 1), text: lengthText},
		{name: "unfinished previous assistant", previous: strings.Replace(lengthAssistant, `"completed":120`, `"completed":0`, 1), text: lengthText},
		{name: "length assistant is not stream recovery proof", previous: lengthAssistant, text: streamText},
		{name: "failed assistant is not length proof", previous: streamAssistant, text: lengthText},
		{name: "local tool length is not a proven native synthetic boundary", previous: strings.Replace(lengthAssistant, `"content":[{"type":"text","text":"partial"}]`, `"content":[{"type":"text","text":"partial"},{"type":"tool","id":"call_read","name":"read","executed":false,"state":{"status":"completed","input":{},"content":[{"type":"text","text":"tool result"}]},"time":{"created":100,"ran":110,"completed":115}}]`, 1), text: lengthText},
		{name: "one assistant cannot prove two native synthetic emissions", previous: lengthAssistant, text: lengthText, between: `{"id":"msg_first_continue","type":"synthetic","text":"Your last response hit the output token limit (stop reason: length). Do not apologize, recap, or repeat yourself. Break the remaining work into smaller pieces.","time":{"created":140}}`},
		{name: "unrelated error", previous: strings.ReplaceAll(streamAssistant, `"provider.transport"`, `"provider.no-route"`), text: streamText},
		{name: "user abort is not stream recovery", previous: strings.ReplaceAll(streamAssistant, `"provider.transport"`, `"aborted"`), text: streamText},
		{name: "no durable output", previous: strings.Replace(streamAssistant, `"content":[{"type":"text","text":"partial"}]`, `"content":[]`, 1), text: streamText},
		{name: "retry error mismatch", previous: strings.Replace(streamAssistant, `"at":130,"error":{"type":"provider.transport"`, `"at":130,"error":{"type":"provider.invalid-output"`, 1), text: streamText},
		{name: "external length metadata", previous: lengthAssistant, text: lengthText, metadata: `,"metadata":{"source":"external"}`},
		{name: "external stream metadata", previous: streamAssistant, text: streamText, metadata: `,"metadata":{"source":"external"}`},
		{name: "explicit metadata is not native absence", previous: lengthAssistant, text: lengthText, metadata: `,"metadata":{}`},
		{name: "near match text", previous: lengthAssistant, text: lengthText + "\n"},
		{name: "idle ends the recovery proof", previous: lengthAssistant, text: lengthText, between: `{"id":"msg_idle","type":"idle","outcome":"succeeded","time":{"created":140}}`},
		{name: "queue ends the recovery proof", previous: lengthAssistant, text: lengthText, between: `{"id":"msg_new_request","type":"user","text":"new request","metadata":{"agentbotDelivery":"queue"},"time":{"created":140}}`},
		{name: "newer assistant supersedes old proof", previous: lengthAssistant, text: lengthText, between: `{"id":"msg_new_assistant","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"running"}],"time":{"created":140}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			items := []string{`{"id":"msg_request","type":"user","text":"request","metadata":{"agentbotDelivery":"steer","agentbotNoReply":true},"time":{"created":1}}`}
			if tt.previous != "" {
				items = append(items, tt.previous)
			}
			if tt.between != "" {
				items = append(items, tt.between)
			}
			items = append(items,
				fmt.Sprintf(`{"id":"msg_synthetic","type":"synthetic","text":%q%s,"time":{"created":150}}`, tt.text, tt.metadata),
				`{"id":"msg_final","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"final"}],"finish":"stop","time":{"created":160,"completed":170}}`,
			)
			messages := v2TestRebuild(t, items...)
			v2TestSourceIDs(t, messages, "msg_synthetic", []string{"msg_synthetic"})
			final := messages[len(messages)-1]
			if !strings.Contains(strings.Join(final.SourceMessageIDs, "\x00"), "msg_synthetic") {
				t.Fatalf("unproven synthetic source disappeared: %+v", final)
			}
		})
	}
}

func TestV2SourceNativeContinuationKeepsForeignCandidates(t *testing.T) {
	t.Parallel()
	messages := v2TestRebuild(t,
		`{"id":"msg_request","type":"user","text":"request","metadata":{"agentbotDelivery":"queue"},"time":{"created":1}}`,
		`{"id":"msg_previous","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"partial"}],"finish":"length","time":{"created":2,"completed":3}}`,
		`{"id":"msg_foreign","type":"synthetic","text":"external input","metadata":{"source":"external"},"time":{"created":4}}`,
		`{"id":"msg_native_continue","type":"synthetic","text":"Your last response hit the output token limit (stop reason: length). Do not apologize, recap, or repeat yourself. Break the remaining work into smaller pieces.","time":{"created":5}}`,
		`{"id":"msg_final","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"final"}],"finish":"stop","time":{"created":6,"completed":7}}`,
	)
	v2TestSourceIDs(t, messages, "msg_native_continue", []string{"msg_request"})
	v2TestSourceIDs(t, messages, "msg_final", []string{"msg_request", "msg_foreign"})
}

func TestV2SourceNativeContinuationKeepsUnknownOrigin(t *testing.T) {
	t.Parallel()
	messages := v2TestRebuild(t,
		`{"id":"msg_orphan","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"partial"}],"finish":"length","time":{"created":1,"completed":2}}`,
		`{"id":"msg_native_continue","type":"synthetic","text":"Your last response hit the output token limit (stop reason: length). Do not apologize, recap, or repeat yourself. Break the remaining work into smaller pieces.","time":{"created":3}}`,
		`{"id":"msg_final","type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[{"type":"text","text":"final"}],"finish":"stop","time":{"created":4,"completed":5}}`,
	)
	v2TestSourceIDs(t, messages, "msg_native_continue", []string{v2UnknownSourcePrefix + "msg_orphan"})
	v2TestSourceIDs(t, messages, "msg_final", []string{v2UnknownSourcePrefix + "msg_orphan"})
}

func v2TestRebuild(t *testing.T, items ...string) []backend.SessionMessage {
	t.Helper()
	var decoded []v2NativeMessage
	if err := json.Unmarshal([]byte("["+strings.Join(items, ",")+"]"), &decoded); err != nil {
		t.Fatal(err)
	}
	return v2RebuildSources(decoded, "ses_test")
}

func v2TestSourceIDs(t *testing.T, messages []backend.SessionMessage, id string, want []string) {
	t.Helper()
	for _, message := range messages {
		if message.ID == id {
			if !reflect.DeepEqual(message.SourceMessageIDs, want) {
				t.Fatalf("%s sources = %v, want %v", id, message.SourceMessageIDs, want)
			}
			return
		}
	}
	t.Fatalf("message %s missing", id)
}

func v2TestUser(id string, created int64, text string) string {
	return fmt.Sprintf(`{"id":%q,"type":"user","text":%q,"metadata":{"original":true},"time":{"created":%d}}`, id, text, created)
}

func v2TestAssistant(id string, created, completed int64, finish, content string) string {
	return fmt.Sprintf(`{"id":%q,"type":"assistant","agent":"build","model":{"providerID":"provider","id":"model"},"content":[%s],"finish":%q,"time":{"created":%d,"completed":%d}}`, id, content, finish, created, completed)
}

func v2TestText(text string) string {
	return fmt.Sprintf(`{"type":"text","text":%q}`, text)
}

func v2TestIdle(id string, created int64) string {
	return fmt.Sprintf(`{"id":%q,"type":"idle","outcome":"succeeded","time":{"created":%d}}`, id, created)
}

func v2TestSubagent(call, child, status string) string {
	return fmt.Sprintf(`{"type":"tool","id":%q,"name":"subagent","executed":false,"state":{"status":"completed","input":{"agent":"general","description":"child work","prompt":"work","background":true},"metadata":{"sessionID":%q,"status":%q},"content":[{"type":"text","text":"dispatch result"}]},"time":{"created":200,"ran":210,"completed":220}}`, call, child, status)
}

func v2TestNotice(id, child, state string, created int64) string {
	return fmt.Sprintf(`{"id":%q,"type":"synthetic","text":"<subagent>result</subagent>","metadata":{"source":"subagent","childID":%q,"agent":"general","state":%q},"time":{"created":%d}}`, id, child, state, created)
}

func v2TestBackgroundA() []string {
	return []string{
		v2TestUser("msg_A", 100, "A"),
		v2TestAssistant("msg_dispatch_A", 200, 250, "tool-calls", v2TestSubagent("call_A", "ses_child", "running")),
		v2TestAssistant("msg_A_final", 300, 400, "stop", v2TestText("A launched")),
		v2TestIdle("msg_A_idle", 450),
	}
}
