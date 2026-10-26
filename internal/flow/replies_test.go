package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	appstore "github.com/chenxuan520/agentbot/internal/store"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

func newReplyTestService(t *testing.T) (*Service, *fakeProviderClient, *fakeBackendClient, *replyTestStore, conversation.Ref, string) {
	t.Helper()
	service, provider, client, _, _, ref := newTestFlowService(t)
	db := &replyTestStore{sources: map[string]appstore.ReplySourceRecord{}, delivered: map[string]bool{}}
	service.replies = db
	service.afterReply = func(context.Context, string, TextInput, string) (afterReplyHookResult, error) {
		return afterReplyHookResult{}, nil
	}
	current, err := service.sessions.Current(ref)
	if err != nil {
		t.Fatal(err)
	}
	return service, provider, client, db, ref, current.Workspace.Path
}

func addReplySource(t *testing.T, service *Service, path, sessionID, messageID string, input TextInput) {
	t.Helper()
	record := service.promptMessageRecorder(withReplyRoute(context.Background(), input, "thread"), path, sessionID, nil)
	if record == nil {
		t.Fatal("missing prompt message recorder")
	}
	if err := record(messageID); err != nil {
		t.Fatal(err)
	}
}

func replyMessage(sessionID, id, parent, text string, created int64) backend.SessionMessage {
	return backend.SessionMessage{
		SessionID: sessionID, ID: id, ParentID: parent, Role: "assistant",
		CreatedAt: created, CompletedAt: created + 1, Finish: "stop",
		Parts: []backend.SessionMessagePart{{Type: "text", Text: text}},
	}
}

func backgroundMessage(sessionID, id, taskID, state string, created int64) backend.SessionMessage {
	tag := "task_result"
	if state == "error" {
		tag = "task_error"
	}
	return backend.SessionMessage{
		SessionID: sessionID, ID: id, Role: "user", CreatedAt: created,
		Parts: []backend.SessionMessagePart{{
			Type: "text", Synthetic: true,
			Text: fmt.Sprintf("<task id=\"%s\" state=\"%s\"><%s>done</%s></task>", taskID, state, tag, tag),
		}},
	}
}

type replyTestLookup struct {
	messages map[string][]backend.SessionMessage
	refresh  map[string]backend.SessionMessage
	calls    []string
}

func (l *replyTestLookup) GetSessionReplyMessages(_ context.Context, _ string, sessionID string) ([]backend.SessionMessage, error) {
	l.calls = append(l.calls, sessionID)
	return append([]backend.SessionMessage(nil), l.messages[sessionID]...), nil
}

func (l *replyTestLookup) GetSessionReplyMessage(_ context.Context, _ string, sessionID, messageID string) (backend.SessionMessage, error) {
	if message, ok := l.refresh[messageID]; ok {
		return message, nil
	}
	for _, message := range l.messages[sessionID] {
		if message.ID == messageID {
			return message, nil
		}
	}
	return backend.SessionMessage{}, errors.New("missing test message")
}

