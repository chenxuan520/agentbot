package sqlite

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/progress"
	"github.com/chenxuan520/agentbot/internal/scheduler"
	appstore "github.com/chenxuan520/agentbot/internal/store"
)

var _ appstore.ReplyStore = (*Store)(nil)

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
	want := appstore.WorkspaceRecord{
		Provider:        "feishu",
		ConversationID:  "legacy-chat",
		WorkspacePath:   "/tmp/legacy-chat",
		Template:        "default",
		AgentBackend:    "opencode",
		ActiveSessionID: "session-main",
		LastMessageAt:   now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if *loaded != want {
		t.Fatalf("migrated workspace = %+v, want %+v", *loaded, want)
	}

	source := appstore.ReplySourceRecord{
		SessionID:      "session-main",
		MessageID:      "msg-legacy",
		Provider:       want.Provider,
		ConversationID: want.ConversationID,
		WorkspacePath:  want.WorkspacePath,
		InputJSON:      `{"message_id":"om-legacy"}`,
	}
	if err := store.UpsertReplySource(source); err != nil {
		t.Fatalf("upsert migrated reply source: %v", err)
	}
	if sources, err := store.ListReplySources(source.SessionID); err != nil || !slices.Equal(sources, []appstore.ReplySourceRecord{source}) {
		t.Fatalf("migrated reply sources = %+v (err %v), want %+v", sources, err, source)
	}
	if err := store.MarkReplyDelivered(source.SessionID, source.MessageID); err != nil {
		t.Fatalf("mark migrated reply delivered: %v", err)
	}
	if delivered, err := store.ReplyDelivered(source.SessionID, source.MessageID); err != nil || !delivered {
		t.Fatalf("migrated reply delivered = %v (err %v), want true", delivered, err)
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
	if listed, err := store.ListTopicSessions(ref, 10); err != nil {
		t.Fatalf("list topic sessions: %v", err)
	} else if len(listed) != 2 {
		t.Fatalf("listed topic sessions = %d, want 2", len(listed))
	} else if listed[0].TopicKey != "om-root-a" || listed[1].TopicKey != "om-root-b" {
		t.Fatalf("unexpected topic sessions: %+v", listed)
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

func TestStoreReplySourcesRoundTrip(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "state.sqlite3")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if sources, err := store.ListReplySources(""); err != nil || len(sources) != 0 {
		t.Fatalf("initial reply sources = %+v (err %v), want empty", sources, err)
	}

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-a"}
	now := time.Now().UTC().Truncate(time.Second)
	workspace := appstore.WorkspaceRecord{
		Provider:        ref.Provider,
		ConversationID:  ref.ConversationID,
		WorkspacePath:   "/tmp/chat-a",
		Template:        "default",
		AgentBackend:    "opencode",
		ActiveSessionID: "session-new",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := store.Upsert(workspace); err != nil {
		t.Fatalf("upsert workspace: %v", err)
	}

	sources := []appstore.ReplySourceRecord{
		{
			SessionID:      "session-a",
			MessageID:      "msg-1",
			Provider:       "feishu",
			ConversationID: "chat-a",
			WorkspacePath:  "/tmp/chat-a",
			InputJSON:      `{"input":{"message_id":"om-1","thread_id":"thread-1","sender_id":"ou-1"},"reply_mode":"thread","slot":"main"}`,
		},
		{
			SessionID:      "session-a",
			MessageID:      "msg-2",
			Provider:       "feishu",
			ConversationID: "chat-a",
			WorkspacePath:  "/tmp/chat-a",
			InputJSON:      `{"input":{"message_id":"om-2","thread_id":"thread-2","sender_id":"ou-2"},"reply_mode":"thread","slot":"main"}`,
		},
		{
			SessionID:      "session-b",
			MessageID:      "msg-1",
			Provider:       "feishu",
			ConversationID: "chat-b",
			WorkspacePath:  "/tmp/chat-b",
			InputJSON:      `{"input":{"message_id":"om-3","sender_id":"ou-3"},"reply_mode":"message","slot":"main"}`,
		},
	}
	for i := len(sources) - 1; i >= 0; i-- {
		if err := store.UpsertReplySource(sources[i]); err != nil {
			t.Fatalf("upsert reply source %d: %v", i, err)
		}
	}

	sources[0].Provider = "other-provider"
	sources[0].ConversationID = "chat-updated"
	sources[0].WorkspacePath = "/tmp/chat-updated"
	sources[0].InputJSON = `{"input":{"message_id":"om-updated","thread_id":"thread-updated","sender_id":"ou-updated"},"reply_mode":"thread","slot":"main"}`
	if err := store.UpsertReplySource(sources[0]); err != nil {
		t.Fatalf("update reply source: %v", err)
	}

	for _, tc := range []struct {
		sessionID string
		want      []appstore.ReplySourceRecord
	}{
		{sessionID: "session-a", want: sources[:2]},
		{sessionID: "session-b", want: sources[2:]},
		{sessionID: "", want: sources},
		{sessionID: "missing"},
	} {
		listed, err := store.ListReplySources(tc.sessionID)
		if err != nil || !slices.Equal(listed, tc.want) {
			t.Fatalf("reply sources for %q = %+v (err %v), want %+v", tc.sessionID, listed, err, tc.want)
		}
	}

	if delivered, err := store.ReplyDelivered("session-a", "msg-1"); err != nil || delivered {
		t.Fatalf("initial reply delivered = %v (err %v), want false", delivered, err)
	}
	if err := store.MarkReplyDelivered("session-a", "msg-1"); err != nil {
		t.Fatalf("mark reply delivered: %v", err)
	}
	var deliveredAt int64
	if err := store.db.QueryRow(`SELECT delivered_at FROM backend_reply_deliveries WHERE session_id = ? AND message_id = ?`, "session-a", "msg-1").Scan(&deliveredAt); err != nil || deliveredAt <= 0 {
		t.Fatalf("delivered at = %d (err %v), want positive timestamp", deliveredAt, err)
	}
	if _, err := store.db.Exec(`UPDATE backend_reply_deliveries SET delivered_at = ? WHERE session_id = ? AND message_id = ?`, 123, "session-a", "msg-1"); err != nil {
		t.Fatalf("set delivered timestamp: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.MarkReplyDelivered("session-a", "msg-1"); err != nil {
			t.Fatalf("mark reply delivered again: %v", err)
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*), MIN(delivered_at) FROM backend_reply_deliveries WHERE session_id = ? AND message_id = ?`, "session-a", "msg-1").Scan(&count, &deliveredAt); err != nil || count != 1 || deliveredAt != 123 {
		t.Fatalf("delivery count = %d, delivered at = %d (err %v), want 1, 123", count, deliveredAt, err)
	}
	for _, tc := range []struct {
		sessionID string
		messageID string
		want      bool
	}{
		{sessionID: "session-a", messageID: "msg-1", want: true},
		{sessionID: "session-a", messageID: "msg-2"},
		{sessionID: "session-b", messageID: "msg-1"},
	} {
		if delivered, err := store.ReplyDelivered(tc.sessionID, tc.messageID); err != nil || delivered != tc.want {
			t.Fatalf("reply delivered for %s/%s = %v (err %v), want %v", tc.sessionID, tc.messageID, delivered, err, tc.want)
		}
	}
	if err := store.MarkReplyDelivered("session-b", "msg-1"); err != nil {
		t.Fatalf("mark reply delivered in other session: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	if listed, err := reopened.ListReplySources(""); err != nil || !slices.Equal(listed, sources) {
		t.Fatalf("reopened reply sources = %+v (err %v), want %+v", listed, err, sources)
	}
	for _, sessionID := range []string{"session-a", "session-b"} {
		if delivered, err := reopened.ReplyDelivered(sessionID, "msg-1"); err != nil || !delivered {
			t.Fatalf("reopened reply delivered for %s = %v (err %v), want true", sessionID, delivered, err)
		}
	}
	if loaded, err := reopened.Get(ref); err != nil || loaded == nil || *loaded != workspace {
		t.Fatalf("workspace after old session replies = %+v (err %v), want %+v", loaded, err, workspace)
	}
}

func TestDeleteConversationStateDeletesOnlyOwnReplies(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	refs := []conversation.Ref{
		{Provider: "feishu", ConversationID: "chat-delete"},
		{Provider: "feishu", ConversationID: "chat-keep"},
		{Provider: "other-provider", ConversationID: "chat-delete"},
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, ref := range refs {
		if err := store.Upsert(appstore.WorkspaceRecord{
			Provider:        ref.Provider,
			ConversationID:  ref.ConversationID,
			WorkspacePath:   "/tmp/" + ref.ConversationID,
			Template:        "default",
			AgentBackend:    "opencode",
			ActiveSessionID: "session-new",
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			t.Fatalf("upsert workspace: %v", err)
		}
	}

	sources := []appstore.ReplySourceRecord{
		{SessionID: "session-old", MessageID: "msg-user", Provider: refs[0].Provider, ConversationID: refs[0].ConversationID, WorkspacePath: "/tmp/chat-delete", InputJSON: `{}`},
		{SessionID: "session-older", MessageID: "msg-user", Provider: refs[0].Provider, ConversationID: refs[0].ConversationID, WorkspacePath: "/tmp/chat-delete", InputJSON: `{}`},
		{SessionID: "session-keep", MessageID: "msg-user", Provider: refs[1].Provider, ConversationID: refs[1].ConversationID, WorkspacePath: "/tmp/chat-keep", InputJSON: `{}`},
		{SessionID: "session-other", MessageID: "msg-user", Provider: refs[1].Provider, ConversationID: refs[1].ConversationID, WorkspacePath: "/tmp/chat-keep", InputJSON: `{}`},
		{SessionID: "session-provider", MessageID: "msg-user", Provider: refs[2].Provider, ConversationID: refs[2].ConversationID, WorkspacePath: "/tmp/chat-delete", InputJSON: `{}`},
	}
	for _, source := range sources {
		if err := store.UpsertReplySource(source); err != nil {
			t.Fatalf("upsert reply source: %v", err)
		}
		if err := store.MarkReplyDelivered(source.SessionID, "msg-assistant"); err != nil {
			t.Fatalf("mark reply delivered: %v", err)
		}
	}

	if err := store.DeleteConversationState(refs[0]); err != nil {
		t.Fatalf("delete conversation state: %v", err)
	}
	if err := store.DeleteConversationState(refs[0]); err != nil {
		t.Fatalf("delete conversation state again: %v", err)
	}
	if listed, err := store.ListReplySources(""); err != nil || !slices.Equal(listed, sources[2:]) {
		t.Fatalf("remaining reply sources = %+v (err %v), want %+v", listed, err, sources[2:])
	}
	for i, source := range sources {
		want := i >= 2
		if delivered, err := store.ReplyDelivered(source.SessionID, "msg-assistant"); err != nil || delivered != want {
			t.Fatalf("reply delivered for %s/msg-assistant = %v (err %v), want %v", source.SessionID, delivered, err, want)
		}
	}
	for i, ref := range refs {
		if loaded, err := store.Get(ref); err != nil || (loaded == nil) != (i == 0) {
			t.Fatalf("workspace after delete for %+v = %+v (err %v), want deleted = %v", ref, loaded, err, i == 0)
		}
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

func TestStoreReclaimRunningJobs(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-reclaim"}
	now := time.Now().UTC().Truncate(time.Second)
	mkJob := func(id, status string) scheduler.Job {
		return scheduler.Job{
			ID:             id,
			Provider:       ref.Provider,
			ConversationID: ref.ConversationID,
			Route:          "reminder.follow_up",
			Payload:        `{"promptText":"x"}`,
			RunAt:          now,
			Status:         status,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
	}
	// A running (crashed) job, plus a pending and a done job that must be left alone.
	for _, job := range []scheduler.Job{
		mkJob("job-running", scheduler.StatusRunning),
		mkJob("job-pending", scheduler.StatusPending),
		mkJob("job-done", scheduler.StatusDone),
	} {
		if err := store.CreateJob(job); err != nil {
			t.Fatalf("create job %s: %v", job.ID, err)
		}
	}

	reclaimed, deadLettered, err := store.ReclaimRunningJobs(now.Add(time.Minute), 3)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if reclaimed != 1 || deadLettered != 0 {
		t.Fatalf("reclaimed=%d deadLettered=%d, want 1 0", reclaimed, deadLettered)
	}
	got, err := store.GetJob("job-running")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != scheduler.StatusPending {
		t.Fatalf("status = %q, want pending after reclaim", got.Status)
	}

	// Reclaiming again drives attempts to the cap and dead-letters the job.
	for attempt := 0; attempt < 3; attempt++ {
		if err := store.UpdateJobStatus("job-running", scheduler.StatusRunning, now); err != nil {
			t.Fatalf("mark running: %v", err)
		}
		reclaimed, deadLettered, err = store.ReclaimRunningJobs(now.Add(time.Minute), 3)
		if err != nil {
			t.Fatalf("reclaim attempt %d: %v", attempt, err)
		}
	}
	if deadLettered != 1 {
		t.Fatalf("deadLettered=%d, want 1 once attempts hit the cap", deadLettered)
	}
	got, err = store.GetJob("job-running")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status != scheduler.StatusFailed {
		t.Fatalf("status = %q, want failed after dead-letter", got.Status)
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
