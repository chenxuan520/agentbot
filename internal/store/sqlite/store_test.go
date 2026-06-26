package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/progress"
	"github.com/chenxuan520/agentbot/internal/scheduler"
	appstore "github.com/chenxuan520/agentbot/internal/store"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Second)
	record := appstore.WorkspaceRecord{
		Provider:        "feishu",
		ConversationID:  "chat-1",
		WorkspacePath:   "/tmp/chat-1",
		Template:        "default",
		AgentBackend:    "opencode",
		ActiveSessionID: "session-1",
		BTWSessionID:    "session-btw-1",
		LastMessageAt:   now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := store.Upsert(record); err != nil {
		t.Fatalf("upsert record: %v", err)
	}

	loaded, err := store.Get(conversation.Ref{Provider: "feishu", ConversationID: "chat-1"})
	if err != nil {
		t.Fatalf("get record: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected record")
	}
	if loaded.WorkspacePath != record.WorkspacePath {
		t.Fatalf("workspace path = %q, want %q", loaded.WorkspacePath, record.WorkspacePath)
	}
	if loaded.ActiveSessionID != record.ActiveSessionID {
		t.Fatalf("active session = %q, want %q", loaded.ActiveSessionID, record.ActiveSessionID)
	}
	if loaded.BTWSessionID != record.BTWSessionID {
		t.Fatalf("btw session = %q, want %q", loaded.BTWSessionID, record.BTWSessionID)
	}

	if err := store.Delete(conversation.Ref{Provider: "feishu", ConversationID: "chat-1"}); err != nil {
		t.Fatalf("delete record: %v", err)
	}

	loaded, err = store.Get(conversation.Ref{Provider: "feishu", ConversationID: "chat-1"})
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if loaded != nil {
		t.Fatal("expected deleted record to be nil")
	}
}

