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

const afterReplyHookRelativePath = ".agents/hooks/after_reply.py"

type afterReplyHookRunner func(context.Context, string, TextInput, string) (afterReplyHookResult, error)

type afterReplyHookResult struct {
	ReplyText      string   `json:"replyText"`
	MentionUserID  string   `json:"mentionUserId"`
	MentionUserIDs []string `json:"mentionUserIds"`
}

func runAfterReplyHook(ctx context.Context, workspacePath string, input TextInput, replyText string) (afterReplyHookResult, error) {
	hookPath := filepath.Join(workspacePath, afterReplyHookRelativePath)
	info, err := os.Stat(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return afterReplyHookResult{}, nil
		}
		return afterReplyHookResult{}, err
	}
	if info.IsDir() {
		return afterReplyHookResult{}, fmt.Errorf("after_reply hook is a directory: %s", hookPath)
	}

	payload := map[string]any{
		"provider":         input.Provider,
		"conversationId":   input.ConversationID,
		"conversationType": input.ConversationType,
		"messageType":      input.MessageType,
		"messageId":        input.MessageID,
		"rootMessageId":    input.RootMessageID,
		"parentMessageId":  input.ParentMessageID,
		"threadId":         input.ThreadID,
		"senderType":       input.SenderType,
		"senderId":         input.SenderID,
		"senderOpenId":     input.SenderID,
		"text":             input.Text,
		"systemText":       input.SystemText,
		"replyText":        replyText,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return afterReplyHookResult{}, err
	}

	cmd := exec.CommandContext(ctx, "python3", hookPath)
	cmd.Dir = workspacePath
	cmd.Stdin = bytes.NewReader(data)
	baseURL := localAPIBaseURLFromWorkspace(workspacePath)
	cmd.Env = append(os.Environ(),
		"AGENT_BOT_WORKSPACE="+workspacePath,
		"AGENT_BOT_API_BASE_URL="+baseURL,
		"AGENT_BOT_PROVIDER="+input.Provider,
		"AGENT_BOT_CONVERSATION_ID="+input.ConversationID,
		"AGENT_BOT_CONVERSATION_TYPE="+input.ConversationType,
		"AGENT_BOT_MESSAGE_TYPE="+input.MessageType,
		"AGENT_BOT_MESSAGE_ID="+input.MessageID,
		"AGENT_BOT_ROOT_MESSAGE_ID="+input.RootMessageID,
		"AGENT_BOT_PARENT_MESSAGE_ID="+input.ParentMessageID,
		"AGENT_BOT_THREAD_ID="+input.ThreadID,
		"AGENT_BOT_SENDER_TYPE="+input.SenderType,
		"AGENT_BOT_SENDER_ID="+input.SenderID,
		"AGENT_BOT_SENDER_OPEN_ID="+input.SenderID,
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return afterReplyHookResult{}, fmt.Errorf("run after_reply.py: %s", detail)
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return afterReplyHookResult{}, nil
	}

	var result afterReplyHookResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return afterReplyHookResult{}, fmt.Errorf("decode after_reply.py output: %w", err)
	}
	return result, nil
}

func localAPIBaseURLFromWorkspace(workspacePath string) string {
	data, err := os.ReadFile(filepath.Join(workspacePath, ".agents", "runtime", "localapi.json"))
	if err != nil {
		return ""
	}
	var payload struct {
		BaseURL string `json:"baseURL"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.BaseURL)
}
