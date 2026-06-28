package scheduler

import (
	"fmt"
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

func (s *Service) Schedule(ref conversation.Ref, route, payload string, runAt time.Time) (Job, error) {
	now := time.Now().UTC()
	job := Job{
		ID:             uuid.NewString(),
		Provider:       ref.Provider,
		ConversationID: ref.ConversationID,
		Route:          route,
		Payload:        payload,
		RunAt:          runAt.UTC(),
		Status:         StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateJob(job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) List(ref conversation.Ref, limit int) ([]Job, error) {
	return s.store.ListJobs(ref, limit)
}

func (s *Service) ListActive(ref conversation.Ref, limit int) ([]Job, error) {
	return s.store.ListActiveJobs(ref, limit)
}

func (s *Service) Due(now time.Time, limit int) ([]Job, error) {
	return s.store.ListDueJobs(now.UTC(), limit)
}

// RecoverStuckJobs requeues jobs left running by a previous crash, dead-lettering
// any that have exhausted maxAttempts. It returns (reclaimed, deadLettered).
func (s *Service) RecoverStuckJobs(maxAttempts int) (int, int, error) {
	return s.store.ReclaimRunningJobs(time.Now().UTC(), maxAttempts)
}

func (s *Service) Complete(id string) error {
	return s.store.UpdateJobStatus(id, StatusDone, time.Now().UTC())
}

func (s *Service) Cancel(id string) error {
	return s.store.UpdateJobStatus(id, StatusCancel, time.Now().UTC())
}

func (s *Service) MarkRunning(id string) error {
	return s.store.UpdateJobStatus(id, StatusRunning, time.Now().UTC())
}

func (s *Service) Fail(id string) error {
	return s.store.UpdateJobStatus(id, StatusFailed, time.Now().UTC())
}

func (s *Service) Reschedule(id string, runAt time.Time) error {
	return s.store.RescheduleJob(id, runAt.UTC(), time.Now().UTC())
}

func (s *Service) Get(id string) (Job, error) {
	getter, ok := s.store.(interface {
		GetJob(id string) (Job, error)
	})
	if !ok {
		return Job{}, fmt.Errorf("scheduler store does not support get job")
	}
	return getter.GetJob(id)
}

func (s *Service) UpdatePayload(id, payload string) error {
	updater, ok := s.store.(interface {
		UpdateJobPayload(id, payload string, updatedAt time.Time) error
	})
	if !ok {
		return fmt.Errorf("scheduler store does not support updating job payload")
	}
	return updater.UpdateJobPayload(id, payload, time.Now().UTC())
}
