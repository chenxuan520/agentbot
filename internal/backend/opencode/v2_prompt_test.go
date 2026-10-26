package opencode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func v2PromptTestInbox(id, text, delivery string, noReply bool) map[string]any {
	payload := map[string]any{"text": text}
	if noReply {
		payload["metadata"] = map[string]any{"agentbotDelivery": delivery, "agentbotNoReply": true}
	}
	return map[string]any{
		"id": id, "sessionID": "ses_native", "type": "user", "payload": payload,
		"delivery": delivery, "time": map[string]int64{"created": 1},
	}
}

func v2PromptTestUser(id, text string, created int64, noReply bool) map[string]any {
	message := map[string]any{"id": id, "type": "user", "text": text, "time": map[string]int64{"created": created}}
	if noReply {
		message["metadata"] = map[string]any{"agentbotDelivery": "steer", "agentbotNoReply": true}
	}
	return message
}

func v2PromptTestAssistant(id, text, finish string, created, completed int64) map[string]any {
	return map[string]any{
		"id": id, "type": "assistant", "agent": "build", "model": map[string]string{"id": "catalog-id", "providerID": "offline"},
		"content": []any{map[string]string{"type": "text", "text": text}}, "finish": finish,
		"time": map[string]int64{"created": created, "streamed": completed, "completed": completed},
	}
}

func v2PromptTestIdle(id, outcome string, created int64) map[string]any {
	return map[string]any{"id": id, "type": "idle", "outcome": outcome, "time": map[string]int64{"created": created}}
}

func v2PromptTestMessages(t *testing.T, w http.ResponseWriter, messages []any) {
	t.Helper()
	v2TestJSON(t, w, map[string]any{"data": messages, "cursor": map[string]string{}})
}

