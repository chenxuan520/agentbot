package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
)

func TestPromptFileStoreMaterializeRecurringPayloadWritesPromptFile(t *testing.T) {
	t.Parallel()

	store := NewPromptFileStore(config.Default(t.TempDir()))
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-1"}
	original := map[string]any{"promptText": "check the backlog", "title": "Daily Check"}

	result, err := store.MaterializeRecurringPayload(ref, "report.daily_summary", original)
	if err != nil {
		t.Fatalf("MaterializeRecurringPayload: %v", err)
	}
	if _, ok := result["promptText"]; ok {
		t.Fatalf("promptText should be removed after materialization: %+v", result)
	}
	relPath, _ := result["promptFile"].(string)
	if !strings.HasPrefix(relPath, "data/scheduler/prompts/feishu/chat-1/") {
		t.Fatalf("promptFile = %q", relPath)
	}
	content, err := store.ReadPrompt(relPath)
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if content != "check the backlog" {
		t.Fatalf("prompt content = %q", content)
	}
	if original["promptText"] != "check the backlog" {
		t.Fatalf("original payload unexpectedly mutated: %+v", original)
	}
}

func TestPromptFileStoreMaterializeRecurringPayloadCopiesExistingPromptFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewPromptFileStore(config.Default(root))
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-2"}
	existingPath := "docs/existing-prompt.md"
	fullPath := root + "/" + existingPath
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir existing prompt dir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("prompt from existing file"), 0o644); err != nil {
		t.Fatalf("write existing prompt: %v", err)
	}

	result, err := store.MaterializeRecurringPayload(ref, "report.daily_copy", map[string]any{"promptFile": existingPath})
	if err != nil {
		t.Fatalf("MaterializeRecurringPayload: %v", err)
	}
	relPath, _ := result["promptFile"].(string)
	if relPath == existingPath {
		t.Fatalf("expected recurring prompt file to be copied into scheduler dir, got %q", relPath)
	}
	if !strings.HasPrefix(relPath, "data/scheduler/prompts/feishu/chat-2/") {
		t.Fatalf("promptFile = %q", relPath)
	}
	content, err := store.ReadPrompt(relPath)
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if content != "prompt from existing file" {
		t.Fatalf("prompt content = %q", content)
	}
}

func TestPromptFileStoreWritePromptContentUpdatesExistingPromptFile(t *testing.T) {
	t.Parallel()

	store := NewPromptFileStore(config.Default(t.TempDir()))
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-update"}
	relPath, err := store.WritePrompt(ref, "report.daily_update", "first prompt")
	if err != nil {
		t.Fatalf("WritePrompt: %v", err)
	}
	if err := store.WritePromptContent(relPath, "second prompt"); err != nil {
		t.Fatalf("WritePromptContent: %v", err)
	}
	content, err := store.ReadPrompt(relPath)
	if err != nil {
		t.Fatalf("ReadPrompt: %v", err)
	}
	if content != "second prompt" {
		t.Fatalf("prompt content = %q", content)
	}
}

func TestFirstRunAtCronUsesTimezone(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 10, 23, 30, 0, 0, time.UTC)
	runAt, err := FirstRunAt("", "0 8 * * *", "Asia/Shanghai", now)
	if err != nil {
		t.Fatalf("FirstRunAt: %v", err)
	}
	want := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	if !runAt.Equal(want) {
		t.Fatalf("runAt = %s, want %s", runAt, want)
	}
}

func TestFirstRunAtRejectsPastOneShot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 10, 23, 30, 0, 0, time.UTC)
	_, err := FirstRunAt("2026-05-10T23:29:59Z", "", "", now)
	if err == nil {
		t.Fatal("expected past runAt to be rejected")
	}
	if !strings.Contains(err.Error(), "must not be in the past") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNextRunAtCronUsesTimezone(t *testing.T) {
	t.Parallel()

	job := Job{
		Payload: `{"cron":"0 8 * * *","timezone":"Asia/Shanghai"}`,
	}
	next, recurring, err := NextRunAt(job, time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NextRunAt: %v", err)
	}
	if !recurring {
		t.Fatal("expected cron job to be recurring")
	}
	want := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}
