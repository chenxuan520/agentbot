package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	appstore "github.com/chenxuan520/agentbot/internal/store"
)

var ErrWorkspaceNotFound = errors.New("workspace not found")

type Manager struct {
	cfg   config.Config
	store appstore.WorkspaceStore
}

type RuntimeWorkspace struct {
	Ref      conversation.Ref
	Path     string
	Settings Settings
	Record   appstore.WorkspaceRecord
}

func NewManager(cfg config.Config, store appstore.WorkspaceStore) *Manager {
	return &Manager{cfg: cfg, store: store}
}

func (m *Manager) Ensure(ref conversation.Ref) (*RuntimeWorkspace, error) {
	record, err := m.store.Get(ref)
	if err != nil {
		return nil, err
	}

	workspacePath := ref.WorkspacePath(m.cfg.ChatRootDir)
	created := false
	if record != nil && record.WorkspacePath != "" {
		workspacePath = record.WorkspacePath
	}

	if _, err := os.Stat(workspacePath); errors.Is(err, os.ErrNotExist) {
		templateName := DefaultSettings().Template
		if record != nil && record.Template != "" {
			templateName = record.Template
		}
		settings, err := m.loadTemplateSettings(templateName)
		if err != nil {
			return nil, err
		}
		if err := m.materialize(ref, workspacePath, settings, nil, true); err != nil {
			return nil, err
		}
		created = true
	}

	settings, err := LoadSettings(workspacePath)
	if err != nil {
		return nil, err
	}
	if err := m.syncRuntimeArtifacts(ref, workspacePath, settings); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	current := appstore.WorkspaceRecord{
		Provider:       ref.Provider,
		ConversationID: ref.ConversationID,
		WorkspacePath:  workspacePath,
		Template:       settings.Template,
		AgentBackend:   settings.Agent.Backend,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if record != nil {
		current = *record
		current.WorkspacePath = workspacePath
		current.Template = settings.Template
		current.AgentBackend = settings.Agent.Backend
		current.UpdatedAt = now
		if current.CreatedAt.IsZero() {
			current.CreatedAt = now
		}
	}

	if err := m.store.Upsert(current); err != nil {
		return nil, err
	}
	if created {
		if err := m.syncLegacyBotPollerState(); err != nil {
			return nil, err
		}
	}

	return &RuntimeWorkspace{Ref: ref, Path: workspacePath, Settings: settings, Record: current}, nil
}

func (m *Manager) Rebuild(ref conversation.Ref) (*RuntimeWorkspace, error) {
	record, err := m.store.Get(ref)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, ErrWorkspaceNotFound
	}

	settings, err := LoadSettings(record.WorkspacePath)
	if err != nil {
		return nil, err
	}

	// Capture the current custom AGENTS.md (if any) before the overlay below
	// overwrites it, so SyncAgentsFile can re-apply the session's own content.
	customAgentsData, _, err := readCustomAgentsSnapshot(record.WorkspacePath)
	if err != nil {
		return nil, err
	}

	// Rebuild reconciles the template-owned surface in place. It must never
	// delete the whole workspace: the session-owned state dirs (memory, hooks,
	// session-skills and runtime — which holds runtime databases and the
	// other-bot poll dedup state — have to survive a rebuild.
	if err := m.materialize(ref, record.WorkspacePath, settings, customAgentsData, false); err != nil {
		return nil, err
	}

	record.Template = settings.Template
	record.AgentBackend = settings.Agent.Backend
	record.ActiveSessionID = ""
	record.BTWSessionID = ""
	record.UpdatedAt = time.Now().UTC()
	if err := m.store.DeleteBTWSessions(ref); err != nil {
		return nil, err
	}
	if err := m.store.DeleteTopicSessions(ref); err != nil {
		return nil, err
	}
	if err := m.store.Upsert(*record); err != nil {
		return nil, err
	}
	if err := m.syncLegacyBotPollerState(); err != nil {
		return nil, err
	}

	return &RuntimeWorkspace{Ref: ref, Path: record.WorkspacePath, Settings: settings, Record: *record}, nil
}

