package feishu

// botMessagePoller polls each opted-in conversation for messages sent by other
// bots and replays them into the normal processing chain.
//
// Feishu deliberately does not push bot-to-bot messages over the realtime event
// stream to avoid loops, so the only way to observe another bot's messages is to
// poll the chat history. This is therefore part of Feishu inbound and lives next
// to the realtime listener: the listener owns it and runs it alongside the
// websocket. It used to run as a separate external poller; it now runs
// in-process, the same shape as the scheduler loop.
//
// Other providers, when added, are expected to handle their own ingress in their
// own gateway package, so this stays Feishu-specific by design.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/flow"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
)

const (
	// botPollInterval matches the legacy external poller cadence.
	botPollInterval = time.Second

	botPollWindow         = 24 * time.Hour
	botPollPageSize       = 50
	botPollStateRetention = 24 * time.Hour
	botPollStateRelPath   = ".agents/runtime/poll-group-bot-cards-state.json"
	senderTypeApp         = "app"
	msgTypeInteractive    = "interactive"
)

type botPollEnabledRefsFunc func() ([]conversation.Ref, error)
type botPollProcessFunc func(context.Context, flow.TextInput) (flow.PromptResult, error)
type botPollAcceptInteractiveFunc func(conversation.Ref) (bool, error)

// botMessagePoller replays other bots' messages for conversations that opted in
// via settings.acceptOtherBotMessages.
type botMessagePoller struct {
	chatRootDir       string
	selfAppID         string
	lister            providerapi.ChatMessageLister
	enabledRefs       botPollEnabledRefsFunc
	process           botPollProcessFunc
	acceptInteractive botPollAcceptInteractiveFunc
}

// loop polls on a fixed interval until ctx is cancelled. Transient errors are
// logged to stderr and never abort the loop, so a flaky provider call cannot
// take down the daemon.
func (p *botMessagePoller) loop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = botPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *botMessagePoller) tick(ctx context.Context) {
	defer recoverBotPollPanic("tick")
	if p.enabledRefs == nil {
		return
	}
	refs, err := p.enabledRefs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "feishu bot poller: list enabled refs: %v\n", err)
		return
	}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return
		}
		input, err := p.selectForDelivery(ctx, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "feishu bot poller [%s/%s]: %v\n", ref.Provider, ref.ConversationID, err)
			continue
		}
		if input == nil {
			continue
		}
		p.deliver(*input)
	}
}

// selectForDelivery finds the latest unprocessed other-bot message for ref,
// records it as processed (persisting dedup state), and returns the input to
// replay. It returns nil when there is nothing new to deliver.
func (p *botMessagePoller) selectForDelivery(ctx context.Context, ref conversation.Ref) (*flow.TextInput, error) {
	if p.lister == nil {
		return nil, nil
	}

	end := time.Now()
	start := end.Add(-botPollWindow)
	result, err := p.lister.ListChatMessages(ctx, ref.ConversationID, providerapi.ChatMessageListOptions{
		StartTime: strconv.FormatInt(start.Unix(), 10),
		EndTime:   strconv.FormatInt(end.Unix(), 10),
		SortType:  "ByCreateTimeDesc",
		PageSize:  botPollPageSize,
	})
	if err != nil {
		return nil, err
	}

	latest := p.latestOtherBotMessage(result.Items)
	if latest == nil {
		return nil, nil
	}
	messageID := strings.TrimSpace(latest.MessageID)
	if messageID == "" {
		return nil, nil
	}

	// Keep the interactive-card gate consistent with the realtime listener: when
	// a conversation does not accept interactive cards, the poller must not
	// replay an interactive card either. Other message types (text/post) still
	// flow through, governed only by acceptOtherBotMessages.
	if strings.EqualFold(strings.TrimSpace(latest.MsgType), msgTypeInteractive) {
		allowed, err := p.interactiveAllowed(ref)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, nil
		}
	}

	workspacePath := ref.WorkspacePath(p.chatRootDir)
	state := loadBotPollState(workspacePath)
	if _, seen := state[messageID]; seen {
		return nil, nil
	}

	state[messageID] = float64(time.Now().Unix())
	if err := saveBotPollState(workspacePath, state); err != nil {
		return nil, err
	}

	input := p.buildInput(ref, *latest)
	return &input, nil
}

