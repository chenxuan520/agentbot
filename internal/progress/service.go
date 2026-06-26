package progress

import (
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

type Record struct {
	Provider          string
	ConversationID    string
	LastMessageID     string
	LastMessageTimeMS int64
	UpdatedAt         time.Time
}

type Store interface {
	GetProgress(ref conversation.Ref) (*Record, error)
	UpsertProgress(record Record) error
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Accept(ref conversation.Ref, messageID string, createTimeMS int64) (bool, error) {
	if createTimeMS <= 0 {
		return true, nil
	}

	record, err := s.store.GetProgress(ref)
	if err != nil {
		return false, err
	}
	if record != nil {
		if createTimeMS < record.LastMessageTimeMS {
			return false, nil
		}
		if createTimeMS == record.LastMessageTimeMS && messageID == record.LastMessageID {
			return false, nil
		}
	}

	return true, s.store.UpsertProgress(Record{
		Provider:          ref.Provider,
		ConversationID:    ref.ConversationID,
		LastMessageID:     messageID,
		LastMessageTimeMS: createTimeMS,
		UpdatedAt:         time.Now().UTC(),
	})
}
