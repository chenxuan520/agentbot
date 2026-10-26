package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func v2TestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func v2TestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode request: %v", err)
	}
	if r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("content type = %q", r.Header.Get("Content-Type"))
	}
	return body
}

func v2TestNoBody(t *testing.T, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) != 0 {
		t.Errorf("body = %q, error = %v, want no body", body, err)
	}
}

func v2TestModel(providerID, id, name string) map[string]any {
	return map[string]any{
		"id": id, "modelID": "remote-" + id, "providerID": providerID, "name": name,
		"capabilities": map[string]any{"tools": true, "input": []string{"text"}, "output": []string{"text"}},
		"variants":     []any{}, "time": map[string]int{"released": 0}, "cost": []any{},
		"status": "active", "enabled": true, "limit": map[string]int{"context": 200000, "output": 32000},
	}
}

func TestV2ClientHealthUsesNativeInfoAndAuthentication(t *testing.T) {
	t.Parallel()
	for _, username := range []string{"", "custom-user"} {
		t.Run(username, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/info" || r.URL.RawQuery != "" {
					t.Errorf("health request = %s %s", r.Method, r.URL)
				}
				wantUsername := username
				if wantUsername == "" {
					wantUsername = "opencode"
				}
				gotUsername, password, ok := r.BasicAuth()
				if !ok || gotUsername != wantUsername || password != "offline-token" {
					t.Error("missing configured Basic authentication")
				}
				v2TestJSON(t, w, map[string]any{"version": "2.1.4", "pid": 0, "urls": []any{}, "paths": map[string]string{"tmp": "/tmp"}})
			}))
			defer server.Close()
			client := NewWithOptions(server.URL, Options{Username: username, Password: "offline-token"})
			if err := client.v2Health(context.Background()); err != nil {
				t.Fatalf("health: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("requests = %d, want one native request", calls.Load())
			}
		})
	}
}

