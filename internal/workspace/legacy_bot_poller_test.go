package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenxuan520/agentbot/internal/config"
)

func TestSyncLegacyBotPollerStopsLegacyScript(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runDir := filepath.Join(root, "data", "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	scriptPath := filepath.Join(root, "scripts", "restart-legacy-bot-poller.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("mkdir script dir: %v", err)
	}
	script := "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s\n' \"${1:-start}\" > data/run/poller-action.txt\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write poller script: %v", err)
	}

	// Polling now runs in-process, so syncLegacyBotPoller only ever tears down
	// any legacy external poller (always "stop"), never starts one.
	cfg := config.Default(root)
	if err := syncLegacyBotPoller(cfg); err != nil {
		t.Fatalf("stop legacy bot poller: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "poller-action.txt"))
	if err != nil {
		t.Fatalf("read action marker: %v", err)
	}
	if strings.TrimSpace(string(data)) != "stop" {
		t.Fatalf("poller action = %q, want stop", string(data))
	}
}

func TestSyncLegacyBotPollerNoScriptIsNoop(t *testing.T) {
	t.Parallel()

	cfg := config.Default(t.TempDir())
	if err := syncLegacyBotPoller(cfg); err != nil {
		t.Fatalf("missing poller script should be a no-op, got: %v", err)
	}
}
