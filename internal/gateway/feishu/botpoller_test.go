package feishu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/flow"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
)

type fakeBotPollLister struct {
	result providerapi.ChatMessageListResult
	err    error
	calls  int
}

func (f *fakeBotPollLister) ListChatMessages(_ context.Context, _ string, _ providerapi.ChatMessageListOptions) (providerapi.ChatMessageListResult, error) {
	f.calls++
	return f.result, f.err
}

func appMessage(id, appID, createTime string) providerapi.ChatMessage {
	return providerapi.ChatMessage{
		MessageID:  id,
		MsgType:    "interactive",
		CreateTime: createTime,
		Sender:     providerapi.ChatMessageSender{ID: appID, IDType: "app_id", SenderType: "app"},
		Body:       providerapi.ChatMessageBody{Content: `{"card":"` + id + `"}`},
	}
}

func userMessage(id, openID, createTime string) providerapi.ChatMessage {
	return providerapi.ChatMessage{
		MessageID:  id,
		MsgType:    "text",
		CreateTime: createTime,
		Sender:     providerapi.ChatMessageSender{ID: openID, IDType: "open_id", SenderType: "user"},
		Body:       providerapi.ChatMessageBody{Content: `{"text":"hi"}`},
	}
}

func newTestPoller(t *testing.T, fake *fakeBotPollLister) (*botMessagePoller, conversation.Ref) {
	t.Helper()
	root := t.TempDir()
	ref := conversation.Ref{Provider: "feishu", ConversationID: "oc_test"}
	p := &botMessagePoller{
		chatRootDir:       root,
		selfAppID:         "cli_self",
		lister:            fake,
		enabledRefs:       func() ([]conversation.Ref, error) { return []conversation.Ref{ref}, nil },
		process:           func(context.Context, flow.TextInput) (flow.PromptResult, error) { return flow.PromptResult{}, nil },
		acceptInteractive: func(conversation.Ref) (bool, error) { return true, nil },
	}
	return p, ref
}

func TestSelectForDeliveryPicksLatestOtherBot(t *testing.T) {
	t.Parallel()

	fake := &fakeBotPollLister{result: providerapi.ChatMessageListResult{Items: []providerapi.ChatMessage{
		userMessage("om_user", "ou_human", "300"),
		appMessage("om_self", "cli_self", "250"),
		appMessage("om_other", "cli_other", "200"),
	}}}
	p, ref := newTestPoller(t, fake)

	input, err := p.selectForDelivery(context.Background(), ref)
	if err != nil {
		t.Fatalf("selectForDelivery: %v", err)
	}
	if input == nil {
		t.Fatal("expected an other-bot message to deliver")
	}
	if input.MessageID != "om_other" {
		t.Fatalf("delivered messageID = %q, want om_other", input.MessageID)
	}
	if input.SenderType != "app" || input.SenderID != "cli_other" {
		t.Fatalf("unexpected sender: type=%q id=%q", input.SenderType, input.SenderID)
	}
	if input.MessageType != "interactive" {
		t.Fatalf("messageType = %q, want interactive", input.MessageType)
	}
	if input.Text != `{"card":"om_other"}` {
		t.Fatalf("text = %q, want raw card content", input.Text)
	}
	if input.ConversationType != "group" || !input.AddReaction {
		t.Fatalf("unexpected conversation/reaction: %+v", input)
	}
}

func TestSelectForDeliveryExcludesSelfAndHuman(t *testing.T) {
	t.Parallel()

	fake := &fakeBotPollLister{result: providerapi.ChatMessageListResult{Items: []providerapi.ChatMessage{
		userMessage("om_user", "ou_human", "300"),
		appMessage("om_self", "cli_self", "250"),
	}}}
	p, ref := newTestPoller(t, fake)

	input, err := p.selectForDelivery(context.Background(), ref)
	if err != nil {
		t.Fatalf("selectForDelivery: %v", err)
	}
	if input != nil {
		t.Fatalf("expected nothing to deliver, got %+v", input)
	}
}

func TestSelectForDeliverySkipsInteractiveWhenNotAccepted(t *testing.T) {
	t.Parallel()

	fake := &fakeBotPollLister{result: providerapi.ChatMessageListResult{Items: []providerapi.ChatMessage{
		appMessage("om_card", "cli_other", "200"),
	}}}
	p, ref := newTestPoller(t, fake)
	p.acceptInteractive = func(conversation.Ref) (bool, error) { return false, nil }

	input, err := p.selectForDelivery(context.Background(), ref)
	if err != nil {
		t.Fatalf("selectForDelivery: %v", err)
	}
	if input != nil {
		t.Fatalf("expected interactive card to be skipped when interactive is off, got %+v", input)
	}
	// The skipped card must not be recorded as processed, so enabling the switch
	// later can still let it through.
	if state := loadBotPollState(ref.WorkspacePath(p.chatRootDir)); len(state) != 0 {
		t.Fatalf("expected no dedup state for a skipped card, got %v", state)
	}
}

