package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

func TestRunnerReschedulesCronJob(t *testing.T) {
	t.Parallel()

	job := Job{
		ID:             "job-1",
		Provider:       "feishu",
		ConversationID: "chat-1",
		Payload:        `{"cron":"0 8 * * *","timezone":"Asia/Shanghai"}`,
		Status:         StatusPending,
	}
	store := &fakeSchedulerStore{dueJobs: []Job{job}}
	runner := NewRunner(NewService(store), fakeSchedulerHandler(func(job Job, triggeredAt time.Time) error {
		return nil
	}))

	now := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	triggered, err := runner.RunDue(now, 10)
	if err != nil {
		t.Fatalf("RunDue: %v", err)
	}
	if len(triggered) != 1 {
		t.Fatalf("triggered len = %d, want 1", len(triggered))
	}
	if len(store.rescheduled) != 1 {
		t.Fatalf("rescheduled len = %d, want 1", len(store.rescheduled))
	}
	if len(store.completed) != 0 {
		t.Fatalf("completed = %+v, want none", store.completed)
	}
	want := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	if !store.rescheduled[0].runAt.Equal(want) {
		t.Fatalf("rescheduled runAt = %s, want %s", store.rescheduled[0].runAt, want)
	}
	if triggered[0].Status != StatusPending {
		t.Fatalf("job status = %q, want pending", triggered[0].Status)
	}
	if !triggered[0].RunAt.Equal(want) {
		t.Fatalf("job runAt = %s, want %s", triggered[0].RunAt, want)
	}
}

func TestRunnerMarksFailedJobAndContinues(t *testing.T) {
	t.Parallel()

	jobs := []Job{
		{ID: "job-fail", Provider: "feishu", ConversationID: "chat-1", Route: "reminder.follow_up", Payload: `{"promptText":"first"}`, Status: StatusPending},
		{ID: "job-ok", Provider: "feishu", ConversationID: "chat-1", Route: "reminder.follow_up", Payload: `{"promptText":"second"}`, Status: StatusPending},
	}
	store := &fakeSchedulerStore{dueJobs: jobs}
	runner := NewRunner(NewService(store), fakeSchedulerHandler(func(job Job, triggeredAt time.Time) error {
		if job.ID == "job-fail" {
			return errFakeSchedulerFailure
		}
		return nil
	}))

	triggered, err := runner.RunDue(time.Date(2026, 6, 11, 9, 16, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("RunDue: %v", err)
	}
	if len(triggered) != 1 || triggered[0].ID != "job-ok" {
		t.Fatalf("triggered = %+v, want only job-ok", triggered)
	}
	if len(store.completed) != 1 || store.completed[0] != "job-ok" {
		t.Fatalf("completed = %+v, want [job-ok]", store.completed)
	}
	if len(store.failed) != 1 || store.failed[0] != "job-fail" {
		t.Fatalf("failed = %+v, want [job-fail]", store.failed)
	}
	if got := store.statuses; len(got) != 4 || got[0] != StatusRunning || got[1] != StatusFailed || got[2] != StatusRunning || got[3] != StatusDone {
		t.Fatalf("statuses = %+v, want [running failed running done]", got)
	}
}

func TestRunnerLoopContinuesAfterRunDueError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &fakeSchedulerStore{}
	store.listDueFunc = func(now time.Time, limit int) ([]Job, error) {
		store.listDueCalls++
		if store.listDueCalls == 1 {
			return nil, errFakeSchedulerStoreFailure
		}
		cancel()
		return nil, nil
	}

	runner := NewRunner(NewService(store), fakeSchedulerHandler(func(job Job, triggeredAt time.Time) error {
		return nil
	}))

	done := make(chan error, 1)
	go func() {
		done <- runner.Loop(ctx, time.Millisecond, 10)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Loop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Loop did not stop after context cancellation")
	}

	if store.listDueCalls < 2 {
		t.Fatalf("ListDueJobs calls = %d, want at least 2", store.listDueCalls)
	}
}