func TestV2PromptQueueBeforeSSEUsesOwnCompleteNativeHistory(t *testing.T) {
	t.Parallel()
	var callbackID atomic.Value
	var inputID, inputMetadata atomic.Value
	var submits, waits, historyPages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/session/ses_native/inbox":
			if waits.Load() != 1 {
				t.Error("normal prompt did not wait before inspecting its inbox")
			}
			v2TestJSON(t, w, map[string]any{"data": []any{v2PromptTestInbox("msg_external_pending", "external steer", "steer", false)}})
		case "POST /api/session/ses_native/prompt":
			submits.Add(1)
			body := v2TestBody(t, r)
			id, _ := body["id"].(string)
			if id != callbackID.Load() || !regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`).MatchString(id) {
				t.Errorf("submitted id = %q, callback = %v", id, callbackID.Load())
			}
			wantFiles := []any{
				map[string]any{"uri": "data:text/plain;base64,aGVsbG8=", "name": "hello.txt"},
				map[string]any{"uri": "file:///tmp/offline%20file.txt", "name": "offline file.txt"},
			}
			if len(body) != 6 || body["text"] != "hello" || body["delivery"] != "queue" || body["resume"] != true || !reflect.DeepEqual(body["files"], wantFiles) || !reflect.DeepEqual(body["metadata"], map[string]any{"agentbotDelivery": "queue"}) {
				t.Errorf("native prompt body = %#v", body)
			}
			inputID.Store(id)
			metadata, _ := body["metadata"].(map[string]any)
			inputMetadata.Store(metadata)
			v2TestJSON(t, w, map[string]any{"data": v2PromptTestInbox(id, "admission is not an answer", "queue", false)})
		case "POST /api/experimental/session/ses_native/wait":
			if callbackID.Load() == nil {
				t.Error("idle wait preceded callback registration")
			}
			waits.Add(1)
			v2TestNoBody(t, r)
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/session/ses_native/message":
			if waits.Load() != 2 {
				t.Error("history was read before waiting for idle")
			}
			historyPages.Add(1)
			if r.URL.Query().Get("cursor") == "" {
				if r.URL.Query().Get("order") != "asc" {
					t.Errorf("history order = %q", r.URL.Query().Get("order"))
				}
				user := v2PromptTestUser(inputID.Load().(string), "hello", 20, false)
				user["metadata"] = inputMetadata.Load()
				v2TestJSON(t, w, map[string]any{"data": []any{
					v2PromptTestUser("msg_old", "old source", 10, false),
					v2PromptTestAssistant("msg_old_final", "old reply", "stop", 11, 12),
					v2PromptTestIdle("msg_old_idle", "succeeded", 13),
					user,
				}, "cursor": map[string]string{"next": "next-page"}})
				return
			}
			if r.URL.Query().Get("cursor") != "next-page" {
				t.Errorf("history cursor = %q", r.URL.Query().Get("cursor"))
			}
			final := v2PromptTestAssistant("msg_own_final", "unused", "stop", 22, 23)
			final["content"] = []any{
				map[string]string{"type": "reasoning", "text": "not a reply"},
				map[string]string{"type": "text", "text": "first text"},
				map[string]any{
					"type": "tool", "id": "provider_tool", "name": "websearch", "executed": true,
					"state": map[string]any{"status": "completed", "input": map[string]string{"query": "offline"}, "content": []any{map[string]string{"type": "text", "text": "offline result"}}},
					"time":  map[string]int64{"created": 22, "completed": 23},
				},
				map[string]string{"type": "text", "text": " final reply "},
				map[string]string{"type": "text", "text": " "},
			}
			v2PromptTestMessages(t, w, []any{
				v2PromptTestAssistant("msg_partial", "progress", "tool-calls", 21, 21), final,
				v2PromptTestIdle("msg_own_idle", "succeeded", 24),
				v2PromptTestUser("msg_later", "later independent request", 30, false),
				v2PromptTestAssistant("msg_later_final", "wrong latest reply", "stop", 31, 32),
				v2PromptTestIdle("msg_later_idle", "succeeded", 33),
			})
		default:
			t.Errorf("unexpected legacy/SSE request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	callbacks := 0
	result, err := New(server.URL).v2Prompt(context.Background(), "/tmp/offline", "ses_native", "hello", []backend.Attachment{
		{URL: "data:text/plain;base64,aGVsbG8=", Filename: "hello.txt", Mime: "text/plain"},
		{URL: "file:///tmp/offline%20file.txt", Filename: "offline file.txt", Mime: "text/plain"},
	}, backend.PromptOptions{OnPromptMessage: func(id string) error {
		callbacks++
		callbackID.Store(id)
		return nil
	}})
	if err != nil || result.MessageID != "msg_own_final" || result.ReplyText != "final reply" || result.SessionID != "ses_native" || !result.Native || result.ParentID != "" {
		t.Fatalf("prompt result = %+v, %v", result, err)
	}
	if !reflect.DeepEqual(result.SourceMessageIDs, []string{callbackID.Load().(string)}) || callbacks != 1 || submits.Load() != 1 || waits.Load() != 2 || historyPages.Load() != 2 {
		t.Fatalf("sources=%v callbacks=%d submits=%d waits=%d history pages=%d", result.SourceMessageIDs, callbacks, submits.Load(), waits.Load(), historyPages.Load())
	}
}

func TestV2PromptNoReplyWaitsIdleThenAdmitsWithoutResume(t *testing.T) {
	t.Parallel()
	waitStarted := make(chan struct{})
	release := make(chan struct{})
	var callbackID atomic.Value
	var submits, waits, puts, deletes atomic.Int32
	system := " \n# Constraints\nKeep the exact JSON string.\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
			waits.Add(1)
			v2TestNoBody(t, r)
			close(waitStarted)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
			puts.Add(1)
			if r.URL.Path != v2InstructionPath("ses_native")+"/"+v2SystemKey(callbackID.Load().(string)) || !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`).MatchString(strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")) {
				t.Errorf("invalid instruction key: %s", r.URL.Path)
			}
			if body := v2TestBody(t, r); !reflect.DeepEqual(body, map[string]any{"value": system}) {
				t.Errorf("system entry must be the original JSON string: %#v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/prompt":
			submits.Add(1)
			body := v2TestBody(t, r)
			id, _ := body["id"].(string)
			want := map[string]any{"id": id, "text": "batch input", "delivery": "steer", "resume": false, "metadata": map[string]any{"agentbotDelivery": "steer", "agentbotNoReply": true}}
			if id != callbackID.Load() || !reflect.DeepEqual(body, want) || puts.Load() != 1 {
				t.Errorf("NoReply body = %#v, system puts=%d", body, puts.Load())
			}
			v2TestJSON(t, w, map[string]any{"data": v2PromptTestInbox(id, "batch input", "steer", true)})
		case r.Method == http.MethodDelete:
			deletes.Add(1)
			t.Errorf("NoReply prematurely deleted an instruction: %s", r.URL)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("NoReply must not read history or wake LLM: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type outcome struct {
		result backend.PromptResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := New(server.URL).v2Prompt(ctx, "/tmp/offline", "ses_native", "batch input", nil, backend.PromptOptions{
			NoReply: true, System: system, OnPromptMessage: func(id string) error { callbackID.Store(id); return nil },
		})
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-waitStarted:
	case <-ctx.Done():
		t.Fatal("NoReply did not wait for idle")
	}
	if submits.Load() != 0 || puts.Load() != 0 {
		t.Fatal("NoReply mutated or submitted while the active loop could consume it")
	}
	release <- struct{}{}
	got := <-done
	if got.err != nil || got.result.MessageID != callbackID.Load() || got.result.SessionID != "ses_native" || got.result.ReplyText != "" || !got.result.Native || got.result.ParentID != "" {
		t.Fatalf("admission = %+v, %v", got.result, got.err)
	}
	if !reflect.DeepEqual(got.result.SourceMessageIDs, []string{callbackID.Load().(string)}) || submits.Load() != 1 || waits.Load() != 1 || deletes.Load() != 0 {
		t.Fatalf("admission sources=%v submits=%d waits=%d deletes=%d", got.result.SourceMessageIDs, submits.Load(), waits.Load(), deletes.Load())
	}
}

func TestV2PromptBatchLastInputSteersAndCleansOnlyBatchKeys(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var callbackIDs, requestIDs, deleted []string
	var users []any
	pending := []any{v2PromptTestInbox("msg_external_pending", "external steer", "steer", false)}
	entries := map[string]string{
		"other-client-key":                  "external context",
		v2SystemKey("msg_external_pending"): "external pending context",
		v2SystemKey("msg_old_batch"):        "previous batch context",
	}
	var waits, inboxReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
			waits.Add(1)
			v2TestNoBody(t, r)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/inbox":
			inboxReads.Add(1)
			v2TestJSON(t, w, map[string]any{"data": pending})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
			if waits.Load() != int32(len(callbackIDs)) {
				t.Error("batch system entry did not follow its input's idle wait")
			}
			key := strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
			body := v2TestBody(t, r)
			value, ok := body["value"].(string)
			if !ok || len(body) != 1 || len(callbackIDs) == 0 || key != v2SystemKey(callbackIDs[len(callbackIDs)-1]) {
				t.Errorf("instruction entry = %s %#v", key, body)
			}
			entries[key] = value
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/prompt":
			body := v2TestBody(t, r)
			id, _ := body["id"].(string)
			text, _ := body["text"].(string)
			requestIDs = append(requestIDs, id)
			noReply := len(requestIDs) < 3
			if len(callbackIDs) != len(requestIDs) || callbackIDs[len(callbackIDs)-1] != id || body["delivery"] != "steer" || body["resume"] != !noReply {
				t.Errorf("batch prompt = %#v, callbacks=%v", body, callbackIDs)
			}
			wantMetadata := map[string]any{"agentbotDelivery": "steer"}
			if noReply {
				wantMetadata["agentbotNoReply"] = true
			}
			if !reflect.DeepEqual(body["metadata"], wantMetadata) {
				t.Errorf("batch metadata = %#v, want %#v", body["metadata"], wantMetadata)
			}
			user := v2PromptTestUser(id, text, int64(10+len(requestIDs)), noReply)
			user["metadata"] = body["metadata"]
			users = append(users, user)
			item := v2PromptTestInbox(id, text, "steer", noReply)
			if noReply {
				pending = append(pending, item)
			}
			v2TestJSON(t, w, map[string]any{"data": item})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/message":
			if len(users) != 3 || waits.Load() != 4 {
				t.Errorf("history before last batch wait: users=%d waits=%d", len(users), waits.Load())
			}
			messages := append([]any(nil), users...)
			messages = append(messages, v2PromptTestAssistant("msg_batch_final", "batch result", "stop", 20, 21), v2PromptTestIdle("msg_batch_idle", "succeeded", 22))
			v2PromptTestMessages(t, w, messages)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
			key := strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
			deleted = append(deleted, key)
			delete(entries, key)
			v2TestNoBody(t, r)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected batch request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := New(server.URL)
	callback := func(id string) error {
		mu.Lock()
		defer mu.Unlock()
		callbackIDs = append(callbackIDs, id)
		return nil
	}
	for _, text := range []string{"first input", "second input"} {
		result, err := client.v2Prompt(context.Background(), "/tmp/offline", "ses_native", text, nil, backend.PromptOptions{NoReply: true, System: text + " system", OnPromptMessage: callback})
		if err != nil || result.ReplyText != "" {
			t.Fatalf("NoReply = %+v, %v", result, err)
		}
		mu.Lock()
		retained := entries[v2SystemKey(result.MessageID)] == text+" system" && len(deleted) == 0
		mu.Unlock()
		if !retained {
			t.Fatal("a preceding batch instruction was deleted before the final input")
		}
	}
	result, err := client.v2Prompt(context.Background(), "/tmp/offline", "ses_native", "last input", nil, backend.PromptOptions{System: "last system", OnPromptMessage: callback})
	if err != nil || result.MessageID != "msg_batch_final" || result.ReplyText != "batch result" || result.ParentID != "" || !result.Native {
		t.Fatalf("batch result = %+v, %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(result.SourceMessageIDs, callbackIDs) || !reflect.DeepEqual(requestIDs, callbackIDs) || len(deleted) != 3 || len(entries) != 3 || inboxReads.Load() != 1 {
		t.Fatalf("sources=%v callbacks=%v requests=%v deleted=%v entries=%v inbox reads=%d", result.SourceMessageIDs, callbackIDs, requestIDs, deleted, entries, inboxReads.Load())
	}
	for i, id := range callbackIDs {
		if deleted[i] != v2SystemKey(id) {
			t.Fatalf("deleted=%v, want only keys for %v", deleted, callbackIDs)
		}
	}
}

func TestV2PromptModelSwitchUsesRefAndUnsetPreservesSessionModel(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"configured/catalog-id#xhigh", "configured/catalog-id", ""} {
		t.Run(model, func(t *testing.T) {
			var inputID, callbackID, inputMetadata atomic.Value
			var modelCalls, systemCalls, submits, waits, callbacks atomic.Int32
			system := "private system text must stay outside prompt metadata"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/inbox":
					if waits.Load() != 1 {
						t.Error("normal prompt read inbox before its initial idle wait")
					}
					v2TestJSON(t, w, map[string]any{"data": []any{}})
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/model":
					if waits.Load() != 1 || systemCalls.Load() != 0 || submits.Load() != 0 {
						t.Error("model switch must follow the initial idle wait and precede system/admission")
					}
					modelCalls.Add(1)
					ref := map[string]any{"id": "catalog-id", "providerID": "configured"}
					if strings.Contains(model, "#") {
						ref["variant"] = "xhigh"
					}
					if body := v2TestBody(t, r); !reflect.DeepEqual(body, map[string]any{"model": ref}) {
						t.Errorf("model switch body = %#v", body)
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
					if waits.Load() != 1 || submits.Load() != 0 || (modelCalls.Load() == 1) != (model != "") {
						t.Error("system entry must follow idle/model and precede admission")
					}
					if r.URL.Path != v2InstructionPath("ses_native")+"/"+v2SystemKey(callbackID.Load().(string)) || !reflect.DeepEqual(v2TestBody(t, r), map[string]any{"value": system}) {
						t.Error("system entry did not use the registered input ID and original JSON string")
					}
					systemCalls.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/prompt":
					body := v2TestBody(t, r)
					if _, ok := body["model"]; ok {
						t.Error("model must not be sent in the native prompt body")
					}
					if waits.Load() != 1 || systemCalls.Load() != 1 || (modelCalls.Load() == 1) != (model != "") {
						t.Error("admission did not follow idle/model/system, or an empty model reset the session")
					}
					if len(body) != 5 || body["delivery"] != "queue" || body["resume"] != true || !reflect.DeepEqual(body["metadata"], map[string]any{"agentbotDelivery": "queue"}) {
						t.Errorf("normal metadata must match delivery without leaking system text: %#v", body)
					}
					id := body["id"].(string)
					if id != callbackID.Load() {
						t.Error("admission changed the registered input ID")
					}
					submits.Add(1)
					inputID.Store(id)
					metadata, _ := body["metadata"].(map[string]any)
					inputMetadata.Store(metadata)
					v2TestJSON(t, w, map[string]any{"data": v2PromptTestInbox(id, "hello", "queue", false)})
				case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
					if callbacks.Load() != 1 || (waits.Load() == 0 && (modelCalls.Load() != 0 || systemCalls.Load() != 0 || submits.Load() != 0)) {
						t.Error("the first wait must follow callback registration and precede mutations/admission")
					}
					waits.Add(1)
					v2TestNoBody(t, r)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/message":
					if waits.Load() != 2 || submits.Load() != 1 {
						t.Error("initial idle wait must not count as a final response")
					}
					user := v2PromptTestUser(inputID.Load().(string), "hello", 1, false)
					user["metadata"] = inputMetadata.Load()
					v2PromptTestMessages(t, w, []any{user, v2PromptTestAssistant("msg_final", "reply", "stop", 2, 3), v2PromptTestIdle("msg_idle", "succeeded", 4)})
				case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
					if waits.Load() != 2 {
						t.Error("system entry was removed before the post-admission idle wait")
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected model request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			if _, err := New(server.URL).v2Prompt(context.Background(), "/tmp/offline", "ses_native", "hello", nil, backend.PromptOptions{Model: model, System: system, OnPromptMessage: func(id string) error {
				callbacks.Add(1)
				callbackID.Store(id)
				return nil
			}}); err != nil {
				t.Fatalf("prompt: %v", err)
			}
			if callbacks.Load() != 1 || submits.Load() != 1 || waits.Load() != 2 {
				t.Fatalf("callbacks=%d submits=%d waits=%d, want one ID/admission and two waits", callbacks.Load(), submits.Load(), waits.Load())
			}
		})
	}
}

func TestV2PromptCallbackFailureMakesZeroRequests(t *testing.T) {
	t.Parallel()
	callbackErr := errors.New("callback registration failed")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	for _, noReply := range []bool{false, true} {
		callbacks := 0
		_, err := New(server.URL).v2Prompt(context.Background(), "/tmp/offline", "ses_native", "hello", nil, backend.PromptOptions{
			NoReply: noReply, Model: "configured/catalog-id#xhigh", System: "constraints", SystemMessageIDs: []string{"msg_consumed_prefix"},
			OnPromptMessage: func(id string) error {
				callbacks++
				if !regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`).MatchString(id) {
					t.Errorf("invalid callback id = %q", id)
				}
				return callbackErr
			},
		})
		if err != callbackErr || callbacks != 1 || calls.Load() != 0 {
			t.Fatalf("callback error=%v callbacks=%d HTTP requests=%d", err, callbacks, calls.Load())
		}
	}
}

func TestV2PromptWaitCancellationAndTimeoutNeverResubmit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name           string
		noReply        bool
		timeout        bool
		afterAdmission bool
	}{
		{name: "normal before admission cancellation"}, {name: "normal before admission timeout", timeout: true},
		{name: "normal after admission cancellation", afterAdmission: true}, {name: "normal after admission timeout", afterAdmission: true, timeout: true},
		{name: "NoReply cancellation", noReply: true}, {name: "NoReply timeout", noReply: true, timeout: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			waitStarted := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			var callbackID atomic.Value
			var submits, waits, callbacks, deletes, modelCalls, systemCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/inbox":
					v2TestJSON(t, w, map[string]any{"data": []any{}})
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/model":
					modelCalls.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
					systemCalls.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/prompt":
					submits.Add(1)
					body := v2TestBody(t, r)
					if body["id"] != callbackID.Load() || !reflect.DeepEqual(body["metadata"], map[string]any{"agentbotDelivery": "queue"}) {
						t.Errorf("admission changed the input ID or delivery marker: %#v", body)
					}
					v2TestJSON(t, w, map[string]any{"data": v2PromptTestInbox(body["id"].(string), "hello", "queue", false)})
				case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
					if callbacks.Load() != 1 {
						t.Error("wait preceded callback registration")
					}
					wait := waits.Add(1)
					v2TestNoBody(t, r)
					if tt.afterAdmission && wait == 1 {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					once.Do(func() { close(waitStarted) })
					select {
					case <-r.Context().Done():
					case <-release:
					}
				case r.Method == http.MethodDelete:
					deletes.Add(1)
					t.Errorf("cleanup ran without an idle acknowledgement: %s", r.URL)
				default:
					t.Errorf("unexpected retry, interrupt or history request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			defer close(release)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := 3 * time.Second
			if tt.timeout {
				timeout = 250 * time.Millisecond
			}
			client := NewWithOptions(server.URL, Options{HTTPTimeout: timeout})
			done := make(chan error, 1)
			go func() {
				_, err := client.v2Prompt(ctx, "/tmp/offline", "ses_native", "hello", nil, backend.PromptOptions{NoReply: tt.noReply, Model: "configured/catalog-id#xhigh", System: "constraints", OnPromptMessage: func(id string) error {
					callbacks.Add(1)
					callbackID.Store(id)
					return nil
				}})
				done <- err
			}()
			select {
			case <-waitStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("wait was not reached")
			}
			if !tt.timeout {
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("wait did not honor cancellation or the HTTP timeout")
			}
			wantErr := context.Canceled
			if tt.timeout {
				wantErr = context.DeadlineExceeded
			}
			wantSubmits, wantWaits := int32(0), int32(1)
			if tt.afterAdmission {
				wantSubmits, wantWaits = 1, 2
			}
			if !errors.Is(err, wantErr) || submits.Load() != wantSubmits || waits.Load() != wantWaits || callbacks.Load() != 1 || deletes.Load() != 0 || modelCalls.Load() != wantSubmits || systemCalls.Load() != wantSubmits {
				t.Fatalf("error=%v submits=%d waits=%d callbacks=%d deletes=%d model=%d system=%d", err, submits.Load(), waits.Load(), callbacks.Load(), deletes.Load(), modelCalls.Load(), systemCalls.Load())
			}
		})
	}
}

func TestV2PromptTimeoutBoundsWholeOperation(t *testing.T) {
	t.Parallel()
	var callbacks, submits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
		switch r.URL.Path {
		case "/api/experimental/session/ses_native/wait":
			v2TestNoBody(t, r)
			w.WriteHeader(http.StatusNoContent)
		case "/api/session/ses_native/inbox":
			v2TestJSON(t, w, map[string]any{"data": []any{}})
		case "/api/session/ses_native/model":
			w.WriteHeader(http.StatusNoContent)
		case "/api/session/ses_native/prompt":
			submits.Add(1)
			body := v2TestBody(t, r)
			v2TestJSON(t, w, map[string]any{"data": v2PromptTestInbox(body["id"].(string), "hello", "queue", false)})
		default:
			t.Errorf("outer deadline failed to stop the operation: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewWithOptions(server.URL, Options{HTTPTimeout: 500 * time.Millisecond})
	_, err := client.v2Prompt(context.Background(), "/tmp/offline", "ses_native", "hello", nil, backend.PromptOptions{Model: "configured/catalog-id", OnPromptMessage: func(id string) error { callbacks.Add(1); return nil }})
	if !errors.Is(err, context.DeadlineExceeded) || callbacks.Load() != 1 || submits.Load() > 1 {
		t.Fatalf("outer timeout error=%v callbacks=%d submits=%d", err, callbacks.Load(), submits.Load())
	}
}

func TestV2PromptTransportFailureNeverRetriesWithNewIDOrContinue(t *testing.T) {
	t.Parallel()
	for _, status := range []int{0, 409, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var callbackID atomic.Value
			var callbacks, submits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/experimental/session/ses_native/wait":
					v2TestNoBody(t, r)
					w.WriteHeader(http.StatusNoContent)
				case "/api/session/ses_native/inbox":
					v2TestJSON(t, w, map[string]any{"data": []any{}})
				case "/api/session/ses_native/prompt":
					submits.Add(1)
					body := v2TestBody(t, r)
					if body["id"] != callbackID.Load() || body["text"] != "original input" || !reflect.DeepEqual(body["metadata"], map[string]any{"agentbotDelivery": "queue"}) {
						t.Errorf("retry or replacement input = %#v", body)
					}
					if status == 0 {
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Errorf("hijack: %v", err)
							return
						}
						_ = connection.Close()
						return
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte("offline-secret"))
				default:
					t.Errorf("unexpected retry, wait or abort: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			_, err := New(server.URL).v2Prompt(context.Background(), "/tmp/offline", "ses_native", "original input", nil, backend.PromptOptions{OnPromptMessage: func(id string) error { callbacks.Add(1); callbackID.Store(id); return nil }})
			if err == nil || strings.Contains(err.Error(), "offline-secret") || callbacks.Load() != 1 || submits.Load() != 1 {
				t.Fatalf("transport error=%v callbacks=%d submits=%d", err, callbacks.Load(), submits.Load())
			}
		})
	}
}

func TestV2PromptAdmissionAndIdleNeverReplaceOwnFinal(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"empty history", "other source", "no final", "unfinished", "tool calls", "local tool", "error", "retry", "retry status", "interrupted"} {
		t.Run(kind, func(t *testing.T) {
			var inputID atomic.Value
			var submits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/session/ses_native/inbox":
					v2TestJSON(t, w, map[string]any{"data": []any{}})
				case "/api/session/ses_native/prompt":
					submits.Add(1)
					body := v2TestBody(t, r)
					inputID.Store(body["id"].(string))
					v2TestJSON(t, w, map[string]any{"data": v2PromptTestInbox(body["id"].(string), "ack text is not a reply", "queue", false)})
				case "/api/experimental/session/ses_native/wait":
					w.WriteHeader(http.StatusNoContent)
				case "/api/session/ses_native/message":
					messages := []any{
						v2PromptTestUser("msg_foreign", "foreign input", 1, false),
						v2PromptTestAssistant("msg_foreign_final", "foreign reply", "stop", 2, 3),
						v2PromptTestIdle("msg_foreign_idle", "succeeded", 4),
					}
					if kind == "empty history" {
						v2PromptTestMessages(t, w, []any{})
						return
					}
					if kind == "other source" {
						v2PromptTestMessages(t, w, messages)
						return
					}
					messages = append(messages, v2PromptTestUser(inputID.Load().(string), "hello", 5, false))
					assistant := v2PromptTestAssistant("msg_not_final", "candidate", "stop", 6, 7)
					outcome := "succeeded"
					switch kind {
					case "no final":
						assistant["content"] = []any{}
					case "unfinished":
						assistant["time"] = map[string]int64{"created": 6}
					case "tool calls":
						assistant["finish"] = "tool-calls"
					case "local tool":
						assistant["content"] = []any{map[string]string{"type": "text", "text": "not final"}, map[string]any{
							"type": "tool", "id": "local_call", "name": "read", "executed": false,
							"state": map[string]any{"status": "completed", "input": map[string]string{"filePath": "/tmp/offline"}, "content": []any{map[string]string{"type": "text", "text": "file"}}},
							"time":  map[string]int64{"created": 6, "completed": 7},
						}}
					case "error":
						assistant["error"] = map[string]string{"type": "ProviderError", "message": "failed"}
						outcome = "failed"
					case "retry":
						assistant["retry"] = map[string]any{"attempt": 1, "at": 8, "error": map[string]string{"type": "ProviderError", "message": "retrying"}}
					case "retry status":
						assistant["content"] = []any{map[string]string{"type": "text", "text": "已重试成功。"}}
					case "interrupted":
						assistant["error"] = map[string]string{"type": "Interrupted", "message": "Interrupted"}
						outcome = "interrupted"
					}
					messages = append(messages, assistant, v2PromptTestIdle("msg_terminal_idle", outcome, 9))
					v2PromptTestMessages(t, w, messages)
				default:
					t.Errorf("unexpected automatic continuation: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			result, err := New(server.URL).v2Prompt(context.Background(), "/tmp/offline", "ses_native", "hello", nil, backend.PromptOptions{})
			if err == nil || result.ReplyText != "" || submits.Load() != 1 {
				t.Fatalf("non-final result=%+v error=%v submits=%d", result, err, submits.Load())
			}
		})
	}
}

func TestV2PromptReplyChoosesOwnFirstValidSequenceAndCopiesSources(t *testing.T) {
	t.Parallel()
	own := func(id string, sequence int64, text string) backend.SessionMessage {
		return backend.SessionMessage{ID: id, SessionID: "ses_native", ParentID: "not-a-native-parent", Role: "assistant", Native: true, Sequence: sequence, CompletedAt: 1, Finish: "stop", SourceMessageIDs: []string{"msg_batch", "msg_input"}, Parts: []backend.SessionMessagePart{{Type: "text", Text: text}}}
	}
	messages := []backend.SessionMessage{own("msg_valid_newest", 42, "newest own reply"), own("msg_valid_older", 9, "older reply"), own("msg_invalid", 50, "error"), own("msg_foreign", 100, "wrong latest run"), own("msg_legacy", 200, "legacy reply")}
	messages[2].HasError = true
	messages[3].SourceMessageIDs = []string{"msg_foreign_input"}
	messages[4].Native = false
	result, err := v2PromptReply("ses_native", "msg_input", messages)
	if err != nil || result.MessageID != "msg_valid_older" || result.ReplyText != "older reply" || result.ParentID != "" || !result.Native || !reflect.DeepEqual(result.SourceMessageIDs, []string{"msg_batch", "msg_input"}) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	result.SourceMessageIDs[0] = "changed"
	if messages[1].SourceMessageIDs[0] != "msg_batch" {
		t.Fatal("result source IDs alias the history")
	}
}

func TestV2PromptAbortCleansOnlyIdentifiedBatchKeysAfterIdle(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending batch", true: "interrupted batch"}[interrupted], func(t *testing.T) {
			var mu sync.Mutex
			entries := map[string]string{
				"other-client-key":                  "foreign",
				v2SystemKey("msg_previous"):         "previous batch",
				v2SystemKey("msg_current"):          "current batch",
				v2SystemKey("msg_pending_no_reply"): "pending batch",
				v2SystemKey("msg_other_queued"):     "independent queue",
			}
			var deleted []string
			var waited atomic.Bool
			var inboxReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/interrupt":
					v2TestNoBody(t, r)
					if r.URL.RawQuery != "" {
						t.Error("abort must not request resume")
					}
					v2TestJSON(t, w, map[string]bool{"interrupted": interrupted})
				case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/inbox":
					inboxReads.Add(1)
					if waited.Load() {
						t.Error("abort selected a later batch instead of snapshotting its inbox before idle")
					}
					v2TestJSON(t, w, map[string]any{"data": []any{v2PromptTestInbox("msg_pending_no_reply", "pending", "steer", true), v2PromptTestInbox("msg_other_queued", "other input", "queue", false)}})
				case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
					v2TestNoBody(t, r)
					waited.Store(true)
					entries[v2SystemKey("msg_later_no_reply")] = "later batch"
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodGet && r.URL.Path == v2InstructionPath("ses_native"):
					data := make([]any, 0, len(entries))
					for key, value := range entries {
						data = append(data, map[string]string{"key": key, "value": value})
					}
					v2TestJSON(t, w, map[string]any{"data": data})
				case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/message":
					if r.URL.Query().Get("type") == "idle" {
						if r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("order") != "desc" {
							t.Errorf("idle marker query = %s", r.URL)
						}
						v2PromptTestMessages(t, w, []any{v2PromptTestIdle("msg_interrupted_idle", "interrupted", 9), v2PromptTestIdle("msg_previous_idle", "succeeded", 4)})
						return
					}
					v2PromptTestMessages(t, w, []any{
						v2PromptTestUser("msg_previous", "previous input", 1, false), v2PromptTestAssistant("msg_previous_final", "old reply", "stop", 2, 3), v2PromptTestIdle("msg_previous_idle", "succeeded", 4),
						v2PromptTestUser("msg_current", "current input", 5, false), v2PromptTestAssistant("msg_partial", "partial", "other", 6, 7), v2PromptTestIdle("msg_interrupted_idle", "interrupted", 9),
					})
				case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
					if !waited.Load() {
						t.Error("instruction deleted before idle acknowledgement")
					}
					key := strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
					deleted = append(deleted, key)
					delete(entries, key)
					v2TestNoBody(t, r)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected resume/session removal: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			if err := New(server.URL).v2AbortSession(context.Background(), "ses_native"); err != nil {
				t.Fatalf("abort: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			wantDeleted := []string{v2SystemKey("msg_pending_no_reply")}
			if interrupted {
				wantDeleted = append(wantDeleted, v2SystemKey("msg_current"))
			}
			sort.Strings(deleted)
			sort.Strings(wantDeleted)
			if !reflect.DeepEqual(deleted, wantDeleted) || inboxReads.Load() != 1 || entries["other-client-key"] == "" || entries[v2SystemKey("msg_previous")] == "" || entries[v2SystemKey("msg_other_queued")] == "" || entries[v2SystemKey("msg_later_no_reply")] == "" {
				t.Fatalf("deleted=%v remaining=%v inbox reads=%d", deleted, entries, inboxReads.Load())
			}
		})
	}
}

func TestV2PromptAbortWaitFailureDoesNotDeleteInstructions(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/api/session/ses_native/interrupt":
			v2TestJSON(t, w, map[string]bool{"interrupted": true})
		case "/api/session/ses_native/inbox":
			v2TestJSON(t, w, map[string]any{"data": []any{}})
		case "/api/experimental/session/ses_native/wait":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("cleanup ran after a failed wait: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	if err := New(server.URL).v2AbortSession(context.Background(), "ses_native"); err == nil || calls.Load() != 3 {
		t.Fatalf("abort error=%v requests=%d", err, calls.Load())
	}
}

func TestV2PromptConsumedSystemPrefixIsCleanedWithoutInbox(t *testing.T) {
	t.Parallel()
	prefixID := "msg_ConsumedPrefix"
	var mu sync.Mutex
	entries := map[string]string{v2SystemKey(prefixID): "consumed prefix", v2SystemKey("msg_foreign"): "foreign platform key", "other-client-key": "foreign client"}
	var inputID string
	var metadata any
	var deleted []string
	var callbacks, submits, waits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
			waits.Add(1)
			v2TestNoBody(t, r)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/inbox":
			v2TestJSON(t, w, map[string]any{"data": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/prompt":
			submits.Add(1)
			body := v2TestBody(t, r)
			inputID = body["id"].(string)
			metadata = body["metadata"]
			if len(body) != 5 || body["delivery"] != "steer" || body["resume"] != true || !reflect.DeepEqual(metadata, map[string]any{"agentbotDelivery": "steer"}) {
				t.Errorf("consumed prefix must steer the final input without putting SystemMessageIDs on wire: %#v", body)
			}
			v2TestJSON(t, w, map[string]any{"data": v2PromptTestInbox(inputID, "last input", "steer", false)})
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/message":
			user := v2PromptTestUser(inputID, "last input", 5, false)
			user["metadata"] = metadata
			v2PromptTestMessages(t, w, []any{
				v2PromptTestUser(prefixID, "prefix input", 1, true), v2PromptTestAssistant("msg_prefix_final", "prefix consumed", "stop", 2, 3), v2PromptTestIdle("msg_prefix_idle", "succeeded", 4),
				user, v2PromptTestAssistant("msg_final", "batch done", "stop", 6, 7), v2PromptTestIdle("msg_final_idle", "succeeded", 8),
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
			key := strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
			if waits.Load() != 2 || key != v2SystemKey(prefixID) {
				t.Errorf("unexpected or premature prefix cleanup: %s", r.URL)
			}
			deleted = append(deleted, key)
			delete(entries, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	result, err := New(server.URL).v2Prompt(context.Background(), "/tmp/offline", "ses_native", "last input", nil, backend.PromptOptions{SystemMessageIDs: []string{prefixID, prefixID}, OnPromptMessage: func(id string) error { callbacks.Add(1); return nil }})
	if err != nil || result.MessageID != "msg_final" || result.ReplyText != "batch done" || !result.Native || result.ParentID != "" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(deleted, []string{v2SystemKey(prefixID)}) || len(entries) != 2 || callbacks.Load() != 1 || submits.Load() != 1 || waits.Load() != 2 {
		t.Fatalf("deleted=%v entries=%v callbacks=%d submits=%d waits=%d", deleted, entries, callbacks.Load(), submits.Load(), waits.Load())
	}
}

func TestV2PromptRejectedAdmissionCleansOnlyCurrentSystemKey(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 403, 404, 405, 413, 415, 422} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var mu sync.Mutex
			prefixID := "msg_prefix"
			entries := map[string]string{v2SystemKey(prefixID): "prefix", v2SystemKey("msg_foreign"): "foreign"}
			var currentKey string
			var deleted []string
			var submits, callbacks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
					if submits.Load() != 0 {
						t.Error("rejected admission must not resume or wait for a new run")
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/inbox":
					v2TestJSON(t, w, map[string]any{"data": []any{}})
				case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
					currentKey = strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
					entries[currentKey] = v2TestBody(t, r)["value"].(string)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/prompt":
					submits.Add(1)
					body := v2TestBody(t, r)
					if currentKey != v2SystemKey(body["id"].(string)) {
						t.Error("current system key does not belong to the admitted input ID")
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"_tag":"InvalidRequestError","message":"Unable to read attachment","field":"files"}`))
				case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
					key := strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
					if key != currentKey {
						t.Errorf("rejection deleted a prefix or foreign system key: %s", key)
					}
					deleted = append(deleted, key)
					delete(entries, key)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected rejection request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			_, err := New(server.URL).v2Prompt(context.Background(), "/tmp/offline", "ses_native", "input", []backend.Attachment{{URL: "file:///tmp/offline-missing-attachment", Filename: "missing.txt"}}, backend.PromptOptions{System: "current system", SystemMessageIDs: []string{prefixID}, OnPromptMessage: func(id string) error { callbacks.Add(1); return nil }})
			var responseErr *v2HTTPError
			if !errors.As(err, &responseErr) || responseErr.Status != status {
				t.Fatalf("admission error=%v, want HTTP %d", err, status)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(deleted, []string{currentKey}) || len(entries) != 2 || callbacks.Load() != 1 || submits.Load() != 1 {
				t.Fatalf("deleted=%v current=%q entries=%v callbacks=%d submits=%d", deleted, currentKey, entries, callbacks.Load(), submits.Load())
			}
		})
	}
}

func TestV2PromptUncertainAdmissionResolvesOriginalIDBeforeCleanup(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		response   string
		state      string
		lookupFail bool
		wantDelete bool
	}{
		{name: "network not admitted", response: "network", wantDelete: true},
		{name: "network pending", response: "network", state: "pending"},
		{name: "network delivered", response: "network", state: "delivered"},
		{name: "decode delivered", response: "decode", state: "delivered"},
		{name: "invalid ack not admitted", response: "invalid", wantDelete: true},
		{name: "invalid ack delivered", response: "invalid", state: "delivered"},
		{name: "conflict delivered", response: "conflict", state: "delivered"},
		{name: "lookup unknown", response: "network", lookupFail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			entries := map[string]string{v2SystemKey("msg_prefix"): "prefix", v2SystemKey("msg_foreign"): "foreign"}
			var inputID, currentKey string
			var submits, callbacks, inboxLookups, singleLookups, modelCalls atomic.Int32
			var deleted []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
					if submits.Load() != 0 {
						t.Error("uncertain admission must not start or wait for another run")
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/inbox":
					if submits.Load() != 0 {
						inboxLookups.Add(1)
						if tt.lookupFail {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						if tt.state == "pending" {
							v2TestJSON(t, w, map[string]any{"data": []any{v2PromptTestInbox(inputID, "input", "steer", false)}})
							return
						}
					}
					v2TestJSON(t, w, map[string]any{"data": []any{}})
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/model":
					modelCalls.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
					currentKey = strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
					entries[currentKey] = v2TestBody(t, r)["value"].(string)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/prompt":
					submits.Add(1)
					body := v2TestBody(t, r)
					inputID = body["id"].(string)
					if currentKey != v2SystemKey(inputID) {
						t.Error("admission changed the registered system key")
					}
					switch tt.response {
					case "network":
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Errorf("hijack: %v", err)
							return
						}
						_ = connection.Close()
					case "decode":
						_, _ = w.Write([]byte(`{"data":`))
					case "invalid":
						v2TestJSON(t, w, map[string]any{"data": v2PromptTestInbox("msg_wrong_ack", "input", "steer", false)})
					case "conflict":
						w.WriteHeader(http.StatusConflict)
					}
				case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/session/ses_native/message/"):
					singleLookups.Add(1)
					if r.URL.Path != "/api/session/ses_native/message/"+inputID || r.URL.RawQuery != "" {
						t.Errorf("verification did not use the original admitted input ID: %s", r.URL)
					}
					if tt.state == "delivered" {
						v2TestJSON(t, w, map[string]any{"data": v2PromptTestUser(inputID, "input", 1, false)})
						return
					}
					w.WriteHeader(http.StatusNotFound)
				case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
					key := strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
					if key != currentKey || !tt.wantDelete {
						t.Errorf("uncertain/admitted input or foreign key was deleted: %s", key)
					}
					deleted = append(deleted, key)
					delete(entries, key)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected admission verification request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			_, err := New(server.URL).v2Prompt(context.Background(), "/tmp/offline", "ses_native", "input", nil, backend.PromptOptions{System: "current system", Model: "offline/catalog-id", SystemMessageIDs: []string{"msg_prefix"}, OnPromptMessage: func(id string) error { callbacks.Add(1); return nil }})
			if err == nil || submits.Load() != 1 || callbacks.Load() != 1 || modelCalls.Load() != 1 || inboxLookups.Load() != 1 || (tt.state != "pending" && singleLookups.Load() != 1) {
				t.Fatalf("error=%v submits=%d callbacks=%d model=%d inbox=%d single=%d", err, submits.Load(), callbacks.Load(), modelCalls.Load(), inboxLookups.Load(), singleLookups.Load())
			}
			mu.Lock()
			defer mu.Unlock()
			wantRemaining := 3
			if tt.wantDelete {
				wantRemaining = 2
			}
			if len(entries) != wantRemaining || (len(deleted) == 1) != tt.wantDelete || entries[v2SystemKey("msg_prefix")] == "" || entries[v2SystemKey("msg_foreign")] == "" {
				t.Fatalf("deleted=%v entries=%v, wantDelete=%v", deleted, entries, tt.wantDelete)
			}
		})
	}
}

func TestV2PromptReplyReturnsA1BeforeA2ForSameInput(t *testing.T) {
	t.Parallel()
	messages := []backend.SessionMessage{
		{ID: "msg_a2", SessionID: "ses_native", Native: true, Sequence: 2, Role: "assistant", CompletedAt: 2, Finish: "stop", SourceMessageIDs: []string{"msg_input"}, Parts: []backend.SessionMessagePart{{Type: "text", Text: "result"}}},
		{ID: "msg_a1", SessionID: "ses_native", Native: true, Sequence: 1, Role: "assistant", CompletedAt: 1, Finish: "stop", SourceMessageIDs: []string{"msg_input"}, Parts: []backend.SessionMessagePart{{Type: "text", Text: "started"}}},
	}
	result, err := v2PromptReply("ses_native", "msg_input", messages)
	if err != nil || result.MessageID != "msg_a1" || result.ReplyText != "started" || result.ParentID != "" || !reflect.DeepEqual(result.SourceMessageIDs, []string{"msg_input"}) {
		t.Fatalf("result=%+v error=%v, want A1 before A2", result, err)
	}
}

type v2PromptTestAdmissionTransport func(*http.Request) (*http.Response, error)

func (f v2PromptTestAdmissionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestV2PromptFailedAdmissionCleanupUsesBoundedUncancelledContext(t *testing.T) {
	t.Parallel()
	type cleanupContextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), cleanupContextKey{}, "test-request-context"))
	defer cancel()
	var mu sync.Mutex
	entries := map[string]string{v2SystemKey("msg_prefix"): "prefix", v2SystemKey("msg_foreign"): "foreign"}
	var inputID, currentKey string
	var submits, callbacks, checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/session/ses_native/wait":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/inbox":
			v2TestJSON(t, w, map[string]any{"data": []any{}})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/"):
			currentKey = strings.TrimPrefix(r.URL.Path, v2InstructionPath("ses_native")+"/")
			entries[currentKey] = v2TestBody(t, r)["value"].(string)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_native/prompt":
			submits.Add(1)
			inputID = v2TestBody(t, r)["id"].(string)
			cancel()
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = connection.Close()
		case r.Method == http.MethodGet && r.URL.Path == "/api/session/ses_native/message/"+inputID:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete && r.URL.Path == v2InstructionPath("ses_native")+"/"+currentKey:
			delete(entries, currentKey)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected cleanup request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := New(server.URL)
	client.httpClient.Transport = v2PromptTestAdmissionTransport(func(r *http.Request) (*http.Response, error) {
		if submits.Load() > 0 && (r.Method == http.MethodGet || r.Method == http.MethodDelete) {
			checks.Add(1)
			deadline, ok := r.Context().Deadline()
			if !ok || r.Context().Err() != nil || !deadline.After(time.Now()) || deadline.After(time.Now().Add(abortTimeout)) {
				t.Error("admission verification/cleanup must use a fresh context bounded by abortTimeout")
			}
			if r.Context().Value(cleanupContextKey{}) != "test-request-context" {
				t.Error("admission cleanup discarded the request context values")
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	_, err := client.v2Prompt(ctx, "/tmp/offline", "ses_native", "input", nil, backend.PromptOptions{System: "current system", SystemMessageIDs: []string{"msg_prefix"}, OnPromptMessage: func(id string) error { callbacks.Add(1); return nil }})
	if !errors.Is(err, context.Canceled) || submits.Load() != 1 || callbacks.Load() != 1 || checks.Load() != 3 {
		t.Fatalf("error=%v submits=%d callbacks=%d background checks=%d", err, submits.Load(), callbacks.Load(), checks.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(entries) != 2 || entries[v2SystemKey("msg_prefix")] == "" || entries[v2SystemKey("msg_foreign")] == "" {
		t.Fatalf("cancelled admission cleanup left or deleted the wrong entries: %v", entries)
	}
}
