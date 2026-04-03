package progress

import (
	"sync"

	"github.com/google/uuid"
)

// Event represents a single progress update during workspace creation.
type Event struct {
	Step          string `json:"step"`
	Message       string `json:"message"`
	Done          bool   `json:"done"`
	Error         string `json:"error,omitempty"`
	EstimatedSecs int    `json:"estimated_secs,omitempty"`
}

// Func is a callback for emitting progress updates.
type Func func(step, message string)

// Store tracks in-flight workspace creation progress via channels.
type Store struct {
	channels sync.Map // uuid.UUID -> chan Event
	timing   *TimingStore
}

// NewStore creates a progress store with an optional timing store for estimates.
func NewStore(timing *TimingStore) *Store {
	return &Store{timing: timing}
}

// Create registers a progress channel for a workspace. Returns the channel for the SSE handler.
func (s *Store) Create(id uuid.UUID) <-chan Event {
	ch := make(chan Event, 20)
	s.channels.Store(id, ch)
	return ch
}

// Subscribe returns the progress channel for a workspace, if one exists.
func (s *Store) Subscribe(id uuid.UUID) (<-chan Event, bool) {
	v, ok := s.channels.Load(id)
	if !ok {
		return nil, false
	}
	return v.(chan Event), true
}

// Send emits a progress event. Non-blocking; drops if buffer is full.
func (s *Store) Send(id uuid.UUID, step, message string) {
	v, ok := s.channels.Load(id)
	if !ok {
		return
	}
	ch := v.(chan Event)
	select {
	case ch <- Event{Step: step, Message: message}:
	default:
	}
}

// SendWithEstimate emits a progress event that includes the estimated total build time.
func (s *Store) SendWithEstimate(id uuid.UUID, repo, step, message string) {
	v, ok := s.channels.Load(id)
	if !ok {
		return
	}
	ch := v.(chan Event)
	evt := Event{Step: step, Message: message}
	if s.timing != nil {
		evt.EstimatedSecs = int(s.timing.Estimate(repo).Seconds())
	}
	select {
	case ch <- evt:
	default:
	}
}

// Complete sends a final event and closes the channel.
func (s *Store) Complete(id uuid.UUID, err error) {
	v, ok := s.channels.LoadAndDelete(id)
	if !ok {
		return
	}
	ch := v.(chan Event)
	evt := Event{Step: "done", Message: "Workspace ready!", Done: true}
	if err != nil {
		evt.Message = err.Error()
		evt.Error = err.Error()
	}
	select {
	case ch <- evt:
	default:
	}
	close(ch)
}

// CreateIfAbsent registers a progress channel only if one doesn't already exist.
// Used for activation progress on existing workspaces.
func (s *Store) CreateIfAbsent(id uuid.UUID) <-chan Event {
	if ch, ok := s.Subscribe(id); ok {
		return ch
	}
	return s.Create(id)
}

// Timing returns the underlying timing store, or nil.
func (s *Store) Timing() *TimingStore {
	return s.timing
}

// ProgressFunc returns a Func callback bound to a specific workspace ID.
func (s *Store) ProgressFunc(id uuid.UUID) Func {
	return func(step, message string) {
		s.Send(id, step, message)
	}
}