func TestRepliesPreserveOriginalThreadAfterNewPrompt(t *testing.T) {
	t.Parallel()
	service, provider, _, db, ref, path := newReplyTestService(t)
	a := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-A", RootMessageID: "root-A", SenderID: "user-A", Text: "first"}
	b := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-B", RootMessageID: "root-B", SenderID: "user-B", Text: "second"}
	addReplySource(t, service, path, "ses-main", "msg-A", a)
	addReplySource(t, service, path, "ses-main", "msg-B", b)
	if _, err := service.finalizeAgentReply(context.Background(), provider, a, "thread", path, PromptResult{SessionID: "ses-main", MessageID: "msg-start", ReplyText: "started"}); err != nil {
		t.Fatal(err)
	}
	hookInputs := []TextInput{}
	service.afterReply = func(_ context.Context, _ string, input TextInput, text string) (afterReplyHookResult, error) {
		hookInputs = append(hookInputs, input)
		return afterReplyHookResult{ReplyText: "hook:" + text}, nil
	}
	task := replyMessage("ses-main", "msg-tool", "msg-A", "working", 2)
	task.Finish = "tool-calls"
	task.Parts = append(task.Parts, backend.SessionMessagePart{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolBackground: true})
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-main": {
		{SessionID: "ses-main", ID: "msg-A", Role: "user", CreatedAt: 1},
		task,
		replyMessage("ses-main", "msg-start", "msg-A", "started", 3),
		{SessionID: "ses-main", ID: "msg-B", Role: "user", CreatedAt: 4},
		replyMessage("ses-main", "msg-answer-B", "msg-B", "second answer", 5),
		backgroundMessage("ses-main", "msg-notice", "ses-child", "completed", 6),
		replyMessage("ses-main", "msg-background", "msg-notice", "first result", 7),
	}}}
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-main"); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 3 {
		t.Fatalf("replies = %+v", provider.replies)
	}
	last := provider.replies[2]
	if last.messageID != a.MessageID || last.text != "hook:first result" || !last.options.InThread || last.options.MentionUserID != a.SenderID {
		t.Fatalf("background reply = %+v", last)
	}
	if len(hookInputs) != 2 || hookInputs[1].RootMessageID != a.RootMessageID || hookInputs[1].Text != a.Text {
		t.Fatalf("hook inputs = %+v", hookInputs)
	}
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-main"); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 3 {
		t.Fatalf("duplicate replies = %+v", provider.replies)
	}
	delivered, err := db.ReplyDelivered("ses-main", "msg-background")
	if err != nil || !delivered {
		t.Fatalf("delivered = %v, err = %v", delivered, err)
	}
}

func TestRepliesDeliverEveryFinalMessageAndIgnoreUnregisteredSessions(t *testing.T) {
	t.Parallel()
	service, provider, _, _, ref, path := newReplyTestService(t)
	for _, id := range []string{"A", "B"} {
		addReplySource(t, service, path, "ses-"+id, "msg-user-"+id, TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-" + id})
	}
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{
		"ses-A": {
			replyMessage("ses-A", "msg-3", "msg-user-A", "third", 3),
			replyMessage("ses-A", "msg-old", "msg-unregistered", "old history", 0),
			replyMessage("ses-A", "msg-1", "msg-user-A", "first", 1),
			replyMessage("ses-A", "msg-2", "msg-user-A", "second", 2),
		},
		"ses-B":     {replyMessage("ses-B", "msg-B", "msg-user-B", "other topic", 1)},
		"ses-child": {replyMessage("ses-child", "msg-child", "msg-user-A", "hidden", 1)},
	}}
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-child"); err != nil {
		t.Fatal(err)
	}
	if len(lookup.calls) != 0 {
		t.Fatal("unregistered child session queried")
	}
	service.reconcileReplies(context.Background(), lookup, "")
	if len(provider.replies) != 4 {
		t.Fatalf("replies = %+v", provider.replies)
	}
	for i, text := range []string{"first", "second", "third"} {
		if provider.replies[i].text != text || provider.replies[i].messageID != "om-A" {
			t.Fatalf("reply %d = %+v", i, provider.replies[i])
		}
	}
	if provider.replies[3].messageID != "om-B" {
		t.Fatalf("other topic reply = %+v", provider.replies[3])
	}
}