func TestV2ClientHealthRejectsNotReadyAndInvalidVersions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "starting", status: 503, body: `{"version":"2.1.4"}`},
		{name: "stopping", status: 503, body: `{"version":"2.1.4"}`},
		{name: "failed", status: 500, body: `offline-token`},
		{name: "unauthorized", status: 401, body: `offline-token`},
		{name: "legacy", status: 200, body: `{"version":"1.18.16","healthy":true}`},
		{name: "unknown", status: 200, body: `{"version":"3.0.0"}`},
		{name: "missing version", status: 200, body: `{}`},
		{name: "wrapped info", status: 200, body: `{"data":{"version":"2.1.4"}}`},
		{name: "html", status: 200, body: `<!DOCTYPE html>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/info" {
					t.Errorf("unexpected fallback: %s", r.URL.Path)
				}
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := NewWithOptions(server.URL, Options{AutoDetect: true, Password: "offline-token"})
			if err := client.v2Health(context.Background()); err == nil || strings.Contains(err.Error(), "offline-token") {
				t.Fatalf("health error = %v", err)
			}
			if tt.status == 503 || tt.status == 500 || tt.status == 401 {
				if _, err := client.useV2(context.Background()); err == nil || client.version != 0 {
					t.Fatalf("readiness failure downgraded detection: version=%d error=%v", client.version, err)
				}
			} else if calls.Load() != 1 {
				t.Fatalf("native health requests = %d, want one", calls.Load())
			}
		})
	}
}

func TestV2ClientSessionUsesLocationAndQuestionPermission(t *testing.T) {
	t.Parallel()
	workspace := "/tmp/offline workspace/subdir"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("session query = %q, want none", r.URL.RawQuery)
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/session":
			body := v2TestBody(t, r)
			want := map[string]any{
				"location":    map[string]any{"directory": workspace},
				"permissions": []any{map[string]any{"action": "question", "resource": "*", "effect": "deny"}},
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("create body = %#v, want %#v", body, want)
			}
		case "GET /api/session/ses_native":
			v2TestNoBody(t, r)
		default:
			t.Errorf("unexpected session request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		v2TestJSON(t, w, map[string]any{"data": map[string]any{
			"id": "ses_native", "location": map[string]string{"directory": workspace},
			"model": map[string]string{"id": "catalog-model", "providerID": "configured", "variant": "xhigh"},
		}})
	}))
	defer server.Close()
	client := New(server.URL)
	id, err := client.v2CreateSession(context.Background(), workspace)
	if err != nil || id != "ses_native" {
		t.Fatalf("create session = %q, %v", id, err)
	}
	info, err := client.v2GetSession(context.Background(), id)
	if err != nil || info != (backend.SessionInfo{ID: id, Directory: workspace}) {
		t.Fatalf("get session = %+v, %v", info, err)
	}
}

func TestV2ClientSessionRejectsEmptyNativeIDs(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"data":null}`, `{"data":{"id":""}}`, `{"id":"legacy-id"}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := New(server.URL)
			if _, err := client.v2CreateSession(context.Background(), "/tmp/offline"); err == nil {
				t.Fatal("create accepted an empty native id")
			}
			if _, err := client.v2GetSession(context.Background(), "ses_native"); err == nil {
				t.Fatal("get accepted an empty native id")
			}
		})
	}
}

func TestV2ClientRejectsMissingSessionBeforeHTTP(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := New(server.URL)
	if _, err := client.v2GetSession(context.Background(), " "); err == nil {
		t.Fatal("get accepted an empty session")
	}
	if err := client.v2AbortSession(context.Background(), " "); err == nil {
		t.Fatal("abort accepted an empty session")
	}
	if err := client.v2CompactSession(context.Background(), "/tmp/offline", " "); err == nil {
		t.Fatal("compact accepted an empty session")
	}
	if calls.Load() != 0 {
		t.Fatalf("requests = %d, want zero", calls.Load())
	}
}

func TestV2ClientAbortInterruptsThenWaitsWithoutResume(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "active"}[interrupted], func(t *testing.T) {
			var step atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch step.Add(1) {
				case 1:
					if r.Method != http.MethodPost || r.URL.Path != "/api/session/ses_native/interrupt" || r.URL.RawQuery != "" {
						t.Errorf("interrupt = %s %s", r.Method, r.URL)
					}
					v2TestNoBody(t, r)
					v2TestJSON(t, w, map[string]bool{"interrupted": interrupted})
				case 2:
					if r.Method != http.MethodGet || r.URL.Path != "/api/session/ses_native/inbox" {
						t.Errorf("inbox snapshot = %s %s", r.Method, r.URL)
					}
					v2TestJSON(t, w, map[string]any{"data": []any{}})
				case 3:
					if r.Method != http.MethodPost || r.URL.Path != "/api/experimental/session/ses_native/wait" {
						t.Errorf("wait = %s %s", r.Method, r.URL)
					}
					v2TestNoBody(t, r)
					w.WriteHeader(http.StatusNoContent)
				case 4:
					if r.Method != http.MethodGet || r.URL.Path != "/api/experimental/session/ses_native/instructions/entries" {
						t.Errorf("cleanup = %s %s", r.Method, r.URL)
					}
					v2TestJSON(t, w, map[string]any{"data": []any{}})
				default:
					t.Errorf("unexpected abort request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			if err := New(server.URL).v2AbortSession(context.Background(), "ses_native"); err != nil {
				t.Fatalf("abort: %v", err)
			}
			if step.Load() != 4 {
				t.Fatalf("abort requests = %d, want four", step.Load())
			}
		})
	}
}

func TestV2CompactUsesReturnedIDAndRequiresCompletedMessage(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		status  string
		errType string
		wantErr bool
	}{
		{name: "completed", status: "completed"},
		{name: "failed", status: "failed", errType: "ProviderError", wantErr: true},
		{name: "interrupted", status: "failed", errType: "Interrupted", wantErr: true},
		{name: "still running", status: "running", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var step atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch step.Add(1) {
				case 1:
					if r.Method != http.MethodPost || r.URL.Path != "/api/session/ses_native/compact" || r.URL.RawQuery != "" {
						t.Errorf("compact request = %s %s", r.Method, r.URL)
					}
					body := v2TestBody(t, r)
					id, _ := body["id"].(string)
					if len(body) != 2 || body["delivery"] != "steer" || !regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`).MatchString(id) {
						t.Errorf("compact body = %#v", body)
					}
					v2TestJSON(t, w, map[string]any{"data": map[string]any{
						"id": "msg_returned", "sessionID": "ses_native", "type": "compaction",
						"payload": map[string]any{}, "delivery": "steer", "time": map[string]int64{"created": 1},
					}})
				case 2:
					if r.Method != http.MethodPost || r.URL.Path != "/api/experimental/session/ses_native/wait" {
						t.Errorf("compact wait = %s %s", r.Method, r.URL)
					}
					v2TestNoBody(t, r)
					w.WriteHeader(http.StatusNoContent)
				case 3:
					if r.Method != http.MethodGet || r.URL.Path != "/api/session/ses_native/message/msg_returned" {
						t.Errorf("compaction lookup did not use returned id: %s %s", r.Method, r.URL)
					}
					message := map[string]any{"id": "msg_returned", "type": "compaction", "status": tt.status, "reason": "manual", "time": map[string]int64{"created": 2}}
					if tt.errType != "" {
						message["error"] = map[string]string{"type": tt.errType, "message": "not completed"}
					} else {
						message["summary"] = "native compaction"
					}
					v2TestJSON(t, w, map[string]any{"data": message})
				default:
					t.Errorf("unexpected compact request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			err := New(server.URL).v2CompactSession(context.Background(), "/tmp/offline", "ses_native")
			if (err != nil) != tt.wantErr || step.Load() != 3 {
				t.Fatalf("compact = %v, requests = %d", err, step.Load())
			}
		})
	}
}

