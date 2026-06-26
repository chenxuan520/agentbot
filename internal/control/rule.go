package control

import (
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

const (
	KindRefuse   = "refuse"
	StatusActive = "active"
	StatusStop   = "cancelled"
)

type Rule struct {
	ID             string
	Provider       string
	ConversationID string
	Kind           string
	Scope          string
	MatchKey       string
	Reason         string
	UntilAt        time.Time
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (r Rule) Ref() conversation.Ref {
	return conversation.Ref{Provider: r.Provider, ConversationID: r.ConversationID}
}

type Store interface {
	CreateRule(rule Rule) error
	ListActiveRules(ref conversation.Ref, now time.Time) ([]Rule, error)
	UpdateRuleStatus(id, status string, updatedAt time.Time) error
}