func TestRepliesIgnoreNonFinalAndRecheckErrors(t *testing.T) {
	t.Parallel()
	service, provider, _, _, ref, path := newReplyTestService(t)
	addReplySource(t, service, path, "ses-main", "msg-user", TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-user"})
	messages := []backend.SessionMessage{}
	for i, change := range []func(*backend.SessionMessage){
		func(m *backend.SessionMessage) { m.CompletedAt = 0 },
		func(m *backend.SessionMessage) { m.Summary = true },
		func(m *backend.SessionMessage) { m.HasError = true },
		func(m *backend.SessionMessage) { m.Finish = "tool-calls" },
		func(m *backend.SessionMessage) { m.Parts[0].Synthetic = true },
		func(m *backend.SessionMessage) { m.Parts[0].Ignored = true },
		func(m *backend.SessionMessage) { m.Parts[0].Type = "reasoning" },
		func(m *backend.SessionMessage) {
			m.Parts = append(m.Parts, backend.SessionMessagePart{Type: "tool", Tool: "task"})
		},
	} {
		message := replyMessage("ses-main", fmt.Sprintf("msg-invalid-%d", i), "msg-user", "not final", int64(i+1))
		change(&message)
		messages = append(messages, message)
	}
	candidate := replyMessage("ses-main", "msg-race", "msg-user", "candidate", 20)
	messages = append(messages, candidate)
	candidate.HasError = true
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-main": messages}, refresh: map[string]backend.SessionMessage{candidate.ID: candidate}}
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-main"); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 0 {
		t.Fatalf("unexpected replies: %+v", provider.replies)
	}
}

func TestReplyRoutesHandleFailureAndCompactionWithoutInheritingForeignUser(t *testing.T) {
	t.Parallel()
	service, _, _, db, ref, path := newReplyTestService(t)
	input := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-start"}
	addReplySource(t, service, path, "ses-main", "msg-user", input)
	sources, err := db.ListReplySources("ses-main")
	if err != nil {
		t.Fatal(err)
	}
	task := replyMessage("ses-main", "msg-task", "msg-user", "", 2)
	task.Parts = []backend.SessionMessagePart{{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolBackground: true}}
	messages := []backend.SessionMessage{
		{SessionID: "ses-main", ID: "msg-user", Role: "user", CreatedAt: 1},
		task,
		backgroundMessage("ses-main", "msg-error", "ses-child", "error", 3),
		{SessionID: "ses-main", ID: "msg-compact", Role: "user", CreatedAt: 4, Parts: []backend.SessionMessagePart{{Type: "compaction"}}},
		{SessionID: "ses-main", ID: "msg-continue", Role: "user", CreatedAt: 5, Parts: []backend.SessionMessagePart{{Type: "text", Synthetic: true, CompactionContinue: true, Text: "continue"}}},
		{SessionID: "ses-main", ID: "msg-foreign", Role: "user", CreatedAt: 6, Parts: []backend.SessionMessagePart{{Type: "text", Text: "outside platform"}}},
		{SessionID: "ses-main", ID: "msg-foreign-compact", Role: "user", CreatedAt: 7, Parts: []backend.SessionMessagePart{{Type: "compaction"}}},
		backgroundMessage("ses-main", "msg-unknown-task", "ses-unknown", "completed", 8),
	}
	routes, err := service.replyRoutes("ses-main", sources, messages)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"msg-error", "msg-compact", "msg-continue"} {
		var route replyRoute
		if err := json.Unmarshal([]byte(routes[id].InputJSON), &route); err != nil || route.Input.MessageID != input.MessageID {
			t.Fatalf("route %s = %+v, err = %v", id, route, err)
		}
	}
	for _, id := range []string{"msg-foreign", "msg-foreign-compact", "msg-unknown-task"} {
		if _, ok := routes[id]; ok {
			t.Fatalf("foreign input %s inherited a route", id)
		}
	}
}

type replyFailProvider struct {
	*fakeProviderClient
	fail bool
}

func (p *replyFailProvider) ReplyTextToMessage(ctx context.Context, messageID, text, title string, options providerapi.ReplyOptions) error {
	if p.fail {
		return errors.New("send failed")
	}
	return p.fakeProviderClient.ReplyTextToMessage(ctx, messageID, text, title, options)
}

