package backend

import "context"

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

type SessionInfo struct {
	ID        string
	Directory string
}

type SessionMessage struct {
	ID        string
	Role      string
	CreatedAt int64
	Parts     []SessionMessagePart
}

type SessionMessagePart struct {
	Type       string
	Text       string
	Reason     string
	Tool       string
	ToolStatus string
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