func (p *botMessagePoller) deliver(input flow.TextInput) {
	if p.process == nil {
		return
	}
	go func() {
		defer recoverBotPollPanic("deliver")
		// Use a detached context so an in-flight agent turn is not cancelled when
		// the current poll tick returns; mirrors the async mock-message path.
		_, _ = p.process(context.Background(), input)
	}()
}

// interactiveAllowed reports whether the conversation accepts interactive cards.
// When no callback is wired it defaults to false, mirroring the realtime
// listener's behavior of dropping interactive messages unless explicitly opted in.
func (p *botMessagePoller) interactiveAllowed(ref conversation.Ref) (bool, error) {
	if p.acceptInteractive == nil {
		return false, nil
	}
	return p.acceptInteractive(ref)
}

func (p *botMessagePoller) latestOtherBotMessage(items []providerapi.ChatMessage) *providerapi.ChatMessage {
	var latest *providerapi.ChatMessage
	var latestTS int64 = -1
	for i := range items {
		message := items[i]
		if !p.isOtherBot(message) {
			continue
		}
		if strings.TrimSpace(message.MessageID) == "" {
			continue
		}
		ts := botMessageTimestamp(message)
		if latest == nil || ts > latestTS {
			selected := message
			latest = &selected
			latestTS = ts
		}
	}
	return latest
}

// isOtherBot reports whether the message was sent by another bot (provider
// senderType "app") and not by ourselves. App messages carry the app_id in
// sender.id (e.g. "cli_..."), which is the same namespace as cfg.FeishuAppID, so
// excluding ourselves is a direct id comparison.
func (p *botMessagePoller) isOtherBot(message providerapi.ChatMessage) bool {
	if !strings.EqualFold(strings.TrimSpace(message.Sender.SenderType), senderTypeApp) {
		return false
	}
	if message.Deleted {
		return false
	}
	if p.selfAppID != "" && strings.TrimSpace(message.Sender.ID) == p.selfAppID {
		return false
	}
	return true
}

func (p *botMessagePoller) buildInput(ref conversation.Ref, message providerapi.ChatMessage) flow.TextInput {
	return flow.TextInput{
		Provider:         ref.Provider,
		ConversationID:   ref.ConversationID,
		ConversationType: "group",
		MessageType:      strings.TrimSpace(message.MsgType),
		MessageID:        strings.TrimSpace(message.MessageID),
		RootMessageID:    strings.TrimSpace(message.RootID),
		ParentMessageID:  strings.TrimSpace(message.ParentID),
		ThreadID:         strings.TrimSpace(message.ThreadID),
		SenderType:       senderTypeApp,
		SenderID:         strings.TrimSpace(message.Sender.ID),
		Text:             message.Body.Content,
		AddReaction:      true,
	}
}

func botMessageTimestamp(message providerapi.ChatMessage) int64 {
	for _, raw := range []string{message.CreateTime, message.UpdateTime} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return value
		}
	}
	return 0
}

func botPollStatePath(workspacePath string) string {
	return filepath.Join(workspacePath, botPollStateRelPath)
}

// loadBotPollState reads the dedup state shared with the legacy external poller:
// a JSON map of messageID -> unix seconds, pruned to botPollStateRetention.
func loadBotPollState(workspacePath string) map[string]float64 {
	data, err := os.ReadFile(botPollStatePath(workspacePath))
	if err != nil {
		return map[string]float64{}
	}
	var raw map[string]json.Number
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]float64{}
	}
	now := float64(time.Now().Unix())
	state := make(map[string]float64, len(raw))
	for key, value := range raw {
		ts, err := value.Float64()
		if err != nil {
			continue
		}
		if now-ts <= botPollStateRetention.Seconds() {
			state[key] = ts
		}
	}
	return state
}

func saveBotPollState(workspacePath string, state map[string]float64) error {
	path := botPollStatePath(workspacePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func recoverBotPollPanic(stage string) {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr, "feishu bot poller %s panic: %v\n", stage, r)
	}
}