func TestReplyRoutesKeepOriginForRunningTaskUpdates(t *testing.T) {
	t.Parallel()
	service, _, _, db, ref, path := newReplyTestService(t)
	for _, id := range []string{"A", "B", "C"} {
		addReplySource(t, service, path, "ses-main", "msg-user-"+id, TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-" + id})
	}
	task := func(id, parent string, created int64) backend.SessionMessage {
		message := replyMessage("ses-main", id, parent, "", created)
		message.Parts = []backend.SessionMessagePart{{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolBackground: true}}
		return message
	}
	messages := []backend.SessionMessage{
		{ID: "msg-user-A", Role: "user", CreatedAt: 1},
		task("msg-task-A", "msg-user-A", 2),
		{ID: "msg-user-B", Role: "user", CreatedAt: 3},
		func() backend.SessionMessage {
			update := task("msg-task-update", "msg-user-B", 4)
			update.Parts[0].ToolUpdated = true
			return update
		}(),
		backgroundMessage("ses-main", "msg-notice-A", "ses-child", "completed", 5),
		{ID: "msg-user-C", Role: "user", CreatedAt: 6},
		task("msg-task-resume", "msg-user-C", 7),
		backgroundMessage("ses-main", "msg-notice-C", "ses-child", "completed", 8),
	}
	for i := 0; i < 2; i++ {
		sources, err := db.ListReplySources("ses-main")
		if err != nil {
			t.Fatal(err)
		}
		routes, err := service.replyRoutes("ses-main", sources, messages)
		if err != nil {
			t.Fatal(err)
		}
		for id, original := range map[string]string{"msg-notice-A": "om-A", "msg-notice-C": "om-C"} {
			var route replyRoute
			if err := json.Unmarshal([]byte(routes[id].InputJSON), &route); err != nil || route.Input.MessageID != original {
				t.Fatalf("route %s = %+v, expected %s, err = %v", id, route, original, err)
			}
		}
	}
}

func TestReplyRoutesUseNewOriginAfterCancelledTask(t *testing.T) {
	t.Parallel()
	service, _, _, db, ref, path := newReplyTestService(t)
	for _, id := range []string{"A", "B"} {
		addReplySource(t, service, path, "ses-main", "msg-user-"+id, TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-" + id})
	}
	old := replyMessage("ses-main", "msg-old-task", "msg-user-A", "", 2)
	old.Parts = []backend.SessionMessagePart{{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolBackground: true}}
	restart := replyMessage("ses-main", "msg-new-task", "msg-user-B", "", 4)
	restart.Parts = []backend.SessionMessagePart{{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolBackground: true}}
	sources, err := db.ListReplySources("ses-main")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := service.replyRoutes("ses-main", sources, []backend.SessionMessage{
		{ID: "msg-user-A", Role: "user", CreatedAt: 1}, old,
		{ID: "msg-user-B", Role: "user", CreatedAt: 3}, restart,
		backgroundMessage("ses-main", "msg-notice", "ses-child", "completed", 5),
	})
	if err != nil {
		t.Fatal(err)
	}
	var route replyRoute
	if err := json.Unmarshal([]byte(routes["msg-notice"].InputJSON), &route); err != nil || route.Input.MessageID != "om-B" {
		t.Fatalf("resumed task route = %+v, err = %v", route, err)
	}
}

func TestUnregisteredTaskChangesDoNotInheritOldThread(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"cancel", "restart", "cancel-and-restart", "update"} {
		t.Run(change, func(t *testing.T) {
			service, provider, _, db, ref, path := newReplyTestService(t)
			addReplySource(t, service, path, "ses-main", "msg-user-A", TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-A"})
			original := replyMessage("ses-main", "msg-task-A", "msg-user-A", "", 2)
			original.Finish = "tool-calls"
			original.Parts = []backend.SessionMessagePart{{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolBackground: true}}
			messages := []backend.SessionMessage{
				{ID: "msg-user-A", Role: "user", CreatedAt: 1}, original,
				{ID: "msg-user-X", Role: "user", CreatedAt: 3},
			}
			if change == "cancel" || change == "cancel-and-restart" {
				cancel := replyMessage("ses-main", "msg-cancel-X", "msg-user-X", "", 4)
				cancel.Parts = []backend.SessionMessagePart{{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolCancelled: true}}
				messages = append(messages, cancel)
			}
			if change != "cancel" {
				restart := replyMessage("ses-main", "msg-task-X", "msg-user-X", "", 5)
				restart.Parts = []backend.SessionMessagePart{{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolBackground: true, ToolUpdated: change == "update"}}
				messages = append(messages, restart)
			}
			messages = append(messages, backgroundMessage("ses-main", "msg-notice-X", "ses-child", "completed", 6), replyMessage("ses-main", "msg-answer-X", "msg-notice-X", "result", 7))
			lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-main": messages}}
			if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-main"); err != nil {
				t.Fatal(err)
			}
			want := 0
			if change == "update" {
				want = 1
			}
			if len(provider.replies) != want {
				t.Fatalf("%s from unregistered input routed to old thread: %+v", change, provider.replies)
			}
			for _, reply := range provider.replies {
				if reply.messageID != "om-A" {
					t.Fatalf("running update lost original route: %+v", reply)
				}
			}
			if want == 0 {
				sources, err := db.ListReplySources("ses-main")
				if err != nil || len(sources) != 1 {
					t.Fatalf("foreign notification persisted a route: %+v, err=%v", sources, err)
				}
			}
		})
	}
}

func TestRepliesRetrySendFailureWithoutMarkingDelivered(t *testing.T) {
	t.Parallel()
	service, provider, _, db, ref, path := newReplyTestService(t)
	addReplySource(t, service, path, "ses-main", "msg-user", TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-user"})
	failing := &replyFailProvider{fakeProviderClient: provider, fail: true}
	service.providers = func(config.Config, string) (providerapi.Client, error) { return failing, nil }
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-main": {replyMessage("ses-main", "msg-answer", "msg-user", "answer", 1)}}}
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-main"); err == nil {
		t.Fatal("expected send failure")
	}
	delivered, err := db.ReplyDelivered("ses-main", "msg-answer")
	if err != nil || delivered {
		t.Fatalf("failed send delivered = %v, err = %v", delivered, err)
	}
	failing.fail = false
	if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-main"); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 1 {
		t.Fatalf("replies = %+v", provider.replies)
	}
}