func TestSelectForDeliveryDeliversNonInteractiveWhenInteractiveOff(t *testing.T) {
	t.Parallel()

	textMsg := providerapi.ChatMessage{
		MessageID:  "om_text",
		MsgType:    "text",
		CreateTime: "200",
		Sender:     providerapi.ChatMessageSender{ID: "cli_other", IDType: "app_id", SenderType: "app"},
		Body:       providerapi.ChatMessageBody{Content: `{"text":"hello"}`},
	}
	fake := &fakeBotPollLister{result: providerapi.ChatMessageListResult{Items: []providerapi.ChatMessage{textMsg}}}
	p, ref := newTestPoller(t, fake)
	p.acceptInteractive = func(conversation.Ref) (bool, error) { return false, nil }

	input, err := p.selectForDelivery(context.Background(), ref)
	if err != nil {
		t.Fatalf("selectForDelivery: %v", err)
	}
	if input == nil {
		t.Fatal("expected a non-interactive other-bot message to be delivered even when interactive is off")
	}
	if input.MessageID != "om_text" || input.MessageType != "text" {
		t.Fatalf("unexpected delivered message: %+v", input)
	}
}

func TestSelectForDeliveryDedupAndNewer(t *testing.T) {
	t.Parallel()

	fake := &fakeBotPollLister{result: providerapi.ChatMessageListResult{Items: []providerapi.ChatMessage{
		appMessage("om_other", "cli_other", "200"),
	}}}
	p, ref := newTestPoller(t, fake)

	first, err := p.selectForDelivery(context.Background(), ref)
	if err != nil || first == nil {
		t.Fatalf("first selectForDelivery: input=%v err=%v", first, err)
	}

	// Same latest message on the next tick must not be re-delivered.
	again, err := p.selectForDelivery(context.Background(), ref)
	if err != nil {
		t.Fatalf("second selectForDelivery: %v", err)
	}
	if again != nil {
		t.Fatalf("expected dedup to skip already-processed message, got %+v", again)
	}

	// A newer other-bot message should be delivered.
	fake.result.Items = append([]providerapi.ChatMessage{appMessage("om_other2", "cli_other", "400")}, fake.result.Items...)
	newer, err := p.selectForDelivery(context.Background(), ref)
	if err != nil || newer == nil {
		t.Fatalf("expected newer message: input=%v err=%v", newer, err)
	}
	if newer.MessageID != "om_other2" {
		t.Fatalf("delivered messageID = %q, want om_other2", newer.MessageID)
	}
}

func TestSelectForDeliveryPersistsState(t *testing.T) {
	t.Parallel()

	fake := &fakeBotPollLister{result: providerapi.ChatMessageListResult{Items: []providerapi.ChatMessage{
		appMessage("om_other", "cli_other", "200"),
	}}}
	p, ref := newTestPoller(t, fake)

	if _, err := p.selectForDelivery(context.Background(), ref); err != nil {
		t.Fatalf("selectForDelivery: %v", err)
	}
	state := loadBotPollState(ref.WorkspacePath(p.chatRootDir))
	if _, ok := state["om_other"]; !ok {
		t.Fatalf("expected om_other to be recorded in state, got %v", state)
	}
}

func TestSelectForDeliverySkipsWhenNoLister(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ref := conversation.Ref{Provider: "feishu", ConversationID: "oc_test"}
	p := &botMessagePoller{
		chatRootDir: root,
		selfAppID:   "cli_self",
		lister:      nil,
		enabledRefs: func() ([]conversation.Ref, error) { return []conversation.Ref{ref}, nil },
		process:     func(context.Context, flow.TextInput) (flow.PromptResult, error) { return flow.PromptResult{}, nil },
	}
	input, err := p.selectForDelivery(context.Background(), ref)
	if err != nil {
		t.Fatalf("selectForDelivery: %v", err)
	}
	if input != nil {
		t.Fatalf("expected nil when no lister is wired, got %+v", input)
	}
}

func TestTickRecoversFromPanic(t *testing.T) {
	t.Parallel()

	p := &botMessagePoller{
		enabledRefs: func() ([]conversation.Ref, error) { panic("boom") },
	}
	// Must not crash the process.
	p.tick(context.Background())
}

func TestDeliverRecoversFromPanic(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	wg.Add(1)
	p := &botMessagePoller{
		process: func(context.Context, flow.TextInput) (flow.PromptResult, error) {
			defer wg.Done()
			panic("boom")
		},
	}
	p.deliver(flow.TextInput{})
	wg.Wait()
	// Give the deferred recover a moment to run; reaching here means no crash.
	time.Sleep(10 * time.Millisecond)
}

func TestLoadStatePrunesAndIgnoresGarbage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := float64(time.Now().Unix())
	old := now - (botPollStateRetention.Seconds() + 60)
	body := fmt.Sprintf(`{"om_fresh": %f, "om_old": %f}`, now, old)
	path := filepath.Join(dir, botPollStateRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	state := loadBotPollState(dir)
	if _, ok := state["om_fresh"]; !ok {
		t.Fatal("expected fresh entry to be kept")
	}
	if _, ok := state["om_old"]; ok {
		t.Fatal("expected stale entry to be pruned")
	}

	// Malformed JSON yields an empty state rather than an error.
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if got := loadBotPollState(dir); len(got) != 0 {
		t.Fatalf("expected empty state for garbage, got %v", got)
	}

	// Round-trip through saveBotPollState.
	if err := saveBotPollState(dir, map[string]float64{"om_rt": now}); err != nil {
		t.Fatalf("saveBotPollState: %v", err)
	}
	if _, ok := loadBotPollState(dir)["om_rt"]; !ok {
		t.Fatal("expected round-tripped entry to be present")
	}
}
