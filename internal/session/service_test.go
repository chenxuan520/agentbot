package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/progress"
	"github.com/chenxuan520/agentbot/internal/scheduler"
	"github.com/chenxuan520/agentbot/internal/session"
	appstore "github.com/chenxuan520/agentbot/internal/store"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

func TestPrepareRotatesExpiredSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 1)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-1"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	if err := service.Bind(ref, "opencode", "session-1", time.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	prepared, err := service.Prepare(ref, time.Now().UTC())
	if err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if !prepared.NeedNewSession {
		t.Fatal("expected expired session to rotate")
	}
	if prepared.ActiveSessionID != "" {
		t.Fatalf("active session = %q, want empty", prepared.ActiveSessionID)
	}
	if prepared.Workspace.Path != ws.Path {
		t.Fatalf("workspace path = %q, want %q", prepared.Workspace.Path, ws.Path)
	}
}

func TestPrepareBTWDoesNotRotateByMainTTL(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 1)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-btw"}

	if _, err := manager.Ensure(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := service.BindBTW(ref, "ou-user", "opencode", "session-btw", time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("bind btw session: %v", err)
	}

	prepared, err := service.PrepareBTW(ref, "ou-user", time.Now().UTC())
	if err != nil {
		t.Fatalf("prepare btw session: %v", err)
	}
	if prepared.NeedNewSession {
		t.Fatal("expected btw session to reuse existing session")
	}
	if prepared.ActiveSessionID != "session-btw" {
		t.Fatalf("btw session = %q, want session-btw", prepared.ActiveSessionID)
	}
}

func TestPrepareTopicReusesAndIsolatesPerKey(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 1)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-topic"}
	if _, err := manager.Ensure(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	// A topic with no session yet needs a fresh one.
	prepared, err := service.PrepareTopic(ref, "om-root-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("prepare topic a: %v", err)
	}
	if !prepared.NeedNewSession || prepared.ActiveSessionID != "" {
		t.Fatalf("prepare topic a = %+v, want new session", prepared)
	}

	// Bind it; a bound topic session is reused and is not rotated by the main TTL.
	if err := service.BindTopic(ref, "om-root-a", "opencode", "session-a", time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("bind topic a: %v", err)
	}
	prepared, err = service.PrepareTopic(ref, "om-root-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("re-prepare topic a: %v", err)
	}
	if prepared.NeedNewSession || prepared.ActiveSessionID != "session-a" {
		t.Fatalf("re-prepare topic a = %+v, want reuse session-a", prepared)
	}

	// A different topic key is isolated and starts fresh.
	prepared, err = service.PrepareTopic(ref, "om-root-b", time.Now().UTC())
	if err != nil {
		t.Fatalf("prepare topic b: %v", err)
	}
	if !prepared.NeedNewSession {
		t.Fatalf("prepare topic b = %+v, want new session", prepared)
	}

	if got, err := service.TopicSessionID(ref, "om-root-a"); err != nil || got != "session-a" {
		t.Fatalf("topic session a = %q (err %v), want session-a", got, err)
	}

	// Binding a topic must not touch the shared main session.
	current, err := service.Current(ref)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if current.ActiveSessionID != "" {
		t.Fatalf("main session = %q, want empty", current.ActiveSessionID)
	}

	if err := service.ClearTopic(ref, "om-root-a"); err != nil {
		t.Fatalf("clear topic a: %v", err)
	}
	if got, err := service.TopicSessionID(ref, "om-root-a"); err != nil || got != "" {
		t.Fatalf("topic session a after clear = %q (err %v), want empty", got, err)
	}
}

func TestPrepareTopicEmptyKeyFallsBackToMainSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 1)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-topic-empty"}
	if _, err := manager.Ensure(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	// An empty topic key routes to the shared main session.
	if err := service.BindTopic(ref, "", "opencode", "session-main", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic empty: %v", err)
	}
	current, err := service.Current(ref)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if current.ActiveSessionID != "session-main" {
		t.Fatalf("main session = %q, want session-main", current.ActiveSessionID)
	}

	prepared, err := service.PrepareTopic(ref, "", time.Now().UTC())
	if err != nil {
		t.Fatalf("prepare topic empty: %v", err)
	}
	if prepared.NeedNewSession || prepared.ActiveSessionID != "session-main" {
		t.Fatalf("prepare topic empty = %+v, want reuse main session", prepared)
	}

	// TopicSessionID with an empty key is meaningless and stays empty.
	if got, err := service.TopicSessionID(ref, ""); err != nil || got != "" {
		t.Fatalf("topic session for empty key = %q (err %v), want empty", got, err)
	}
}

