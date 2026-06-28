package observability

import (
	"errors"
	"testing"
)

func TestRecorderSnapshotNewestFirstAndCounters(t *testing.T) {
	t.Parallel()

	rec := NewRecorder(10)
	rec.RecordError("scheduler", "feishu", "chat-1", "job failed", errors.New("boom"))
	rec.Record(Event{Severity: SeverityWarn, Category: "scheduler", Summary: "recovered"})

	snap := rec.Snapshot()
	if len(snap.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(snap.Events))
	}
	if snap.Events[0].Summary != "recovered" {
		t.Fatalf("newest event = %q, want recovered (newest first)", snap.Events[0].Summary)
	}
	if snap.Counters["scheduler/error"] != 1 || snap.Counters["scheduler/warn"] != 1 {
		t.Fatalf("counters = %+v, want one error and one warn", snap.Counters)
	}
	if snap.Events[1].Detail != "boom" {
		t.Fatalf("error detail = %q, want boom", snap.Events[1].Detail)
	}
}

func TestRecorderCapsRingBuffer(t *testing.T) {
	t.Parallel()

	rec := NewRecorder(3)
	for i := 0; i < 6; i++ {
		rec.Record(Event{Category: "test", Summary: string(rune('a' + i))})
	}
	snap := rec.Snapshot()
	if len(snap.Events) != 3 {
		t.Fatalf("events = %d, want 3 (capped)", len(snap.Events))
	}
	// Newest first: last recorded was 'f'.
	if snap.Events[0].Summary != "f" || snap.Events[2].Summary != "d" {
		t.Fatalf("ring window = [%s..%s], want [f..d]", snap.Events[0].Summary, snap.Events[2].Summary)
	}
	if snap.Counters["test/error"] != 6 {
		t.Fatalf("counter = %d, want 6 (counters are monotonic, not capped)", snap.Counters["test/error"])
	}
}

func TestRecorderNilSafe(t *testing.T) {
	t.Parallel()

	var rec *Recorder
	rec.Record(Event{Summary: "noop"}) // must not panic
	snap := rec.Snapshot()
	if len(snap.Events) != 0 {
		t.Fatalf("nil recorder events = %d, want 0", len(snap.Events))
	}
}
