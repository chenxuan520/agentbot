package opencode

import (
	"context"
	"errors"
	"fmt"
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

func TestV2EventBareJSONAndTerminalNormalization(t *testing.T) {
	t.Parallel()
	stream := "\r\n: heartbeat\r\n\r\n" +
		"data: {\"id\":\"evt_connected\",\"type\":\"server.connected\",\"data\":{}}\r\n\r\n" +
		"data: {\"payload\":{\"type\":\"message.updated\",\"properties\":{\"info\":{\"id\":\"v1\"}}}}\r\n\r\n" +
		v2TestEventFrame("session.execution.started", 1, `{"sessionID":"ses_1"}`) +
		v2TestEventFrame("session.step.started", 2, `{"sessionID":"ses_1","assistantMessageID":"msg_1"}`) +
		v2TestEventFrame("session.step.streamed", 3, `{"sessionID":"ses_1","assistantMessageID":"msg_1","completed":999}`) +
		v2TestEventFrame("session.text.delta", 4, `{"sessionID":"ses_1","assistantMessageID":"msg_1","delta":"never send delta"}`) +
		v2TestEventFrame("session.reasoning.delta", 5, `{"sessionID":"ses_1","assistantMessageID":"msg_1","delta":"private reasoning"}`) +
		"id: ignored\r\nevent: custom\r\ndata: {\"id\":\"evt_terminal\",\"created\":42,\r\n" +
		"data: \"type\":\"session.step.ended\",\"durable\":{\"aggregateID\":\"wrong_scope\",\"seq\":19,\"version\":1},\r\n" +
		"data: \"data\":{\"sessionID\":\"ses_1\",\"assistantMessageID\":\"msg_1\",\"finish\":\"stop\"}}\r\n\r\n" +
		v2TestEventFrame("session.step.failed", 43, `{"sessionID":"ses_1","assistantMessageID":"msg_failed","error":{"type":"provider.unknown","message":"private-secret"}}`) +
		v2TestEventFrame("session.retry.scheduled", 44, `{"sessionID":"ses_1","assistantMessageID":"msg_retry","attempt":1,"at":45,"error":{"message":"private-secret"}}`) +
		v2TestEventFrame("session.execution.succeeded", 50, `{"sessionID":"ses_1"}`) +
		v2TestEventFrame("session.execution.failed", 51, `{"sessionID":"ses_2","error":{"message":"private-secret"}}`) +
		v2TestEventFrame("session.execution.interrupted", 52, `{"sessionID":"ses_3","reason":"shutdown"}`) +
		v2TestEventFrame("session.step.ended", 0, `{"sessionID":"ses_4","assistantMessageID":"msg_no_time"}`) +
		v2TestEventFrame("session.step.ended", 60, `{"sessionID":"ses_1"}`) +
		v2TestEventFrame("session.step.ended", 60, `{"assistantMessageID":"msg_no_session"}`) +
		v2TestEventFrame("session.text.ended", 61, `{"sessionID":"ses_1","assistantMessageID":"msg_1","text":"ignore text boundary"}`) +
		"data:   \r\n\r\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/event" || r.URL.RawQuery != "" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("SSE request = %s %s, Accept %q", r.Method, r.URL, r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()
	var events []backend.SessionEvent
	err := New(server.URL).v2WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("watch = %v", err)
	}
	want := []backend.SessionEvent{
		{Type: "server.connected"},
		{Type: "message.updated", SessionID: "ses_1", MessageID: "msg_1", Role: "assistant"},
		{Type: "message.updated", SessionID: "ses_1", MessageID: "msg_1", Role: "assistant"},
		{Type: "message.updated", SessionID: "ses_1", MessageID: "msg_1", Role: "assistant", CompletedAt: 42},
		{Type: "message.updated", SessionID: "ses_1", MessageID: "msg_failed", Role: "assistant", CompletedAt: 43},
		{Type: "message.updated", SessionID: "ses_1", MessageID: "msg_retry", Role: "assistant"},
		{Type: "session.idle", SessionID: "ses_1"},
		{Type: "session.idle", SessionID: "ses_2"},
		{Type: "session.idle", SessionID: "ses_3"},
		{Type: "message.updated", SessionID: "ses_4", MessageID: "msg_no_time", Role: "assistant", CompletedAt: 1},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %+v, want %+v", events, want)
	}
}

func TestV2EventLargeData(t *testing.T) {
	t.Parallel()
	large := strings.Repeat("x", 256*1024)
	stream := v2TestEventFrame("session.text.delta", 1, `{"sessionID":"ses_large","assistantMessageID":"msg_large","delta":"`+large+`"}`) +
		v2TestEventFrame("session.reasoning.delta", 2, `{"sessionID":"ses_large","assistantMessageID":"msg_large","delta":"`+large+`"}`) +
		v2TestEventFrame("session.step.ended", 99, `{"sessionID":"ses_large","assistantMessageID":"msg_large","extra":"`+large+`"}`)
	body := &v2EventReadCloser{ReadCloser: io.NopCloser(strings.NewReader(stream))}
	client := v2TestEventClient(body, http.StatusOK)
	var events []backend.SessionEvent
	err := client.v2WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
		events = append(events, event)
		return nil
	})
	want := []backend.SessionEvent{{Type: "server.connected"}, {Type: "message.updated", SessionID: "ses_large", MessageID: "msg_large", Role: "assistant", CompletedAt: 99}}
	if !errors.Is(err, io.EOF) || !reflect.DeepEqual(events, want) || body.closed.Load() != 1 {
		t.Fatalf("large SSE = %v, events = %+v, closes = %d", err, events, body.closed.Load())
	}
}

