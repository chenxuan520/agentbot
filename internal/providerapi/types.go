package providerapi

import "context"

type Client interface {
	Name() string
	Health(ctx context.Context) error
	AddHandlingReaction(ctx context.Context, messageID string) (string, error)
	AddRemoteHandlingReaction(ctx context.Context, messageID string) (string, error)
	AddBlockedReaction(ctx context.Context, messageID string) error
	AddReaction(ctx context.Context, messageID, emojiType string) error
	DeleteReaction(ctx context.Context, messageID, reactionID string) error
	SendTextToChat(ctx context.Context, chatID, text, title string) error
	ReplyTextToMessage(ctx context.Context, messageID, text, title string, options ReplyOptions) error
}

type ChatMemberLister interface {
	ListChatMembers(ctx context.Context, chatID string) ([]ChatMember, error)
}

type MessageGetter interface {
	GetMessage(ctx context.Context, messageID string) (ChatMessage, error)
}

type ChatMessageLister interface {
	ListChatMessages(ctx context.Context, chatID string, options ChatMessageListOptions) (ChatMessageListResult, error)
}

type MessageRecaller interface {
	RecallMessage(ctx context.Context, messageID string) error
}

type ChatDisplayInfoResolver interface {
	GetChatDisplayInfo(ctx context.Context, chatID string) (ChatDisplayInfo, error)
}

type ChatDisplayInfo struct {
	DisplayName string `json:"displayName,omitempty"`
	ChatMode    string `json:"chatMode,omitempty"`
}

type ChatMember struct {
	MemberID     string `json:"memberId"`
	MemberIDType string `json:"memberIdType"`
	Name         string `json:"name"`
	TenantKey    string `json:"tenantKey,omitempty"`
}

type ChatMessageListOptions struct {
	StartTime          string
	EndTime            string
	SortType           string
	PageSize           int
	PageToken          string
	CardMsgContentType string
}

type ChatMessageListResult struct {
	HasMore   bool          `json:"hasMore"`
	PageToken string        `json:"pageToken,omitempty"`
	Items     []ChatMessage `json:"items"`
}

type ChatMessage struct {
	MessageID  string            `json:"messageId"`
	RootID     string            `json:"rootId,omitempty"`
	ParentID   string            `json:"parentId,omitempty"`
	ThreadID   string            `json:"threadId,omitempty"`
	MsgType    string            `json:"msgType"`
	CreateTime string            `json:"createTime"`
	UpdateTime string            `json:"updateTime,omitempty"`
	Deleted    bool              `json:"deleted"`
	Updated    bool              `json:"updated"`
	ChatID     string            `json:"chatId"`
	Sender     ChatMessageSender `json:"sender"`
	Body       ChatMessageBody   `json:"body"`
	Mentions   []ChatMessageAt   `json:"mentions,omitempty"`
}

type ChatMessageSender struct {
	ID         string `json:"id"`
	IDType     string `json:"idType"`
	SenderType string `json:"senderType"`
	TenantKey  string `json:"tenantKey,omitempty"`
}

type ChatMessageBody struct {
	Content string `json:"content"`
}

type ChatMessageAt struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	IDType    string `json:"idType"`
	Name      string `json:"name"`
	TenantKey string `json:"tenantKey,omitempty"`
}

type ReplyOptions struct {
	InThread       bool
	MentionUserID  string
	MentionUserIDs []string
}