func TestCurrentForSenderDoesNotReuseLegacySharedBTWSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-legacy-btw"}

	if _, err := manager.Ensure(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	record, err := store.Get(ref)
	if err != nil {
		t.Fatalf("get workspace record: %v", err)
	}
	record.BTWSessionID = "legacy-shared-btw"
	record.UpdatedAt = time.Now().UTC()
	if err := store.Upsert(*record); err != nil {
		t.Fatalf("upsert legacy btw record: %v", err)
	}

	current, err := service.CurrentForSender(ref, "ou-user")
	if err != nil {
		t.Fatalf("current for sender: %v", err)
	}
	if current.BTWSessionID != "" {
		t.Fatalf("sender-specific btw session = %q, want empty", current.BTWSessionID)
	}
	prepared, err := service.PrepareBTW(ref, "ou-user", time.Now().UTC())
	if err != nil {
		t.Fatalf("prepare btw for sender: %v", err)
	}
	if !prepared.NeedNewSession {
		t.Fatal("expected sender-specific btw session to require a new session")
	}
	legacyCurrent, err := service.Current(ref)
	if err != nil {
		t.Fatalf("current legacy view: %v", err)
	}
	if legacyCurrent.BTWSessionID != "legacy-shared-btw" {
		t.Fatalf("legacy btw session = %q, want %q", legacyCurrent.BTWSessionID, "legacy-shared-btw")
	}
}

func TestRebuildWorkspaceClearsBTWSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-rebuild-btw"}

	if _, err := manager.Ensure(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := service.Bind(ref, "opencode", "session-main", time.Now().UTC()); err != nil {
		t.Fatalf("bind main session: %v", err)
	}
	if err := service.BindBTW(ref, "ou-user-a", "opencode", "session-btw-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind btw session a: %v", err)
	}
	if err := service.BindBTW(ref, "ou-user-b", "opencode", "session-btw-b", time.Now().UTC()); err != nil {
		t.Fatalf("bind btw session b: %v", err)
	}

	rebuilt, err := service.RebuildWorkspace(ref)
	if err != nil {
		t.Fatalf("rebuild workspace: %v", err)
	}
	if rebuilt.Record.ActiveSessionID != "" {
		t.Fatalf("active session = %q, want cleared", rebuilt.Record.ActiveSessionID)
	}
	if rebuilt.Record.BTWSessionID != "" {
		t.Fatalf("btw session = %q, want cleared", rebuilt.Record.BTWSessionID)
	}
	current, err := service.CurrentForSender(ref, "ou-user-a")
	if err != nil {
		t.Fatalf("current after rebuild for user a: %v", err)
	}
	if current.ActiveSessionID != "" {
		t.Fatalf("current active session = %q, want cleared", current.ActiveSessionID)
	}
	if current.BTWSessionID != "" {
		t.Fatalf("current btw session for user a = %q, want cleared", current.BTWSessionID)
	}
	current, err = service.CurrentForSender(ref, "ou-user-b")
	if err != nil {
		t.Fatalf("current after rebuild for user b: %v", err)
	}
	if current.BTWSessionID != "" {
		t.Fatalf("current btw session for user b = %q, want cleared", current.BTWSessionID)
	}
}

func TestListMarksPerUserBTWSessions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-list-btw"}

	if _, err := manager.Ensure(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := service.BindBTW(ref, "ou-user", "opencode", "session-btw", time.Now().UTC()); err != nil {
		t.Fatalf("bind btw session: %v", err)
	}

	items, err := service.List()
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("summary count = %d, want 1", len(items))
	}
	if items[0].BTWSessionID != "<per-user>" {
		t.Fatalf("summary btw session = %q, want <per-user>", items[0].BTWSessionID)
	}
}

func TestGroupMessagePolicyReadsWorkspaceSetting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-group-setting"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	policy, err := service.GroupMessagePolicy(ref)
	if err != nil {
		t.Fatalf("group message policy: %v", err)
	}
	if policy.AcceptHumanMessagesWithoutMention {
		t.Fatalf("unexpected default group policy: %+v", policy)
	}

	settings := ws.Settings
	settings.Settings.AcceptGroupHumanMessagesWithoutMention = true
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	policy, err = service.GroupMessagePolicy(ref)
	if err != nil {
		t.Fatalf("group message policy after update: %v", err)
	}
	if !policy.AcceptHumanMessagesWithoutMention {
		t.Fatalf("unexpected updated group policy: %+v", policy)
	}
}

