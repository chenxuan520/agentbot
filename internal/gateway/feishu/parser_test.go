package feishu

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestParseIncomingTextGroupMention(t *testing.T) {
	t.Parallel()

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
			},
			Message: &larkim.EventMessage{
				ChatId:      strPtr("oc_group"),
				MessageId:   strPtr("om_group"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 请检查报警"}`),
				Mentions: []*larkim.MentionEvent{{
					Id:   &larkim.UserId{OpenId: strPtr("ou_bot")},
					Name: strPtr("bot-name"),
				}},
			},
		},
	}

	incoming := parseIncoming(event)
	if incoming == nil {
		t.Fatal("expected parsed incoming message")
	}
	if !incoming.Mentioned {
		t.Fatal("expected group mention to be detected")
	}
	if incoming.SenderType != senderTypeUser {
		t.Fatalf("sender type = %q, want %q", incoming.SenderType, senderTypeUser)
	}
	if len(incoming.MentionOpenIDs) != 1 || incoming.MentionOpenIDs[0] != "ou_bot" {
		t.Fatalf("mention open ids = %+v", incoming.MentionOpenIDs)
	}
	if len(incoming.MentionNames) != 1 || incoming.MentionNames[0] != "bot-name" {
		t.Fatalf("mention names = %+v", incoming.MentionNames)
	}
	if incoming.Text != "请检查报警" {
		t.Fatalf("text = %q, want %q", incoming.Text, "请检查报警")
	}
	if incoming.ConversationType != "group" {
		t.Fatalf("conversation type = %q, want group", incoming.ConversationType)
	}
}

func TestParseIncomingTextGroupWithoutMention(t *testing.T) {
	t.Parallel()

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderType: strPtr("user"),
			},
			Message: &larkim.EventMessage{
				ChatId:      strPtr("oc_group"),
				MessageId:   strPtr("om_group_plain"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"请直接处理这个报警"}`),
			},
		},
	}

	incoming := parseIncoming(event)
	if incoming == nil {
		t.Fatal("expected parsed incoming message")
	}
	if incoming.Mentioned {
		t.Fatal("expected unmentioned group message")
	}
	if incoming.SenderType != senderTypeUser {
		t.Fatalf("sender type = %q, want %q", incoming.SenderType, senderTypeUser)
	}
	if incoming.Text != "请直接处理这个报警" {
		t.Fatalf("text = %q, want %q", incoming.Text, "请直接处理这个报警")
	}
}

func TestParseIncomingRobotMessage(t *testing.T) {
	t.Parallel()

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderType: strPtr("app")},
			Message: &larkim.EventMessage{
				ChatId:      strPtr("oc_group"),
				MessageId:   strPtr("om_robot"),
				ChatType:    strPtr("group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"机器人消息"}`),
			},
		},
	}

	incoming := parseIncoming(event)
	if incoming == nil {
		t.Fatal("expected parsed robot message")
	}
	if incoming.SenderType != "app" {
		t.Fatalf("sender type = %q, want app", incoming.SenderType)
	}
	if incoming.Text != "机器人消息" {
		t.Fatalf("text = %q, want 机器人消息", incoming.Text)
	}
}

func TestParseIncomingInteractive(t *testing.T) {
	t.Parallel()

	rawContent := `{"type":"template","data":{"foo":"bar"}}`
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderType: strPtr("user")},
			Message: &larkim.EventMessage{
				ChatId:      strPtr("oc_interactive"),
				MessageId:   strPtr("om_interactive"),
				RootId:      strPtr("om_root_reply"),
				ParentId:    strPtr("om_parent_reply"),
				ThreadId:    strPtr("omt_thread_reply"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("interactive"),
				Content:     strPtr(rawContent),
			},
		},
	}

	incoming := parseIncoming(event)
	if incoming == nil {
		t.Fatal("expected parsed interactive message")
	}
	if !incoming.IsInteractiveMessage() {
		t.Fatal("expected interactive message type")
	}
	if incoming.RootMessageID != "om_root_reply" {
		t.Fatalf("root message id = %q, want om_root_reply", incoming.RootMessageID)
	}
	if incoming.ParentMessageID != "om_parent_reply" {
		t.Fatalf("parent message id = %q, want om_parent_reply", incoming.ParentMessageID)
	}
	if incoming.ThreadID != "omt_thread_reply" {
		t.Fatalf("thread id = %q, want omt_thread_reply", incoming.ThreadID)
	}
	if incoming.Text != rawContent {
		t.Fatalf("text = %q, want %q", incoming.Text, rawContent)
	}
}

func TestParseIncomingPost(t *testing.T) {
	t.Parallel()

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderType: strPtr("user")},
			Message: &larkim.EventMessage{
				ChatId:      strPtr("oc_p2p"),
				MessageId:   strPtr("om_p2p"),
				ChatType:    strPtr("p2p"),
				MessageType: strPtr("post"),
				Content: strPtr(`{
				  "zh_cn": {
				    "title": " ",
				    "content": [
				      [{"tag":"text","text":"第一行"}],
				      [{"tag":"md","text":"第二行"}]
				    ]
				  }
				}`),
			},
		},
	}

	incoming := parseIncoming(event)
	if incoming == nil {
		t.Fatal("expected parsed post message")
	}
	if incoming.Text != "第一行\n第二行" {
		t.Fatalf("text = %q, want %q", incoming.Text, "第一行\n第二行")
	}
}

func TestParseIncomingTopicGroupPreservesChatType(t *testing.T) {
	t.Parallel()

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderType: strPtr("user")},
			Message: &larkim.EventMessage{
				ChatId:      strPtr("oc_topic"),
				MessageId:   strPtr("om_topic"),
				ChatType:    strPtr("topic_group"),
				MessageType: strPtr("text"),
				Content:     strPtr(`{"text":"@_user_1 topic ping"}`),
				Mentions:    []*larkim.MentionEvent{{}},
			},
		},
	}

	incoming := parseIncoming(event)
	if incoming == nil {
		t.Fatal("expected parsed topic message")
	}
	if !incoming.Mentioned {
		t.Fatal("expected topic mention to be detected")
	}
	if incoming.SenderType != senderTypeUser {
		t.Fatalf("sender type = %q, want %q", incoming.SenderType, senderTypeUser)
	}
	if incoming.ConversationType != "thread" {
		t.Fatalf("conversation type = %q, want thread", incoming.ConversationType)
	}
	if incoming.Text != "topic ping" {
		t.Fatalf("text = %q, want topic ping", incoming.Text)
	}
}

func strPtr(value string) *string {
	return &value
}
