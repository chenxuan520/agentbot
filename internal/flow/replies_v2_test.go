package flow

import (
	"context"
	"testing"

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/conversation"
)

func nativeReplyMessage(sessionID, id, text string, sequence int64, sources ...string) backend.SessionMessage {
	message := replyMessage(sessionID, id, "", text, 100-sequence)
	message.Native = true
	message.Sequence = sequence
	message.SourceMessageIDs = sources
	return message
}

func TestNativeRepliesKeepProvenRoutesAndServerOrder(t *testing.T) {
	t.Parallel()
	service, provider, _, _, ref, path := newReplyTestService(t)
	for _, id := range []string{"A", "B"} {
		addReplySource(t, service, path, "ses-native", "msg-input-"+id, TextInput{
			Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
			MessageID: "om-" + id, SenderID: "user-" + id,
		})
	}
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-native": {
		nativeReplyMessage("ses-native", "msg-final-B", "B answer", 2, "msg-input-B"),
		nativeReplyMessage("ses-native", "msg-background-A", "A background", 3, "msg-input-A"),
		nativeReplyMessage("ses-native", "msg-final-A", "A answer", 1, "msg-input-A"),
		nativeReplyMessage("ses-native", "msg-external", "foreign answer", 4, "msg-foreign"),
	}}}
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-native"); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 3 {
		t.Fatalf("native replies = %+v", provider.replies)
	}
	for i, expected := range []struct{ messageID, text, sender string }{
		{"om-A", "A answer", "user-A"}, {"om-B", "B answer", "user-B"}, {"om-A", "A background", "user-A"},
	} {
		actual := provider.replies[i]
		if actual.messageID != expected.messageID || actual.text != expected.text || actual.options.MentionUserID != expected.sender || !actual.options.InThread {
			t.Fatalf("native reply %d = %+v", i, actual)
		}
	}
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-native"); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 3 {
		t.Fatal("native replay duplicated replies")
	}
}

func TestNativeRepliesDoNotGuessAmbiguousOrChangedSources(t *testing.T) {
	t.Parallel()
	service, provider, _, _, ref, path := newReplyTestService(t)
	for _, id := range []string{"A", "B"} {
		addReplySource(t, service, path, "ses-native", "msg-input-"+id, TextInput{
			Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-" + id,
		})
	}
	for _, sources := range [][]string{{"msg-input-A", "msg-input-B"}, {"msg-input-A", "msg-unknown"}} {
		lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-native": {
			nativeReplyMessage("ses-native", "msg-ambiguous", "must not be sent", 1, sources...),
		}}}
		if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-native"); err == nil {
			t.Fatal("ambiguous native reply did not report an error")
		}
	}
	candidate := nativeReplyMessage("ses-native", "msg-changed", "candidate", 1, "msg-input-A")
	lookup := &replyTestLookup{
		messages: map[string][]backend.SessionMessage{"ses-native": {candidate}},
		refresh:  map[string]backend.SessionMessage{candidate.ID: nativeReplyMessage("ses-native", candidate.ID, "changed", 1, "msg-input-B")},
	}
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-native"); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 0 {
		t.Fatalf("unproven native result sent: %+v", provider.replies)
	}
}

func TestNativeNoReplyBatchSharesFinalMessageRoute(t *testing.T) {
	t.Parallel()
	service, provider, client, _, ref, _ := newReplyTestService(t)
	var inputs []string
	client.promptFunc = func(_ context.Context, _ string, sessionID, text string, _ []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		inputID := "msg-input-" + text
		if err := options.OnPromptMessage(inputID); err != nil {
			return backend.PromptResult{}, err
		}
		inputs = append(inputs, inputID)
		if options.NoReply {
			return backend.PromptResult{SessionID: sessionID, MessageID: inputID, Native: true}, nil
		}
		return backend.PromptResult{
			SessionID: sessionID, MessageID: "msg-final", ReplyText: "batch answer", Native: true,
			SourceMessageIDs: append([]string(nil), inputs...),
		}, nil
	}
	prompts := []queuedPrompt{
		{input: TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: conversation.TypeThread, MessageID: "om-first", Text: "first"}},
		{input: TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: conversation.TypeThread, MessageID: "om-last", Text: "last"}},
	}
	if _, err := service.processPromptBatch(context.Background(), ref, prompts); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 1 || provider.replies[0].messageID != "om-last" {
		t.Fatalf("native batch lost final route: %+v", provider.replies)
	}
}

func TestNativeSynchronousReplyRejectsUnknownOrigin(t *testing.T) {
	t.Parallel()
	service, provider, _, _, ref, path := newReplyTestService(t)
	input := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-user"}
	addReplySource(t, service, path, "ses-native", "msg-input", input)
	if _, err := service.finalizeAgentReply(context.Background(), provider, input, "thread", path, PromptResult{
		SessionID: "ses-native", MessageID: "msg-unknown", ReplyText: "private foreign result", Native: true,
		SourceMessageIDs: []string{"msg-foreign"},
	}); err == nil {
		t.Fatal("unknown native origin fell back to current message")
	}
	if len(provider.replies) != 0 {
		t.Fatalf("unknown result leaked: %+v", provider.replies)
	}
}