func TestAcceptOtherBotMessagesReadsWorkspaceSetting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-other-bot-setting"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	allowed, err := service.AcceptOtherBotMessages(ref)
	if err != nil {
		t.Fatalf("accept other bot messages: %v", err)
	}
	if allowed {
		t.Fatal("expected other bot messages to stay disabled by default")
	}

	settings := ws.Settings
	settings.Settings.AcceptOtherBotMessages = true
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	allowed, err = service.AcceptOtherBotMessages(ref)
	if err != nil {
		t.Fatalf("accept other bot messages after update: %v", err)
	}
	if !allowed {
		t.Fatal("expected other bot messages to be enabled after update")
	}
}

func TestAcceptInteractiveCardMessagesReadsWorkspaceSetting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-interactive-setting"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	allowed, err := service.AcceptInteractiveCardMessages(ref)
	if err != nil {
		t.Fatalf("accept interactive cards: %v", err)
	}
	if allowed {
		t.Fatal("expected interactive cards to stay disabled by default")
	}

	settings := ws.Settings
	settings.Settings.AcceptInteractiveCardMessages = true
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	allowed, err = service.AcceptInteractiveCardMessages(ref)
	if err != nil {
		t.Fatalf("accept interactive cards after update: %v", err)
	}
	if !allowed {
		t.Fatal("expected interactive cards to be enabled after update")
	}
}

func TestRebuildPreservesMemoryAndMounts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)
	writeTemplate(t, root, "switched", "switched-agent", 24)

	skillDir := filepath.Join(root, "agents", "skills", "skill-a")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# skill-a\n"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	writeSubagent(t, root, "code-reviewer", "# code-reviewer\n")

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-2"}
	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	defaultInfoPath := filepath.Join(ws.Path, ".agents", "memory", "info.md")
	defaultData, err := os.ReadFile(defaultInfoPath)
	if err != nil {
		t.Fatalf("read default info.md: %v", err)
	}
	if string(defaultData) != "" {
		t.Fatalf("default info.md = %q, want empty", string(defaultData))
	}

	memoryFile := filepath.Join(ws.Path, ".agents", "memory", "info.md")
	if err := os.WriteFile(memoryFile, []byte("remember this\n"), 0o644); err != nil {
		t.Fatalf("write memory file: %v", err)
	}

	settings := ws.Settings
	settings.Template = "switched"
	settings.Mounts.SkillIDs = []string{"skill-a"}
	settings.Mounts.SubagentIDs = []string{"code-reviewer"}
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	rebuilt, err := manager.Rebuild(ref)
	if err != nil {
		t.Fatalf("rebuild workspace: %v", err)
	}
	if rebuilt.Record.Template != "switched" {
		t.Fatalf("template = %q, want switched", rebuilt.Record.Template)
	}

	data, err := os.ReadFile(memoryFile)
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	if string(data) != "remember this\n" {
		t.Fatalf("memory content = %q", string(data))
	}

	agentsData, err := os.ReadFile(filepath.Join(rebuilt.Path, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read agents file: %v", err)
	}
	if string(agentsData) != "switched-agent\n" {
		t.Fatalf("agents content = %q", string(agentsData))
	}

	linkPath := filepath.Join(rebuilt.Path, ".agents", "skills", "skill-a")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("stat skill link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected skill mount to be a symlink")
	}

	subagentLinkPath := filepath.Join(rebuilt.Path, ".agents", "agents", "code-reviewer.md")
	subagentInfo, err := os.Lstat(subagentLinkPath)
	if err != nil {
		t.Fatalf("stat subagent link: %v", err)
	}
	if subagentInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected subagent mount to be a symlink")
	}

	hookFile := filepath.Join(rebuilt.Path, ".agents", "hooks", "before_message.py")
	if err := os.WriteFile(hookFile, []byte("# custom hook\n"), 0o644); err != nil {
		t.Fatalf("write custom hook file: %v", err)
	}

	rebuilt, err = manager.Rebuild(ref)
	if err != nil {
		t.Fatalf("rebuild workspace second time: %v", err)
	}

	hookData, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	if string(hookData) != "# custom hook\n" {
		t.Fatalf("hook content = %q", string(hookData))
	}

	data, err = os.ReadFile(memoryFile)
	if err != nil {
		t.Fatalf("read memory file after second rebuild: %v", err)
	}
	if string(data) != "remember this\n" {
		t.Fatalf("memory content after second rebuild = %q", string(data))
	}

	rebuildScript := filepath.Join(rebuilt.Path, ".agents", "bin", "rebuild-workspace")
	scriptData, err := os.ReadFile(rebuildScript)
	if err != nil {
		t.Fatalf("read rebuild script: %v", err)
	}
	scriptText := string(scriptData)
	if !strings.Contains(scriptText, "workspace rebuild") || strings.Contains(scriptText, ref.Provider) || strings.Contains(scriptText, ref.ConversationID) {
		t.Fatalf("unexpected rebuild script content: %q", scriptText)
	}
}

