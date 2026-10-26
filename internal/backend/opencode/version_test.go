package opencode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDetectOpenCodeAPIVersion(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status int
		body   string
		v2     bool
		err    bool
	}{
		{name: "v2", status: 200, body: `{"version":"2.1.2"}`, v2: true},
		{name: "prefixed v2", status: 200, body: `{"version":"v2.1.4"}`, v2: true},
		{name: "legacy missing endpoint", status: 404},
		{name: "legacy web fallback", status: 200, body: "<!DOCTYPE html>"},
		{name: "legacy json fallback", status: 200, body: `{}`},
		{name: "requires authentication", status: 401, err: true},
		{name: "unavailable", status: 503, err: true},
		{name: "unknown version", status: 200, body: `{"version":"3.0.0"}`, err: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/info" || r.Method != http.MethodGet {
					t.Errorf("unexpected detection request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := NewWithOptions(server.URL, Options{AutoDetect: true})
			for i := 0; i < 2; i++ {
				v2, err := client.useV2(context.Background())
				if v2 != tt.v2 || (err != nil) != tt.err {
					t.Fatalf("useV2 = %v, %v; want v2=%v err=%v", v2, err, tt.v2, tt.err)
				}
			}
			if !tt.err && calls.Load() != 1 {
				t.Fatalf("version detection repeated: %d", calls.Load())
			}
		})
	}
}

func TestAPIDetectionDoesNotCacheTruncatedJSON(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"version":"2.1.4"`))
			return
		}
		_, _ = w.Write([]byte(`{"version":"2.1.4"}`))
	}))
	defer server.Close()
	client := NewWithOptions(server.URL, Options{AutoDetect: true})
	if _, err := client.useV2(context.Background()); err == nil || client.version != 0 {
		t.Fatalf("truncated response became a cached version: %d, %v", client.version, err)
	}
	if v2, err := client.useV2(context.Background()); err != nil || !v2 {
		t.Fatalf("valid retry = %v, %v", v2, err)
	}
}

func TestAPIDetectionHonorsBodyCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":`))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewWithOptions(server.URL, Options{AutoDetect: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.useV2(ctx)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || client.version != 0 {
		t.Fatalf("cancelled body cached version %d: %v", client.version, err)
	}
}

func TestAPIDetectionHonorsCancellation(t *testing.T) {
	t.Parallel()
	client := NewWithOptions("http://127.0.0.1:1", Options{AutoDetect: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.useV2(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("detection error = %v", err)
	}
}

func TestOpenCodeBasicAuthDoesNotLeakAcrossOrigins(t *testing.T) {
	t.Parallel()
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization") != "")
		_, _ = w.Write([]byte(`{"version":"2.1.4"}`))
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "opencode" || password != "test-session-token" {
			t.Error("missing backend Basic authentication")
		}
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer server.Close()
	client := NewWithOptions(server.URL, Options{AutoDetect: true, Password: "test-session-token"})
	if v2, err := client.useV2(context.Background()); err != nil || !v2 {
		t.Fatalf("authenticated detection = %v, %v", v2, err)
	}
	if leaked.Load() {
		t.Fatal("backend credentials leaked to another origin")
	}
}

func TestOpenCodeAuthenticationErrorDoesNotExposePassword(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("test-session-token"))
	}))
	defer server.Close()
	client := NewWithOptions(server.URL, Options{AutoDetect: true, Password: "test-session-token"})
	_, err := client.useV2(context.Background())
	if err == nil || strings.Contains(err.Error(), "test-session-token") {
		t.Fatalf("unsafe authentication error: %v", err)
	}
}
