package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/config"
)

// syncLegacyBotPoller tears down any legacy external poller process. The
// other-bot message polling now runs in-process inside the Feishu gateway (see
// internal/gateway/feishu botMessagePoller), so this only ensures the old
// standalone poller loop is not left running alongside it,
// which would double-deliver messages.
func syncLegacyBotPoller(cfg config.Config) error {
	scriptPath := filepath.Join(cfg.RootDir, "scripts", "restart-legacy-bot-poller.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, scriptPath, "stop")
	cmd.Dir = cfg.RootDir
	cmd.Env = append(os.Environ(), "AGENT_BOT_API_BASE_URL="+cfg.LocalAPIBaseURL())
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("stop legacy bot poller: %s", message)
	}
	return nil
}