func TestV2EventBoundsSingleAndMultilineData(t *testing.T) {
	t.Parallel()
	for _, multiline := range []bool{false, true} {
		t.Run(fmt.Sprint(multiline), func(t *testing.T) {
			t.Parallel()
			stream := "data: " + strings.Repeat("x", maxSessionEventSize+1024) + "\n\n"
			if multiline {
				line := "data: " + strings.Repeat(" ", maxSessionEventSize/2+1) + "\r\n"
				stream = line + line + "\r\n"
			}
			body := &v2EventReadCloser{ReadCloser: io.NopCloser(strings.NewReader(stream))}
			calls := 0
			err := v2TestEventClient(body, http.StatusOK).v2WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
				calls++
				if event.Type != "server.connected" {
					t.Errorf("oversized event dispatched: %+v", event)
				}
				return nil
			})
			if err == nil || errors.Is(err, io.EOF) || calls != 1 || body.closed.Load() != 1 {
				t.Fatalf("oversized SSE = %v, calls = %d, closes = %d", err, calls, body.closed.Load())
			}
		})
	}
}

func TestV2EventConnectBeforeDataAndEOF(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		stream string
		want   int
	}{
		{name: "empty EOF", want: 1},
		{name: "heartbeat EOF", stream: "\n: heartbeat\n\n", want: 1},
		{name: "terminal at EOF without blank separator", stream: strings.TrimSuffix(v2TestEventFrame("session.step.ended", 42, `{"sessionID":"ses","assistantMessageID":"msg"}`), "\n"), want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := &v2EventReadCloser{ReadCloser: io.NopCloser(strings.NewReader(tt.stream))}
			var events []backend.SessionEvent
			err := v2TestEventClient(body, http.StatusOK).v2WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
				events = append(events, event)
				return nil
			})
			if err != io.EOF || len(events) != tt.want || events[0].Type != "server.connected" || body.closed.Load() != 1 {
				t.Fatalf("EOF = %v, events = %+v, closes = %d", err, events, body.closed.Load())
			}
		})
	}
}

func TestV2EventCancellationNoTimeoutAndAuth(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "test-user" || password != "test-password" {
			t.Error("authentication transport not preserved")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/event" || r.URL.RawQuery != "" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("unexpected subscription: %s", r.URL)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewWithOptions(server.URL, Options{HTTPTimeout: 10 * time.Millisecond, Username: "test-user", Password: "test-password"})
	transport := client.httpClient.Transport
	var requests atomic.Int32
	client.httpClient.Transport = eventRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return transport.RoundTrip(r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- client.v2WatchSessionEvents(ctx, func(event backend.SessionEvent) error {
			if event.Type == "server.connected" {
				connected <- struct{}{}
			}
			return nil
		})
	}()
	select {
	case <-connected:
	case err := <-result:
		t.Fatalf("SSE ended before connect: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("connection callback missing before first frame")
	}
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-result:
		t.Fatalf("normal HTTP timeout applied to SSE: %v", err)
	case <-timer.C:
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSE did not close on cancellation")
	}
	if client.httpClient.Timeout != 10*time.Millisecond || requests.Load() != 1 {
		t.Fatalf("original client changed: timeout %s, requests %d", client.httpClient.Timeout, requests.Load())
	}
}

func TestV2EventCancellationClosesEvenNonContextReader(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &v2EventReadCloser{ReadCloser: reader}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- v2TestEventClient(body, http.StatusOK).v2WatchSessionEvents(ctx, func(event backend.SessionEvent) error {
			connected <- struct{}{}
			return nil
		})
	}()
	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("connection callback missing")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || body.closed.Load() == 0 {
			t.Fatalf("cancellation = %v, closes = %d", err, body.closed.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unblock response reader")
	}
}

func TestV2EventCanceledContextSendsNoRequest(t *testing.T) {
	t.Parallel()
	client := New("http://unused.invalid")
	var requests atomic.Int32
	client.httpClient.Transport = eventRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected request")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.v2WatchSessionEvents(ctx, func(event backend.SessionEvent) error {
		t.Errorf("unexpected event: %+v", event)
		return nil
	})
	if !errors.Is(err, context.Canceled) || requests.Load() != 0 {
		t.Fatalf("canceled subscription = %v, requests = %d", err, requests.Load())
	}
}

