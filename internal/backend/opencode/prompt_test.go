package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func TestIsRetryStatusOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "exact retry success", input: "已重试成功。", want: true},
		{name: "plain retry success", input: "重试成功", want: true},
		{name: "retry success with spaces", input: "  已重试成功  ", want: true},
		{name: "retry checked done", input: "已从失败处重试并核实完成。", want: true},
		{name: "retry confirmed done", input: "已从失败处重试并确认完成", want: true},
		{name: "retry completed", input: "重试完成。", want: true},
		{name: "retry success plus content", input: "已重试成功。当前结论：报警已恢复。", want: false},
		{name: "retry checked plus content", input: "已从失败处重试并核实完成。当前结论：服务已恢复。", want: false},
		{name: "normal reply", input: "已处理，建议继续观察15分钟。", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryStatusOnly(tt.input); got != tt.want {
				t.Fatalf("isRetryStatusOnly(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestPromptContinuesAfterRetryStatusOnly(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requestTexts := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		case r.URL.Path == "/session/session-1/message":
			var body struct {
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			mu.Lock()
			requestTexts = append(requestTexts, body.Parts[0].Text)
			callIndex := len(requestTexts)
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			if callIndex == 1 {
				_, _ = w.Write([]byte(`{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"已从失败处重试并核实完成。"},{"type":"step-finish","reason":"stop"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"final reply"},{"type":"step-finish","reason":"stop"}]}`))
			return
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	result, err := client.Prompt(context.Background(), "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.ReplyText != "final reply" {
		t.Fatalf("reply text = %q, want final reply", result.ReplyText)
	}
	if len(requestTexts) != 2 {
		t.Fatalf("request count = %d, want 2", len(requestTexts))
	}
	if requestTexts[1] != incompleteReplyContinuePrompt {
		t.Fatalf("continue prompt = %q, want %q", requestTexts[1], incompleteReplyContinuePrompt)
	}
}

func TestPromptSendsModelOverrideWhenSet(t *testing.T) {
	t.Parallel()

	type capturedModel struct {
		ProviderID string `json:"providerID"`
		ModelID    string `json:"modelID"`
	}
	var mu sync.Mutex
	var gotModel *capturedModel
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/session-1/message" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Model *capturedModel `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		mu.Lock()
		gotModel = body.Model
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"ok"},{"type":"step-finish","reason":"stop"}]}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	if _, err := client.Prompt(context.Background(), "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{Model: "openai/gpt-5.5-pro"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotModel == nil {
		t.Fatal("expected model override in request body, got none")
	}
	if gotModel.ProviderID != "openai" || gotModel.ModelID != "gpt-5.5-pro" {
		t.Fatalf("model override = %+v, want openai/gpt-5.5-pro", gotModel)
	}
}

func TestPromptOmitsModelWhenUnset(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	hasModelKey := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/session-1/message" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		mu.Lock()
		_, hasModelKey = raw["model"]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"ok"},{"type":"step-finish","reason":"stop"}]}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	if _, err := client.Prompt(context.Background(), "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hasModelKey {
		t.Fatal("expected no model key in request body when model is unset")
	}
}

func TestPromptStopsAfterAbort(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	messageCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/session/session-1/message":
			mu.Lock()
			messageCalls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			// Mirror opencode's aborted-message response: HTTP 200 with an error
			// in info and no text parts.
			_, _ = w.Write([]byte(`{"info":{"sessionID":"session-1","error":{"name":"MessageAbortedError","data":{"message":"Aborted"}}},"parts":[{"type":"step-start"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	result, err := client.Prompt(context.Background(), "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.ReplyText != abortedReplyText {
		t.Fatalf("reply text = %q, want %q", result.ReplyText, abortedReplyText)
	}
	if result.SessionID != "session-1" {
		t.Fatalf("session id = %q, want session-1", result.SessionID)
	}
	mu.Lock()
	defer mu.Unlock()
	if messageCalls != 1 {
		t.Fatalf("message call count = %d, want 1 (abort must not re-prompt)", messageCalls)
	}
}

func TestPromptContinuesAfterServerErrorWhileWebAvailable(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requestTexts := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		case r.URL.Path == "/session/session-1/abort":
			w.WriteHeader(http.StatusOK)
			return
		case r.URL.Path == "/session/session-1/message":
			var body struct {
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			mu.Lock()
			requestTexts = append(requestTexts, body.Parts[0].Text)
			callIndex := len(requestTexts)
			mu.Unlock()

			if callIndex == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`boom`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"final reply"},{"type":"step-finish","reason":"stop"}]}`))
			return
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	result, err := client.Prompt(context.Background(), "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.ReplyText != "final reply" {
		t.Fatalf("reply text = %q, want final reply", result.ReplyText)
	}
	if len(requestTexts) != 2 {
		t.Fatalf("request count = %d, want 2", len(requestTexts))
	}
	if requestTexts[1] != errorContinuePrompt {
		t.Fatalf("retry prompt = %q, want %q", requestTexts[1], errorContinuePrompt)
	}
}
