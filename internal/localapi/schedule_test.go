package localapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/scheduler"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
)

func TestScheduleListResolvesPromptTextAndCanUpdatePrompt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	server := New(cfg, nil, scheduler.NewService(store), nil, nil)
	router := server.router()

	createBody := `{"provider":"feishu","conversationId":"chat-1","cron":"0 8 * * *","timezone":"Asia/Shanghai","route":"report.daily_summary","payload":{"replyMessageID":"","promptText":"first prompt"}}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/schedule", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	router.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", createResp.Code, createResp.Body.String())
	}
	var created struct {
		ID      string `json:"ID"`
		Payload string `json:"Payload"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected created job id")
	}
	if !strings.Contains(created.Payload, `"promptFile":"data/scheduler/prompts/feishu/chat-1/`) {
		t.Fatalf("expected recurring promptFile in payload, got %s", created.Payload)
	}

	assertPromptText := func(want string) {
		listReq := httptest.NewRequest(http.MethodGet, "/api/v1/schedule?provider=feishu&conversationId=chat-1", nil)
		listResp := httptest.NewRecorder()
		router.ServeHTTP(listResp, listReq)
		if listResp.Code != http.StatusOK {
			t.Fatalf("list status = %d, body=%s", listResp.Code, listResp.Body.String())
		}
		var listed []struct {
			ID                 string `json:"ID"`
			PromptTextResolved string `json:"PromptTextResolved"`
		}
		if err := json.Unmarshal(listResp.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		if len(listed) != 1 {
			t.Fatalf("listed len = %d, want 1", len(listed))
		}
		if listed[0].ID != created.ID {
			t.Fatalf("listed id = %q, want %q", listed[0].ID, created.ID)
		}
		if listed[0].PromptTextResolved != want {
			t.Fatalf("prompt text = %q, want %q", listed[0].PromptTextResolved, want)
		}
	}

	assertPromptText("first prompt")

	updateBody := []byte(`{"jobId":"` + created.ID + `","kind":"prompt","content":"second prompt"}`)
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/schedule", bytes.NewReader(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}
	var updated struct {
		PromptTextResolved string `json:"PromptTextResolved"`
	}
	if err := json.Unmarshal(updateResp.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.PromptTextResolved != "second prompt" {
		t.Fatalf("updated prompt text = %q", updated.PromptTextResolved)
	}

	assertPromptText("second prompt")
}
