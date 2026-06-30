// Package observability provides a small, process-wide recorder for failures
// and notable events so that errors which would otherwise be swallowed become
// visible through the admin API. It is intentionally dependency-free (stdlib
// only) and safe to call from any goroutine, including a nil receiver.
package observability

import (
	"strings"
	"sync"
	"time"
)

const (
	SeverityError = "error"
	SeverityWarn  = "warn"
	SeverityInfo  = "info"
)

// Event is a single recorded failure or notable occurrence.
type Event struct {
	Time           time.Time `json:"time"`
	Severity       string    `json:"severity"`
	Category       string    `json:"category"`
	Provider       string    `json:"provider,omitempty"`
	ConversationID string    `json:"conversationId,omitempty"`
	Summary        string    `json:"summary"`
	Detail         string    `json:"detail,omitempty"`
}

// Snapshot is a point-in-time, JSON-serializable view of the recorder.
type Snapshot struct {
	StartedAt time.Time        `json:"startedAt"`
	Now       time.Time        `json:"now"`
	Counters  map[string]int64 `json:"counters"`
	Events    []Event          `json:"events"`
}

// Recorder keeps a capped ring buffer of recent events plus monotonic counters
// keyed by "<category>/<severity>".
type Recorder struct {
	mu        sync.Mutex
	capacity  int
	startedAt time.Time
	events    []Event
	counters  map[string]int64
}

func NewRecorder(capacity int) *Recorder {
	if capacity <= 0 {
		capacity = 200
	}
	return &Recorder{
		capacity:  capacity,
		startedAt: time.Now().UTC(),
		events:    make([]Event, 0, capacity),
		counters:  map[string]int64{},
	}
}

// Record stores an event, applying defaults for time/severity/category and
// trimming the oldest entries once capacity is exceeded.
func (r *Recorder) Record(event Event) {
	if r == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	event.Severity = strings.TrimSpace(event.Severity)
	if event.Severity == "" {
		event.Severity = SeverityError
	}
	event.Category = strings.TrimSpace(event.Category)
	if event.Category == "" {
		event.Category = "general"
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[event.Category+"/"+event.Severity]++
	r.events = append(r.events, event)
	if len(r.events) > r.capacity {
		// Left-shift in place to drop the oldest events; safe because the
		// destination starts before the source.
		r.events = append(r.events[:0], r.events[len(r.events)-r.capacity:]...)
	}
}

// RecordError is a convenience wrapper for the common error case.
func (r *Recorder) RecordError(category, provider, conversationID, summary string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	r.Record(Event{
		Severity:       SeverityError,
		Category:       category,
		Provider:       provider,
		ConversationID: conversationID,
		Summary:        summary,
		Detail:         detail,
	})
}

// Snapshot returns a copy of the counters and the recent events, newest first.
func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{Counters: map[string]int64{}, Events: []Event{}, Now: time.Now().UTC()}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	counters := make(map[string]int64, len(r.counters))
	for key, value := range r.counters {
		counters[key] = value
	}
	events := make([]Event, len(r.events))
	for i := range r.events {
		events[i] = r.events[len(r.events)-1-i]
	}
	return Snapshot{
		StartedAt: r.startedAt,
		Now:       time.Now().UTC(),
		Counters:  counters,
		Events:    events,
	}
}

// Reset drops all recorded events and counters. The start time is preserved so
// that daemon uptime keeps reflecting the original process start.
func (r *Recorder) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = r.events[:0]
	r.counters = map[string]int64{}
}

// Default is the process-wide recorder used by the package-level helpers.
var Default = NewRecorder(500)

func Record(event Event) { Default.Record(event) }

func RecordError(category, provider, conversationID, summary string, err error) {
	Default.RecordError(category, provider, conversationID, summary, err)
}

func SnapshotDefault() Snapshot { return Default.Snapshot() }

func ResetDefault() { Default.Reset() }
