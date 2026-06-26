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
      "baseURL": "http://localhost:5000"
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
	if cfg.ProjectToken != "project-token" {
		t.Fatalf("project token = %q", cfg.ProjectToken)
	}
	if cfg.AuthSecret != "secret-token" {
		t.Fatalf("auth secret = %q", cfg.AuthSecret)
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
