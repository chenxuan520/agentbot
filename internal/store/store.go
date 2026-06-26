package store

import (
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

type WorkspaceRecord struct {
	Provider        string
	ConversationID  string
	WorkspacePath   string
	Template        string
	AgentBackend    string
	ActiveSessionID string
	BTWSessionID    string
	LastMessageAt   time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type SessionTokenRecord struct {
	Provider        string
	ConversationID  string
	TokenHash       string
	TokenCiphertext string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type WorkspaceStore interface {
	Get(ref conversation.Ref) (*WorkspaceRecord, error)
	Upsert(record WorkspaceRecord) error
	Delete(ref conversation.Ref) error
	List() ([]WorkspaceRecord, error)
	GetBTWSession(ref conversation.Ref, senderID string) (string, error)
	HasBTWSessions(ref conversation.Ref) (bool, error)
	UpsertBTWSession(ref conversation.Ref, senderID, sessionID string, updatedAt time.Time) error
	DeleteBTWSession(ref conversation.Ref, senderID string) error
	DeleteBTWSessions(ref conversation.Ref) error
	GetTopicSession(ref conversation.Ref, topicKey string) (string, error)
	HasTopicSessions(ref conversation.Ref) (bool, error)
	UpsertTopicSession(ref conversation.Ref, topicKey, sessionID string, updatedAt time.Time) error
	DeleteTopicSession(ref conversation.Ref, topicKey string) error
	DeleteTopicSessions(ref conversation.Ref) error
}

type SessionTokenStore interface {
	GetSessionToken(ref conversation.Ref) (*SessionTokenRecord, error)
	GetSessionTokenByHash(tokenHash string) (*SessionTokenRecord, error)
	UpsertSessionToken(record SessionTokenRecord) error
	DeleteSessionToken(ref conversation.Ref) error
}
