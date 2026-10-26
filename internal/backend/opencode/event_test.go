package opencode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func TestWatchSessionEventsParsesGlobalEnvelope(t *testing.T) {
	t.Parallel()

	stream := "\r\n: heartbeat\r\n\r\n" +
		"event: sync\r\ndata: {\"payload\":{\"type\":\"sync\",\"properties\":{\"info\":\"ignored\"}}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"server.connected\",\"properties\":{}}}\r\n\r\n" +
		"id: ignored\r\ndata: {\"directory\":\"/tmp/workspace\",\"payload\":\r\n" +
		"data: {\"type\":\"message.updated\",\"properties\":{\"sessionID\":\"fallback\",\"info\":{\"sessionID\":\"ses_1\",\"id\":\"msg_1\",\"role\":\"assistant\",\"time\":{\"completed\":42}}}}}\r\n\r\n" +
		"data:{\"payload\":{\"type\":\"message.updated\",\"properties\":{\"sessionID\":\"ses_fallback\",\"info\":{\"id\":\"msg_user\",\"role\":\"user\"}}}}\r\n\r\n" +
		"data: {\"directory\":\"/tmp/workspace\",\"payload\":{\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"ses_1\"}}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"session.status\",\"properties\":{\"sessionID\":\"ses_2\",\"status\":{\"type\":\"idle\"}}}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"session.status\",\"properties\":{\"sessionID\":\"ses_2\",\"status\":{\"type\":\"busy\"}}}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"session.status\",\"properties\":{\"sessionID\":\"ses_2\",\"status\":{\"type\":\"retry\"}}}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"message.part.delta\",\"properties\":{\"info\":\"ignored\"}}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"server.heartbeat\",\"properties\":{}}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"message.part.updated\",\"properties\":{}}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"session.updated\",\"properties\":{}}}\r\n\r\n" +
		"data:   \r\n\r\n"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/global/event" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	var source backend.SessionEventSource = client
	var events []backend.SessionEvent
	err := source.WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("watch events error = %v, want io.EOF", err)
	}
	want := []backend.SessionEvent{
		{Type: "server.connected"},
		{Type: "message.updated", SessionID: "ses_1", MessageID: "msg_1", Role: "assistant", CompletedAt: 42},
		{Type: "message.updated", SessionID: "ses_fallback", MessageID: "msg_user", Role: "user"},
		{Type: "session.idle", SessionID: "ses_1"},
		{Type: "session.idle", SessionID: "ses_2"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %+v, want %+v", events, want)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want one global subscription", got)
	}
}

func TestWatchSessionEventsHandlesLargeData(t *testing.T) {
	t.Parallel()

	largeText := strings.Repeat("x", 128*1024)
	stream := "data: {\"payload\":{\"type\":\"message.part.delta\",\"properties\":{\"delta\":\"" + largeText + "\"}}}\n\n" +
		"data: {\"directory\":\"/tmp/workspace\",\"payload\":{\"type\":\"message.updated\",\"properties\":{\"info\":{\"sessionID\":\"ses_large\",\"id\":\"msg_large\",\"role\":\"assistant\",\"time\":{\"completed\":99},\"text\":\"" + largeText + "\"}}}}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	var events []backend.SessionEvent
	err := client.WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("watch events error = %v, want io.EOF", err)
	}
	want := []backend.SessionEvent{{Type: "message.updated", SessionID: "ses_large", MessageID: "msg_large", Role: "assistant", CompletedAt: 99}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %+v, want %+v", events, want)
	}
}

func TestWatchSessionEventsBoundsData(t *testing.T) {
	t.Parallel()

	for _, multiline := range []bool{false, true} {
		name := "single line"
		if multiline {
			name = "multiple data lines"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if multiline {
					data := "data: " + strings.Repeat(" ", (8<<20)/2+1) + "\n"
					_, _ = io.WriteString(w, data)
					_, _ = io.WriteString(w, data+"\n")
					return
				}
				_, _ = io.WriteString(w, "data: "+strings.Repeat("x", 9<<20)+"\n\n")
			}))
			defer server.Close()

			client := New(server.URL)
			client.httpClient = server.Client()
			err := client.WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
				t.Errorf("unexpected event: %+v", event)
				return nil
			})
			if err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("watch events error = %v, want oversized data error", err)
			}
		})
	}
}

func TestWatchSessionEventsCancellationAndNoHTTPTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"payload\":{\"type\":\"server.connected\",\"properties\":{}}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	client.httpClient.Timeout = 10 * time.Millisecond
	transport := client.httpClient.Transport
	var transportCalls atomic.Int32
	client.httpClient.Transport = eventRoundTripper(func(request *http.Request) (*http.Response, error) {
		transportCalls.Add(1)
		return transport.RoundTrip(request)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- client.WatchSessionEvents(ctx, func(event backend.SessionEvent) error {
			if event.Type == "server.connected" {
				connected <- struct{}{}
			}
			return nil
		})
	}()
	select {
	case <-connected:
	case err := <-result:
		t.Fatalf("watch ended before connected: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server.connected")
	}
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-result:
		t.Fatalf("SSE must not use normal HTTP timeout: %v", err)
	case <-timer.C:
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("watch error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not exit on context cancellation")
	}
	if client.httpClient.Timeout != 10*time.Millisecond {
		t.Fatalf("original timeout changed: %s", client.httpClient.Timeout)
	}
	if got := transportCalls.Load(); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestWatchSessionEventsCanceledContext(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.WatchSessionEvents(ctx, func(event backend.SessionEvent) error {
		t.Errorf("unexpected event: %+v", event)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("watch error = %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestWatchSessionEventsEmptyEOF(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "\n\n: heartbeat\n\n")
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	err := client.WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
		t.Errorf("unexpected event: %+v", event)
		return nil
	})
	if err != io.EOF {
		t.Fatalf("watch error = %v, want io.EOF", err)
	}
}

func TestWatchSessionEventsReturnsCallbackError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"payload\":{\"type\":\"server.connected\",\"properties\":{}}}\n\ndata: {\"payload\":{\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"ses_test\"}}}\n\n")
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	callbackErr := errors.New("handle event failed")
	calls := 0
	err := client.WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
		calls++
		return callbackErr
	})
	if err != callbackErr || calls != 1 {
		t.Fatalf("watch error = %v, callback calls = %d, want callback error and one call", err, calls)
	}
}

func TestWatchSessionEventsHTTPErrorDoesNotExposeBody(t *testing.T) {
	t.Parallel()

	const secret = "private-response-body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, strings.Repeat(secret, 4096))
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	err := client.WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
		t.Errorf("unexpected event: %+v", event)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("watch error = %v, want HTTP 401", err)
	}
	if strings.Contains(err.Error(), secret) || len(err.Error()) > 200 {
		t.Fatalf("watch error must not expose response body: %v", err)
	}
}

func TestWatchSessionEventsInvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {invalid}\n\n")
	}))
	defer server.Close()

	client := New(server.URL)
	client.httpClient = server.Client()
	err := client.WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
		t.Errorf("unexpected event: %+v", event)
		return nil
	})
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("watch error = %v, want JSON decode error", err)
	}
}

type eventRoundTripper func(*http.Request) (*http.Response, error)

func (fn eventRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
