package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
			_, _ = w.Write([]byte(`{"info":{"id":"msg_aborted","sessionID":"session-1","parentID":"msg_aborted_parent","error":{"name":"MessageAbortedError","data":{"message":"Aborted"}}},"parts":[{"type":"step-start"}]}`))
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
	if result.MessageID != "msg_aborted" {
		t.Fatalf("message id = %q, want msg_aborted", result.MessageID)
	}
	if result.ParentID != "msg_aborted_parent" {
		t.Fatalf("parent id = %q, want msg_aborted_parent", result.ParentID)
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

func TestPromptMessageIDForEveryAttempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "network error"},
		{name: "server error", status: http.StatusInternalServerError, body: "boom"},
		{name: "assistant error", status: http.StatusOK, body: `{"info":{"error":{"name":"APIError","data":{"message":"retry"}}},"parts":[]}`},
		{name: "tool calls", status: http.StatusOK, body: `{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"partial"},{"type":"step-finish","reason":"tool-calls"}]}`},
		{name: "other finish", status: http.StatusOK, body: `{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"partial"},{"type":"step-finish","reason":"other"}]}`},
		{name: "empty output", status: http.StatusOK, body: `{"info":{"sessionID":"session-1"},"parts":[{"type":"step-finish","reason":"stop"}]}`},
		{name: "retry status", status: http.StatusOK, body: `{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"已重试成功。"},{"type":"step-finish","reason":"stop"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var callbackIDs []string
			var requestIDs []string
			var requestTexts []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/", "/session/session-1/abort":
					w.WriteHeader(http.StatusOK)
				case "/session/session-1/message":
					var body struct {
						MessageID string `json:"messageID"`
						Parts     []struct {
							Text string `json:"text"`
						} `json:"parts"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					mu.Lock()
					requestIDs = append(requestIDs, body.MessageID)
					callIndex := len(requestIDs)
					if len(callbackIDs) != callIndex || callbackIDs[callIndex-1] != body.MessageID {
						t.Errorf("callbacks = %v, request IDs = %v; callback must precede request", callbackIDs, requestIDs)
					}
					if len(body.Parts) != 1 {
						t.Errorf("parts = %+v, want one text part", body.Parts)
					} else {
						requestTexts = append(requestTexts, body.Parts[0].Text)
					}
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					if callIndex == 1 {
						if tt.status == 0 {
							connection, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Errorf("hijack request: %v", err)
								return
							}
							connection.Close()
							return
						}
						w.WriteHeader(tt.status)
						_, _ = w.Write([]byte(tt.body))
						return
					}
					_, _ = w.Write([]byte(`{"info":{"id":"msg_actual_assistant","sessionID":"session-1","parentID":"msg_background_parent","role":"assistant"},"parts":[{"type":"text","text":"final reply"},{"type":"step-finish","reason":"stop"}]}`))
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := New(server.URL)
			client.httpClient = server.Client()
			options := backend.PromptOptions{OnPromptMessage: func(messageID string) error {
				mu.Lock()
				defer mu.Unlock()
				callbackIDs = append(callbackIDs, messageID)
				return nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := client.Prompt(ctx, "/tmp/workspace", "session-1", "hello", nil, options)
			if err != nil {
				t.Fatalf("prompt: %v", err)
			}
			if result.MessageID != "msg_actual_assistant" || result.ParentID != "msg_background_parent" || result.SessionID != "session-1" || result.ReplyText != "final reply" {
				t.Fatalf("result = %+v, want actual assistant ID, background parent and final reply", result)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requestIDs) != 2 || len(callbackIDs) != 2 {
				t.Fatalf("requests = %v, callbacks = %v, want two each", requestIDs, callbackIDs)
			}
			pattern := regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
			for _, id := range requestIDs {
				if !pattern.MatchString(id) {
					t.Fatalf("invalid message ID: %q", id)
				}
			}
			if requestIDs[0] == requestIDs[1] || requestIDs[0][:16] >= requestIDs[1][:16] {
				t.Fatalf("request IDs must be distinct and ascending: %v", requestIDs)
			}
			if len(requestTexts) != 2 || requestTexts[0] != "hello" || requestTexts[1] != "继续" {
				t.Fatalf("request texts = %v, want hello then 继续", requestTexts)
			}
		})
	}
}

func TestPromptCallbackFailureDoesNotSendHTTP(t *testing.T) {
	t.Parallel()

	callbackErr := errors.New("record prompt failed")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"info":{"sessionID":"session-1"},"parts":[{"type":"text","text":"unexpected"}]}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	callbacks := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.Prompt(ctx, "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{OnPromptMessage: func(messageID string) error {
		callbacks++
		if messageID == "" {
			t.Error("callback received empty message ID")
		}
		return callbackErr
	}})
	if err != callbackErr {
		t.Fatalf("error = %v, want callback error", err)
	}
	if callbacks != 1 || requests.Load() != 0 {
		t.Fatalf("callbacks = %d, HTTP requests = %d, want 1 and 0", callbacks, requests.Load())
	}
}

