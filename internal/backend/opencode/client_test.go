package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func TestNewWithOptionsUsesConfiguredTimeout(t *testing.T) {
	t.Parallel()

	client := NewWithOptions("http://example.com", Options{HTTPTimeout: 42 * time.Second})
	if client.httpClient.Timeout != 42*time.Second {
		t.Fatalf("timeout = %s, want %s", client.httpClient.Timeout, 42*time.Second)
	}
}

func TestNewWithOptionsFallsBackToDefaultTimeout(t *testing.T) {
	t.Parallel()

	client := NewWithOptions("http://example.com", Options{})
	if client.httpClient.Timeout != defaultHTTPTimeout {
		t.Fatalf("timeout = %s, want %s", client.httpClient.Timeout, defaultHTTPTimeout)
	}
}

func TestGetSessionReturnsDirectory(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_test" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ses_test","directory":"/tmp/workspace"}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()

	info, err := client.GetSession(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if info.ID != "ses_test" {
		t.Fatalf("session id = %q, want ses_test", info.ID)
	}
	if info.Directory != "/tmp/workspace" {
		t.Fatalf("directory = %q, want /tmp/workspace", info.Directory)
	}
}

func TestGetSessionMessagesParsesTranscriptParts(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_test/message" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"info":{"id":"msg-user","role":"user","time":{"created":1779785200000}},"parts":[{"type":"text","text":"hello"}]},
			{"info":{"id":"msg-assistant","role":"assistant","time":{"created":1779785210000},"tokens":{"total":14983,"input":14952,"output":7,"reasoning":24,"cache":{"read":3,"write":1}}},"parts":[{"type":"step-start"},{"type":"reasoning","text":"hidden"},{"type":"text","text":"partial"},{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"pwd"}}},{"type":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"/root/agent-bot/README.md"}}},{"type":"step-finish","reason":"tool-calls"}]}
		]`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()

	messages, err := client.GetSessionMessages(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("get session messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	if messages[1].ID != "msg-assistant" || messages[1].Role != "assistant" {
		t.Fatalf("unexpected assistant message: %+v", messages[1])
	}
	if len(messages[1].Parts) != 6 {
		t.Fatalf("assistant part count = %d, want 6", len(messages[1].Parts))
	}
	if messages[1].Parts[2].Text != "partial" {
		t.Fatalf("text part = %q, want partial", messages[1].Parts[2].Text)
	}
	if messages[1].Parts[3].Tool != "bash" || messages[1].Parts[3].ToolStatus != "completed" || messages[1].Parts[3].ToolInputSummary != "pwd" {
		t.Fatalf("tool part = %+v, want bash/completed", messages[1].Parts[3])
	}
	if messages[1].Parts[4].Tool != "read" || messages[1].Parts[4].ToolStatus != "completed" || messages[1].Parts[4].ToolInputSummary != "/root/agent-bot/README.md" {
		t.Fatalf("read tool part = %+v, want read/completed with path", messages[1].Parts[4])
	}
	if messages[1].Parts[5].Reason != "tool-calls" {
		t.Fatalf("finish reason = %q, want tool-calls", messages[1].Parts[5].Reason)
	}
	wantTokens := backend.TokenUsage{Total: 14983, Input: 14952, Output: 7, Reasoning: 24, CacheRead: 3, CacheWrite: 1}
	if messages[1].Tokens != wantTokens {
		t.Fatalf("assistant tokens = %+v, want %+v", messages[1].Tokens, wantTokens)
	}
}

func TestListModelsGroupsProvidersAndReportsCurrent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config/providers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"providers":[{"id":"openai","name":"OpenAI","models":{"gpt-5.5":{"id":"gpt-5.5","name":"GPT-5.5","limit":{"context":400000}},"gpt-5.4":{"id":"gpt-5.4","name":"GPT-5.4","limit":{"context":1050000}}}}],"default":{"openai":"gpt-5.5"}}`))
		case "/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"model":"openai/gpt-5.4"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()

	catalog, err := client.ListModels(context.Background(), "/tmp/workspace")
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if catalog.Current != "openai/gpt-5.4" {
		t.Fatalf("current = %q, want openai/gpt-5.4", catalog.Current)
	}
	if len(catalog.Providers) != 1 {
		t.Fatalf("provider count = %d, want 1", len(catalog.Providers))
	}
	provider := catalog.Providers[0]
	if provider.ID != "openai" || provider.Name != "OpenAI" || provider.Default != "gpt-5.5" {
		t.Fatalf("unexpected provider: %+v", provider)
	}
	// Models are sorted by id for deterministic output.
	if len(provider.Models) != 2 || provider.Models[0].ID != "gpt-5.4" || provider.Models[1].ID != "gpt-5.5" {
		t.Fatalf("unexpected models: %+v", provider.Models)
	}
	if provider.Models[0].ContextLimit != 1050000 || provider.Models[0].Name != "GPT-5.4" {
		t.Fatalf("unexpected first model: %+v", provider.Models[0])
	}
}

func TestCurrentModelCombinesProviderAndModel(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"opencode/big-pickle"}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()

	model, err := client.CurrentModel(context.Background(), "/tmp/workspace")
	if err != nil {
		t.Fatalf("current model: %v", err)
	}
	if model != "opencode/big-pickle" {
		t.Fatalf("model = %q, want opencode/big-pickle", model)
	}
}
