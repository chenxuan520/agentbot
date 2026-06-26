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

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/conversation"
)

const beforeMessageHookRelativePath = ".agents/hooks/before_message.py"

type beforeMessageHookRunner func(context.Context, string, TextInput) (beforeMessageHookResult, error)

type beforeMessageHookResult struct {
	Drop          bool   `json:"drop"`
	ReplyText     string `json:"replyText"`
	Text          string `json:"text"`
	SystemText    string `json:"systemText"`
	ReactionEmoji string `json:"reactionEmoji"`
}

func runBeforeMessageHook(ctx context.Context, workspacePath string, input TextInput) (beforeMessageHookResult, error) {
	hookPath := filepath.Join(workspacePath, beforeMessageHookRelativePath)
	info, err := os.Stat(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return beforeMessageHookResult{}, nil
		}
		return beforeMessageHookResult{}, err
	}
	if info.IsDir() {
		return beforeMessageHookResult{}, fmt.Errorf("before_message hook is a directory: %s", hookPath)
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
		return beforeMessageHookResult{}, fmt.Errorf("run before_message.py: %s", detail)
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return beforeMessageHookResult{}, nil
	}

	var result beforeMessageHookResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return beforeMessageHookResult{}, fmt.Errorf("decode before_message.py output: %w", err)
	}
	return result, nil
}

func buildBeforeMessageHookPayload(input TextInput) map[string]any {
	legacyType := legacyHookConversationType(input.ConversationType)
	return map[string]any{
		"provider":         input.Provider,
		"conversationId":   input.ConversationID,
		"conversationType": input.ConversationType,
		"chatType":         legacyType,
		"messageType":      input.MessageType,
		"messageId":        input.MessageID,
		"rootMessageId":    input.RootMessageID,
		"parentMessageId":  input.ParentMessageID,
		"threadId":         input.ThreadID,
		"senderType":       input.SenderType,
		"senderId":         input.SenderID,
		"senderOpenId":     input.SenderID,
		"eventAction":      input.EventAction,
		"reactionEmoji":    input.ReactionEmoji,
		"text":             input.Text,
		"attachments":      marshalHookAttachments(input.Attachments),
	}
}

func buildBeforeMessageHookEnv(workspacePath string, input TextInput) []string {
	legacyType := legacyHookConversationType(input.ConversationType)
	return append(os.Environ(),
		"AGENT_BOT_WORKSPACE="+workspacePath,
		"AGENT_BOT_PROVIDER="+input.Provider,
		"AGENT_BOT_CONVERSATION_ID="+input.ConversationID,
		"AGENT_BOT_CONVERSATION_TYPE="+input.ConversationType,
		"AGENT_BOT_CHAT_TYPE="+legacyType,
		"AGENT_BOT_MESSAGE_TYPE="+input.MessageType,
		"AGENT_BOT_MESSAGE_ID="+input.MessageID,
		"AGENT_BOT_ROOT_MESSAGE_ID="+input.RootMessageID,
		"AGENT_BOT_PARENT_MESSAGE_ID="+input.ParentMessageID,
		"AGENT_BOT_THREAD_ID="+input.ThreadID,
		"AGENT_BOT_SENDER_TYPE="+input.SenderType,
		"AGENT_BOT_SENDER_ID="+input.SenderID,
		"AGENT_BOT_SENDER_OPEN_ID="+input.SenderID,
		"AGENT_BOT_EVENT_ACTION="+input.EventAction,
		"AGENT_BOT_REACTION_EMOJI="+input.ReactionEmoji,
	)
}

func legacyHookConversationType(conversationType string) string {
	switch conversation.NormalizeType(conversationType) {
	case conversation.TypeGroup:
		return "group"
	case conversation.TypeThread:
		return "topic_group"
	default:
		return "p2p"
	}
}

func marshalHookAttachments(attachments []backend.Attachment) []map[string]string {
	if len(attachments) == 0 {
		return nil
	}
	result := make([]map[string]string, 0, len(attachments))
	for _, attachment := range attachments {
		result = append(result, map[string]string{
			"mime":     attachment.Mime,
			"filename": attachment.Filename,
			"url":      attachment.URL,
		})
	}
	return result
}