func TestPromptCallbackFailureOnContinue(t *testing.T) {
	t.Parallel()

	callbackErr := errors.New("record continue failed")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/session/session-1/message" {
			t.Errorf("unexpected request after callback failure: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"sessionID":"session-1"},"parts":[{"type":"step-finish","reason":"stop"}]}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	callbacks := 0
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.Prompt(ctx, "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{OnPromptMessage: func(messageID string) error {
		callbacks++
		if callbacks == 2 {
			return callbackErr
		}
		return nil
	}})
	if err != callbackErr {
		t.Fatalf("error = %v, want callback error", err)
	}
	if callbacks != 2 || requests.Load() != 1 {
		t.Fatalf("callbacks = %d, HTTP requests = %d, want 2 and 1", callbacks, requests.Load())
	}
}

func TestPromptReturnsMessageIDAndPreservesDefaults(t *testing.T) {
	t.Parallel()

	for _, noReply := range []bool{false, true} {
		t.Run(strconv.FormatBool(noReply), func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if _, ok := body["messageID"]; ok {
					t.Error("messageID must be omitted without callback")
				}
				if got, ok := body["noReply"]; ok != noReply || (noReply && got != true) {
					t.Errorf("noReply = %v, present = %v, want %v", got, ok, noReply)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"info":{"id":"msg_assistant","role":"assistant"},"parts":[{"type":"text","text":"first"},{"type":"text","text":"last"},{"type":"step-finish","reason":"stop"}]}`))
			}))
			defer server.Close()

			client := New(server.URL)
			client.httpClient = server.Client()
			result, err := client.Prompt(context.Background(), "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{NoReply: noReply})
			if err != nil {
				t.Fatalf("prompt: %v", err)
			}
			wantReply := "last"
			if noReply {
				wantReply = ""
			}
			if result.MessageID != "msg_assistant" || result.SessionID != "session-1" || result.ReplyText != wantReply {
				t.Fatalf("result = %+v, want assistant ID, fallback session and reply %q", result, wantReply)
			}
		})
	}
}

func TestPromptParentIDParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		parentField string
		parentID    string
		noReply     bool
	}{
		{name: "parent present", parentField: `,"parentID":"msg_parent"`, parentID: "msg_parent"},
		{name: "parent absent"},
		{name: "parent null", parentField: `,"parentID":null`},
		{name: "no reply parent", parentField: `,"parentID":"msg_parent"`, parentID: "msg_parent", noReply: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"info":{"id":"msg_assistant","sessionID":"session-1"` + tt.parentField + `},"parts":[{"type":"text","text":"reply"},{"type":"step-finish","reason":"stop"}]}`))
			}))
			defer server.Close()

			client := New(server.URL)
			client.httpClient = server.Client()
			result, err := client.Prompt(context.Background(), "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{NoReply: tt.noReply})
			if err != nil {
				t.Fatalf("prompt: %v", err)
			}
			if result.ParentID != tt.parentID {
				t.Fatalf("parent id = %q, want %q", result.ParentID, tt.parentID)
			}
		})
	}
}

func TestPromptNoReplyWithMessageCallback(t *testing.T) {
	t.Parallel()

	var callbackID atomic.Value
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body struct {
			MessageID string `json:"messageID"`
			NoReply   bool   `json:"noReply"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !body.NoReply || body.MessageID == "" || callbackID.Load() != body.MessageID {
			t.Errorf("body = %+v, callback ID = %v", body, callbackID.Load())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"id":"msg_no_reply","sessionID":"session-1"},"parts":[{"type":"step-finish","reason":"tool-calls"}]}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := client.Prompt(ctx, "/tmp/workspace", "session-1", "hello", nil, backend.PromptOptions{
		NoReply: true,
		OnPromptMessage: func(messageID string) error {
			callbackID.Store(messageID)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.MessageID != "msg_no_reply" || result.ReplyText != "" || requests.Load() != 1 {
		t.Fatalf("result = %+v, requests = %d, want response ID, no reply and one request", result, requests.Load())
	}
}

func TestPromptMessageIDTimestampAndMonotonicity(t *testing.T) {
	t.Parallel()

	before := time.Now().UnixMilli()
	pattern := regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	var previous uint64
	for i := 0; i < 5000; i++ {
		id, err := newPromptMessageID()
		if err != nil {
			t.Fatalf("new prompt message ID: %v", err)
		}
		if !pattern.MatchString(id) {
			t.Fatalf("invalid message ID: %q", id)
		}
		encoded, err := strconv.ParseUint(id[4:16], 16, 64)
		if err != nil {
			t.Fatalf("parse timestamp: %v", err)
		}
		if encoded <= previous {
			t.Fatalf("timestamp and counter = %x, previous = %x", encoded, previous)
		}
		previous = encoded
		timestamp := int64(encoded / 0x1000)
		if mask := int64((1 << 36) - 1); timestamp < before&mask || timestamp > (time.Now().UnixMilli()+2)&mask {
			t.Fatalf("timestamp = %d, want encoded current milliseconds", timestamp)
		}
	}
}
