package backend

import (
	"context"
	"strings"
)

type Client interface {
	Name() string
	Health(ctx context.Context) error
	CreateSession(ctx context.Context, workspacePath string) (string, error)
	AbortSession(ctx context.Context, sessionID string) error
	Prompt(ctx context.Context, workspacePath, sessionID, text string, attachments []Attachment, options PromptOptions) (PromptResult, error)
}

type SessionLookup interface {
	GetSession(ctx context.Context, sessionID string) (SessionInfo, error)
}

type SessionMessageLookup interface {
	GetSessionMessages(ctx context.Context, sessionID string) ([]SessionMessage, error)
}

type SessionReplyLookup interface {
	GetSessionReplyMessages(ctx context.Context, workspacePath, sessionID string) ([]SessionMessage, error)
	GetSessionReplyMessage(ctx context.Context, workspacePath, sessionID, messageID string) (SessionMessage, error)
}

type SessionEvent struct {
	Type        string
	SessionID   string
	MessageID   string
	Role        string
	CompletedAt int64
}

type SessionEventSource interface {
	WatchSessionEvents(ctx context.Context, handle func(SessionEvent) error) error
}

// SessionCompactor compacts a session's context in place (the session id stays
// the same). Backends that cannot summarize/compact simply omit this interface.
type SessionCompactor interface {
	CompactSession(ctx context.Context, workspacePath, sessionID string) error
}

// SessionModelCatalog reports the models a backend can run for a workspace and
// the model currently in effect. Backends without a notion of selectable models
// simply omit this interface.
type SessionModelCatalog interface {
	// ListModels returns the selectable models grouped by provider plus the
	// currently effective model id ("providerID/modelID").
	ListModels(ctx context.Context, workspacePath string) (ModelCatalog, error)
	// CurrentModel returns the effective model id ("providerID/modelID") used
	// for new turns in the workspace.
	CurrentModel(ctx context.Context, workspacePath string) (string, error)
}

// ModelCatalog is the set of selectable models for a workspace plus the model
// currently in effect.
type ModelCatalog struct {
	// Current is the effective model id ("providerID/modelID"), possibly empty.
	Current   string
	Providers []ModelProvider
}

// ModelProvider groups a provider's selectable models.
type ModelProvider struct {
	ID      string
	Name    string
	Default string
	Models  []ModelInfo
}

// ModelInfo describes one selectable model.
type ModelInfo struct {
	ID           string
	Name         string
	ContextLimit int
}

type SessionInfo struct {
	ID        string
	Directory string
}

type SessionMessage struct {
	ID               string
	SessionID        string
	ParentID         string
	Role             string
	CreatedAt        int64
	CompletedAt      int64
	Summary          bool
	Finish           string
	HasError         bool
	ReplySuppressed  bool
	Native           bool
	Sequence         int64
	SourceMessageIDs []string
	Tokens           TokenUsage
	Parts            []SessionMessagePart
}

// TokenUsage is opencode's per-message token accounting. For the latest
// assistant message it approximates the session's current context size.
type TokenUsage struct {
	Total      int
	Input      int
	Output     int
	Reasoning  int
	CacheRead  int
	CacheWrite int
}

// LatestContextTokens returns the most recent assistant message's token usage,
// which approximates the session's current context size. The boolean is false
// when no assistant message carries usable token counts.
func LatestContextTokens(messages []SessionMessage) (TokenUsage, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(messages[i].Role), "assistant") {
			continue
		}
		tokens := messages[i].Tokens
		if tokens.Total <= 0 {
			tokens.Total = tokens.Input + tokens.Output + tokens.Reasoning
		}
		if tokens.Total <= 0 {
			continue
		}
		return tokens, true
	}
	return TokenUsage{}, false
}

type SessionMessagePart struct {
	Type               string
	Text               string
	Reason             string
	Tool               string
	ToolStatus         string
	ToolInputSummary   string
	Synthetic          bool
	Ignored            bool
	ToolSessionID      string
	ToolBackground     bool
	ToolUpdated        bool
	ToolCancelled      bool
	ProviderExecuted   bool
	Interrupted        bool
	CompactionContinue bool
}

func FinalReplyText(message SessionMessage) (string, bool) {
	if message.Role != "assistant" || message.CompletedAt <= 0 || message.Summary || message.HasError || message.ReplySuppressed || message.Finish != "stop" {
		return "", false
	}
	text := ""
	for _, part := range message.Parts {
		if part.Type == "tool" && !part.ProviderExecuted && !part.Interrupted {
			return "", false
		}
		if part.Type != "text" || part.Synthetic || part.Ignored {
			continue
		}
		if value := strings.TrimSpace(part.Text); value != "" {
			text = value
		}
	}
	return text, text != ""
}

type PromptOptions struct {
	NoReply          bool
	System           string
	OnPromptMessage  func(messageID string) error
	SystemMessageIDs []string
	// Model, when set ("providerID/modelID"), overrides the backend's default
	// model for this single prompt. Backends that cannot switch models per
	// request ignore it.
	Model string
}

type Attachment struct {
	Mime     string
	Filename string
	URL      string
}

type PromptResult struct {
	SessionID        string
	MessageID        string
	ParentID         string
	ReplyText        string
	Native           bool
	SourceMessageIDs []string
}