type replyTestEvents struct {
	watch func(context.Context, func(backend.SessionEvent) error) error
}

func (e replyTestEvents) WatchSessionEvents(ctx context.Context, handle func(backend.SessionEvent) error) error {
	return e.watch(ctx, handle)
}

type replyThreadFailProvider struct {
	*fakeProviderClient
}

func (p *replyThreadFailProvider) ReplyTextToMessage(ctx context.Context, messageID, text, title string, options providerapi.ReplyOptions) error {
	if messageID == "om-A" {
		return errors.New("original message deleted")
	}
	return p.fakeProviderClient.ReplyTextToMessage(ctx, messageID, text, title, options)
}

func TestFailedReplyDoesNotBlockOtherThreadInSameSession(t *testing.T) {
	t.Parallel()
	service, provider, _, db, ref, path := newReplyTestService(t)
	for _, id := range []string{"A", "B"} {
		addReplySource(t, service, path, "ses-main", "msg-user-"+id, TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-" + id})
	}
	service.providers = func(config.Config, string) (providerapi.Client, error) {
		return &replyThreadFailProvider{fakeProviderClient: provider}, nil
	}
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-main": {
		replyMessage("ses-main", "msg-answer-A", "msg-user-A", "answer A", 1),
		replyMessage("ses-main", "msg-answer-B", "msg-user-B", "answer B", 2),
	}}}
	for i := 0; i < 2; i++ {
		if err := service.reconcileSessionReplies(context.Background(), lookup, "ses-main"); err == nil {
			t.Fatal("missing failed delivery error")
		}
		if len(provider.replies) != 1 || provider.replies[0].messageID != "om-B" {
			t.Fatalf("failed A blocked or duplicated B: %+v", provider.replies)
		}
	}
	if delivered, err := db.ReplyDelivered("ses-main", "msg-answer-A"); err != nil || delivered {
		t.Fatalf("failed reply marked delivered: %v, %v", delivered, err)
	}
	if delivered, err := db.ReplyDelivered("ses-main", "msg-answer-B"); err != nil || !delivered {
		t.Fatalf("healthy reply not delivered: %v, %v", delivered, err)
	}
}

