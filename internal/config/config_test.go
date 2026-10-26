package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromAgentBotJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "agent-bot.json")
	content := `{
  "runtime": {
    "defaultProvider": "telegram",
    "defaultBackend": "opencode",
    "schedulerIntervalSeconds": 3,
    "schedulerBatchLimit": 9
  },
  "server": {
    "host": "0.0.0.0",
    "port": 18080
  },
  "auth": {
    "projectToken": "project-token",
    "secret": "secret-token"
  },
  "providers": {
    "feishu": {
      "appID": "app_xxx",
      "appSecret": "secret_xxx",
      "ackEmoji": "SMILE"
    }
  },
  "backends": {
    "opencode": {
      "baseURL": "http://localhost:5000",
      "username": "opencode-user",
      "password": "opencode-session-token"
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DefaultProvider != "telegram" {
		t.Fatalf("default provider = %q", cfg.DefaultProvider)
	}
	if cfg.ServerPort != 18080 {
		t.Fatalf("server port = %d", cfg.ServerPort)
	}
	if cfg.FeishuAppID != "app_xxx" {
		t.Fatalf("feishu app id = %q", cfg.FeishuAppID)
	}
	if cfg.BackendBaseURLs["opencode"] != "http://localhost:5000" {
		t.Fatalf("opencode base url = %q", cfg.BackendBaseURLs["opencode"])
	}
	if cfg.OpencodeUsername != "opencode-user" || cfg.OpencodePassword != "opencode-session-token" {
		t.Fatal("opencode backend authentication was not loaded")
	}
	if cfg.ProjectToken != "project-token" {
		t.Fatalf("project token = %q", cfg.ProjectToken)
	}
	if cfg.AuthSecret != "secret-token" {
		t.Fatalf("auth secret = %q", cfg.AuthSecret)
	}
}

func TestOpenCodeAuthenticationEnvironmentFallback(t *testing.T) {
	t.Setenv("OPENCODE_SERVER_USERNAME", "opencode-user")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "opencode-session-token")
	t.Setenv("OPENCODE_PASSWORD", "")
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpencodeUsername != "opencode-user" || cfg.OpencodePassword != "opencode-session-token" {
		t.Fatal("opencode authentication environment was not loaded")
	}
}

func TestOpenCodeNativePasswordEnvironmentTakesPrecedence(t *testing.T) {
	t.Setenv("OPENCODE_SERVER_PASSWORD", "legacy-password")
	t.Setenv("OPENCODE_PASSWORD", "native-password")
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpencodePassword != "native-password" {
		t.Fatal("native server password did not take precedence")
	}
}

func TestDefaultRepoRootDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cfg := Default(root)
	want := filepath.Join(cfg.RootDir, "agents", "repos")
	if cfg.RepoRootDir != want {
		t.Fatalf("repo root dir = %q, want %q", cfg.RepoRootDir, want)
	}
}
