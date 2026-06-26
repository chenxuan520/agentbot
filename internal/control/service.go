package control

import (
	"time"

	"github.com/google/uuid"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Refuse(ref conversation.Ref, until time.Time, reason string) (Rule, error) {
	return s.createRule(ref, KindRefuse, "conversation", "", until, reason)
}

func (s *Service) Active(ref conversation.Ref, now time.Time) ([]Rule, error) {
	return s.store.ListActiveRules(ref, now.UTC())
}

func (s *Service) HasActiveRefuse(ref conversation.Ref, now time.Time) (bool, error) {
	rules, err := s.store.ListActiveRules(ref, now.UTC())
	if err != nil {
		return false, err
	}
	for _, rule := range rules {
		if rule.Kind == KindRefuse {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) CancelActiveRefuse(ref conversation.Ref, now time.Time) (int, error) {
	rules, err := s.store.ListActiveRules(ref, now.UTC())
	if err != nil {
		return 0, err
	}

	updatedAt := time.Now().UTC()
	cancelled := 0
	for _, rule := range rules {
		if rule.Kind != KindRefuse {
			continue
		}
		if err := s.store.UpdateRuleStatus(rule.ID, StatusStop, updatedAt); err != nil {
			return cancelled, err
		}
		cancelled++
	}
	return cancelled, nil
}

func (s *Service) Cancel(id string) error {
	return s.store.UpdateRuleStatus(id, StatusStop, time.Now().UTC())
}

func (s *Service) createRule(ref conversation.Ref, kind, scope, matchKey string, until time.Time, reason string) (Rule, error) {
	now := time.Now().UTC()
	rule := Rule{
		ID:             uuid.NewString(),
		Provider:       ref.Provider,
		ConversationID: ref.ConversationID,
		Kind:           kind,
		Scope:          scope,
		MatchKey:       matchKey,
		Reason:         reason,
		UntilAt:        until.UTC(),
		Status:         StatusActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateRule(rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}