func TestV2CompactDoesNotTreatAdmissionOrWaitAsSuccess(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		fail string
	}{
		{name: "empty admission", fail: "admission"},
		{name: "wait unavailable", fail: "wait"},
		{name: "message missing", fail: "message"},
		{name: "wrong message type", fail: "type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/session/ses_native/compact":
					if tt.fail == "admission" {
						v2TestJSON(t, w, map[string]any{"data": map[string]any{}})
						return
					}
					v2TestJSON(t, w, map[string]any{"data": map[string]string{"id": "msg_returned", "sessionID": "ses_native", "type": "compaction"}})
				case "/api/experimental/session/ses_native/wait":
					if tt.fail == "wait" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case "/api/session/ses_native/message/msg_returned":
					if tt.fail == "message" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					v2TestJSON(t, w, map[string]any{"data": map[string]any{"id": "msg_returned", "type": "assistant", "status": "completed"}})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			if err := New(server.URL).v2CompactSession(context.Background(), "/tmp/offline", "ses_native"); err == nil {
				t.Fatal("compact accepted an unverified compaction")
			}
		})
	}
}

func TestV2ModelCurrentUsesCatalogIDAndNativeDefault(t *testing.T) {
	t.Parallel()
	workspace := "/tmp/offline workspace"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/model/default" || r.URL.Query().Get("location[directory]") != workspace || len(r.URL.Query()) != 1 {
			t.Errorf("default model request = %s %s", r.Method, r.URL)
		}
		model := v2TestModel("configured", "catalog-id", "Display Model")
		model["variants"] = []any{map[string]string{"id": "xhigh"}}
		v2TestJSON(t, w, map[string]any{"location": map[string]string{"directory": workspace}, "data": model})
	}))
	defer server.Close()
	model, err := New(server.URL).v2CurrentModel(context.Background(), workspace)
	if err != nil || model != "configured/catalog-id" {
		t.Fatalf("current model = %q, %v", model, err)
	}
}

func TestV2ModelNullDefaultIsExplicitButCatalogStillLists(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{"location": map[string]string{"directory": "/tmp/offline"}}
		switch r.URL.Path {
		case "/api/model/default":
			response["data"] = nil
		case "/api/model":
			response["data"] = []any{v2TestModel("configured", "catalog-id", "Display Model")}
		case "/api/provider":
			response["data"] = []any{map[string]string{"id": "configured", "name": "Configured Provider", "activation": "enabled", "package": "@offline/provider"}}
		default:
			t.Errorf("unexpected model request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		v2TestJSON(t, w, response)
	}))
	defer server.Close()
	client := New(server.URL)
	if _, err := client.v2CurrentModel(context.Background(), "/tmp/offline"); err == nil {
		t.Fatal("null default was accepted")
	}
	catalog, err := client.v2ListModels(context.Background(), "/tmp/offline")
	if err != nil || catalog.Current != "" || len(catalog.Providers) != 1 || catalog.Providers[0].Default != "" {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
}