func TestRebuildWorkspaceMountsLegacySubagentSource(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)
	writeLegacySubagent(t, root, "code-reviewer", "# legacy reviewer\n")

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-legacy-subagent"}
	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	settings := ws.Settings
	settings.Mounts.SubagentIDs = []string{"code-reviewer"}
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	rebuilt, err := manager.Rebuild(ref)
	if err != nil {
		t.Fatalf("rebuild workspace: %v", err)
	}

	linkPath := filepath.Join(rebuilt.Path, ".agents", "agents", "code-reviewer.md")
	resolvedPath, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		t.Fatalf("resolve subagent link: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(filepath.Join(root, "subagents", "code-reviewer.md"))
	if err != nil {
		t.Fatalf("resolve legacy subagent path: %v", err)
	}
	if resolvedPath != wantPath {
		t.Fatalf("resolved subagent path = %q, want %q", resolvedPath, wantPath)
	}
}

func TestSwitchTemplatePreservesMemoryAndHooksAndAppliesTargetMounts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)
	writeTemplate(t, root, "ops", "ops-agent", 24)

	skillA := filepath.Join(root, "agents", "skills", "skill-a")
	if err := os.MkdirAll(skillA, 0o755); err != nil {
		t.Fatalf("mkdir skill-a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillA, "SKILL.md"), []byte("# skill-a\n"), 0o644); err != nil {
		t.Fatalf("write skill-a: %v", err)
	}
	skillB := filepath.Join(root, "agents", "skills", "skill-b")
	if err := os.MkdirAll(skillB, 0o755); err != nil {
		t.Fatalf("mkdir skill-b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillB, "SKILL.md"), []byte("# skill-b\n"), 0o644); err != nil {
		t.Fatalf("write skill-b: %v", err)
	}
	writeSubagent(t, root, "code-reviewer", "# code-reviewer\n")
	writeSubagent(t, root, "code-writer", "# code-writer\n")

	opsSettings, err := workspace.LoadSettings(filepath.Join(root, "templates", "ops"))
	if err != nil {
		t.Fatalf("load ops settings: %v", err)
	}
	opsSettings.Mounts.SkillIDs = []string{"skill-b"}
	opsSettings.Mounts.SubagentIDs = []string{"code-writer"}
	if err := workspace.SaveSettings(filepath.Join(root, "templates", "ops"), opsSettings); err != nil {
		t.Fatalf("save ops settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "templates", "ops", ".agents", "commands", "role.txt"), []byte("ops role preset\n"), 0o644); err != nil {
		t.Fatalf("write ops preset file: %v", err)
	}

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-role-switch"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	settings := ws.Settings
	settings.Mounts.SkillIDs = []string{"skill-a"}
	settings.Mounts.SubagentIDs = []string{"code-reviewer"}
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save workspace settings: %v", err)
	}
	ws, err = manager.Rebuild(ref)
	if err != nil {
		t.Fatalf("rebuild workspace: %v", err)
	}

	memoryFile := filepath.Join(ws.Path, ".agents", "memory", "info.md")
	if err := os.WriteFile(memoryFile, []byte("keep memory\n"), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	hookFile := filepath.Join(ws.Path, ".agents", "hooks", "before_message.py")
	if err := os.WriteFile(hookFile, []byte("# keep hook\n"), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if err := service.Bind(ref, "opencode", "session-old", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	switched, err := service.SwitchTemplate(ref, "ops")
	if err != nil {
		t.Fatalf("switch template: %v", err)
	}
	if switched.Settings.Template != "ops" {
		t.Fatalf("template = %q, want ops", switched.Settings.Template)
	}
	if switched.Record.ActiveSessionID != "" {
		t.Fatalf("active session = %q, want cleared", switched.Record.ActiveSessionID)
	}
	if switched.Record.Template != "ops" {
		t.Fatalf("record template = %q, want ops", switched.Record.Template)
	}

	agentsData, err := os.ReadFile(filepath.Join(switched.Path, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read switched AGENTS.md: %v", err)
	}
	if string(agentsData) != "ops-agent\n" {
		t.Fatalf("agents content = %q", string(agentsData))
	}
	agentsInfo, err := os.Lstat(filepath.Join(switched.Path, "AGENTS.md"))
	if err != nil {
		t.Fatalf("stat switched AGENTS.md: %v", err)
	}
	if agentsInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected switched AGENTS.md to be a symlink")
	}
	resolvedAgentsPath, err := filepath.EvalSymlinks(filepath.Join(switched.Path, "AGENTS.md"))
	if err != nil {
		t.Fatalf("resolve switched AGENTS.md: %v", err)
	}
	wantAgentsPath, err := filepath.EvalSymlinks(filepath.Join(root, "templates", "ops", "AGENTS.md"))
	if err != nil {
		t.Fatalf("resolve expected AGENTS.md: %v", err)
	}
	if resolvedAgentsPath != wantAgentsPath {
		t.Fatalf("resolved AGENTS path = %q, want %q", resolvedAgentsPath, wantAgentsPath)
	}
	memoryData, err := os.ReadFile(memoryFile)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	if string(memoryData) != "keep memory\n" {
		t.Fatalf("memory content = %q", string(memoryData))
	}
	hookData, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if string(hookData) != "# keep hook\n" {
		t.Fatalf("hook content = %q", string(hookData))
	}
	if _, err := os.Lstat(filepath.Join(switched.Path, ".agents", "skills", "skill-b")); err != nil {
		t.Fatalf("stat skill-b link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(switched.Path, ".agents", "skills", "skill-a")); !os.IsNotExist(err) {
		t.Fatalf("expected skill-a to be removed after switch, err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(switched.Path, ".agents", "agents", "code-writer.md")); err != nil {
		t.Fatalf("stat code-writer subagent link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(switched.Path, ".agents", "agents", "code-reviewer.md")); !os.IsNotExist(err) {
		t.Fatalf("expected code-reviewer subagent to be removed after switch, err=%v", err)
	}
	commandData, err := os.ReadFile(filepath.Join(switched.Path, ".agents", "commands", "role.txt"))
	if err != nil {
		t.Fatalf("read role preset file: %v", err)
	}
	if string(commandData) != "ops role preset\n" {
		t.Fatalf("role preset content = %q", string(commandData))
	}
}

func TestRebuildAndSwitchTemplatePreserveRuntimeState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)
	writeTemplate(t, root, "ops", "ops-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-runtime-preserve"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	// Per-conversation runtime state that a rebuild must never destroy: a runtime
	// sqlite DB, the other-bot poll dedup state, and a private memory note.
	runtimeDir := filepath.Join(ws.Path, ".agents", "runtime")
	if err := os.WriteFile(filepath.Join(runtimeDir, "session-events.sqlite3"), []byte("SESSION-DB-CONTENT"), 0o644); err != nil {
		t.Fatalf("write runtime db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "poll-group-bot-cards-state.json"), []byte(`{"m1":"t"}`), 0o644); err != nil {
		t.Fatalf("write poll state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, ".agents", "memory", "info.md"), []byte("keep memory\n"), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}

	assertPreserved := func(stage string, p *workspace.RuntimeWorkspace) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(p.Path, ".agents", "runtime", "session-events.sqlite3"))
		if err != nil {
			t.Fatalf("%s: read runtime db: %v", stage, err)
		}
		if string(got) != "SESSION-DB-CONTENT" {
			t.Fatalf("%s: runtime db content = %q, want preserved", stage, string(got))
		}
		if _, err := os.Stat(filepath.Join(p.Path, ".agents", "runtime", "poll-group-bot-cards-state.json")); err != nil {
			t.Fatalf("%s: poll dedup state missing: %v", stage, err)
		}
		mem, err := os.ReadFile(filepath.Join(p.Path, ".agents", "memory", "info.md"))
		if err != nil {
			t.Fatalf("%s: read memory: %v", stage, err)
		}
		if string(mem) != "keep memory\n" {
			t.Fatalf("%s: memory content = %q, want preserved", stage, string(mem))
		}
		// Regenerated runtime artifacts must still be present after a rebuild.
		if _, err := os.Stat(filepath.Join(p.Path, ".agents", "runtime", "localapi.json")); err != nil {
			t.Fatalf("%s: localapi.json missing: %v", stage, err)
		}
	}

	rebuilt, err := service.RebuildWorkspace(ref)
	if err != nil {
		t.Fatalf("rebuild workspace: %v", err)
	}
	assertPreserved("after rebuild", rebuilt)

	switched, err := service.SwitchTemplate(ref, "ops")
	if err != nil {
		t.Fatalf("switch template: %v", err)
	}
	if switched.Settings.Template != "ops" {
		t.Fatalf("template = %q, want ops", switched.Settings.Template)
	}
	assertPreserved("after switch", switched)
}

func TestRebuildPreservesHooksButReseedsWhenMissing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-hooks-reseed"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	// A customised hook must be preserved across a rebuild.
	hookFile := filepath.Join(ws.Path, ".agents", "hooks", "before_message.py")
	if err := os.WriteFile(hookFile, []byte("# custom hook\n"), 0o644); err != nil {
		t.Fatalf("write custom hook: %v", err)
	}
	if _, err := manager.Rebuild(ref); err != nil {
		t.Fatalf("rebuild with existing hooks: %v", err)
	}
	got, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatalf("read hook after rebuild: %v", err)
	}
	if string(got) != "# custom hook\n" {
		t.Fatalf("hook content = %q, want custom hook preserved", string(got))
	}

	// But if the whole hooks dir is gone, a rebuild must re-seed it from the
	// template (legacy parity: platform hooks like after_reply.py must exist).
	if err := os.RemoveAll(filepath.Join(ws.Path, ".agents", "hooks")); err != nil {
		t.Fatalf("remove hooks dir: %v", err)
	}
	if _, err := manager.Rebuild(ref); err != nil {
		t.Fatalf("rebuild with missing hooks: %v", err)
	}
	reseeded, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatalf("read reseeded hook: %v", err)
	}
	if string(reseeded) != "pass\n" {
		t.Fatalf("reseeded hook content = %q, want template default", string(reseeded))
	}
}

func TestRebuildPreservesCustomAgentsFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-custom-agents-rebuild"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	settings := ws.Settings
	settings.Agent.AgentsMode = workspace.AgentsModeCustom
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	ws, err = manager.Rebuild(ref)
	if err != nil {
		t.Fatalf("rebuild workspace: %v", err)
	}

	agentsPath := filepath.Join(ws.Path, workspace.AgentsFileName)
	if err := os.WriteFile(agentsPath, []byte("# custom role\n"), 0o644); err != nil {
		t.Fatalf("write custom agents: %v", err)
	}

	rebuilt, err := manager.Rebuild(ref)
	if err != nil {
		t.Fatalf("rebuild workspace second time: %v", err)
	}
	agentsData, err := os.ReadFile(filepath.Join(rebuilt.Path, workspace.AgentsFileName))
	if err != nil {
		t.Fatalf("read rebuilt agents: %v", err)
	}
	if string(agentsData) != "# custom role\n" {
		t.Fatalf("agents content = %q", string(agentsData))
	}
	agentsInfo, err := os.Lstat(filepath.Join(rebuilt.Path, workspace.AgentsFileName))
	if err != nil {
		t.Fatalf("stat rebuilt agents: %v", err)
	}
	if agentsInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected rebuilt custom AGENTS.md to stay a regular file")
	}
}

func TestSwitchTemplatePreservesCustomAgentsFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)
	writeTemplate(t, root, "ops", "ops-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-custom-agents-switch"}

	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	settings := ws.Settings
	settings.Agent.AgentsMode = workspace.AgentsModeCustom
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	ws, err = manager.Rebuild(ref)
	if err != nil {
		t.Fatalf("rebuild workspace: %v", err)
	}

	memoryFile := filepath.Join(ws.Path, ".agents", "memory", "info.md")
	if err := os.WriteFile(memoryFile, []byte("keep memory\n"), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	hookFile := filepath.Join(ws.Path, ".agents", "hooks", "before_message.py")
	if err := os.WriteFile(hookFile, []byte("# keep hook\n"), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, workspace.AgentsFileName), []byte("# custom switch role\n"), 0o644); err != nil {
		t.Fatalf("write custom agents: %v", err)
	}
	if err := service.Bind(ref, "opencode", "session-old", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	switched, err := service.SwitchTemplate(ref, "ops")
	if err != nil {
		t.Fatalf("switch template: %v", err)
	}
	if switched.Settings.Template != "ops" {
		t.Fatalf("template = %q, want ops", switched.Settings.Template)
	}
	agentsData, err := os.ReadFile(filepath.Join(switched.Path, workspace.AgentsFileName))
	if err != nil {
		t.Fatalf("read switched agents: %v", err)
	}
	if string(agentsData) != "# custom switch role\n" {
		t.Fatalf("agents content = %q", string(agentsData))
	}
	agentsInfo, err := os.Lstat(filepath.Join(switched.Path, workspace.AgentsFileName))
	if err != nil {
		t.Fatalf("stat switched agents: %v", err)
	}
	if agentsInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected switched custom AGENTS.md to stay a regular file")
	}
	memoryData, err := os.ReadFile(memoryFile)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	if string(memoryData) != "keep memory\n" {
		t.Fatalf("memory content = %q", string(memoryData))
	}
	hookData, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if string(hookData) != "# keep hook\n" {
		t.Fatalf("hook content = %q", string(hookData))
	}
}