func (m *Manager) Delete(ref conversation.Ref) error {
	record, err := m.store.Get(ref)
	if err != nil {
		return err
	}
	workspacePath := ref.WorkspacePath(m.cfg.ChatRootDir)
	if record != nil && strings.TrimSpace(record.WorkspacePath) != "" {
		workspacePath = record.WorkspacePath
	}
	if err := os.RemoveAll(workspacePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	promptPath := ref.WorkspacePath(filepath.Join(m.cfg.RootDir, "data", "scheduler", "prompts"))
	if err := os.RemoveAll(promptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) SyncLegacyBotPoller(ref conversation.Ref) error {
	return m.syncLegacyBotPollerState()
}

func (m *Manager) syncLegacyBotPollerState() error {
	return syncLegacyBotPoller(m.cfg)
}

// EnabledOtherBotMessageRefs returns the conversations whose settings opt in to
// replaying other bots' messages (settings.acceptOtherBotMessages).
func (m *Manager) EnabledOtherBotMessageRefs() ([]conversation.Ref, error) {
	records, err := m.store.List()
	if err != nil {
		return nil, err
	}
	refs := make([]conversation.Ref, 0, len(records))
	for _, record := range records {
		ref := conversation.Ref{Provider: record.Provider, ConversationID: record.ConversationID}
		workspacePath := record.WorkspacePath
		if workspacePath == "" {
			workspacePath = ref.WorkspacePath(m.cfg.ChatRootDir)
		}
		settings, err := LoadSettings(workspacePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if settings.Settings.AcceptOtherBotMessages {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func (m *Manager) ListTemplates() ([]string, error) {
	entries, err := os.ReadDir(m.cfg.TemplateRootDir)
	if err != nil {
		return nil, err
	}

	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		settingsPath := filepath.Join(m.cfg.TemplateRootDir, entry.Name(), SettingsFileName)
		if _, err := os.Stat(settingsPath); err != nil {
			continue
		}
		result = append(result, entry.Name())
	}
	sort.Strings(result)
	return result, nil
}

func (m *Manager) SwitchTemplate(ref conversation.Ref, templateName string) (*RuntimeWorkspace, error) {
	templateName = strings.TrimSpace(templateName)
	if templateName == "" {
		return nil, fmt.Errorf("template name is required")
	}

	ws, err := m.Ensure(ref)
	if err != nil {
		return nil, err
	}
	targetSettings, err := m.loadTemplateSettings(templateName)
	if err != nil {
		return nil, err
	}

	settings := ws.Settings
	settings.Template = targetSettings.Template
	settings.Mounts = targetSettings.Mounts
	if err := SaveSettings(ws.Path, settings); err != nil {
		return nil, err
	}
	return m.Rebuild(ref)
}

// materialize lays the template-owned files into a workspace and then runs the
// deterministic reconcile steps (settings, AGENTS.md, runtime artifacts, mount
// links).
//
// When fresh is true (first creation) the whole template tree is copied into a
// clean directory. When fresh is false (rebuild) the template is overlaid in
// place: existing files are refreshed but the workspace is never deleted, and
// the session-owned state dirs (memory/hooks/session-skills/runtime) are left
// untouched so accumulated state — most importantly runtime database files and
// the other-bot poll dedup state under .agents/runtime — survives the rebuild.
func (m *Manager) materialize(ref conversation.Ref, workspacePath string, settings Settings, customAgentsData []byte, fresh bool) error {
	templateDir := filepath.Join(m.cfg.TemplateRootDir, settings.Template)
	if _, err := os.Stat(templateDir); err != nil {
		return fmt.Errorf("template %q not found: %w", settings.Template, err)
	}

	if fresh {
		if err := os.RemoveAll(workspacePath); err != nil {
			return err
		}
		if err := copyDir(templateDir, workspacePath); err != nil {
			return err
		}
	} else {
		if err := copyDirOverlay(templateDir, workspacePath, preservedOverlayRelDirs); err != nil {
			return err
		}
	}

	if err := SaveSettings(workspacePath, settings); err != nil {
		return err
	}
	if err := SyncAgentsFile(templateDir, workspacePath, settings, customAgentsData); err != nil {
		return err
	}
	if err := m.syncRuntimeArtifacts(ref, workspacePath, settings); err != nil {
		return err
	}
	if err := ensureDefaultMemoryFiles(workspacePath); err != nil {
		return err
	}
	return nil
}

// preservedOverlayRelDirs are workspace-relative subtrees that an in-place
// rebuild must never overwrite or delete. They are owned by the session/runtime,
// not the template: memory and session-skills are private state, hooks are kept
// as the session customised them, and runtime holds context.env/localapi.json
// (regenerated separately) plus runtime state files and poll dedup state.
var preservedOverlayRelDirs = map[string]struct{}{
	filepath.Join(".agents", "memory"):         {},
	filepath.Join(".agents", "hooks"):          {},
	filepath.Join(".agents", "session-skills"): {},
	filepath.Join(".agents", "runtime"):        {},
}

func (m *Manager) loadTemplateSettings(templateName string) (Settings, error) {
	templateDir := filepath.Join(m.cfg.TemplateRootDir, templateName)
	settings, err := LoadSettings(templateDir)
	if err != nil {
		return Settings{}, err
	}
	settings.Template = templateName
	return settings, nil
}

func (m *Manager) syncRuntimeArtifacts(ref conversation.Ref, workspacePath string, settings Settings) error {
	if err := ensureRuntimeLayout(workspacePath, settings.Agent.Backend, m.cfg.RootDir, ref); err != nil {
		return err
	}
	if err := ensureDefaultMemoryFiles(workspacePath); err != nil {
		return err
	}
	if err := writeRuntimeContext(m.cfg, workspacePath, ref); err != nil {
		return err
	}
	if err := writeBackendConfig(m.cfg, workspacePath, settings); err != nil {
		return err
	}
	if err := reconcileSkillLinks(m.cfg.SkillRootDir, sessionSkillRoot(workspacePath), workspacePath, settings.Mounts.SkillIDs); err != nil {
		return err
	}
	if err := reconcileSubagentLinks(m.cfg.SubagentRootDir, filepath.Join(m.cfg.RootDir, "subagents"), workspacePath, settings.Mounts.SubagentIDs); err != nil {
		return err
	}
	if err := reconcileRepoLinks(m.cfg.RepoRootDir, workspacePath, settings.Mounts.RepoIDs); err != nil {
		return err
	}
	return nil
}

func ensureRuntimeLayout(workspacePath, backend, rootDir string, ref conversation.Ref) error {
	paths := []string{
		filepath.Join(workspacePath, ".agents"),
		filepath.Join(workspacePath, ".agents", "bin"),
		filepath.Join(workspacePath, ".agents", "hooks"),
		filepath.Join(workspacePath, ".agents", "memory"),
		filepath.Join(workspacePath, ".agents", "session-skills"),
		filepath.Join(workspacePath, ".agents", "runtime"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}

	backend = strings.TrimSpace(backend)
	if backend == "" {
		backend = "opencode"
	}

	_ = os.Remove(filepath.Join(workspacePath, "skills"))

	aliasPath := filepath.Join(workspacePath, "."+backend)
	_ = os.Remove(aliasPath)
	if err := os.Symlink(".agents", aliasPath); err != nil {
		return err
	}

	if backend == "claudecode" {
		claudePath := filepath.Join(workspacePath, "CLAUDE.md")
		_ = os.Remove(claudePath)
		if err := os.Symlink(AgentsFileName, claudePath); err != nil {
			return err
		}
	}

	if err := writeCLIWrapper(workspacePath, rootDir, ref); err != nil {
		return err
	}
	return writeWorkspaceRebuildScript(workspacePath, ref)
}

func sessionSkillRoot(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "session-skills")
}

func writeCLIWrapper(workspacePath, rootDir string, ref conversation.Ref) error {
	content := "#!/usr/bin/env bash\nset -euo pipefail\nexport AGENT_BOT_ROOT=" + shellQuote(rootDir) + "\nexport AGENT_BOT_PROVIDER=" + shellQuote(ref.Provider) + "\nexport AGENT_BOT_CONVERSATION_ID=" + shellQuote(ref.ConversationID) + "\ngo run \"" + filepath.Join(rootDir, "cmd", "agent-bot") + "\" \"$@\"\n"
	path := filepath.Join(workspacePath, ".agents", "bin", "agent-bot")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return err
	}
	return nil
}

func writeWorkspaceRebuildScript(workspacePath string, ref conversation.Ref) error {
	content := "#!/usr/bin/env bash\nset -euo pipefail\nDIR=\"$(cd \"$(dirname \"$0\")\" && pwd)\"\nexec \"$DIR/agent-bot\" workspace rebuild\n"
	path := filepath.Join(workspacePath, ".agents", "bin", "rebuild-workspace")
	return os.WriteFile(path, []byte(content), 0o755)
}

func ensureDefaultMemoryFiles(workspacePath string) error {
	infoPath := filepath.Join(workspacePath, ".agents", "memory", "info.md")
	if _, err := os.Stat(infoPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(infoPath, []byte{}, 0o644)
}

func shellQuote(value string) string {
	escaped := strings.ReplaceAll(value, "'", `'"'"'`)
	return "'" + escaped + "'"
}