func TestV2ModelCatalogGroupsAvailableCatalogIDs(t *testing.T) {
	t.Parallel()
	workspace := "/tmp/offline workspace/subdir"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("location[directory]") != workspace || len(r.URL.Query()) != 1 {
			t.Errorf("model query = %s %s", r.Method, r.URL)
		}
		response := map[string]any{"location": map[string]string{"directory": workspace}}
		switch r.URL.Path {
		case "/api/model":
			disabled := v2TestModel("public", "disabled", "Not Selectable")
			disabled["enabled"] = false
			response["data"] = []any{
				v2TestModel("z-provider", "z-id", "Z Model"),
				v2TestModel("configured", "b-id", "B Model"),
				v2TestModel("configured", "a-id", ""), disabled,
			}
		case "/api/provider":
			response["data"] = []any{
				map[string]string{"id": "public", "name": "Public Catalog", "activation": "disabled", "package": "@offline/public"},
				map[string]string{"id": "configured", "name": "Configured Provider", "activation": "enabled", "package": "@offline/provider"},
			}
		case "/api/model/default":
			response["data"] = v2TestModel("configured", "b-id", "B Model")
		default:
			t.Errorf("unexpected legacy/catalog request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		v2TestJSON(t, w, response)
	}))
	defer server.Close()
	catalog, err := New(server.URL).v2ListModels(context.Background(), workspace)
	want := backend.ModelCatalog{Current: "configured/b-id", Providers: []backend.ModelProvider{
		{ID: "configured", Name: "Configured Provider", Default: "b-id", Models: []backend.ModelInfo{
			{ID: "a-id", Name: "a-id", ContextLimit: 200000},
			{ID: "b-id", Name: "B Model", ContextLimit: 200000},
		}},
		{ID: "z-provider", Name: "z-provider", Models: []backend.ModelInfo{{ID: "z-id", Name: "Z Model", ContextLimit: 200000}}},
	}}
	if err != nil || !reflect.DeepEqual(catalog, want) {
		t.Fatalf("catalog = %+v, %v; want %+v", catalog, err, want)
	}
}

func TestV2ModelRefSeparatesCatalogIDAndVariant(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		value   string
		want    v2ModelRef
		wantErr bool
	}{
		{value: "configured/catalog-id#xhigh", want: v2ModelRef{ID: "catalog-id", ProviderID: "configured", Variant: "xhigh"}},
		{value: " configured/nested/catalog-id ", want: v2ModelRef{ID: "nested/catalog-id", ProviderID: "configured"}},
		{value: "configured/catalog-id", want: v2ModelRef{ID: "catalog-id", ProviderID: "configured"}},
		{value: "", wantErr: true},
		{value: "catalog-id", wantErr: true},
		{value: "/catalog-id", wantErr: true},
		{value: "configured/", wantErr: true},
		{value: "configured/#xhigh", wantErr: true},
		{value: "configured/catalog-id#", wantErr: true},
		{value: "configured/catalog-id#xhigh#other", wantErr: true},
		{value: "configured#other/catalog-id", wantErr: true},
	} {
		t.Run(tt.value, func(t *testing.T) {
			ref, err := v2ParseModelRef(tt.value)
			if (err != nil) != tt.wantErr || ref != tt.want {
				t.Fatalf("model ref = %+v, %v; want %+v, error=%v", ref, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestV2ClientOperationContextUsesConfiguredAndDefaultTimeout(t *testing.T) {
	t.Parallel()
	for _, timeout := range []time.Duration{0, 42 * time.Second} {
		client := New("http://offline.invalid")
		client.httpClient.Timeout = timeout
		before := time.Now()
		ctx, cancel := client.v2OperationContext(context.Background())
		deadline, ok := ctx.Deadline()
		cancel()
		want := timeout
		if want <= 0 {
			want = defaultHTTPTimeout
		}
		if !ok || deadline.Before(before.Add(want)) || deadline.After(time.Now().Add(want)) {
			t.Fatalf("deadline = %v, want timeout %v", deadline, want)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error = %v", ctx.Err())
		}
	}
}
