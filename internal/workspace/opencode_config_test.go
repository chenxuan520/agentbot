package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenxuan520/agentbot/internal/config"
)

func TestWriteBackendConfigMergesSessionOpencodeConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspacePath := filepath.Join(root, "chat")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	cfg := config.Default(root)
	cfg.OpencodeConfig = map[string]any{
		"provider": map[string]any{
			"openai": map[string]any{
				"models": map[string]any{
					"gpt-5": map[string]any{
						"options": map[string]any{
							"reasoningEffort": "medium",
							"textVerbosity":   "low",
						},
					},
				},
			},
		},
	}
	settings := DefaultSettings()
	settings.Agent.OpencodeConfig = map[string]any{
		"provider": map[string]any{
			"openai": map[string]any{
				"models": map[string]any{
					"gpt-5": map[string]any{
						"options": map[string]any{
							"reasoningEffort": "high",
						},
					},
				},
			},
		},
	}

	if err := writeBackendConfig(cfg, workspacePath, settings); err != nil {
		t.Fatalf("write backend config: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspacePath, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode opencode.json: %v", err)
	}

	if payload["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("schema = %v", payload["$schema"])
	}

	provider := payload["provider"].(map[string]any)
	openai := provider["openai"].(map[string]any)
	models := openai["models"].(map[string]any)
	gpt5 := models["gpt-5"].(map[string]any)
	options := gpt5["options"].(map[string]any)

	if options["reasoningEffort"] != "high" {
		t.Fatalf("reasoningEffort = %v, want high", options["reasoningEffort"])
	}
	if options["textVerbosity"] != "low" {
		t.Fatalf("textVerbosity = %v, want low", options["textVerbosity"])
	}
}

func TestWriteBackendConfigSupportsSessionOnlyPatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspacePath := filepath.Join(root, "chat")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	settings := DefaultSettings()
	settings.Agent.OpencodeConfig = map[string]any{
		"provider": map[string]any{
			"anthropic": map[string]any{
				"models": map[string]any{
					"claude-sonnet-4-5-20250929": map[string]any{
						"options": map[string]any{
							"thinking": map[string]any{
								"type":         "enabled",
								"budgetTokens": 8000,
							},
						},
					},
				},
			},
		},
	}

	if err := writeBackendConfig(config.Default(root), workspacePath, settings); err != nil {
		t.Fatalf("write backend config: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspacePath, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode opencode.json: %v", err)
	}

	provider := payload["provider"].(map[string]any)
	anthropic := provider["anthropic"].(map[string]any)
	models := anthropic["models"].(map[string]any)
	model := models["claude-sonnet-4-5-20250929"].(map[string]any)
	options := model["options"].(map[string]any)
	thinking := options["thinking"].(map[string]any)

	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled", thinking["type"])
	}
	if thinking["budgetTokens"] != float64(8000) {
		t.Fatalf("thinking.budgetTokens = %v, want 8000", thinking["budgetTokens"])
	}

	permission := payload["permission"].(map[string]any)
	if permission["question"] != "deny" {
		t.Fatalf("permission.question = %v, want deny", permission["question"])
	}
}

func TestWriteBackendConfigWritesQuestionDenyWithoutCustomConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspacePath := filepath.Join(root, "chat")
	if err := os.MkdirAll(filepath.Join(workspacePath, ".agents"), 0o755); err != nil {
		t.Fatalf("mkdir workspace agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "opencode.json"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale root config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, ".agents", "opencode.json"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	if err := writeBackendConfig(config.Default(root), workspacePath, DefaultSettings()); err != nil {
		t.Fatalf("write backend config: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspacePath, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode opencode.json: %v", err)
	}

	permission := payload["permission"].(map[string]any)
	if permission["question"] != "deny" {
		t.Fatalf("permission.question = %v, want deny", permission["question"])
	}
	if _, err := os.Stat(filepath.Join(workspacePath, ".agents", "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy .agents/opencode.json err = %v, want not exist", err)
	}
}

func TestWriteBackendConfigOverridesQuestionPermissionToDeny(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspacePath := filepath.Join(root, "chat")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	cfg := config.Default(root)
	cfg.OpencodeConfig = map[string]any{
		"permission": map[string]any{
			"bash":     "allow",
			"question": "allow",
		},
		"agent": map[string]any{
			"build": map[string]any{
				"permission": map[string]any{
					"question": "allow",
				},
			},
		},
	}
	settings := DefaultSettings()
	settings.Agent.OpencodeConfig = map[string]any{
		"permission": map[string]any{
			"read":     "allow",
			"question": "ask",
		},
		"agent": map[string]any{
			"custom-reviewer": map[string]any{
				"permission": map[string]any{
					"question": "allow",
				},
			},
		},
	}

	if err := writeBackendConfig(cfg, workspacePath, settings); err != nil {
		t.Fatalf("write backend config: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspacePath, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode opencode.json: %v", err)
	}

	permission := payload["permission"].(map[string]any)
	if permission["bash"] != "allow" {
		t.Fatalf("permission.bash = %v, want allow", permission["bash"])
	}
	if permission["read"] != "allow" {
		t.Fatalf("permission.read = %v, want allow", permission["read"])
	}
	if permission["question"] != "deny" {
		t.Fatalf("permission.question = %v, want deny", permission["question"])
	}

	agents := payload["agent"].(map[string]any)
	build := agents["build"].(map[string]any)
	buildPermission := build["permission"].(map[string]any)
	if buildPermission["question"] != "deny" {
		t.Fatalf("agent.build.permission.question = %v, want deny", buildPermission["question"])
	}
	custom := agents["custom-reviewer"].(map[string]any)
	customPermission := custom["permission"].(map[string]any)
	if customPermission["question"] != "deny" {
		t.Fatalf("agent.custom-reviewer.permission.question = %v, want deny", customPermission["question"])
	}
}

func TestWriteBackendConfigRemovesStaleRootFileForNonOpencodeBackend(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspacePath := filepath.Join(root, "chat")
	if err := os.MkdirAll(filepath.Join(workspacePath, ".agents"), 0o755); err != nil {
		t.Fatalf("mkdir workspace agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "opencode.json"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale root config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, ".agents", "opencode.json"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	settings := DefaultSettings()
	settings.Agent.Backend = "claudecode"
	if err := writeBackendConfig(config.Default(root), workspacePath, settings); err != nil {
		t.Fatalf("write backend config: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspacePath, "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("root opencode.json err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(workspacePath, ".agents", "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy .agents/opencode.json err = %v, want not exist", err)
	}
}
