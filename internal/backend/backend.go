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

// SessionCompactor compacts a session's context in place (the session id stays
// the same). Backends that cannot summarize/compact simply omit this interface.
type SessionCompactor interface {
	CompactSession(ctx context.Context, workspacePath, sessionID string) error
}

type SessionInfo struct {
	ID        string
	Directory string
}

type SessionMessage struct {
	ID        string
	Role      string
	CreatedAt int64
	Tokens    TokenUsage
	Parts     []SessionMessagePart
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
	Type             string
	Text             string
	Reason           string
	Tool             string
	ToolStatus       string
	ToolInputSummary string
}

type PromptOptions struct {
	NoReply bool
	System  string
}

type Attachment struct {
	Mime     string
	Filename string
	URL      string
}

type PromptResult struct {
	SessionID string
	ReplyText string
}