func TestReplyListenerRetriesFailedSendWithoutAnotherMessage(t *testing.T) {
	t.Parallel()
	service, provider, client, db, ref, path := newReplyTestService(t)
	addReplySource(t, service, path, "ses-main", "msg-user", TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-user"})
	failing := &replyFailProvider{fakeProviderClient: provider, fail: true}
	service.providers = func(config.Config, string) (providerapi.Client, error) { return failing, nil }
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-main": {replyMessage("ses-main", "msg-answer", "msg-user", "answer", 1)}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	events := replyTestEvents{watch: func(ctx context.Context, handle func(backend.SessionEvent) error) error {
		calls++
		if calls > 1 {
			failing.fail = false
		}
		if err := handle(backend.SessionEvent{Type: "server.connected"}); err != nil {
			return err
		}
		delivered, err := db.ReplyDelivered("ses-main", "msg-answer")
		if err != nil {
			return err
		}
		if delivered {
			cancel()
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := service.listenReplies(ctx, events, lookup, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(provider.replies) != 1 || provider.replies[0].messageID != "om-user" {
		t.Fatalf("failed send was not retried automatically: watches=%d, replies=%+v", calls, provider.replies)
	}
	if client.promptCalls != 0 {
		t.Fatal("delivery retry called the model")
	}
}

func TestRepliesReconnectAndRestartDoNotRepeatDeliveredMessages(t *testing.T) {
	t.Parallel()
	service, provider, _, db, ref, path := newReplyTestService(t)
	addReplySource(t, service, path, "ses-main", "msg-user", TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "direct"})
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-main": {replyMessage("ses-main", "msg-answer", "msg-user", "answer", 1)}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	events := replyTestEvents{watch: func(_ context.Context, handle func(backend.SessionEvent) error) error {
		calls++
		if calls == 1 {
			return io.EOF
		}
		if err := handle(backend.SessionEvent{Type: "server.connected"}); err != nil {
			return err
		}
		if err := handle(backend.SessionEvent{Type: "session.idle", SessionID: "ses-main"}); err != nil {
			return err
		}
		cancel()
		return context.Canceled
	}}
	if err := service.listenReplies(ctx, events, lookup, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(provider.sentChats) != 1 {
		t.Fatalf("watch calls = %d, sends = %+v", calls, provider.sentChats)
	}
	restarted := NewService(service.cfg, service.sessions, service.control)
	restarted.replies = db
	restarted.providers = service.providers
	restarted.afterReply = service.afterReply
	if err := restarted.reconcileSessionReplies(context.Background(), lookup, "ses-main"); err != nil {
		t.Fatal(err)
	}
	if len(provider.sentChats) != 1 {
		t.Fatalf("restart duplicated reply: %+v", provider.sentChats)
	}
}

func TestReplyRecorderRegistersPromptBeforeBackendExecution(t *testing.T) {
	t.Parallel()
	service, provider, client, db, ref, _ := newReplyTestService(t)
	client.promptFunc = func(_ context.Context, _ string, sessionID, _ string, _ []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		if options.OnPromptMessage == nil {
			t.Fatal("missing recorder")
		}
		if err := options.OnPromptMessage("msg-user"); err != nil {
			return backend.PromptResult{}, err
		}
		sources, err := db.ListReplySources(sessionID)
		if err != nil || len(sources) != 1 {
			t.Fatalf("sources = %+v, err = %v", sources, err)
		}
		return backend.PromptResult{SessionID: sessionID, MessageID: "msg-answer", ReplyText: "answer"}, nil
	}
	input := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-user", SenderID: "user", Text: "hello"}
	result, err := service.ProcessText(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{result.SessionID: {replyMessage(result.SessionID, "msg-answer", "msg-user", "answer", 1)}}}
	if err := service.reconcileSessionReplies(context.Background(), lookup, result.SessionID); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 1 {
		t.Fatalf("synchronous answer duplicated: %+v", provider.replies)
	}
}

func TestSynchronousReplyUsesActualParentRoute(t *testing.T) {
	t.Parallel()
	service, provider, _, _, ref, path := newReplyTestService(t)
	a := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-A", SenderID: "user-A"}
	b := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-B", SenderID: "user-B"}
	addReplySource(t, service, path, "ses-main", "msg-user-A", a)
	addReplySource(t, service, path, "ses-main", "msg-user-B", b)
	result := PromptResult{SessionID: "ses-main", MessageID: "msg-answer-A", ParentID: "msg-user-A", ReplyText: "answer A"}
	if _, err := service.finalizeAgentReply(context.Background(), provider, b, "thread", path, result); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 1 || provider.replies[0].messageID != a.MessageID || provider.replies[0].options.MentionUserID != a.SenderID {
		t.Fatalf("synchronous response routed to latest caller: %+v", provider.replies)
	}
}

type replyParentLookup struct {
	*fakeBackendClient
	*replyTestLookup
}

func TestSynchronousBackgroundReplyResolvesUnregisteredParent(t *testing.T) {
	t.Parallel()
	service, provider, client, _, ref, path := newReplyTestService(t)
	a := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-A", SenderID: "user-A"}
	b := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-B", SenderID: "user-B"}
	addReplySource(t, service, path, "ses-main", "msg-user-A", a)
	addReplySource(t, service, path, "ses-main", "msg-user-B", b)
	task := replyMessage("ses-main", "msg-task", "msg-user-A", "", 2)
	task.Parts = []backend.SessionMessagePart{{Type: "tool", Tool: "task", ToolSessionID: "ses-child", ToolBackground: true}}
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"ses-main": {
		{ID: "msg-user-A", Role: "user", CreatedAt: 1}, task,
		{ID: "msg-user-B", Role: "user", CreatedAt: 3},
		backgroundMessage("ses-main", "msg-notice", "ses-child", "completed", 4),
	}}}
	service.backends = func(config.Config, workspace.Settings) (backend.Client, error) {
		return &replyParentLookup{fakeBackendClient: client, replyTestLookup: lookup}, nil
	}
	result := PromptResult{SessionID: "ses-main", MessageID: "msg-final", ParentID: "msg-notice", ReplyText: "background A"}
	if _, err := service.finalizeAgentReply(context.Background(), provider, b, "thread", path, result); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 1 || provider.replies[0].messageID != a.MessageID {
		t.Fatalf("background synchronous result misrouted: %+v", provider.replies)
	}
}

