package scheduler

import (
	"context"
	"log"
	"time"
)

type Handler interface {
	Handle(job Job, triggeredAt time.Time) error
}

type Runner struct {
	service           *Service
	handler           Handler
	loopErrorNotifier func(error)
}

func NewRunner(service *Service, handler Handler) *Runner {
	return &Runner{service: service, handler: handler}
}

func (r *Runner) SetLoopErrorNotifier(notifier func(error)) {
	r.loopErrorNotifier = notifier
}

func (r *Runner) RunDue(now time.Time, limit int) ([]Job, error) {
	jobs, err := r.service.Due(now, limit)
	if err != nil {
		return nil, err
	}

	triggered := make([]Job, 0, len(jobs))
	for _, job := range jobs {
		if err := r.service.MarkRunning(job.ID); err != nil {
			return triggered, err
		}
		if err := r.handler.Handle(job, now.UTC()); err != nil {
			if failErr := r.service.Fail(job.ID); failErr != nil {
				return triggered, failErr
			}
			log.Printf("scheduler job failed: id=%s route=%s ref=%s/%s err=%v", job.ID, job.Route, job.Provider, job.ConversationID, err)
			continue
		}
		nextRunAt, recurring, err := NextRunAt(job, now.UTC())
		if err != nil {
			if failErr := r.service.Fail(job.ID); failErr != nil {
				return triggered, failErr
			}
			log.Printf("scheduler job reschedule failed: id=%s route=%s ref=%s/%s err=%v", job.ID, job.Route, job.Provider, job.ConversationID, err)
			continue
		}
		if recurring {
			if err := r.service.Reschedule(job.ID, nextRunAt); err != nil {
				return triggered, err
			}
			job.Status = StatusPending
			job.RunAt = nextRunAt
			triggered = append(triggered, job)
			continue
		}
		if err := r.service.Complete(job.ID); err != nil {
			return triggered, err
		}
		job.Status = StatusDone
		triggered = append(triggered, job)
	}

	return triggered, nil
}

func (r *Runner) Loop(ctx context.Context, interval time.Duration, limit int) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := r.RunDue(time.Now().UTC(), limit); err != nil {
			log.Printf("scheduler loop error: %v", err)
			if r.loopErrorNotifier != nil {
				r.loopErrorNotifier(err)
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
