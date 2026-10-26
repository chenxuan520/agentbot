package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestWriteBackendConfigNativeQuestionPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		global  string
		session string
		want    string
	}{
		{
			name:   "native_global_allow",
			global: `{"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"*","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"}]}`,
			want:   `{"permission":{"question":"deny"},"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"*","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"},{"action":"question","resource":"*","effect":"deny"}]}`,
		},
		{
			name:    "native_session_allow",
			session: `{"permissions":[{"action":"question","resource":"*","effect":"allow"}]}`,
			want:    `{"permission":{"question":"deny"},"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"question","resource":"*","effect":"deny"}]}`,
		},
		{
			name: "native_agents_allow",
			global: `{"agents":{
				"build":{"description":"builder","permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"bash","resource":"*","effect":"ask"}]},
				"review":{"description":"reviewer","permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"}]},
				"empty":{"permissions":[]},"implicit":{"description":"no permissions"}
			}}`,
			want: `{"permission":{"question":"deny"},"agents":{
				"build":{"description":"builder","permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"bash","resource":"*","effect":"ask"},{"action":"question","resource":"*","effect":"deny"}]},
				"review":{"description":"reviewer","permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"},{"action":"question","resource":"*","effect":"deny"}]},
				"empty":{"permissions":[{"action":"question","resource":"*","effect":"deny"}]},
				"implicit":{"description":"no permissions","permissions":[{"action":"question","resource":"*","effect":"deny"}]}
			}}`,
		},
		{
			name: "mixed_fields_and_native_provider_variants",
			global: `{
				"permission":{"*":"ask","question":"allow","task":"ask"},
				"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"}],
				"model":"ttadk/gpt55#xhigh",
				"provider":{"legacy":{"models":{"gpt-5":{"options":{"textVerbosity":"low"}}}}},
				"providers":{"ttadk":{"models":{"gpt55":{"name":"GPT-5.5","options":{"textVerbosity":"low"},"body":{"temperature":0.2},"variants":{"xhigh":{"options":{"reasoningEffort":"xhigh"}}}}}}},
				"agent":{"build":{"model":"ttadk/gpt55#xhigh","permission":{"bash":"allow","question":"allow","task":"ask"}}},
				"agents":{"native":{"model":"ttadk/gpt55#xhigh","permissions":[{"action":"question","resource":"*","effect":"allow"}]}}
			}`,
			session: `{"providers":{"ttadk":{"models":{"gpt55":{"options":{"reasoningEffort":"high"},"body":{"text":{"verbosity":"low"}},"variants":{"xhigh":{"body":{"reasoning":{"effort":"xhigh"}}}}}}}}}`,
			want: `{
				"permission":{"*":"ask","question":"deny","task":"ask"},
				"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"},{"action":"question","resource":"*","effect":"deny"}],
				"model":"ttadk/gpt55#xhigh",
				"provider":{"legacy":{"models":{"gpt-5":{"options":{"textVerbosity":"low"}}}}},
				"providers":{"ttadk":{"models":{"gpt55":{"name":"GPT-5.5","options":{"textVerbosity":"low","reasoningEffort":"high"},"body":{"temperature":0.2,"text":{"verbosity":"low"}},"variants":{"xhigh":{"options":{"reasoningEffort":"xhigh"},"body":{"reasoning":{"effort":"xhigh"}}}}}}}},
				"agent":{"build":{"model":"ttadk/gpt55#xhigh","permission":{"bash":"allow","question":"deny","task":"ask"}}},
				"agents":{"native":{"model":"ttadk/gpt55#xhigh","permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"question","resource":"*","effect":"deny"}]}}
			}`,
		},
		{
			name:   "native_empty_permissions",
			global: `{"permissions":[]}`,
			want:   `{"permission":{"question":"deny"},"permissions":[{"action":"question","resource":"*","effect":"deny"}]}`,
		},
		{
			name:   "native_trailing_deny",
			global: `{"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"question","resource":"*","effect":"deny"}],"agents":{"build":{"permissions":[{"action":"question","resource":"*","effect":"deny"}]}}}`,
			want:   `{"permission":{"question":"deny"},"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"question","resource":"*","effect":"deny"}],"agents":{"build":{"permissions":[{"action":"question","resource":"*","effect":"deny"}]}}}`,
		},
		{
			name:   "native_deny_before_allow",
			global: `{"permissions":[{"action":"question","resource":"*","effect":"deny"},{"action":"question","resource":"*","effect":"allow"}],"agents":{"build":{"permissions":[{"action":"question","resource":"*","effect":"deny"},{"action":"question","resource":"*","effect":"allow"}]}}}`,
			want:   `{"permission":{"question":"deny"},"permissions":[{"action":"question","resource":"*","effect":"deny"},{"action":"question","resource":"*","effect":"allow"},{"action":"question","resource":"*","effect":"deny"}],"agents":{"build":{"permissions":[{"action":"question","resource":"*","effect":"deny"},{"action":"question","resource":"*","effect":"allow"},{"action":"question","resource":"*","effect":"deny"}]}}}`,
		},
		{
			name: "no_config",
			want: `{"permission":{"question":"deny"}}`,
		},
		{
			name:   "legacy_only",
			global: `{"permission":{"*":"ask","question":"allow","task":"ask"},"agent":{"build":{"description":"legacy","permission":{"bash":"allow","question":"allow","task":"ask"}}}}`,
			want:   `{"permission":{"*":"ask","question":"deny","task":"ask"},"agent":{"build":{"description":"legacy","permission":{"bash":"allow","question":"deny","task":"ask"}}}}`,
		},
		{
			name:   "invalid_native_fields_unchanged",
			global: `{"permissions":"allow","agents":{"broken":false,"null":null,"invalid":{"description":"unchanged","permissions":"allow"}}}`,
			want:   `{"permission":{"question":"deny"},"permissions":"allow","agents":{"broken":false,"null":null,"invalid":{"description":"unchanged","permissions":"allow"}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			cfg := config.Default(root)
			if tt.global != "" {
				if err := json.Unmarshal([]byte(tt.global), &cfg.OpencodeConfig); err != nil {
					t.Fatalf("decode global config: %v", err)
				}
			}
			settings := DefaultSettings()
			if tt.session != "" {
				if err := json.Unmarshal([]byte(tt.session), &settings.Agent.OpencodeConfig); err != nil {
					t.Fatalf("decode session config: %v", err)
				}
			}
			if err := writeBackendConfig(cfg, root, settings); err != nil {
				t.Fatalf("write backend config: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(root, "opencode.json"))
			if err != nil {
				t.Fatalf("read opencode.json: %v", err)
			}
			var payload, want map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("decode opencode.json: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatalf("decode expected config: %v", err)
			}
			want["$schema"] = "https://opencode.ai/config.json"

			for pass := 0; pass < 3; pass++ {
				if pass > 0 {
					enforceQuestionDenied(payload)
				}
				if !reflect.DeepEqual(payload, want) {
					t.Fatalf("config after pass %d = %#v, want %#v", pass, payload, want)
				}
			}
		})
	}
}

func TestWriteBackendConfigNativePermissionsSharedConfig(t *testing.T) {
	t.Parallel()

	const input = `{
		"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"},{"action":"bash","resource":"*","effect":"allow"}],
		"agents":{
			"build":{"description":"builder","options":{"temperature":0.2},"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"},{"action":"bash","resource":"*","effect":"allow"}]},
			"review":{"permissions":[{"action":"question","resource":"*","effect":"deny"}]},
			"implicit":{"description":"no permissions"}
		}
	}`
	root := t.TempDir()
	cfg := config.Default(root)
	if err := json.Unmarshal([]byte(input), &cfg.OpencodeConfig); err != nil {
		t.Fatalf("decode shared config: %v", err)
	}
	var original map[string]any
	if err := json.Unmarshal([]byte(input), &original); err != nil {
		t.Fatalf("decode original config: %v", err)
	}

	topPermissions := cfg.OpencodeConfig["permissions"].([]any)
	topBacking := append(topPermissions, "reserved")
	cfg.OpencodeConfig["permissions"] = topBacking[:len(topPermissions)]
	build := cfg.OpencodeConfig["agents"].(map[string]any)["build"].(map[string]any)
	agentPermissions := build["permissions"].([]any)
	agentBacking := append(agentPermissions, "reserved")
	build["permissions"] = agentBacking[:len(agentPermissions)]

	workspaces := make([]string, 64)
	for i := range workspaces {
		workspaces[i] = t.TempDir()
	}
	settings := DefaultSettings()
	start := make(chan struct{})
	results := make(chan error, len(workspaces))
	for _, workspacePath := range workspaces {
		go func() {
			<-start
			results <- writeBackendConfig(cfg, workspacePath, settings)
		}()
	}
	close(start)
	for range workspaces {
		if err := <-results; err != nil {
			t.Errorf("write backend config: %v", err)
		}
	}

	if !reflect.DeepEqual(cfg.OpencodeConfig, original) {
		t.Fatalf("shared config = %#v, want unchanged %#v", cfg.OpencodeConfig, original)
	}
	if topBacking[len(topBacking)-1] != "reserved" {
		t.Fatalf("top permissions backing array changed: %#v", topBacking)
	}
	if agentBacking[len(agentBacking)-1] != "reserved" {
		t.Fatalf("agent permissions backing array changed: %#v", agentBacking)
	}

	var want map[string]any
	if err := json.Unmarshal([]byte(`{
		"permission":{"question":"deny"},
		"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"},{"action":"bash","resource":"*","effect":"allow"},{"action":"question","resource":"*","effect":"deny"}],
		"agents":{
			"build":{"description":"builder","options":{"temperature":0.2},"permissions":[{"action":"question","resource":"*","effect":"allow"},{"action":"task","resource":"*","effect":"ask"},{"action":"bash","resource":"*","effect":"allow"},{"action":"question","resource":"*","effect":"deny"}]},
			"review":{"permissions":[{"action":"question","resource":"*","effect":"deny"}]},
			"implicit":{"description":"no permissions","permissions":[{"action":"question","resource":"*","effect":"deny"}]}
		}
	}`), &want); err != nil {
		t.Fatalf("decode expected config: %v", err)
	}
	want["$schema"] = "https://opencode.ai/config.json"
	for _, workspacePath := range workspaces {
		data, err := os.ReadFile(filepath.Join(workspacePath, "opencode.json"))
		if err != nil {
			t.Fatalf("read opencode.json: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("decode opencode.json: %v", err)
		}
		if !reflect.DeepEqual(payload, want) {
			t.Fatalf("config in %s = %#v, want %#v", workspacePath, payload, want)
		}
	}
}