func TestBTWReplyRegistersAndDeduplicatesItsOwnSession(t *testing.T) {
	t.Parallel()
	service, provider, client, db, ref, _ := newReplyTestService(t)
	client.promptFunc = func(_ context.Context, _ string, sessionID, _ string, _ []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		if err := options.OnPromptMessage("msg-btw-user"); err != nil {
			return backend.PromptResult{}, err
		}
		return backend.PromptResult{SessionID: sessionID, MessageID: "msg-btw-answer", ParentID: "msg-btw-user", ReplyText: "btw answer"}, nil
	}
	if _, err := service.ProcessText(context.Background(), TextInput{
		Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread",
		MessageID: "om-btw", SenderID: "user", Text: "/btw investigate",
	}); err != nil {
		t.Fatal(err)
	}
	sources, err := db.ListReplySources("")
	if err != nil || len(sources) != 1 {
		t.Fatalf("btw sources = %+v, err = %v", sources, err)
	}
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{sources[0].SessionID: {
		replyMessage(sources[0].SessionID, "msg-btw-answer", "msg-btw-user", "btw answer", 1),
	}}}
	if err := service.reconcileSessionReplies(context.Background(), lookup, sources[0].SessionID); err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 1 || provider.replies[0].messageID != "om-btw" {
		t.Fatalf("btw duplicate/routing failure: %+v", provider.replies)
	}
}

