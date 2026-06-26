package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const onReactionHookRelativePath = ".agents/hooks/on_reaction.py"

type onReactionHookRunner func(context.Context, string, TextInput) (beforeMessageHookResult, error)

func runOnReactionHook(ctx context.Context, workspacePath string, input TextInput) (beforeMessageHookResult, error) {
	hookPath := filepath.Join(workspacePath, onReactionHookRelativePath)
	info, err := os.Stat(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return beforeMessageHookResult{}, nil
		}
		return beforeMessageHookResult{}, err
	}
	if info.IsDir() {
		return beforeMessageHookResult{}, fmt.Errorf("on_reaction hook is a directory: %s", hookPath)
	}

	payload := buildBeforeMessageHookPayload(input)
	data, err := json.Marshal(payload)
	if err != nil {
		return beforeMessageHookResult{}, err
	}

	cmd := exec.CommandContext(ctx, "python3", hookPath)
	cmd.Dir = workspacePath
	cmd.Stdin = bytes.NewReader(data)
	cmd.Env = buildBeforeMessageHookEnv(workspacePath, input)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return beforeMessageHookResult{}, fmt.Errorf("run on_reaction.py: %s", detail)
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return beforeMessageHookResult{}, nil
	}

	var result beforeMessageHookResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return beforeMessageHookResult{}, fmt.Errorf("decode on_reaction.py output: %w", err)
	}
	return result, nil
}