func TestRebuildIgnoresMissingMountedSkill(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)
	writeSkill(t, root, "notes", "# Notes\n")

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-missing-skill"}
	ws, err := manager.Ensure(ref)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	settings := ws.Settings
	settings.Mounts.SkillIDs = []string{"notes", "missing-skill"}
	if err := workspace.SaveSettings(ws.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	rebuilt, err := manager.Rebuild(ref)
	if err != nil {
		t.Fatalf("rebuild workspace: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rebuilt.Path, ".agents", "skills", "notes")); err != nil {
		t.Fatalf("stat mounted notes skill: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rebuilt.Path, ".agents", "skills", "missing-skill")); !os.IsNotExist(err) {
		t.Fatalf("missing-skill err = %v, want not exist", err)
	}
}

func TestDeleteRemovesWorkspaceAndConversationState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root, "default", "default-agent", 24)

	cfg := config.Default(root)
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	service := session.NewService(store, manager)
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-delete"}
	current, err := service.Current(ref)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if err := service.Bind(ref, "opencode", "session-old", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}
	if err := service.BindBTW(ref, "ou-user", "opencode", "session-btw", time.Now().UTC()); err != nil {
		t.Fatalf("bind btw session: %v", err)
	}
	access := accesstoken.NewService(store, "project-token", "auth-secret")
	if _, err := access.EnsureSessionToken(ref); err != nil {
		t.Fatalf("ensure session token: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateJob(scheduler.Job{ID: "job-1", Provider: ref.Provider, ConversationID: ref.ConversationID, Route: "task.check", Payload: `{"promptText":"check"}`, RunAt: now, Status: scheduler.StatusPending, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := store.CreateRule(control.Rule{ID: "rule-1", Provider: ref.Provider, ConversationID: ref.ConversationID, Kind: control.KindRefuse, Scope: "conversation", Reason: "mute", UntilAt: now.Add(time.Hour), Status: control.StatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if err := store.UpsertProgress(progress.Record{Provider: ref.Provider, ConversationID: ref.ConversationID, LastMessageID: "om_last", LastMessageTimeMS: 123, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert progress: %v", err)
	}
	promptDir := ref.WorkspacePath(filepath.Join(root, "data", "scheduler", "prompts"))
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatalf("mkdir prompt dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "prompt.md"), []byte("prompt\n"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	if err := service.Delete(ref); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := os.Stat(current.Workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace path err = %v, want not exist", err)
	}
	if _, err := os.Stat(promptDir); !os.IsNotExist(err) {
		t.Fatalf("prompt dir err = %v, want not exist", err)
	}
	record, err := store.Get(ref)
	if err != nil {
		t.Fatalf("get record after delete: %v", err)
	}
	if record != nil {
		t.Fatal("expected workspace record to be deleted")
	}
	tokenRecord, err := store.GetSessionToken(ref)
	if err != nil {
		t.Fatalf("get token after delete: %v", err)
	}
	if tokenRecord != nil {
		t.Fatal("expected session token to be deleted")
	}
	jobs, err := store.ListJobs(ref, 10)
	if err != nil {
		t.Fatalf("list jobs after delete: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs len = %d, want 0", len(jobs))
	}
	rules, err := store.ListActiveRules(ref, now)
	if err != nil {
		t.Fatalf("list rules after delete: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("rules len = %d, want 0", len(rules))
	}
	progressRecord, err := store.GetProgress(ref)
	if err != nil {
		t.Fatalf("get progress after delete: %v", err)
	}
	if progressRecord != nil {
		t.Fatal("expected progress to be deleted")
	}
	if sessionID, err := store.GetBTWSession(ref, "ou-user"); err != nil {
		t.Fatalf("get btw session after delete: %v", err)
	} else if sessionID != "" {
		t.Fatalf("btw session after delete = %q, want empty", sessionID)
	}
}

