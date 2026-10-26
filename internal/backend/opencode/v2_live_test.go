package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
)

func TestV2LiveServerContract(t *testing.T) {
	binary := os.Getenv("AGENT_BOT_OPENCODE2_BINARY")
	if binary == "" {
		t.Skip("AGENT_BOT_OPENCODE2_BINARY is not set")
	}
	const parentPrompt = "agentbot parent background fixture"
	const childPrompt = "agentbot child fixture"
	var calls, parentCalls, childCalls atomic.Int32
	childStarted := make(chan struct{})
	childRelease := make(chan struct{})
	var releaseChild sync.Once
	var mu sync.Mutex
	var requests []string
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" && r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected local model endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, _ := json.Marshal(body)
		mu.Lock()
		requests = append(requests, string(data))
		mu.Unlock()
		calls.Add(1)
		var latestUser string
		messages, _ := body["messages"].([]any)
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			if message["role"] == "user" {
				content, _ := json.Marshal(message["content"])
				latestUser = string(content)
			}
		}
		text, finish := "V2 local fixture answer", "stop"
		var arguments []byte
		if tools, _ := body["tools"].([]any); len(tools) > 0 {
			switch {
			case strings.Contains(latestUser, childPrompt):
				if childCalls.Add(1) != 1 {
					t.Error("unexpected repeated child model request")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				close(childStarted)
				childCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				select {
				case <-childRelease:
				case <-childCtx.Done():
					t.Errorf("child fixture did not get released: %v", childCtx.Err())
					return
				}
				text = "child fixture result"
			case strings.Contains(latestUser, parentPrompt):
				switch parentCalls.Add(1) {
				case 1:
					arguments, _ = json.Marshal(map[string]any{
						"agent": "general", "description": "Controlled background fixture",
						"prompt": childPrompt, "background": true,
					})
					finish = "tool_calls"
				case 2:
					text = "started"
				default:
					t.Error("unexpected repeated parent launch turn")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			case strings.Contains(latestUser, "child fixture result"):
				text = "background fixture result"
			case strings.Contains(latestUser, "queued fixture first"):
				text = "queued fixture first result"
			case strings.Contains(latestUser, "queued fixture second"):
				text = "queued fixture second result"
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(arguments) > 0 {
			fmt.Fprintf(w, "data: {\"id\":\"test-completion\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_fixture_background\",\"type\":\"function\",\"function\":{\"name\":\"subagent\",\"arguments\":%q}}]},\"finish_reason\":null}]}\n\n", arguments)
		} else {
			fmt.Fprintf(w, "data: {\"id\":\"test-completion\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", text)
		}
		fmt.Fprintf(w, "data: {\"id\":\"test-completion\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\ndata: [DONE]\n\n", finish)
	}))
	defer modelServer.Close()
	defer releaseChild.Do(func() { close(childRelease) })
	root := t.TempDir()
	for _, name := range []string{"home", "config", "data", "state", "cache", "tmp", "workspace"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config := map[string]any{
		"model": "agentbot-test/fake",
		"providers": map[string]any{
			"agentbot-test": map[string]any{
				"package":  "aisdk:@ai-sdk/openai-compatible",
				"settings": map[string]any{"baseURL": modelServer.URL + "/v1", "apiKey": "not-a-real-key"},
				"models": map[string]any{"fake": map[string]any{
					"modelID": "fake", "limit": map[string]int{"context": 32000, "output": 8192},
				}},
			},
		},
		"agents":  map[string]any{"title": map[string]any{"model": "agentbot-test/fake"}},
		"plugins": []any{}, "mcp": map[string]any{"servers": map[string]any{}},
		"policies": []map[string]string{
			{"action": "provider.use", "resource": "*", "effect": "deny"},
			{"action": "provider.use", "resource": "agentbot-test", "effect": "allow"},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "server.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(binary, "serve", "--hostname", "127.0.0.1", "--port", "0")
	cmd.Dir = filepath.Join(root, "workspace")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"TMPDIR=" + filepath.Join(root, "tmp"), "OPENCODE_CONFIG_DIR=" + filepath.Join(root, "config"),
		"OPENCODE_DB=" + filepath.Join(root, "data", "state.sqlite"),
		"OPENCODE_CONFIG_PROJECT_DISABLE=1", "OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_CONFIG_CONTENT=" + string(data), "OPENCODE_PASSWORD=agentbot-v2-contract-test",
		"NO_PROXY=127.0.0.1,localhost",
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	})
	address := regexp.MustCompile(`http://127\.0\.0\.1:\d+`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var client *Client
	for ctx.Err() == nil {
		output, _ := os.ReadFile(logPath)
		if url := address.FindString(string(output)); url != "" {
			candidate := NewWithOptions(url, Options{AutoDetect: true, Password: "agentbot-v2-contract-test", HTTPTimeout: 10 * time.Second})
			if candidate.Health(ctx) == nil {
				client = candidate
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if client == nil {
		output, _ := os.ReadFile(logPath)
		t.Fatalf("isolated V2 server did not become ready: %s", output)
	}
	model, err := client.CurrentModel(ctx, cmd.Dir)
	if err != nil || model != "agentbot-test/fake" {
		t.Fatalf("V2 default model = %q, err=%v", model, err)
	}
	sessionID, err := client.CreateSession(ctx, cmd.Dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.GetSession(ctx, sessionID)
	if err != nil || info.Directory != cmd.Dir {
		t.Fatalf("V2 session placement = %+v, err=%v", info, err)
	}
	var ids []string
	callback := func(id string) error { ids = append(ids, id); return nil }
	if _, err := client.Prompt(ctx, cmd.Dir, sessionID, "batch first", nil, backend.PromptOptions{
		NoReply: true, System: "first fixture context", OnPromptMessage: callback,
	}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("NoReply admission executed a model")
	}
	result, err := client.Prompt(ctx, cmd.Dir, sessionID, "reply to the batch", nil, backend.PromptOptions{
		Model: "agentbot-test/fake", System: "second fixture context", OnPromptMessage: callback,
	})
	if err != nil {
		output, _ := os.ReadFile(logPath)
		t.Fatalf("V2 local fixture prompt failed: %v; server=%s", err, output)
	}
	if !result.Native || result.ParentID != "" || result.ReplyText != "V2 local fixture answer" || len(result.SourceMessageIDs) != 2 {
		t.Fatalf("V2 reply normalization = %+v", result)
	}
	for _, id := range ids {
		found := false
		for _, source := range result.SourceMessageIDs {
			found = found || source == id
		}
		if !found {
			t.Fatalf("batch input %s missing from reply sources: %+v", id, result)
		}
	}
	verified, err := client.GetSessionReplyMessage(ctx, cmd.Dir, sessionID, result.MessageID)
	if err != nil || len(verified.SourceMessageIDs) != 2 {
		t.Fatalf("V2 single-message verification = %+v, err=%v", verified, err)
	}
	var entries struct {
		Data []struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := client.v2Request(ctx, http.MethodGet, v2InstructionPath(sessionID), nil, nil, &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries.Data {
		if strings.HasPrefix(entry.Key, v2SystemKeyPrefix) {
			t.Fatalf("batch system instruction leaked: %s", entry.Key)
		}
	}
	mu.Lock()
	captured := strings.Join(requests, "\n")
	mu.Unlock()
	if !strings.Contains(captured, "first fixture context") || !strings.Contains(captured, "second fixture context") {
		t.Fatalf("fixture model did not receive the batch system instructions: %s", captured)
	}
	parentID, err := client.CreateSession(ctx, cmd.Dir)
	if err != nil {
		t.Fatal(err)
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	events := make(chan backend.SessionEvent, 64)
	watchDone := make(chan struct{})
	var watchErr error
	go func() {
		watchErr = client.WatchSessionEvents(watchCtx, func(event backend.SessionEvent) error {
			if event.Type == "server.connected" || event.SessionID == parentID {
				select {
				case events <- event:
				case <-watchCtx.Done():
					return watchCtx.Err()
				}
			}
			return nil
		})
		close(watchDone)
	}()
	defer func() {
		stopWatch()
		select {
		case <-watchDone:
			if !errors.Is(watchErr, watchCtx.Err()) {
				t.Errorf("V2 SSE cancellation = %v", watchErr)
			}
		case <-time.After(2 * time.Second):
			t.Error("V2 SSE did not exit on cancellation")
		}
	}()
	select {
	case event := <-events:
		if event.Type != "server.connected" {
			t.Fatalf("V2 SSE initial event = %+v", event)
		}
	case <-watchDone:
		t.Fatalf("V2 SSE ended before connection: %v", watchErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var inputID string
	started, err := client.Prompt(ctx, cmd.Dir, parentID, parentPrompt, nil, backend.PromptOptions{
		OnPromptMessage: func(id string) error { inputID = id; return nil },
	})
	if err != nil {
		output, _ := os.ReadFile(logPath)
		t.Fatalf("V2 background dispatch failed: %v; server=%s", err, output)
	}
	if inputID == "" || !started.Native || started.ParentID != "" || started.SessionID != parentID || started.ReplyText != "started" || len(started.SourceMessageIDs) != 1 || started.SourceMessageIDs[0] != inputID {
		t.Fatalf("V2 background immediate reply = %+v; input=%s", started, inputID)
	}
	select {
	case <-childStarted:
	case <-ctx.Done():
		t.Fatalf("child model request did not start: %v", ctx.Err())
	}
	history, err := client.GetSessionReplyMessages(ctx, cmd.Dir, parentID)
	if err != nil {
		t.Fatal(err)
	}
	var childID string
	for _, message := range history {
		for _, part := range message.Parts {
			if part.Tool == "subagent" && part.ToolBackground && part.ToolStatus == "completed" && !part.ProviderExecuted {
				childID = part.ToolSessionID
			}
		}
	}
	if childID == "" {
		t.Fatalf("native background dispatch metadata missing from parent history: %+v", history)
	}
	releaseChild.Do(func() { close(childRelease) })
	completed := make(map[string]bool)
	var background backend.SessionMessage
waitBackground:
	for {
		select {
		case event := <-events:
			if event.Type == "message.updated" && event.CompletedAt > 0 && event.Role == "assistant" {
				completed[event.MessageID] = true
			}
			if event.Type != "session.idle" {
				continue
			}
			history, err = client.GetSessionReplyMessages(ctx, cmd.Dir, parentID)
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range history {
				if text, ok := backend.FinalReplyText(message); ok && text == "background fixture result" && completed[message.ID] {
					background = message
					break waitBackground
				}
			}
		case <-watchDone:
			t.Fatalf("V2 SSE ended before background final: %v", watchErr)
		case <-ctx.Done():
			output, _ := os.ReadFile(logPath)
			t.Fatalf("V2 background final did not arrive: %v; parent history=%+v; server=%s", ctx.Err(), history, output)
		}
	}
	if !background.Native || background.ParentID != "" || background.SessionID != parentID || len(background.SourceMessageIDs) != 1 || background.SourceMessageIDs[0] != inputID {
		t.Fatalf("V2 background final normalization = %+v; input=%s", background, inputID)
	}
	notified := false
	for _, message := range history {
		for _, part := range message.Parts {
			if message.Role == "user" && part.Synthetic && strings.Contains(part.Text, "child fixture result") {
				notified = true
				if !message.Native || message.ParentID != "" || len(message.SourceMessageIDs) != 1 || message.SourceMessageIDs[0] != inputID {
					t.Fatalf("V2 native subagent notification sources = %+v", message)
				}
			}
		}
	}
	if !notified {
		t.Fatalf("native child completion notification missing from parent history: %+v", history)
	}
	verified, err = client.GetSessionReplyMessage(ctx, cmd.Dir, parentID, background.ID)
	if text, ok := backend.FinalReplyText(verified); err != nil || !ok || text != "background fixture result" || len(verified.SourceMessageIDs) != 1 || verified.SourceMessageIDs[0] != inputID {
		t.Fatalf("V2 background single-message verification = %+v, err=%v", verified, err)
	}
	t.Logf("native background parent final %s: sources=%v, parentID=%q", background.ID, background.SourceMessageIDs, background.ParentID)
	for _, text := range []string{"queued fixture first", "queued fixture second"} {
		var queuedID string
		queued, err := client.Prompt(ctx, cmd.Dir, parentID, text, nil, backend.PromptOptions{
			OnPromptMessage: func(id string) error { queuedID = id; return nil },
		})
		if err != nil || queuedID == "" || !queued.Native || queued.ParentID != "" || queued.SessionID != parentID || queued.ReplyText != text+" result" || len(queued.SourceMessageIDs) != 1 || queued.SourceMessageIDs[0] != queuedID {
			t.Fatalf("V2 independent queued reply = %+v; input=%s, err=%v", queued, queuedID, err)
		}
		verified, err := client.GetSessionReplyMessage(ctx, cmd.Dir, parentID, queued.MessageID)
		if err != nil || len(verified.SourceMessageIDs) != 1 || verified.SourceMessageIDs[0] != queuedID {
			t.Fatalf("V2 queued single-message sources = %+v, err=%v", verified, err)
		}
	}
}