func TestV2EventCallbackErrorsCloseResponse(t *testing.T) {
	t.Parallel()
	for _, failOn := range []int{1, 2} {
		t.Run(fmt.Sprint(failOn), func(t *testing.T) {
			t.Parallel()
			stream := v2TestEventFrame("session.step.ended", 1, `{"sessionID":"ses","assistantMessageID":"msg"}`) + v2TestEventFrame("session.execution.succeeded", 2, `{"sessionID":"ses"}`)
			body := &v2EventReadCloser{ReadCloser: io.NopCloser(strings.NewReader(stream))}
			callbackErr := errors.New("callback failed")
			calls := 0
			err := v2TestEventClient(body, http.StatusOK).v2WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
				calls++
				if calls == failOn {
					return callbackErr
				}
				return nil
			})
			if err != callbackErr || calls != failOn || body.closed.Load() != 1 {
				t.Fatalf("callback = %v, calls = %d, closes = %d", err, calls, body.closed.Load())
			}
		})
	}
}

func TestV2EventHTTPErrorsDoNotExposeBody(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			const secret = "private-response-body"
			body := &v2EventReadCloser{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat(secret, 4096)))}
			err := v2TestEventClient(body, status).v2WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
				t.Errorf("HTTP failure delivered connected: %+v", event)
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), secret) || len(err.Error()) > 200 || body.closed.Load() != 1 {
				t.Fatalf("HTTP error = %v, closes = %d", err, body.closed.Load())
			}
		})
	}
}

func TestV2EventJSONAndDataErrorsAreSanitized(t *testing.T) {
	t.Parallel()
	const secret = "private-raw-secret"
	for _, stream := range []string{
		"data: {invalid private-raw-secret}\n\n",
		"event: error\ndata: {\"error\":\"" + secret + "\"}\n\n",
		"data: {\"type\":\"error\",\"data\":{\"error\":\"" + secret + "\"}}\n\n",
		"data: {\"data\":{\"error\":{\"message\":\"" + secret + "\"}}}\n\n",
		"data: {\"type\":\"session.step.ended\",\"created\":\"" + secret + "\",\"data\":{}}\n\n",
		"data: {\"type\":\"session.step.ended\",\"created\":1,\"data\":{\"sessionID\":{\"" + secret + "\":true},\"assistantMessageID\":\"msg\"}}\n\n",
	} {
		body := &v2EventReadCloser{ReadCloser: io.NopCloser(strings.NewReader(stream))}
		calls := 0
		err := v2TestEventClient(body, http.StatusOK).v2WatchSessionEvents(context.Background(), func(event backend.SessionEvent) error {
			calls++
			return nil
		})
		if err == nil || errors.Is(err, io.EOF) || strings.Contains(err.Error(), secret) || len(err.Error()) > 200 || calls != 1 || body.closed.Load() != 1 {
			t.Fatalf("SSE data error = %v, calls = %d, closes = %d", err, calls, body.closed.Load())
		}
	}
}

func v2TestEventFrame(kind string, created int64, data string) string {
	return fmt.Sprintf("data: {\"id\":\"evt_native\",\"created\":%d,\"type\":%q,\"durable\":{\"aggregateID\":\"not-session-scope\",\"seq\":17,\"version\":1},\"data\":%s}\n\n", created, kind, data)
}

type v2EventReadCloser struct {
	io.ReadCloser
	closed atomic.Int32
}

func (body *v2EventReadCloser) Close() error {
	body.closed.Add(1)
	return body.ReadCloser.Close()
}

func v2TestEventClient(body io.ReadCloser, status int) *Client {
	client := New("http://unused.invalid")
	client.httpClient = &http.Client{Transport: eventRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: body, Request: r}, nil
	})}
	return client
}