func writeTemplate(t *testing.T, root, name, agentsText string, ttlHours int) {
	t.Helper()

	templateDir := filepath.Join(root, "templates", name)
	if err := os.MkdirAll(filepath.Join(templateDir, ".agents", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir template dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(templateDir, ".agents", "commands"), 0o755); err != nil {
		t.Fatalf("mkdir template commands dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "AGENTS.md"), []byte(agentsText+"\n"), 0o644); err != nil {
		t.Fatalf("write agents file: %v", err)
	}
	settings := workspace.DefaultSettings()
	settings.Template = name
	settings.Settings.HistoryTTLHours = ttlHours
	if err := workspace.SaveSettings(templateDir, settings); err != nil {
		t.Fatalf("write settings file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, ".agents", "hooks", "before_message.py"), []byte("pass\n"), 0o644); err != nil {
		t.Fatalf("write hook file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, ".agents", "hooks", "on_reaction.py"), []byte("pass\n"), 0o644); err != nil {
		t.Fatalf("write reaction hook file: %v", err)
	}
}

func writeSubagent(t *testing.T, root, subagentID, content string) {
	t.Helper()

	subagentDir := filepath.Join(root, "agents", "subagents")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatalf("mkdir subagents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subagentDir, subagentID+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write subagent file: %v", err)
	}
}

func writeLegacySubagent(t *testing.T, root, subagentID, content string) {
	t.Helper()

	legacyDir := filepath.Join(root, "subagents")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy subagents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, subagentID+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write legacy subagent file: %v", err)
	}
}

func writeSkill(t *testing.T, root, skillID, content string) {
	t.Helper()

	skillDir := filepath.Join(root, "agents", "skills", skillID)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
}

type deleteFallbackStore struct {
	deleteBTWSessionsCalled   bool
	deleteTopicSessionsCalled bool
	deleteWorkspaceCalled     bool
	deleteSessionTokenCalled  bool
}

func (s *deleteFallbackStore) Get(conversation.Ref) (*appstore.WorkspaceRecord, error) {
	return nil, nil
}

func (s *deleteFallbackStore) Upsert(appstore.WorkspaceRecord) error {
	return nil
}

func (s *deleteFallbackStore) Delete(conversation.Ref) error {
	s.deleteWorkspaceCalled = true
	return nil
}

func (s *deleteFallbackStore) List() ([]appstore.WorkspaceRecord, error) {
	return nil, nil
}

func (s *deleteFallbackStore) GetBTWSession(conversation.Ref, string) (string, error) {
	return "", nil
}

func (s *deleteFallbackStore) HasBTWSessions(conversation.Ref) (bool, error) {
	return false, nil
}

func (s *deleteFallbackStore) UpsertBTWSession(conversation.Ref, string, string, time.Time) error {
	return nil
}

func (s *deleteFallbackStore) DeleteBTWSession(conversation.Ref, string) error {
	return nil
}

func (s *deleteFallbackStore) DeleteBTWSessions(conversation.Ref) error {
	s.deleteBTWSessionsCalled = true
	return nil
}

func (s *deleteFallbackStore) GetTopicSession(conversation.Ref, string) (string, error) {
	return "", nil
}

func (s *deleteFallbackStore) HasTopicSessions(conversation.Ref) (bool, error) {
	return false, nil
}

func (s *deleteFallbackStore) UpsertTopicSession(conversation.Ref, string, string, time.Time) error {
	return nil
}

func (s *deleteFallbackStore) DeleteTopicSession(conversation.Ref, string) error {
	return nil
}

func (s *deleteFallbackStore) DeleteTopicSessions(conversation.Ref) error {
	s.deleteTopicSessionsCalled = true
	return nil
}

func (s *deleteFallbackStore) DeleteSessionToken(conversation.Ref) error {
	s.deleteSessionTokenCalled = true
	return nil
}
