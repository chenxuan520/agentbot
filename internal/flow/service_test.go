package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	"github.com/chenxuan520/agentbot/internal/session"
	appstore "github.com/chenxuan520/agentbot/internal/store"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

func TestProcessTextRejectsRefusedConversationBeforeSession(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, controlService, store, ref := newTestFlowService(t)
	now := time.Now().UTC()
	if _, err := controlService.Refuse(ref, now.Add(30*time.Minute), "pause noisy traffic"); err != nil {
		t.Fatalf("create refuse rule: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-refused",
		Text:             "please ignore this",
		AddReaction:      true,
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText != "" {
		t.Fatalf("reply text = %q, want empty", result.ReplyText)
	}
	if len(fakeProvider.reactions) != 1 {
		t.Fatalf("reaction count = %d, want 1", len(fakeProvider.reactions))
	}
	if fakeProvider.reactions[0].messageID != "om-refused" || fakeProvider.reactions[0].kind != "blocked" {
		t.Fatalf("unexpected reaction: %+v", fakeProvider.reactions[0])
	}
	if len(fakeProvider.deletedReactionIDs) != 0 {
		t.Fatalf("unexpected reaction deletions: %+v", fakeProvider.deletedReactionIDs)
	}
	if len(fakeProvider.replies) != 0 || len(fakeProvider.sentChats) != 0 {
		t.Fatalf("unexpected outbound messages: replies=%d sends=%d", len(fakeProvider.replies), len(fakeProvider.sentChats))
	}

	record, err := store.Get(ref)
	if err != nil {
		t.Fatalf("get workspace record: %v", err)
	}
	if record != nil {
		t.Fatalf("expected no workspace/session side effect, got %+v", record)
	}
}

func TestUnblockCommandCancelsActiveRefuse(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, controlService, _, ref := newTestFlowService(t)
	now := time.Now().UTC()
	if _, err := controlService.Refuse(ref, now.Add(30*time.Minute), "pause noisy traffic"); err != nil {
		t.Fatalf("create refuse rule: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-resume",
		SenderID:         "ou-user",
		Text:             "/unblock",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if !strings.Contains(result.ReplyText, "已解除当前会话的 refuse 屏蔽") {
		t.Fatalf("unexpected reply text: %q", result.ReplyText)
	}
	if len(fakeProvider.reactions) != 0 {
		t.Fatalf("unexpected reactions: %+v", fakeProvider.reactions)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	if fakeProvider.replies[0].messageID != "om-resume" {
		t.Fatalf("reply message id = %q", fakeProvider.replies[0].messageID)
	}
	if fakeProvider.replies[0].options.MentionUserID != "ou-user" {
		t.Fatalf("reply mention = %q", fakeProvider.replies[0].options.MentionUserID)
	}

	active, err := controlService.HasActiveRefuse(ref, time.Now().UTC())
	if err != nil {
		t.Fatalf("check active refuse: %v", err)
	}
	if active {
		t.Fatal("expected refuse rule to be cancelled")
	}
}

func TestAbortCommandAbortsActiveSession(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	if _, err := service.sessions.Prepare(ref, time.Now().UTC()); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if err := service.sessions.Bind(ref, "opencode", "session-running", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-abort",
		SenderID:         "ou-user",
		Text:             "/abort",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText != "已向当前会话发送中断请求。" {
		t.Fatalf("unexpected reply text: %q", result.ReplyText)
	}
	if len(fakeBackend.abortedSessionIDs) != 1 || fakeBackend.abortedSessionIDs[0] != "session-running" {
		t.Fatalf("unexpected aborted sessions: %+v", fakeBackend.abortedSessionIDs)
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].messageID != "om-abort" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
	if fakeProvider.replies[0].options.MentionUserID != "ou-user" {
		t.Fatalf("reply mention = %q", fakeProvider.replies[0].options.MentionUserID)
	}
	if len(fakeProvider.reactions) != 0 {
		t.Fatalf("unexpected reactions: %+v", fakeProvider.reactions)
	}
}

func TestPeekCommandShowsLatestVisibleOutput(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	if _, err := service.sessions.Prepare(ref, time.Now().UTC()); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if err := service.sessions.Bind(ref, "opencode", "session-running", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}
	fakeBackend.sessionMessages = []backend.SessionMessage{
		{
			ID:        "msg-user",
			Role:      "user",
			CreatedAt: 1779785200000,
			Parts: []backend.SessionMessagePart{{
				Type: "text",
				Text: "show me current output",
			}},
		},
		{
			ID:        "msg-assistant",
			Role:      "assistant",
			CreatedAt: 1779785210000,
			Parts: []backend.SessionMessagePart{
				{Type: "step-start"},
				{Type: "reasoning", Text: "hidden scratchpad"},
				{Type: "text", Text: "partial answer"},
				{Type: "tool", Tool: "bash", ToolStatus: "completed"},
				{Type: "step-finish", Reason: "tool-calls"},
			},
		},
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-peek",
		SenderID:         "ou-user",
		Text:             "/peek",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if !strings.Contains(result.ReplyText, "assistant_state: tool_calls_pending") {
		t.Fatalf("peek reply missing tool state: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "assistant_part_types: step-start,reasoning,text,tool,step-finish") {
		t.Fatalf("peek reply missing part types: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "tool_calls: bash(completed)") {
		t.Fatalf("peek reply missing tool summary: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "partial answer") {
		t.Fatalf("peek reply missing visible text: %q", result.ReplyText)
	}
	if strings.Contains(result.ReplyText, "hidden scratchpad") {
		t.Fatalf("peek reply leaked reasoning text: %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].messageID != "om-peek" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
	if fakeProvider.replies[0].options.MentionUserID != "ou-user" {
		t.Fatalf("reply mention = %q", fakeProvider.replies[0].options.MentionUserID)
	}
	if len(fakeBackend.abortedSessionIDs) != 0 {
		t.Fatalf("unexpected aborted sessions: %+v", fakeBackend.abortedSessionIDs)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("unexpected prompt calls: %d", fakeBackend.promptCalls)
	}
}

func TestAttachCommandBindsSessionForNextPrompt(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session before attach: %v", err)
	}
	fakeBackend.sessionInfo = backend.SessionInfo{ID: "session-attached", Directory: current.Workspace.Path}
	fakeBackend.promptFunc = func(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		if sessionID != "session-attached" {
			t.Fatalf("prompt session id = %q, want session-attached", sessionID)
		}
		return backend.PromptResult{SessionID: sessionID, ReplyText: "ok"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-attach",
		SenderID:         "ou-user",
		Text:             "/attach session-attached",
	})
	if err != nil {
		t.Fatalf("process attach command: %v", err)
	}
	if !strings.Contains(result.ReplyText, "已将当前对话 attach 到 session `session-attached`") {
		t.Fatalf("unexpected attach reply: %q", result.ReplyText)
	}
	current, err = service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session after attach: %v", err)
	}
	if current.ActiveSessionID != "session-attached" {
		t.Fatalf("active session = %q, want session-attached", current.ActiveSessionID)
	}

	result, err = service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-attached-prompt",
		SenderID:         "ou-user",
		Text:             "continue",
	})
	if err != nil {
		t.Fatalf("process attached prompt: %v", err)
	}
	if result.ReplyText != "ok" {
		t.Fatalf("unexpected prompt reply: %q", result.ReplyText)
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", fakeBackend.promptCalls)
	}
	if len(fakeProvider.replies) != 2 {
		t.Fatalf("reply count = %d, want 2", len(fakeProvider.replies))
	}
}

func TestAttachCommandRejectsDifferentWorkspaceSession(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	fakeBackend.sessionInfo = backend.SessionInfo{ID: "session-other", Directory: "/tmp/other-workspace"}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-attach-other",
		SenderID:         "ou-user",
		Text:             "/attach session-other",
	})
	if err != nil {
		t.Fatalf("process attach command: %v", err)
	}
	if !strings.Contains(result.ReplyText, "只支持同一个 workspace path") {
		t.Fatalf("unexpected attach reply: %q", result.ReplyText)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("unexpected prompt calls: %d", fakeBackend.promptCalls)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session after rejected attach: %v", err)
	}
	if current.ActiveSessionID != "" {
		t.Fatalf("active session = %q, want empty", current.ActiveSessionID)
	}
}

func TestBTWCommandUsesSeparateSession(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	if _, err := service.sessions.Current(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	fakeBackend.promptFunc = func(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		if text == "main prompt" {
			return backend.PromptResult{SessionID: "session-main", ReplyText: "main ok"}, nil
		}
		if text == "btw prompt a" {
			if sessionID != "" && sessionID != "session-test" && sessionID != "session-btw" {
				t.Fatalf("btw prompt a session id = %q", sessionID)
			}
			return backend.PromptResult{SessionID: "session-btw-a", ReplyText: "btw ok a"}, nil
		}
		if text == "btw prompt b" {
			if sessionID != "" && sessionID != "session-test" && sessionID != "session-btw-b" {
				t.Fatalf("btw prompt b session id = %q", sessionID)
			}
			return backend.PromptResult{SessionID: "session-btw-b", ReplyText: "btw ok b"}, nil
		}
		return backend.PromptResult{SessionID: sessionID, ReplyText: "ok"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-main", SenderID: "ou-user", Text: "main prompt"})
	if err != nil {
		t.Fatalf("process main text: %v", err)
	}
	if result.ReplyText != "main ok" {
		t.Fatalf("main reply = %q", result.ReplyText)
	}

	result, err = service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-btw-a", SenderID: "ou-user-a", Text: "/btw btw prompt a"})
	if err != nil {
		t.Fatalf("process btw text: %v", err)
	}
	if result.ReplyText != "btw ok a" {
		t.Fatalf("btw reply = %q", result.ReplyText)
	}
	result, err = service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-btw-b", SenderID: "ou-user-b", Text: "/btw btw prompt b"})
	if err != nil {
		t.Fatalf("process second btw text: %v", err)
	}
	if result.ReplyText != "btw ok b" {
		t.Fatalf("second btw reply = %q", result.ReplyText)
	}
	current, err := service.sessions.CurrentForSender(ref, "ou-user-a")
	if err != nil {
		t.Fatalf("current session for user a: %v", err)
	}
	if current.ActiveSessionID != "session-main" {
		t.Fatalf("active session = %q, want session-main", current.ActiveSessionID)
	}
	if current.BTWSessionID != "session-btw-a" {
		t.Fatalf("btw session for user a = %q, want session-btw-a", current.BTWSessionID)
	}
	current, err = service.sessions.CurrentForSender(ref, "ou-user-b")
	if err != nil {
		t.Fatalf("current session for user b: %v", err)
	}
	if current.BTWSessionID != "session-btw-b" {
		t.Fatalf("btw session for user b = %q, want session-btw-b", current.BTWSessionID)
	}
	if len(fakeProvider.replies) != 3 {
		t.Fatalf("reply count = %d, want 3", len(fakeProvider.replies))
	}
}

func TestBTWClearOnlyClearsBTWSession(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)
	if _, err := service.sessions.Current(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := service.sessions.Bind(ref, "opencode", "session-main", time.Now().UTC()); err != nil {
		t.Fatalf("bind main: %v", err)
	}
	if err := service.sessions.BindBTW(ref, "ou-user-a", "opencode", "session-btw-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind btw: %v", err)
	}
	if err := service.sessions.BindBTW(ref, "ou-user-b", "opencode", "session-btw-b", time.Now().UTC()); err != nil {
		t.Fatalf("bind btw for user b: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-btw-clear", SenderID: "ou-user-a", Text: "/btw-clear"})
	if err != nil {
		t.Fatalf("process btw-clear: %v", err)
	}
	if !strings.Contains(result.ReplyText, "已清空你在当前对话的 btw session") {
		t.Fatalf("unexpected btw-clear reply: %q", result.ReplyText)
	}
	current, err := service.sessions.CurrentForSender(ref, "ou-user-a")
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.ActiveSessionID != "session-main" {
		t.Fatalf("active session = %q, want session-main", current.ActiveSessionID)
	}
	if current.BTWSessionID != "" {
		t.Fatalf("btw session for user a = %q, want empty", current.BTWSessionID)
	}
	current, err = service.sessions.CurrentForSender(ref, "ou-user-b")
	if err != nil {
		t.Fatalf("current session for user b: %v", err)
	}
	if current.BTWSessionID != "session-btw-b" {
		t.Fatalf("btw session for user b = %q, want session-btw-b", current.BTWSessionID)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
}

func TestBTWClearReportsClearedWhenLegacySharedBTWSessionExists(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, store, ref := newTestFlowService(t)
	if _, err := service.sessions.Current(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := service.sessions.BindBTW(ref, "", "opencode", "legacy-shared-btw", time.Now().UTC()); err != nil {
		t.Fatalf("bind legacy btw: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-btw-clear-legacy", SenderID: "ou-user-a", Text: "/btw-clear"})
	if err != nil {
		t.Fatalf("process btw-clear legacy: %v", err)
	}
	if !strings.Contains(result.ReplyText, "已清空你在当前对话的 btw session") {
		t.Fatalf("unexpected legacy btw-clear reply: %q", result.ReplyText)
	}
	legacyCurrent, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current legacy session: %v", err)
	}
	if legacyCurrent.BTWSessionID != "" {
		t.Fatalf("legacy btw session = %q, want empty", legacyCurrent.BTWSessionID)
	}
	if sessionID, err := store.GetBTWSession(ref, ""); err != nil {
		t.Fatalf("get shared btw session: %v", err)
	} else if sessionID != "" {
		t.Fatalf("shared btw session row = %q, want empty", sessionID)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
}

func TestBTWCommandWithoutArgsShowsUsage(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	result, err := service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-btw-usage", SenderID: "ou-user", Text: "/btw"})
	if err != nil {
		t.Fatalf("process btw usage: %v", err)
	}
	if !strings.Contains(result.ReplyText, "用法：`/btw <text>`") {
		t.Fatalf("unexpected btw usage reply: %q", result.ReplyText)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("unexpected prompt calls: %d", fakeBackend.promptCalls)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
}

func TestBTWCommandUsesAfterReplyHook(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{ReplyText: "btw rewritten", MentionUserID: "ou-duty"}, nil
	}
	fakeBackend.promptFunc = func(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		if text == "btw prompt" {
			return backend.PromptResult{SessionID: "session-btw", ReplyText: "btw raw"}, nil
		}
		return backend.PromptResult{SessionID: sessionID, ReplyText: "ok"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-btw-after-reply", SenderID: "ou-user", Text: "/btw btw prompt"})
	if err != nil {
		t.Fatalf("process btw text: %v", err)
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", fakeBackend.promptCalls)
	}
	if result.ReplyText != "btw rewritten" {
		t.Fatalf("reply text = %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	if fakeProvider.replies[0].text != "btw rewritten" {
		t.Fatalf("reply text payload = %q", fakeProvider.replies[0].text)
	}
	if fakeProvider.replies[0].options.MentionUserID != "ou-duty" {
		t.Fatalf("mention user = %q", fakeProvider.replies[0].options.MentionUserID)
	}
}

func TestBTWCommandClearsLegacySharedBTWSessionState(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, store, ref := newTestFlowService(t)
	if _, err := service.sessions.Current(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := service.sessions.BindBTW(ref, "", "opencode", "legacy-shared-btw", time.Now().UTC()); err != nil {
		t.Fatalf("bind legacy btw: %v", err)
	}
	fakeBackend.promptFunc = func(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		if text == "btw prompt" {
			if sessionID == "legacy-shared-btw" {
				t.Fatalf("sender-specific btw prompt reused legacy session id %q", sessionID)
			}
			return backend.PromptResult{SessionID: "session-btw-user", ReplyText: "btw ok"}, nil
		}
		return backend.PromptResult{SessionID: sessionID, ReplyText: "ok"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-btw-migrate", SenderID: "ou-user-a", Text: "/btw btw prompt"})
	if err != nil {
		t.Fatalf("process btw text: %v", err)
	}
	if result.ReplyText != "btw ok" {
		t.Fatalf("btw reply = %q", result.ReplyText)
	}
	legacyCurrent, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current legacy session: %v", err)
	}
	if legacyCurrent.BTWSessionID != "" {
		t.Fatalf("legacy btw session = %q, want empty", legacyCurrent.BTWSessionID)
	}
	if sessionID, err := store.GetBTWSession(ref, ""); err != nil {
		t.Fatalf("get shared btw session: %v", err)
	} else if sessionID != "" {
		t.Fatalf("shared btw session row = %q, want empty", sessionID)
	}
	current, err := service.sessions.CurrentForSender(ref, "ou-user-a")
	if err != nil {
		t.Fatalf("current session for user a: %v", err)
	}
	if current.BTWSessionID != "session-btw-user" {
		t.Fatalf("btw session for user a = %q, want session-btw-user", current.BTWSessionID)
	}
}

func TestBTWCommandAfterReplyHookFailureFallsBackToExplicitErrorReply(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{}, errors.New("boom")
	}
	fakeBackend.promptFunc = func(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		if text == "btw prompt" {
			return backend.PromptResult{SessionID: "session-btw", ReplyText: "btw raw"}, nil
		}
		return backend.PromptResult{SessionID: sessionID, ReplyText: "ok"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-btw-after-reply-fail", SenderID: "ou-user", Text: "/btw btw prompt"})
	if err != nil {
		t.Fatalf("process btw text: %v", err)
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", fakeBackend.promptCalls)
	}
	if !strings.Contains(result.ReplyText, "after_reply.py 执行失败: boom") {
		t.Fatalf("reply text = %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	if !strings.Contains(fakeProvider.replies[0].text, "after_reply.py 执行失败: boom") {
		t.Fatalf("reply text payload = %q", fakeProvider.replies[0].text)
	}
	if fakeProvider.replies[0].options.MentionUserID != "ou-user" {
		t.Fatalf("mention user = %q", fakeProvider.replies[0].options.MentionUserID)
	}
}

func TestInfoCommandShowsBTWSessionID(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)
	if _, err := service.sessions.Current(ref); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := service.sessions.Bind(ref, "opencode", "session-main", time.Now().UTC()); err != nil {
		t.Fatalf("bind main: %v", err)
	}
	if err := service.sessions.BindBTW(ref, "ou-user", "opencode", "session-btw", time.Now().UTC()); err != nil {
		t.Fatalf("bind btw: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-info-btw", SenderID: "ou-user", Text: "/info"})
	if err != nil {
		t.Fatalf("process info: %v", err)
	}
	if !strings.Contains(result.ReplyText, "btw_session_id: session-btw") {
		t.Fatalf("info reply missing btw session id: %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
}

func TestBeforeMessageHookCanRewriteText(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.beforeMsg = func(context.Context, string, TextInput) (beforeMessageHookResult, error) {
		return beforeMessageHookResult{Text: "rewritten prompt"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-hook-rewrite",
		SenderID:         "ou-user",
		Text:             "original prompt",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText != "ok" {
		t.Fatalf("unexpected reply text: %q", result.ReplyText)
	}
	if fakeBackend.lastPromptText != "rewritten prompt" {
		t.Fatalf("prompt text = %q", fakeBackend.lastPromptText)
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].messageID != "om-hook-rewrite" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
	if fakeProvider.replies[0].text != "ok" {
		t.Fatalf("unexpected reply payload: %+v", fakeProvider.replies[0])
	}
}

func TestPromptUsesConfiguredModel(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	settings := current.Workspace.Settings
	settings.Agent.Model = "openai/gpt-5.5-pro"
	if err := workspace.SaveSettings(current.Workspace.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-model",
		SenderID:         "ou-user",
		Text:             "hello",
	}); err != nil {
		t.Fatalf("process text: %v", err)
	}

	if len(fakeBackend.promptRequests) != 1 {
		t.Fatalf("prompt requests = %d, want 1", len(fakeBackend.promptRequests))
	}
	if got := fakeBackend.promptRequests[0].options.Model; got != "openai/gpt-5.5-pro" {
		t.Fatalf("prompt model = %q, want openai/gpt-5.5-pro", got)
	}
}

func TestBeforeMessageHookCanSetSystemText(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.beforeMsg = func(context.Context, string, TextInput) (beforeMessageHookResult, error) {
		return beforeMessageHookResult{Text: "rewritten prompt", SystemText: "this is a system message"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-hook-system",
		SenderID:         "ou-user",
		Text:             "original prompt",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText != "ok" {
		t.Fatalf("unexpected reply text: %q", result.ReplyText)
	}
	if len(fakeBackend.promptRequests) != 1 {
		t.Fatalf("prompt requests = %d, want 1", len(fakeBackend.promptRequests))
	}
	if fakeBackend.promptRequests[0].text != "rewritten prompt" {
		t.Fatalf("prompt text = %q, want rewritten prompt", fakeBackend.promptRequests[0].text)
	}
	if fakeBackend.promptRequests[0].options.System != "this is a system message" {
		t.Fatalf("prompt system = %q", fakeBackend.promptRequests[0].options.System)
	}
	if fakeBackend.promptRequests[0].options.NoReply {
		t.Fatal("single prompt should not use noReply")
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].messageID != "om-hook-system" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
}

func TestBeforeMessageHookCanDropWithReply(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.beforeMsg = func(context.Context, string, TextInput) (beforeMessageHookResult, error) {
		return beforeMessageHookResult{Drop: true, ReplyText: "blocked by hook"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-hook-drop",
		SenderID:         "ou-user",
		Text:             "original prompt",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText != "blocked by hook" {
		t.Fatalf("unexpected reply text: %q", result.ReplyText)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("unexpected prompt calls: %d", fakeBackend.promptCalls)
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].text != "blocked by hook" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
}

func TestBeforeMessageHookCanDropWithReaction(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.beforeMsg = func(context.Context, string, TextInput) (beforeMessageHookResult, error) {
		return beforeMessageHookResult{Drop: true, ReactionEmoji: "ENOUGH"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-hook-duplicate",
		SenderID:         "ou-user",
		Text:             "original prompt",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText != "" {
		t.Fatalf("unexpected reply text: %q", result.ReplyText)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("unexpected prompt calls: %d", fakeBackend.promptCalls)
	}
	if len(fakeProvider.reactions) != 1 {
		t.Fatalf("reaction count = %d, want 1", len(fakeProvider.reactions))
	}
	if fakeProvider.reactions[0].messageID != "om-hook-duplicate" || fakeProvider.reactions[0].kind != "ENOUGH" {
		t.Fatalf("unexpected reactions: %+v", fakeProvider.reactions)
	}
	if len(fakeProvider.replies) != 0 {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
}

func TestAfterReplyHookCanOverrideTextAndMention(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{ReplyText: "please ask duty", MentionUserID: "ou-duty"}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-after-reply",
		SenderID:         "ou-user",
		Text:             "original prompt",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", fakeBackend.promptCalls)
	}
	if result.ReplyText != "please ask duty" {
		t.Fatalf("reply text = %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	if fakeProvider.replies[0].text != "please ask duty" {
		t.Fatalf("reply text payload = %q", fakeProvider.replies[0].text)
	}
	if fakeProvider.replies[0].options.MentionUserID != "ou-duty" {
		t.Fatalf("mention user = %q", fakeProvider.replies[0].options.MentionUserID)
	}
}

func TestAfterReplyHookCanOverrideMultipleMentions(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{ReplyText: "please ask both", MentionUserIDs: []string{"ou-duty-1", "ou-duty-2"}}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-after-reply-multi",
		SenderID:         "ou-user",
		Text:             "original prompt",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", fakeBackend.promptCalls)
	}
	if result.ReplyText != "please ask both" {
		t.Fatalf("reply text = %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	if fakeProvider.replies[0].text != "please ask both" {
		t.Fatalf("reply text payload = %q", fakeProvider.replies[0].text)
	}
	if fakeProvider.replies[0].options.MentionUserID != "" {
		t.Fatalf("single mention user = %q, want empty", fakeProvider.replies[0].options.MentionUserID)
	}
	if len(fakeProvider.replies[0].options.MentionUserIDs) != 2 {
		t.Fatalf("mention users = %+v", fakeProvider.replies[0].options.MentionUserIDs)
	}
	if fakeProvider.replies[0].options.MentionUserIDs[0] != "ou-duty-1" || fakeProvider.replies[0].options.MentionUserIDs[1] != "ou-duty-2" {
		t.Fatalf("mention users = %+v", fakeProvider.replies[0].options.MentionUserIDs)
	}
}

func TestAppSenderDoesNotDefaultMentionOnAgentReply(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{}, nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageType:      "interactive",
		MessageID:        "om-alert-card",
		SenderType:       "app",
		SenderID:         "cli_peer_bot",
		Text:             "follow-up prompt",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if fakeBackend.promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", fakeBackend.promptCalls)
	}
	if result.ReplyText != "ok" {
		t.Fatalf("reply text = %q, want ok", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	if fakeProvider.replies[0].options.MentionUserID != "" {
		t.Fatalf("mention user = %q, want empty for app sender", fakeProvider.replies[0].options.MentionUserID)
	}
	if len(fakeProvider.replies[0].options.MentionUserIDs) != 0 {
		t.Fatalf("mention users = %+v, want none for app sender", fakeProvider.replies[0].options.MentionUserIDs)
	}
}

func TestBuildPeekTextIncludesTranscriptURL(t *testing.T) {
	t.Parallel()

	text := buildPeekText("session-main", nil, "https://agent-bot.example.com/?token=abt_sess_demo&tab=transcript")
	if !strings.Contains(text, "[**当前输出快照**](https://agent-bot.example.com/?token=abt_sess_demo&tab=transcript)") {
		t.Fatalf("peek text missing clickable title: %q", text)
	}
	if !strings.Contains(text, "transcript_url: https://agent-bot.example.com/?token=abt_sess_demo&tab=transcript") {
		t.Fatalf("peek text missing transcript url: %q", text)
	}
}

func TestBuildInfoTextIncludesClickableCurrentSessionTitle(t *testing.T) {
	t.Parallel()

	current := &session.CurrentSession{
		Workspace: &workspace.RuntimeWorkspace{
			Path: "/tmp/workspace",
			Settings: workspace.Settings{
				Template: "default",
				Agent:    workspace.AgentConfig{AgentsMode: "template", OpencodeHTTPTimeoutSeconds: 30},
				Mounts:   workspace.MountConfig{SkillIDs: nil, SubagentIDs: nil},
				Settings: workspace.RuntimeConfig{ReplyMode: "direct", HistoryTTLHours: 24},
			},
		},
		AgentBackend: "opencode",
	}
	text := buildInfoText(conversation.Ref{Provider: "feishu", ConversationID: "oc_demo"}, current, "abt_sess_demo", "", "", "https://agent-bot.example.com", false, "disabled", "", "")
	if !strings.Contains(text, "[**当前会话**](https://agent-bot.example.com/?token=abt_sess_demo)") {
		t.Fatalf("info text missing clickable title: %q", text)
	}
	if !strings.Contains(text, "console_url: https://agent-bot.example.com/?token=abt_sess_demo") {
		t.Fatalf("info text missing console url line: %q", text)
	}
}

func TestInfoCommandShowsContextTokens(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	if _, err := service.sessions.Prepare(ref, time.Now().UTC()); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if err := service.sessions.Bind(ref, "opencode", "session-main", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}
	fakeBackend.sessionMessages = []backend.SessionMessage{
		{ID: "m-user", Role: "user"},
		{ID: "m-assistant", Role: "assistant", Tokens: backend.TokenUsage{Total: 14983, Input: 14952, Output: 7}},
	}
	fakeBackend.currentModel = "ttadk_openai/gpt-5.4"

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-info-ctx",
		SenderID:         "ou-user",
		Text:             "/info",
	})
	if err != nil {
		t.Fatalf("process info: %v", err)
	}
	if !strings.Contains(result.ReplyText, "context_tokens: 14983 (input 14952)") {
		t.Fatalf("missing context tokens in reply: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "model: ttadk_openai/gpt-5.4") {
		t.Fatalf("missing model in reply: %q", result.ReplyText)
	}
}

func TestRolesCommandListsAvailableTemplates(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)
	opsDir := filepath.Join(service.cfg.TemplateRootDir, "ops")
	if err := os.MkdirAll(filepath.Join(opsDir, ".agents", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir ops template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opsDir, workspace.SettingsFileName), []byte("{\n  \"version\": 1,\n  \"template\": \"ops\",\n  \"agent\": {\n    \"backend\": \"opencode\"\n  },\n  \"settings\": {\n    \"replyMode\": \"direct\",\n    \"historyTTLHours\": 24\n  },\n  \"mounts\": {\n    \"skillIds\": []\n  }\n}\n"), 0o644); err != nil {
		t.Fatalf("write ops settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opsDir, "AGENTS.md"), []byte("# ops\n"), 0o644); err != nil {
		t.Fatalf("write ops agents: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-roles-list",
		SenderID:         "ou-user",
		Text:             "/roles",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if !strings.Contains(result.ReplyText, "template: default") {
		t.Fatalf("missing current template in reply: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "default") || !strings.Contains(result.ReplyText, "ops") {
		t.Fatalf("missing available templates in reply: %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].messageID != "om-roles-list" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
}

func TestRolesCommandSwitchesTemplatePreservingHooks(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)
	if err := os.MkdirAll(filepath.Join(service.cfg.SkillRootDir, "skill-ops"), 0o755); err != nil {
		t.Fatalf("mkdir skill-ops: %v", err)
	}
	if err := os.WriteFile(filepath.Join(service.cfg.SkillRootDir, "skill-ops", "SKILL.md"), []byte("# skill-ops\n"), 0o644); err != nil {
		t.Fatalf("write skill-ops: %v", err)
	}
	opsDir := filepath.Join(service.cfg.TemplateRootDir, "ops")
	if err := os.MkdirAll(filepath.Join(opsDir, ".agents", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir ops template: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(opsDir, ".agents", "commands"), 0o755); err != nil {
		t.Fatalf("mkdir ops commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opsDir, workspace.SettingsFileName), []byte("{\n  \"version\": 1,\n  \"template\": \"ops\",\n  \"agent\": {\n    \"backend\": \"opencode\"\n  },\n  \"settings\": {\n    \"replyMode\": \"direct\",\n    \"historyTTLHours\": 24\n  },\n  \"mounts\": {\n    \"skillIds\": [\"skill-ops\"]\n  }\n}\n"), 0o644); err != nil {
		t.Fatalf("write ops settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opsDir, "AGENTS.md"), []byte("# ops-agent\n"), 0o644); err != nil {
		t.Fatalf("write ops agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opsDir, ".agents", "commands", "role.txt"), []byte("ops preset\n"), 0o644); err != nil {
		t.Fatalf("write ops preset: %v", err)
	}

	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	hookFile := filepath.Join(current.Workspace.Path, ".agents", "hooks", "before_message.py")
	if err := os.WriteFile(hookFile, []byte("# keep hook\n"), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-roles-switch",
		SenderID:         "ou-user",
		Text:             "/roles ops",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if !strings.Contains(result.ReplyText, "已切换当前会话 role 到 `ops`") {
		t.Fatalf("unexpected reply text: %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].messageID != "om-roles-switch" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}

	updated, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session after switch: %v", err)
	}
	if updated.Workspace.Settings.Template != "ops" {
		t.Fatalf("template = %q, want ops", updated.Workspace.Settings.Template)
	}
	agentsData, err := os.ReadFile(filepath.Join(updated.Workspace.Path, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if string(agentsData) != "# ops-agent\n" {
		t.Fatalf("AGENTS.md = %q", string(agentsData))
	}
	hookData, err := os.ReadFile(hookFile)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if string(hookData) != "# keep hook\n" {
		t.Fatalf("hook content = %q", string(hookData))
	}
	if _, err := os.Lstat(filepath.Join(updated.Workspace.Path, ".agents", "skills", "skill-ops")); err != nil {
		t.Fatalf("stat switched skill: %v", err)
	}
	commandData, err := os.ReadFile(filepath.Join(updated.Workspace.Path, ".agents", "commands", "role.txt"))
	if err != nil {
		t.Fatalf("read role preset file: %v", err)
	}
	if string(commandData) != "ops preset\n" {
		t.Fatalf("role preset content = %q", string(commandData))
	}
}

func TestInfoCommandIncludesSessionConfig(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	for _, skillID := range []string{"notes", "cron"} {
		skillDir := filepath.Join(service.cfg.SkillRootDir, skillID)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir skill dir %s: %v", skillID, err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+skillID+"\n"), 0o644); err != nil {
			t.Fatalf("write skill file %s: %v", skillID, err)
		}
	}
	settings := current.Workspace.Settings
	settings.Settings.AcceptOtherBotMessages = true
	settings.Settings.AcceptInteractiveCardMessages = true
	settings.Mounts.SkillIDs = []string{"notes", "cron"}
	settings.Agent.OpencodeConfig = map[string]any{
		"model": "gpt-5",
		"thinking": map[string]any{
			"budgetTokens": 1200,
		},
	}
	if err := workspace.SaveSettings(current.Workspace.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-info",
		SenderID:         "ou-user",
		Text:             "/info",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if !strings.Contains(result.ReplyText, "accept_interactive_card_messages: true") {
		t.Fatalf("missing interactive card setting in reply: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "accept_other_bot_messages: true") {
		t.Fatalf("missing other bot messages setting in reply: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "agents_mode: template") {
		t.Fatalf("missing agents mode in reply: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "skill_ids: notes,cron") {
		t.Fatalf("missing skill ids in reply: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, `opencode_config: {"model":"gpt-5","thinking":{"budgetTokens":1200}}`) {
		t.Fatalf("missing opencode config in reply: %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].messageID != "om-info" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
}

func TestTopicMessageAlwaysRepliesInThread(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "thread",
		MessageID:        "om-topic",
		SenderID:         "ou-user",
		Text:             "/help",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText == "" {
		t.Fatal("expected help reply")
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	if !fakeProvider.replies[0].options.InThread {
		t.Fatal("expected topic reply to stay in thread")
	}
}

func TestGroupReplyModeDirectDoesNotForceThread(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-group",
		SenderID:         "ou-user",
		Text:             "/help",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText == "" {
		t.Fatal("expected help reply")
	}
	if len(fakeProvider.replies) != 1 {
		t.Fatalf("reply count = %d, want 1", len(fakeProvider.replies))
	}
	if fakeProvider.replies[0].options.InThread {
		t.Fatal("expected direct group reply to stay non-threaded")
	}
}

func enableTopicSessionMode(t *testing.T, service *Service, ref conversation.Ref) {
	t.Helper()
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	settings := current.Workspace.Settings
	settings.Settings.ReplyMode = workspace.ReplyModeTopicSession
	if err := workspace.SaveSettings(current.Workspace.Path, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}

func TestTopicSessionModeIsolatesSessionsPerTopic(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{}, nil
	}

	seq := 0
	fakeBackend.createSessionFunc = func() (string, error) {
		seq++
		return fmt.Sprintf("topic-sess-%d", seq), nil
	}
	var promptSessions []string
	fakeBackend.promptFunc = func(_ context.Context, _ string, sessionID, text string, _ []backend.Attachment, _ backend.PromptOptions) (backend.PromptResult, error) {
		promptSessions = append(promptSessions, sessionID)
		return backend.PromptResult{SessionID: sessionID, ReplyText: "ok:" + text}, nil
	}

	// A fresh top-level message opens topic A (keyed by its own message id).
	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group",
		MessageID: "om-A", SenderID: "ou-user", Text: "a1", AddReaction: true,
	}); err != nil {
		t.Fatalf("process topic A open: %v", err)
	}
	// A follow-up inside topic A carries the root message id and reuses A's session.
	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-A2", RootMessageID: "om-A", SenderID: "ou-user", Text: "a2", AddReaction: true,
	}); err != nil {
		t.Fatalf("process topic A follow-up: %v", err)
	}
	// Another top-level message opens topic B with its own isolated session.
	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group",
		MessageID: "om-B", SenderID: "ou-user", Text: "b1", AddReaction: true,
	}); err != nil {
		t.Fatalf("process topic B open: %v", err)
	}

	if fakeBackend.createCalls != 2 {
		t.Fatalf("create session calls = %d, want 2 (one per topic)", fakeBackend.createCalls)
	}
	wantPromptSessions := []string{"topic-sess-1", "topic-sess-1", "topic-sess-2"}
	if !reflect.DeepEqual(promptSessions, wantPromptSessions) {
		t.Fatalf("prompt sessions = %v, want %v", promptSessions, wantPromptSessions)
	}
	if got, err := service.sessions.TopicSessionID(ref, "om-A"); err != nil || got != "topic-sess-1" {
		t.Fatalf("topic A session = %q (err %v), want topic-sess-1", got, err)
	}
	if got, err := service.sessions.TopicSessionID(ref, "om-B"); err != nil || got != "topic-sess-2" {
		t.Fatalf("topic B session = %q (err %v), want topic-sess-2", got, err)
	}

	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.ActiveSessionID != "" {
		t.Fatalf("main session = %q, want empty in topic-session mode", current.ActiveSessionID)
	}

	if len(fakeProvider.replies) != 3 {
		t.Fatalf("reply count = %d, want 3", len(fakeProvider.replies))
	}
	for i, reply := range fakeProvider.replies {
		if !reply.options.InThread {
			t.Fatalf("reply %d not threaded; topic-session replies must stay in thread", i)
		}
	}
}

func TestTopicSessionBatchRepliesToEveryTopic(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{}, nil
	}
	seq := 0
	fakeBackend.createSessionFunc = func() (string, error) {
		seq++
		return fmt.Sprintf("sess-%d", seq), nil
	}
	fakeBackend.promptFunc = func(_ context.Context, _ string, sessionID, text string, _ []backend.Attachment, _ backend.PromptOptions) (backend.PromptResult, error) {
		return backend.PromptResult{SessionID: sessionID, ReplyText: "ok:" + text}, nil
	}

	// A single queued batch may span multiple topics; each must get its own reply
	// instead of being coalesced into one like the shared-session path does.
	prompts := []queuedPrompt{
		{input: TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-A", SenderID: "ou-user", Text: "a"}},
		{input: TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-B", SenderID: "ou-user", Text: "b"}},
	}
	result, err := service.processPromptBatch(context.Background(), ref, prompts)
	if err != nil {
		t.Fatalf("process prompt batch: %v", err)
	}
	if result.ReplyText != "ok:a" {
		t.Fatalf("batch result reply = %q, want ok:a (first prompt)", result.ReplyText)
	}
	if len(fakeProvider.replies) != 2 {
		t.Fatalf("reply count = %d, want 2 (one per topic)", len(fakeProvider.replies))
	}
	if fakeProvider.replies[0].messageID != "om-A" || fakeProvider.replies[1].messageID != "om-B" {
		t.Fatalf("reply targets = %q,%q, want om-A,om-B", fakeProvider.replies[0].messageID, fakeProvider.replies[1].messageID)
	}
	if fakeBackend.createCalls != 2 {
		t.Fatalf("create session calls = %d, want 2", fakeBackend.createCalls)
	}
	if got, _ := service.sessions.TopicSessionID(ref, "om-A"); got != "sess-1" {
		t.Fatalf("topic A session = %q, want sess-1", got)
	}
	if got, _ := service.sessions.TopicSessionID(ref, "om-B"); got != "sess-2" {
		t.Fatalf("topic B session = %q, want sess-2", got)
	}
	for i, req := range fakeBackend.promptRequests {
		if req.options.NoReply {
			t.Fatalf("prompt %d used NoReply; topic mode must reply to each topic", i)
		}
	}
}

func TestInfoCommandShowsTopicSessionInsideTopic(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	if err := service.sessions.BindTopic(ref, "om-root", "opencode", "sess-topic", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-info", RootMessageID: "om-root", SenderID: "ou-user", Text: "/info",
	})
	if err != nil {
		t.Fatalf("process info: %v", err)
	}
	if !strings.Contains(result.ReplyText, "reply_mode: topic-session") {
		t.Fatalf("info missing topic-session reply mode: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "topic_id: om-root") {
		t.Fatalf("info missing topic id: %q", result.ReplyText)
	}
	if !strings.Contains(result.ReplyText, "topic_session_id: sess-topic") {
		t.Fatalf("info missing topic session id: %q", result.ReplyText)
	}
}

func TestInfoCommandHidesTopicFieldsOutsideTopicMode(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, ref := newTestFlowService(t)

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group",
		MessageID: "om-info", SenderID: "ou-user", Text: "/info",
	})
	if err != nil {
		t.Fatalf("process info: %v", err)
	}
	if strings.Contains(result.ReplyText, "topic_id:") || strings.Contains(result.ReplyText, "topic_session_id:") {
		t.Fatalf("info should not show topic fields outside topic mode: %q", result.ReplyText)
	}
}

func TestPeekCommandTargetsTopicSessionInThread(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	if err := service.sessions.BindTopic(ref, "om-root-a", "opencode", "sess-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic a: %v", err)
	}
	if err := service.sessions.BindTopic(ref, "om-root-b", "opencode", "sess-b", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic b: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-peek", RootMessageID: "om-root-a", SenderID: "ou-user", Text: "/peek",
	})
	if err != nil {
		t.Fatalf("process peek: %v", err)
	}
	if !strings.Contains(result.ReplyText, "session_id: sess-a") {
		t.Fatalf("peek should target topic A session: %q", result.ReplyText)
	}
	if strings.Contains(result.ReplyText, "sess-b") {
		t.Fatalf("peek leaked another topic session: %q", result.ReplyText)
	}
	if len(fakeProvider.replies) != 1 || fakeProvider.replies[0].messageID != "om-peek" {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
}

func TestPeekCommandRefusesAtTopLevelInTopicMode(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group",
		MessageID: "om-peek", SenderID: "ou-user", Text: "/peek",
	})
	if err != nil {
		t.Fatalf("process peek: %v", err)
	}
	if !strings.Contains(result.ReplyText, "请在具体话题") {
		t.Fatalf("expected top-level refuse hint: %q", result.ReplyText)
	}
}

func TestClearCommandClearsOnlyCurrentTopicInThread(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	if err := service.sessions.BindTopic(ref, "om-root-a", "opencode", "sess-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic a: %v", err)
	}
	if err := service.sessions.BindTopic(ref, "om-root-b", "opencode", "sess-b", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic b: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-clear", RootMessageID: "om-root-a", SenderID: "ou-user", Text: "/clear",
	})
	if err != nil {
		t.Fatalf("process clear: %v", err)
	}
	if !strings.Contains(result.ReplyText, "已清空当前话题的 session") {
		t.Fatalf("unexpected clear reply: %q", result.ReplyText)
	}
	if got, _ := service.sessions.TopicSessionID(ref, "om-root-a"); got != "" {
		t.Fatalf("topic A session = %q, want cleared", got)
	}
	if got, _ := service.sessions.TopicSessionID(ref, "om-root-b"); got != "sess-b" {
		t.Fatalf("topic B session = %q, want sess-b intact", got)
	}
}

func TestClearCommandTopLevelResetsAllTopics(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	if err := service.sessions.BindTopic(ref, "om-root-a", "opencode", "sess-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic a: %v", err)
	}
	if err := service.sessions.BindTopic(ref, "om-root-b", "opencode", "sess-b", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic b: %v", err)
	}
	if err := service.sessions.Bind(ref, "opencode", "sess-main", time.Now().UTC()); err != nil {
		t.Fatalf("bind main: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group",
		MessageID: "om-clear", SenderID: "ou-user", Text: "/clear",
	})
	if err != nil {
		t.Fatalf("process clear: %v", err)
	}
	if !strings.Contains(result.ReplyText, "所有话题 session") {
		t.Fatalf("unexpected top-level clear reply: %q", result.ReplyText)
	}
	if got, _ := service.sessions.TopicSessionID(ref, "om-root-a"); got != "" {
		t.Fatalf("topic A session = %q, want cleared", got)
	}
	if got, _ := service.sessions.TopicSessionID(ref, "om-root-b"); got != "" {
		t.Fatalf("topic B session = %q, want cleared", got)
	}
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if current.ActiveSessionID != "" {
		t.Fatalf("main session = %q, want cleared", current.ActiveSessionID)
	}
}

func TestNewCommandResetsCurrentTopicInThread(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	if err := service.sessions.BindTopic(ref, "om-root-a", "opencode", "sess-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic a: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-new", RootMessageID: "om-root-a", SenderID: "ou-user", Text: "/new",
	})
	if err != nil {
		t.Fatalf("process new: %v", err)
	}
	if !strings.Contains(result.ReplyText, "该话题的下一条消息会创建新的 session") {
		t.Fatalf("unexpected new reply: %q", result.ReplyText)
	}
	if got, _ := service.sessions.TopicSessionID(ref, "om-root-a"); got != "" {
		t.Fatalf("topic A session = %q, want cleared", got)
	}
}

func TestCompressCommandTargetsTopicSessionInThread(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{}, nil
	}
	if err := service.sessions.BindTopic(ref, "om-root-a", "opencode", "sess-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic a: %v", err)
	}
	var gotSessionID string
	fakeBackend.compactFunc = func(_ context.Context, _ string, sessionID string) error {
		gotSessionID = sessionID
		return nil
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-compress", RootMessageID: "om-root-a", SenderID: "ou-user", Text: "/compress",
	})
	if err != nil {
		t.Fatalf("process compress: %v", err)
	}
	if gotSessionID != "sess-a" {
		t.Fatalf("compress compacted session = %q, want sess-a", gotSessionID)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("compress should not prompt the agent, got %d prompt calls", fakeBackend.promptCalls)
	}
	if !strings.Contains(result.ReplyText, "已压缩") {
		t.Fatalf("compress reply = %q, want a compaction confirmation", result.ReplyText)
	}
	// Compaction is in place: the topic session id must stay bound to sess-a.
	if got, _ := service.sessions.TopicSessionID(ref, "om-root-a"); got != "sess-a" {
		t.Fatalf("topic A session = %q, want unchanged sess-a", got)
	}
}

func TestCompressCommandRefusesAtTopLevelInTopicMode(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group",
		MessageID: "om-compress", SenderID: "ou-user", Text: "/compress",
	})
	if err != nil {
		t.Fatalf("process compress: %v", err)
	}
	if !strings.Contains(result.ReplyText, "请在具体话题") {
		t.Fatalf("expected top-level refuse hint: %q", result.ReplyText)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("compress at top level must not prompt, calls=%d", fakeBackend.promptCalls)
	}
}

func TestCompressCommandCompactsActiveSessionInDefaultMode(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	if _, err := service.sessions.Prepare(ref, time.Now().UTC()); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	if err := service.sessions.Bind(ref, "opencode", "session-main", time.Now().UTC()); err != nil {
		t.Fatalf("bind session: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-compress",
		SenderID:         "ou-user",
		Text:             "/compress",
	})
	if err != nil {
		t.Fatalf("process compress: %v", err)
	}
	if len(fakeBackend.compactedSessionIDs) != 1 || fakeBackend.compactedSessionIDs[0] != "session-main" {
		t.Fatalf("compacted sessions = %+v, want [session-main]", fakeBackend.compactedSessionIDs)
	}
	if fakeBackend.promptCalls != 0 {
		t.Fatalf("compress should not prompt the agent, got %d prompt calls", fakeBackend.promptCalls)
	}
	if !strings.Contains(result.ReplyText, "已压缩") {
		t.Fatalf("compress reply = %q, want a compaction confirmation", result.ReplyText)
	}
	// In-place compaction keeps the same session id, so the binding must stay.
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if current.ActiveSessionID != "session-main" {
		t.Fatalf("active session = %q, want unchanged session-main", current.ActiveSessionID)
	}
}

func TestAbortCommandTargetsTopicSessionInThread(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	if err := service.sessions.BindTopic(ref, "om-root-a", "opencode", "sess-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic a: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-abort", RootMessageID: "om-root-a", SenderID: "ou-user", Text: "/abort",
	})
	if err != nil {
		t.Fatalf("process abort: %v", err)
	}
	if result.ReplyText != "已向当前会话发送中断请求。" {
		t.Fatalf("unexpected abort reply: %q", result.ReplyText)
	}
	if len(fakeBackend.abortedSessionIDs) != 1 || fakeBackend.abortedSessionIDs[0] != "sess-a" {
		t.Fatalf("aborted sessions = %+v, want [sess-a]", fakeBackend.abortedSessionIDs)
	}
}

func TestAbortCommandRefusesAtTopLevelInTopicMode(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	if err := service.sessions.BindTopic(ref, "om-root-a", "opencode", "sess-a", time.Now().UTC()); err != nil {
		t.Fatalf("bind topic a: %v", err)
	}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group",
		MessageID: "om-abort", SenderID: "ou-user", Text: "/abort",
	})
	if err != nil {
		t.Fatalf("process abort: %v", err)
	}
	if !strings.Contains(result.ReplyText, "请在具体话题") {
		t.Fatalf("expected top-level refuse hint: %q", result.ReplyText)
	}
	if len(fakeBackend.abortedSessionIDs) != 0 {
		t.Fatalf("abort at top level must not abort, got %+v", fakeBackend.abortedSessionIDs)
	}
}

func TestAttachCommandBindsTopicSessionInThread(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	fakeBackend.sessionInfo = backend.SessionInfo{ID: "sess-attach", Directory: current.Workspace.Path}

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-attach", RootMessageID: "om-root-a", SenderID: "ou-user", Text: "/attach sess-attach",
	})
	if err != nil {
		t.Fatalf("process attach: %v", err)
	}
	if !strings.Contains(result.ReplyText, "已将当前对话 attach 到 session") {
		t.Fatalf("unexpected attach reply: %q", result.ReplyText)
	}
	if got, _ := service.sessions.TopicSessionID(ref, "om-root-a"); got != "sess-attach" {
		t.Fatalf("topic A session = %q, want sess-attach", got)
	}
	current, err = service.sessions.Current(ref)
	if err != nil {
		t.Fatalf("current after attach: %v", err)
	}
	if current.ActiveSessionID != "" {
		t.Fatalf("main session = %q, want empty (attach bound topic only)", current.ActiveSessionID)
	}
}

func TestAttachCommandRefusesAtTopLevelInTopicMode(t *testing.T) {
	t.Parallel()

	service, _, _, _, _, ref := newTestFlowService(t)
	enableTopicSessionMode(t, service, ref)

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group",
		MessageID: "om-attach", SenderID: "ou-user", Text: "/attach sess-attach",
	})
	if err != nil {
		t.Fatalf("process attach: %v", err)
	}
	if !strings.Contains(result.ReplyText, "请在具体话题") {
		t.Fatalf("expected top-level refuse hint: %q", result.ReplyText)
	}
	if has, _ := service.sessions.HasTopicSessions(ref); has {
		t.Fatalf("attach at top level must not bind any topic session")
	}
}

func TestLegacyP2PConversationTypeFallsBackToDirect(t *testing.T) {
	t.Parallel()

	service, fakeProvider, _, _, _, ref := newTestFlowService(t)

	result, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "p2p",
		MessageID:        "om-direct",
		Text:             "/help",
	})
	if err != nil {
		t.Fatalf("process text: %v", err)
	}
	if result.ReplyText == "" {
		t.Fatal("expected help reply")
	}
	if len(fakeProvider.sentChats) != 1 {
		t.Fatalf("send count = %d, want 1", len(fakeProvider.sentChats))
	}
	if len(fakeProvider.replies) != 0 {
		t.Fatalf("unexpected replies: %+v", fakeProvider.replies)
	}
}

func TestPromptConversationBindsNewSessionBeforePrompt(t *testing.T) {
	t.Parallel()

	service, _, fakeBackend, _, _, ref := newTestFlowService(t)
	fakeBackend.createdSessionID = "session-first"
	fakeBackend.promptFunc = func(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		current, err := service.sessions.Current(ref)
		if err != nil {
			t.Fatalf("current session: %v", err)
		}
		if current.ActiveSessionID != "session-first" {
			t.Fatalf("active session before prompt = %q", current.ActiveSessionID)
		}
		return backend.PromptResult{SessionID: sessionID, ReplyText: "ok"}, nil
	}

	result, err := service.PromptConversation(context.Background(), ref, "hello")
	if err != nil {
		t.Fatalf("prompt conversation: %v", err)
	}
	if result.SessionID != "session-first" {
		t.Fatalf("result session id = %q", result.SessionID)
	}
}

func TestProcessTextBatchesPendingMessagesWhileBusy(t *testing.T) {
	t.Parallel()

	service, fakeProvider, fakeBackend, _, _, ref := newTestFlowService(t)
	service.beforeMsg = func(_ context.Context, _ string, input TextInput) (beforeMessageHookResult, error) {
		return beforeMessageHookResult{Text: "processed:" + input.Text}, nil
	}

	firstPromptStarted := make(chan struct{})
	releaseFirstPrompt := make(chan struct{})
	promptTexts := make(chan string, 3)
	results := make(chan struct {
		result PromptResult
		err    error
	}, 1)

	fakeBackend.promptFunc = func(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		promptTexts <- text
		if fakeBackend.promptCalls == 1 {
			close(firstPromptStarted)
			<-releaseFirstPrompt
			return backend.PromptResult{SessionID: sessionID, ReplyText: "reply-first"}, nil
		}
		return backend.PromptResult{SessionID: sessionID, ReplyText: "reply-batch"}, nil
	}

	go func() {
		result, err := service.ProcessText(context.Background(), TextInput{
			Provider:         ref.Provider,
			ConversationID:   ref.ConversationID,
			ConversationType: "group",
			MessageID:        "om-1",
			SenderID:         "ou-1",
			Text:             "first",
			AddReaction:      true,
		})
		results <- struct {
			result PromptResult
			err    error
		}{result: result, err: err}
	}()

	<-firstPromptStarted

	result2, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-2",
		SenderID:         "ou-2",
		Text:             "second",
		AddReaction:      true,
	})
	if err != nil {
		t.Fatalf("process second text: %v", err)
	}
	if result2.ReplyText != "" {
		t.Fatalf("second reply text = %q, want empty while queued", result2.ReplyText)
	}

	result3, err := service.ProcessText(context.Background(), TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageID:        "om-3",
		SenderID:         "ou-3",
		Text:             "third",
		AddReaction:      true,
	})
	if err != nil {
		t.Fatalf("process third text: %v", err)
	}
	if result3.ReplyText != "" {
		t.Fatalf("third reply text = %q, want empty while queued", result3.ReplyText)
	}

	close(releaseFirstPrompt)

	first := <-results
	if first.err != nil {
		t.Fatalf("process first text: %v", first.err)
	}
	if first.result.ReplyText != "reply-first" {
		t.Fatalf("first reply text = %q, want reply-first", first.result.ReplyText)
	}

	firstPromptText := <-promptTexts
	secondPromptText := <-promptTexts
	thirdPromptText := <-promptTexts
	if firstPromptText != "processed:first" {
		t.Fatalf("first prompt text = %q, want processed:first", firstPromptText)
	}
	if secondPromptText != "processed:second" {
		t.Fatalf("second prompt text = %q, want processed:second", secondPromptText)
	}
	if thirdPromptText != "processed:third" {
		t.Fatalf("third prompt text = %q, want processed:third", thirdPromptText)
	}
	if len(fakeBackend.promptRequests) != 3 {
		t.Fatalf("prompt requests = %d, want 3", len(fakeBackend.promptRequests))
	}
	if fakeBackend.promptRequests[0].options.NoReply {
		t.Fatal("first prompt should not use noReply")
	}
	if !fakeBackend.promptRequests[1].options.NoReply {
		t.Fatal("second prompt should use noReply")
	}
	if fakeBackend.promptRequests[2].options.NoReply {
		t.Fatal("third prompt should trigger reply")
	}

	if fakeBackend.promptCalls != 3 {
		t.Fatalf("prompt calls = %d, want 3", fakeBackend.promptCalls)
	}
	if len(fakeProvider.replies) != 2 {
		t.Fatalf("reply count = %d, want 2", len(fakeProvider.replies))
	}
	if fakeProvider.replies[0].messageID != "om-1" {
		t.Fatalf("first reply message id = %q, want om-1", fakeProvider.replies[0].messageID)
	}
	if fakeProvider.replies[1].messageID != "om-3" {
		t.Fatalf("batched reply message id = %q, want om-3", fakeProvider.replies[1].messageID)
	}
	if fakeProvider.replies[1].options.MentionUserID != "ou-3" {
		t.Fatalf("batched mention user = %q, want ou-3", fakeProvider.replies[1].options.MentionUserID)
	}
	if len(fakeProvider.reactions) != 3 {
		t.Fatalf("reaction count = %d, want 3", len(fakeProvider.reactions))
	}
	if len(fakeProvider.deletedReactionIDs) != 3 {
		t.Fatalf("deleted reaction count = %d, want 3", len(fakeProvider.deletedReactionIDs))
	}
}

func newTestFlowService(t *testing.T) (*Service, *fakeProviderClient, *fakeBackendClient, *control.Service, *fakeFlowStore, conversation.Ref) {
	t.Helper()

	root := t.TempDir()
	templateDir := filepath.Join(root, "templates", "default")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatalf("create template dir: %v", err)
	}
	settings := "{\n" +
		"  \"version\": 1,\n" +
		"  \"template\": \"default\",\n" +
		"  \"agent\": {\n" +
		"    \"backend\": \"opencode\"\n" +
		"  },\n" +
		"  \"settings\": {\n" +
		"    \"replyMode\": \"direct\",\n" +
		"    \"historyTTLHours\": 24\n" +
		"  },\n" +
		"  \"mounts\": {\n" +
		"    \"skillIds\": []\n" +
		"  }\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(templateDir, workspace.SettingsFileName), []byte(settings), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "AGENTS.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write agents: %v", err)
	}

	cfg := config.Default(root)
	store := newFakeFlowStore()

	workspaceManager := workspace.NewManager(cfg, store)
	sessionService := session.NewService(store, workspaceManager)
	controlService := control.NewService(store)
	fakeProvider := &fakeProviderClient{}
	fakeBackend := &fakeBackendClient{}
	service := NewService(cfg, sessionService, controlService)
	service.providers = func(config.Config, string) (providerapi.Client, error) {
		return fakeProvider, nil
	}
	service.backends = func(config.Config, workspace.Settings) (backend.Client, error) {
		return fakeBackend, nil
	}
	service.beforeMsg = func(context.Context, string, TextInput) (beforeMessageHookResult, error) {
		return beforeMessageHookResult{}, nil
	}

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-test"}
	return service, fakeProvider, fakeBackend, controlService, store, ref
}

type fakeFlowStore struct {
	workspaces map[string]appstore.WorkspaceRecord
	btw        map[string]string
	topic      map[string]string
	rules      map[string]control.Rule
}

func newFakeFlowStore() *fakeFlowStore {
	return &fakeFlowStore{
		workspaces: map[string]appstore.WorkspaceRecord{},
		btw:        map[string]string{},
		topic:      map[string]string{},
		rules:      map[string]control.Rule{},
	}
}

func (s *fakeFlowStore) Get(ref conversation.Ref) (*appstore.WorkspaceRecord, error) {
	record, ok := s.workspaces[workspaceKey(ref)]
	if !ok {
		return nil, nil
	}
	copy := record
	return &copy, nil
}

func (s *fakeFlowStore) Upsert(record appstore.WorkspaceRecord) error {
	s.workspaces[workspaceKey(conversation.Ref{Provider: record.Provider, ConversationID: record.ConversationID})] = record
	return nil
}

func (s *fakeFlowStore) Delete(ref conversation.Ref) error {
	delete(s.workspaces, workspaceKey(ref))
	return nil
}

func (s *fakeFlowStore) List() ([]appstore.WorkspaceRecord, error) {
	items := make([]appstore.WorkspaceRecord, 0, len(s.workspaces))
	for _, record := range s.workspaces {
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].LastMessageAt.Equal(items[j].LastMessageAt) {
			return workspaceKey(conversation.Ref{Provider: items[i].Provider, ConversationID: items[i].ConversationID}) < workspaceKey(conversation.Ref{Provider: items[j].Provider, ConversationID: items[j].ConversationID})
		}
		return items[i].LastMessageAt.After(items[j].LastMessageAt)
	})
	return items, nil
}

func (s *fakeFlowStore) GetBTWSession(ref conversation.Ref, senderID string) (string, error) {
	return s.btw[workspaceKey(ref)+":"+senderID], nil
}

func (s *fakeFlowStore) HasBTWSessions(ref conversation.Ref) (bool, error) {
	prefix := workspaceKey(ref) + ":"
	for key := range s.btw {
		if strings.HasPrefix(key, prefix) {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeFlowStore) UpsertBTWSession(ref conversation.Ref, senderID, sessionID string, updatedAt time.Time) error {
	s.btw[workspaceKey(ref)+":"+senderID] = sessionID
	return nil
}

func (s *fakeFlowStore) DeleteBTWSession(ref conversation.Ref, senderID string) error {
	delete(s.btw, workspaceKey(ref)+":"+senderID)
	return nil
}

func (s *fakeFlowStore) DeleteBTWSessions(ref conversation.Ref) error {
	prefix := workspaceKey(ref) + ":"
	for key := range s.btw {
		if strings.HasPrefix(key, prefix) {
			delete(s.btw, key)
		}
	}
	return nil
}

func (s *fakeFlowStore) GetTopicSession(ref conversation.Ref, topicKey string) (string, error) {
	return s.topic[workspaceKey(ref)+":"+topicKey], nil
}

func (s *fakeFlowStore) HasTopicSessions(ref conversation.Ref) (bool, error) {
	prefix := workspaceKey(ref) + ":"
	for key := range s.topic {
		if strings.HasPrefix(key, prefix) {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeFlowStore) ListTopicSessions(ref conversation.Ref, limit int) ([]appstore.TopicSessionRecord, error) {
	prefix := workspaceKey(ref) + ":"
	items := make([]appstore.TopicSessionRecord, 0, len(s.topic))
	for key, sessionID := range s.topic {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		items = append(items, appstore.TopicSessionRecord{
			TopicKey:  strings.TrimPrefix(key, prefix),
			SessionID: sessionID,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].TopicKey < items[j].TopicKey })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *fakeFlowStore) UpsertTopicSession(ref conversation.Ref, topicKey, sessionID string, updatedAt time.Time) error {
	s.topic[workspaceKey(ref)+":"+topicKey] = sessionID
	return nil
}

func (s *fakeFlowStore) DeleteTopicSession(ref conversation.Ref, topicKey string) error {
	delete(s.topic, workspaceKey(ref)+":"+topicKey)
	return nil
}

func (s *fakeFlowStore) DeleteTopicSessions(ref conversation.Ref) error {
	prefix := workspaceKey(ref) + ":"
	for key := range s.topic {
		if strings.HasPrefix(key, prefix) {
			delete(s.topic, key)
		}
	}
	return nil
}

func (s *fakeFlowStore) CreateRule(rule control.Rule) error {
	s.rules[rule.ID] = rule
	return nil
}

func (s *fakeFlowStore) ListActiveRules(ref conversation.Ref, now time.Time) ([]control.Rule, error) {
	rules := make([]control.Rule, 0, len(s.rules))
	for _, rule := range s.rules {
		if rule.Provider != ref.Provider || rule.ConversationID != ref.ConversationID {
			continue
		}
		if rule.Status != control.StatusActive || !rule.UntilAt.After(now) {
			continue
		}
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].UntilAt.Before(rules[j].UntilAt)
	})
	return rules, nil
}

func (s *fakeFlowStore) UpdateRuleStatus(id, status string, updatedAt time.Time) error {
	rule, ok := s.rules[id]
	if !ok {
		return nil
	}
	rule.Status = status
	rule.UpdatedAt = updatedAt
	s.rules[id] = rule
	return nil
}

func workspaceKey(ref conversation.Ref) string {
	return ref.Provider + ":" + ref.ConversationID
}

type fakeProviderClient struct {
	reactions          []reactionCall
	deletedReactionIDs []string
	replies            []replyCall
	sentChats          []sendCall
}

type fakeBackendClient struct {
	createdSessionID    string
	createCalls         int
	createSessionFunc   func() (string, error)
	abortedSessionIDs   []string
	promptCalls         int
	lastPromptText      string
	promptRequests      []fakePromptRequest
	sessionInfo         backend.SessionInfo
	sessionMessages     []backend.SessionMessage
	getSessionErr       error
	getSessionMsgsErr   error
	compactedSessionIDs []string
	compactFunc         func(ctx context.Context, workspacePath, sessionID string) error
	promptFunc          func(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error)
	currentModel        string
	modelCatalog        backend.ModelCatalog
}

type fakePromptRequest struct {
	text        string
	attachments []backend.Attachment
	options     backend.PromptOptions
}

type reactionCall struct {
	messageID string
	kind      string
}

type replyCall struct {
	messageID string
	text      string
	options   providerapi.ReplyOptions
}

type sendCall struct {
	chatID string
	text   string
}

func (f *fakeProviderClient) Name() string {
	return "fake"
}

func (f *fakeProviderClient) Health(context.Context) error {
	return nil
}

func (f *fakeProviderClient) AddHandlingReaction(_ context.Context, messageID string) (string, error) {
	f.reactions = append(f.reactions, reactionCall{messageID: messageID, kind: "handling"})
	return "reaction-id", nil
}

func (f *fakeProviderClient) AddRemoteHandlingReaction(_ context.Context, messageID string) (string, error) {
	f.reactions = append(f.reactions, reactionCall{messageID: messageID, kind: "remote-handling"})
	return "reaction-id", nil
}

func (f *fakeProviderClient) AddBlockedReaction(_ context.Context, messageID string) error {
	f.reactions = append(f.reactions, reactionCall{messageID: messageID, kind: "blocked"})
	return nil
}

func (f *fakeProviderClient) AddReaction(_ context.Context, messageID, emojiType string) error {
	f.reactions = append(f.reactions, reactionCall{messageID: messageID, kind: emojiType})
	return nil
}

func (f *fakeProviderClient) DeleteReaction(_ context.Context, _ string, reactionID string) error {
	f.deletedReactionIDs = append(f.deletedReactionIDs, reactionID)
	return nil
}

func (f *fakeProviderClient) SendTextToChat(_ context.Context, chatID, text, _ string) error {
	f.sentChats = append(f.sentChats, sendCall{chatID: chatID, text: text})
	return nil
}

func (f *fakeProviderClient) ReplyTextToMessage(_ context.Context, messageID, text, _ string, options providerapi.ReplyOptions) error {
	f.replies = append(f.replies, replyCall{messageID: messageID, text: text, options: options})
	return nil
}

func (f *fakeBackendClient) Name() string {
	return "fake-backend"
}

func (f *fakeBackendClient) Health(context.Context) error {
	return nil
}

func (f *fakeBackendClient) CreateSession(context.Context, string) (string, error) {
	f.createCalls++
	if f.createSessionFunc != nil {
		return f.createSessionFunc()
	}
	if f.createdSessionID != "" {
		return f.createdSessionID, nil
	}
	return "session-test", nil
}

func (f *fakeBackendClient) AbortSession(_ context.Context, sessionID string) error {
	f.abortedSessionIDs = append(f.abortedSessionIDs, sessionID)
	return nil
}

func (f *fakeBackendClient) CompactSession(ctx context.Context, workspacePath, sessionID string) error {
	f.compactedSessionIDs = append(f.compactedSessionIDs, sessionID)
	if f.compactFunc != nil {
		return f.compactFunc(ctx, workspacePath, sessionID)
	}
	return nil
}

func (f *fakeBackendClient) ListModels(_ context.Context, _ string) (backend.ModelCatalog, error) {
	catalog := f.modelCatalog
	if catalog.Current == "" {
		catalog.Current = f.currentModel
	}
	return catalog, nil
}

func (f *fakeBackendClient) CurrentModel(_ context.Context, _ string) (string, error) {
	return f.currentModel, nil
}

func (f *fakeBackendClient) GetSession(_ context.Context, sessionID string) (backend.SessionInfo, error) {
	if f.getSessionErr != nil {
		return backend.SessionInfo{}, f.getSessionErr
	}
	if f.sessionInfo.ID == "" {
		return backend.SessionInfo{ID: sessionID}, nil
	}
	return f.sessionInfo, nil
}

func (f *fakeBackendClient) GetSessionMessages(_ context.Context, sessionID string) ([]backend.SessionMessage, error) {
	if f.getSessionMsgsErr != nil {
		return nil, f.getSessionMsgsErr
	}
	if len(f.sessionMessages) == 0 {
		return []backend.SessionMessage{{ID: sessionID, Role: "assistant"}}, nil
	}
	return append([]backend.SessionMessage(nil), f.sessionMessages...), nil
}

func (f *fakeBackendClient) Prompt(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
	f.promptCalls++
	f.lastPromptText = text
	f.promptRequests = append(f.promptRequests, fakePromptRequest{text: text, attachments: attachments, options: options})
	if f.promptFunc != nil {
		return f.promptFunc(ctx, workspacePath, sessionID, text, attachments, options)
	}
	return backend.PromptResult{SessionID: sessionID, ReplyText: "ok"}, nil
}