func TestRunnerRecoverDeadLettersAndNotifies(t *testing.T) {
	t.Parallel()

	store := &fakeSchedulerStore{reclaimResult: [2]int{2, 1}}
	runner := NewRunner(NewService(store), fakeSchedulerHandler(func(Job, time.Time) error { return nil }))
	var notified []error
	runner.SetLoopErrorNotifier(func(err error) { notified = append(notified, err) })

	reclaimed, deadLettered, err := runner.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if reclaimed != 2 || deadLettered != 1 {
		t.Fatalf("reclaimed=%d deadLettered=%d, want 2 1", reclaimed, deadLettered)
	}
	if store.reclaimCalls != 1 {
		t.Fatalf("reclaimCalls=%d, want 1", store.reclaimCalls)
	}
	if store.reclaimMax != DefaultMaxJobAttempts {
		t.Fatalf("reclaimMax=%d, want %d", store.reclaimMax, DefaultMaxJobAttempts)
	}
	if len(notified) != 1 {
		t.Fatalf("notified=%d, want 1 (dead-letter surfaced)", len(notified))
	}
}

func TestRunnerRecoverQuietWhenNothingStuck(t *testing.T) {
	t.Parallel()

	store := &fakeSchedulerStore{reclaimResult: [2]int{0, 0}}
	runner := NewRunner(NewService(store), fakeSchedulerHandler(func(Job, time.Time) error { return nil }))
	var notified []error
	runner.SetLoopErrorNotifier(func(err error) { notified = append(notified, err) })

	reclaimed, deadLettered, err := runner.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if reclaimed != 0 || deadLettered != 0 {
		t.Fatalf("reclaimed=%d deadLettered=%d, want 0 0", reclaimed, deadLettered)
	}
	if len(notified) != 0 {
		t.Fatalf("notified=%d, want 0 when nothing was stuck", len(notified))
	}
}

type fakeSchedulerStore struct {
	dueJobs       []Job
	statuses      []string
	completed     []string
	failed        []string
	rescheduled   []fakeReschedule
	listDueFunc   func(now time.Time, limit int) ([]Job, error)
	listDueCalls  int
	reclaimResult [2]int
	reclaimErr    error
	reclaimCalls  int
	reclaimMax    int
}

type fakeReschedule struct {
	id    string
	runAt time.Time
}

func (f *fakeSchedulerStore) CreateJob(job Job) error {
	return nil
}

func (f *fakeSchedulerStore) ListJobs(ref conversation.Ref, limit int) ([]Job, error) {
	return nil, nil
}

func (f *fakeSchedulerStore) ListActiveJobs(ref conversation.Ref, limit int) ([]Job, error) {
	return nil, nil
}

func (f *fakeSchedulerStore) ListDueJobs(now time.Time, limit int) ([]Job, error) {
	if f.listDueFunc != nil {
		return f.listDueFunc(now, limit)
	}
	return f.dueJobs, nil
}

func (f *fakeSchedulerStore) UpdateJobStatus(id, status string, updatedAt time.Time) error {
	if status == StatusDone {
		f.completed = append(f.completed, id)
	}
	if status == StatusFailed {
		f.failed = append(f.failed, id)
	}
	f.statuses = append(f.statuses, status)
	return nil
}

func (f *fakeSchedulerStore) RescheduleJob(id string, runAt, updatedAt time.Time) error {
	f.rescheduled = append(f.rescheduled, fakeReschedule{id: id, runAt: runAt})
	return nil
}

func (f *fakeSchedulerStore) ReclaimRunningJobs(now time.Time, maxAttempts int) (int, int, error) {
	f.reclaimCalls++
	f.reclaimMax = maxAttempts
	if f.reclaimErr != nil {
		return 0, 0, f.reclaimErr
	}
	return f.reclaimResult[0], f.reclaimResult[1], nil
}

type fakeSchedulerHandler func(job Job, triggeredAt time.Time) error

var errFakeSchedulerFailure = errors.New("scheduler handler failed")
var errFakeSchedulerStoreFailure = errors.New("scheduler store failed")

func (f fakeSchedulerHandler) Handle(job Job, triggeredAt time.Time) error {
	return f(job, triggeredAt)
}