func TestRepliesCancelWhileConversationIsBusy(t *testing.T) {
	t.Parallel()
	service, _, _, _, ref, path := newReplyTestService(t)
	addReplySource(t, service, path, "ses-main", "msg-user", TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-user"})
	lock := service.lock(ref)
	lock.Lock()
	defer lock.Unlock()
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.reconcileSessionReplies(ctx, lookup, "ses-main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	if len(lookup.calls) != 0 {
		t.Fatal("queried backend after cancellation")
	}
}

func TestReplyListenerWaitsForSynchronousReply(t *testing.T) {
	t.Parallel()
	service, provider, client, _, ref, _ := newReplyTestService(t)
	started := make(chan struct{})
	release := make(chan struct{})
	client.promptFunc = func(_ context.Context, _ string, sessionID, _ string, _ []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
		if err := options.OnPromptMessage("msg-user"); err != nil {
			return backend.PromptResult{}, err
		}
		close(started)
		<-release
		return backend.PromptResult{SessionID: sessionID, MessageID: "msg-answer", ParentID: "msg-user", ReplyText: "last"}, nil
	}
	promptDone := make(chan error, 1)
	go func() {
		_, err := service.ProcessText(context.Background(), TextInput{
			Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "thread", MessageID: "om-user", Text: "hello",
		})
		promptDone <- err
	}()
	<-started
	gate := service.replyLock("prompt:" + conversationKey(ref))
	if gate.TryLock() {
		gate.Unlock()
		close(release)
		<-promptDone
		t.Fatal("synchronous prompt did not reserve its reply gate")
	}
	answer := replyMessage("session-test", "msg-answer", "msg-user", "first", 1)
	answer.Parts = append(answer.Parts, backend.SessionMessagePart{Type: "text", Text: "last"})
	lookup := &replyTestLookup{messages: map[string][]backend.SessionMessage{"session-test": {answer}}}
	listenerDone := make(chan error, 1)
	go func() { listenerDone <- service.reconcileSessionReplies(context.Background(), lookup, "session-test") }()
	close(release)
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	if err := <-listenerDone; err != nil {
		t.Fatal(err)
	}
	if len(provider.replies) != 1 || provider.replies[0].text != "last" {
		t.Fatalf("listener replaced or duplicated synchronous reply: %+v", provider.replies)
	}
}

func TestRepliesAndSynchronousSendUseOneDeliveryLock(t *testing.T) {
	t.Parallel()
	service, provider, _, _, ref, path := newReplyTestService(t)
	input := TextInput{Provider: ref.Provider, ConversationID: ref.ConversationID, ConversationType: "group", MessageID: "om-user"}
	result := PromptResult{SessionID: "ses-main", MessageID: "msg-answer", ReplyText: "answer"}
	const workers = 8
	var wait sync.WaitGroup
	wait.Add(workers)
	errors := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wait.Done()
			_, err := service.finalizeAgentReply(context.Background(), provider, input, "thread", path, result)
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(provider.replies) != 1 {
		t.Fatalf("concurrent sends = %+v", provider.replies)
	}
}

type replyTestStore struct {
	mu        sync.Mutex
	sources   map[string]appstore.ReplySourceRecord
	delivered map[string]bool
}

func (s *replyTestStore) UpsertReplySource(record appstore.ReplySourceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources[record.SessionID+":"+record.MessageID] = record
	return nil
}

func (s *replyTestStore) ListReplySources(sessionID string) ([]appstore.ReplySourceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := []appstore.ReplySourceRecord{}
	for _, record := range s.sources {
		if sessionID == "" || record.SessionID == sessionID {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].SessionID == records[j].SessionID {
			return records[i].MessageID < records[j].MessageID
		}
		return records[i].SessionID < records[j].SessionID
	})
	return records, nil
}

func (s *replyTestStore) ReplyDelivered(sessionID, messageID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered[sessionID+":"+messageID], nil
}

func (s *replyTestStore) MarkReplyDelivered(sessionID, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered[sessionID+":"+messageID] = true
	return nil
}
