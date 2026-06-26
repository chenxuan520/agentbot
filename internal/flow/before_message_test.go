package flow

import (
	"context"
	"os"
	"testing"
)

func TestBuildBeforeMessageHookPayloadKeepsLegacyAliases(t *testing.T) {
	t.Parallel()

	payload := buildBeforeMessageHookPayload(TextInput{
		Provider:         "feishu",
		ConversationID:   "chat-1",
		ConversationType: "thread",
		MessageType:      "interactive",
		MessageID:        "om-1",
		RootMessageID:    "om-root",
		ParentMessageID:  "om-parent",
		ThreadID:         "omt-1",
		SenderType:       "app",
		SenderID:         "ou-1",
		Text:             "hello",
	})

	if payload["conversationType"] != "thread" {
		t.Fatalf("conversationType = %v", payload["conversationType"])
	}
	if payload["chatType"] != "topic_group" {
		t.Fatalf("chatType = %v", payload["chatType"])
	}
	if payload["messageType"] != "interactive" {
		t.Fatalf("messageType = %v", payload["messageType"])
	}
	if payload["rootMessageId"] != "om-root" {
		t.Fatalf("rootMessageId = %v", payload["rootMessageId"])
	}
	if payload["parentMessageId"] != "om-parent" {
		t.Fatalf("parentMessageId = %v", payload["parentMessageId"])
	}
	if payload["threadId"] != "omt-1" {
		t.Fatalf("threadId = %v", payload["threadId"])
	}
	if payload["senderType"] != "app" {
		t.Fatalf("senderType = %v", payload["senderType"])
	}
	if payload["senderId"] != "ou-1" {
		t.Fatalf("senderId = %v", payload["senderId"])
	}
	if payload["senderOpenId"] != "ou-1" {
		t.Fatalf("senderOpenId = %v", payload["senderOpenId"])
	}
}

func TestBuildBeforeMessageHookPayloadIncludesReactionFields(t *testing.T) {
	t.Parallel()

	payload := buildBeforeMessageHookPayload(TextInput{
		Provider:         "feishu",
		ConversationID:   "chat-1",
		ConversationType: "group",
		MessageType:      "reaction",
		MessageID:        "om-1",
		SenderType:       "user",
		SenderID:         "ou-1",
		EventAction:      "created",
		ReactionEmoji:    "DONE",
	})

	if payload["eventAction"] != "created" {
		t.Fatalf("eventAction = %v", payload["eventAction"])
	}
	if payload["reactionEmoji"] != "DONE" {
		t.Fatalf("reactionEmoji = %v", payload["reactionEmoji"])
	}
}

func TestRunAfterReplyHookPassesThreadContextAndEnv(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	hookPath := workspaceDir + "/.agents/hooks"
	if err := os.MkdirAll(hookPath, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	script := "#!/usr/bin/env python3\nimport json, os, sys\npayload = json.load(sys.stdin)\nout = {\"replyText\": payload.get(\"replyText\", \"\"), \"mentionUserId\": \"|\".join([payload.get(\"rootMessageId\", \"\"), payload.get(\"parentMessageId\", \"\"), payload.get(\"threadId\", \"\"), os.environ.get(\"AGENT_BOT_ROOT_MESSAGE_ID\", \"\"), os.environ.get(\"AGENT_BOT_PARENT_MESSAGE_ID\", \"\"), os.environ.get(\"AGENT_BOT_THREAD_ID\", \"\")])}\nsys.stdout.write(json.dumps(out, ensure_ascii=False))\n"
	if err := os.WriteFile(hookPath+"/after_reply.py", []byte(script), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	result, err := runAfterReplyHook(context.Background(), workspaceDir, TextInput{
		Provider:         "feishu",
		ConversationID:   "chat-1",
		ConversationType: "thread",
		MessageType:      "text",
		MessageID:        "om-1",
		RootMessageID:    "om-root",
		ParentMessageID:  "om-parent",
		ThreadID:         "omt-1",
		SenderType:       "user",
		SenderID:         "ou-1",
		SystemText:       "system",
		Text:             "hello",
	}, "reply")
	if err != nil {
		t.Fatalf("run after reply hook: %v", err)
	}
	if result.ReplyText != "reply" {
		t.Fatalf("replyText = %q", result.ReplyText)
	}
	wantMention := "om-root|om-parent|omt-1|om-root|om-parent|omt-1"
	if result.MentionUserID != wantMention {
		t.Fatalf("mentionUserId = %q, want %q", result.MentionUserID, wantMention)
	}
}

func TestRunAfterReplyHookSupportsMultipleMentionUserIDs(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	hookPath := workspaceDir + "/.agents/hooks"
	if err := os.MkdirAll(hookPath, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	script := "#!/usr/bin/env python3\nimport json, sys\nsys.stdout.write(json.dumps({\"mentionUserIds\": [\"ou-1\", \"ou-2\"]}, ensure_ascii=False))\n"
	if err := os.WriteFile(hookPath+"/after_reply.py", []byte(script), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	result, err := runAfterReplyHook(context.Background(), workspaceDir, TextInput{
		Provider:         "feishu",
		ConversationID:   "chat-1",
		ConversationType: "group",
		MessageType:      "text",
		MessageID:        "om-1",
		SenderType:       "user",
		SenderID:         "ou-1",
		Text:             "hello",
	}, "reply")
	if err != nil {
		t.Fatalf("run after reply hook: %v", err)
	}
	if len(result.MentionUserIDs) != 2 {
		t.Fatalf("mentionUserIds len = %d, want 2", len(result.MentionUserIDs))
	}
	if result.MentionUserIDs[0] != "ou-1" || result.MentionUserIDs[1] != "ou-2" {
		t.Fatalf("mentionUserIds = %+v", result.MentionUserIDs)
	}
}