func TestOpenMigratesLegacyWorkspaceTableForBTWSession(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite3")
	legacyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := legacyDB.Exec(`
		CREATE TABLE conversation_workspaces (
			provider TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			workspace_path TEXT NOT NULL,
			template TEXT NOT NULL,
			agent_backend TEXT NOT NULL,
			active_session_id TEXT NOT NULL DEFAULT '',
			last_message_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (provider, conversation_id)
		)
	`); err != nil {
		_ = legacyDB.Close()
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := legacyDB.Exec(`
		INSERT INTO conversation_workspaces (
			provider, conversation_id, workspace_path, template, agent_backend, active_session_id, last_message_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "feishu", "legacy-chat", "/tmp/legacy-chat", "default", "opencode", "session-main", now.Unix(), now.Unix(), now.Unix()); err != nil {
		_ = legacyDB.Close()
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()

	loaded, err := store.Get(conversation.Ref{Provider: "feishu", ConversationID: "legacy-chat"})
	if err != nil {
		t.Fatalf("get migrated record: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected migrated record")
	}
	if loaded.ActiveSessionID != "session-main" {
		t.Fatalf("active session = %q, want %q", loaded.ActiveSessionID, "session-main")
	}
	if loaded.BTWSessionID != "" {
		t.Fatalf("btw session = %q, want empty", loaded.BTWSessionID)
	}

	loaded.BTWSessionID = "session-btw"
	loaded.UpdatedAt = now.Add(time.Minute)
	if err := store.Upsert(*loaded); err != nil {
		t.Fatalf("upsert migrated record: %v", err)
	}
	loadedAgain, err := store.Get(conversation.Ref{Provider: "feishu", ConversationID: "legacy-chat"})
	if err != nil {
		t.Fatalf("get updated migrated record: %v", err)
	}
	if loadedAgain.BTWSessionID != "session-btw" {
		t.Fatalf("btw session after upsert = %q, want %q", loadedAgain.BTWSessionID, "session-btw")
	}
}

func TestStoreBTWSessionRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-btw"}
	now := time.Now().UTC().Truncate(time.Second)
	record := appstore.WorkspaceRecord{
		Provider:       ref.Provider,
		ConversationID: ref.ConversationID,
		WorkspacePath:  "/tmp/chat-btw",
		Template:       "default",
		AgentBackend:   "opencode",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Upsert(record); err != nil {
		t.Fatalf("upsert workspace record: %v", err)
	}

	if err := store.UpsertBTWSession(ref, "ou-a", "session-a", now); err != nil {
		t.Fatalf("upsert btw session a: %v", err)
	}
	if err := store.UpsertBTWSession(ref, "ou-b", "session-b", now); err != nil {
		t.Fatalf("upsert btw session b: %v", err)
	}
	sessionID, err := store.GetBTWSession(ref, "ou-a")
	if err != nil {
		t.Fatalf("get btw session a: %v", err)
	}
	if sessionID != "session-a" {
		t.Fatalf("btw session a = %q, want %q", sessionID, "session-a")
	}
	sessionID, err = store.GetBTWSession(ref, "ou-b")
	if err != nil {
		t.Fatalf("get btw session b: %v", err)
	}
	if sessionID != "session-b" {
		t.Fatalf("btw session b = %q, want %q", sessionID, "session-b")
	}

	if err := store.DeleteBTWSession(ref, "ou-a"); err != nil {
		t.Fatalf("delete btw session a: %v", err)
	}
	sessionID, err = store.GetBTWSession(ref, "ou-a")
	if err != nil {
		t.Fatalf("get btw session a after delete: %v", err)
	}
	if sessionID != "" {
		t.Fatalf("btw session a after delete = %q, want empty", sessionID)
	}

	if err := store.DeleteBTWSessions(ref); err != nil {
		t.Fatalf("delete all btw sessions: %v", err)
	}
	sessionID, err = store.GetBTWSession(ref, "ou-b")
	if err != nil {
		t.Fatalf("get btw session b after delete all: %v", err)
	}
	if sessionID != "" {
		t.Fatalf("btw session b after delete all = %q, want empty", sessionID)
	}
}

func TestStoreTopicSessionRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-topic"}
	now := time.Now().UTC().Truncate(time.Second)

	if has, err := store.HasTopicSessions(ref); err != nil || has {
		t.Fatalf("has topic sessions before insert = %v (err %v), want false", has, err)
	}

	if err := store.UpsertTopicSession(ref, "om-root-a", "session-a", now); err != nil {
		t.Fatalf("upsert topic session a: %v", err)
	}
	if err := store.UpsertTopicSession(ref, "om-root-b", "session-b", now); err != nil {
		t.Fatalf("upsert topic session b: %v", err)
	}
	if has, err := store.HasTopicSessions(ref); err != nil || !has {
		t.Fatalf("has topic sessions after insert = %v (err %v), want true", has, err)
	}

	if sessionID, err := store.GetTopicSession(ref, "om-root-a"); err != nil || sessionID != "session-a" {
		t.Fatalf("topic session a = %q (err %v), want session-a", sessionID, err)
	}

	// Upsert on the same key overwrites rather than duplicating.
	if err := store.UpsertTopicSession(ref, "om-root-a", "session-a2", now.Add(time.Minute)); err != nil {
		t.Fatalf("re-upsert topic session a: %v", err)
	}
	if sessionID, err := store.GetTopicSession(ref, "om-root-a"); err != nil || sessionID != "session-a2" {
		t.Fatalf("topic session a after re-upsert = %q (err %v), want session-a2", sessionID, err)
	}

	if err := store.DeleteTopicSession(ref, "om-root-a"); err != nil {
		t.Fatalf("delete topic session a: %v", err)
	}
	if sessionID, err := store.GetTopicSession(ref, "om-root-a"); err != nil || sessionID != "" {
		t.Fatalf("topic session a after delete = %q (err %v), want empty", sessionID, err)
	}

	// DeleteConversationState must prune any remaining topic sessions.
	if err := store.DeleteConversationState(ref); err != nil {
		t.Fatalf("delete conversation state: %v", err)
	}
	if has, err := store.HasTopicSessions(ref); err != nil || has {
		t.Fatalf("has topic sessions after delete state = %v (err %v), want false", has, err)
	}
}

func TestStoreControl(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-2"}
	now := time.Now().UTC().Truncate(time.Second)

	rule := control.Rule{
		ID:             "rule-1",
		Provider:       ref.Provider,
		ConversationID: ref.ConversationID,
		Kind:           control.KindRefuse,
		Scope:          "conversation",
		MatchKey:       "",
		Reason:         "mute test",
		UntilAt:        now.Add(30 * time.Minute),
		Status:         control.StatusActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateRule(rule); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	rules, err := store.ListActiveRules(ref, now)
	if err != nil {
		t.Fatalf("list active rules: %v", err)
	}
	if len(rules) != 1 || rules[0].Kind != rule.Kind {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}

func TestStoreListJobsByConversation(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-schedule"}
	now := time.Now().UTC().Truncate(time.Second)
	jobs := []scheduler.Job{
		{
			ID:             "job-1",
			Provider:       ref.Provider,
			ConversationID: ref.ConversationID,
			Route:          "task.first",
			Payload:        `{"promptText":"first"}`,
			RunAt:          now.Add(10 * time.Minute),
			Status:         scheduler.StatusPending,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		{
			ID:             "job-2",
			Provider:       ref.Provider,
			ConversationID: ref.ConversationID,
			Route:          "task.second",
			Payload:        `{"promptText":"second"}`,
			RunAt:          now.Add(20 * time.Minute),
			Status:         scheduler.StatusDone,
			CreatedAt:      now.Add(time.Minute),
			UpdatedAt:      now.Add(time.Minute),
		},
		{
			ID:             "job-3",
			Provider:       "feishu",
			ConversationID: "other-chat",
			Route:          "task.other",
			Payload:        `{"promptText":"other"}`,
			RunAt:          now.Add(5 * time.Minute),
			Status:         scheduler.StatusPending,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	}
	for _, job := range jobs {
		if err := store.CreateJob(job); err != nil {
			t.Fatalf("create job %s: %v", job.ID, err)
		}
	}

	listed, err := store.ListJobs(ref, 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("job count = %d, want 2", len(listed))
	}
	if listed[0].ID != "job-1" || listed[1].ID != "job-2" {
		t.Fatalf("unexpected jobs: %+v", listed)
	}
}

func TestStoreListJobsReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	listed, err := store.ListJobs(conversation.Ref{Provider: "feishu", ConversationID: "chat-empty"}, 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if listed == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(listed) != 0 {
		t.Fatalf("job count = %d, want 0", len(listed))
	}
}

func TestStoreRescheduleJob(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-cron"}
	now := time.Now().UTC().Truncate(time.Second)
	job := scheduler.Job{
		ID:             "job-cron",
		Provider:       ref.Provider,
		ConversationID: ref.ConversationID,
		Route:          "task.daily",
		Payload:        `{"cron":"0 8 * * *","timezone":"Asia/Shanghai"}`,
		RunAt:          now,
		Status:         scheduler.StatusRunning,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	nextRunAt := now.Add(24 * time.Hour)
	if err := store.RescheduleJob(job.ID, nextRunAt, now.Add(time.Minute)); err != nil {
		t.Fatalf("reschedule job: %v", err)
	}

	listed, err := store.ListJobs(ref, 10)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("job count = %d, want 1", len(listed))
	}
	if listed[0].Status != scheduler.StatusPending {
		t.Fatalf("status = %q, want pending", listed[0].Status)
	}
	if !listed[0].RunAt.Equal(nextRunAt) {
		t.Fatalf("runAt = %s, want %s", listed[0].RunAt, nextRunAt)
	}
}

func TestStoreListActiveJobs(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-active"}
	now := time.Now().UTC().Truncate(time.Second)
	jobs := []scheduler.Job{
		{
			ID:             "job-pending",
			Provider:       ref.Provider,
			ConversationID: ref.ConversationID,
			Route:          "task.pending",
			Payload:        `{"promptText":"pending"}`,
			RunAt:          now.Add(5 * time.Minute),
			Status:         scheduler.StatusPending,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		{
			ID:             "job-running",
			Provider:       ref.Provider,
			ConversationID: ref.ConversationID,
			Route:          "task.running",
			Payload:        `{"promptText":"running"}`,
			RunAt:          now.Add(10 * time.Minute),
			Status:         scheduler.StatusRunning,
			CreatedAt:      now.Add(time.Minute),
			UpdatedAt:      now.Add(time.Minute),
		},
		{
			ID:             "job-done",
			Provider:       ref.Provider,
			ConversationID: ref.ConversationID,
			Route:          "task.done",
			Payload:        `{"promptText":"done"}`,
			RunAt:          now.Add(15 * time.Minute),
			Status:         scheduler.StatusDone,
			CreatedAt:      now.Add(2 * time.Minute),
			UpdatedAt:      now.Add(2 * time.Minute),
		},
		{
			ID:             "job-cancelled",
			Provider:       ref.Provider,
			ConversationID: ref.ConversationID,
			Route:          "task.cancelled",
			Payload:        `{"promptText":"cancelled"}`,
			RunAt:          now.Add(20 * time.Minute),
			Status:         scheduler.StatusCancel,
			CreatedAt:      now.Add(3 * time.Minute),
			UpdatedAt:      now.Add(3 * time.Minute),
		},
		{
			ID:             "job-other",
			Provider:       ref.Provider,
			ConversationID: "other-chat",
			Route:          "task.other",
			Payload:        `{"promptText":"other"}`,
			RunAt:          now.Add(25 * time.Minute),
			Status:         scheduler.StatusPending,
			CreatedAt:      now.Add(4 * time.Minute),
			UpdatedAt:      now.Add(4 * time.Minute),
		},
	}
	for _, job := range jobs {
		if err := store.CreateJob(job); err != nil {
			t.Fatalf("create job %s: %v", job.ID, err)
		}
	}

	listed, err := store.ListActiveJobs(ref, 100)
	if err != nil {
		t.Fatalf("list active jobs: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("job count = %d, want 2", len(listed))
	}
	if listed[0].ID != "job-pending" || listed[1].ID != "job-running" {
		t.Fatalf("unexpected active jobs: %+v", listed)
	}
}

func TestStoreListActiveJobsReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	listed, err := store.ListActiveJobs(conversation.Ref{Provider: "feishu", ConversationID: "chat-empty"}, 100)
	if err != nil {
		t.Fatalf("list active jobs: %v", err)
	}
	if listed == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(listed) != 0 {
		t.Fatalf("job count = %d, want 0", len(listed))
	}
}

func TestStoreProgress(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-progress"}
	record := progress.Record{
		Provider:          ref.Provider,
		ConversationID:    ref.ConversationID,
		LastMessageID:     "om_xxx",
		LastMessageTimeMS: 123456789,
		UpdatedAt:         time.Now().UTC(),
	}
	if err := store.UpsertProgress(record); err != nil {
		t.Fatalf("upsert progress: %v", err)
	}

	loaded, err := store.GetProgress(ref)
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if loaded == nil || loaded.LastMessageID != record.LastMessageID || loaded.LastMessageTimeMS != record.LastMessageTimeMS {
		t.Fatalf("unexpected progress record: %+v", loaded)
	}
}
