package scheduler

import (
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusCancel  = "cancelled"
)

type Job struct {
	ID             string
	Provider       string
	ConversationID string
	Route          string
	Payload        string
	RunAt          time.Time
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (j Job) Ref() conversation.Ref {
	return conversation.Ref{Provider: j.Provider, ConversationID: j.ConversationID}
}

type Store interface {
	CreateJob(job Job) error
	ListJobs(ref conversation.Ref, limit int) ([]Job, error)
	ListActiveJobs(ref conversation.Ref, limit int) ([]Job, error)
	ListDueJobs(now time.Time, limit int) ([]Job, error)
	UpdateJobStatus(id, status string, updatedAt time.Time) error
	RescheduleJob(id string, runAt, updatedAt time.Time) error
}
