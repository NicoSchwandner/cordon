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

// workspace holds the live channel and event history for a single creation.
type workspace struct {
	mu      sync.Mutex
	ch      chan Event
	history []Event
}

func newWorkspace() *workspace {
	return &workspace{ch: make(chan Event, 20)}
}

func (w *workspace) append(evt Event) {
	w.mu.Lock()
	w.history = append(w.history, evt)
	w.mu.Unlock()
}

func (w *workspace) snapshot() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Event, len(w.history))
	copy(out, w.history)
	return out
}

// Store tracks in-flight workspace creation progress via channels.
type Store struct {
	workspaces sync.Map // uuid.UUID -> *workspace
	timing     *TimingStore
}

// NewStore creates a progress store with an optional timing store for estimates.
func NewStore(timing *TimingStore) *Store {
	return &Store{timing: timing}
}

// Create registers a progress channel for a workspace. Returns the channel for the SSE handler.
func (s *Store) Create(id uuid.UUID) <-chan Event {
	ws := newWorkspace()
	s.workspaces.Store(id, ws)
	return ws.ch
}

// Subscribe returns the event history and live channel for a workspace.
// History contains all events emitted so far; the channel delivers future events.
func (s *Store) Subscribe(id uuid.UUID) ([]Event, <-chan Event, bool) {
	v, ok := s.workspaces.Load(id)
	if !ok {
		return nil, nil, false
	}
	ws := v.(*workspace)
	return ws.snapshot(), ws.ch, true
}

// Send emits a progress event. Non-blocking; drops if buffer is full.
func (s *Store) Send(id uuid.UUID, step, message string) {
	v, ok := s.workspaces.Load(id)
	if !ok {
		return
	}
	ws := v.(*workspace)
	evt := Event{Step: step, Message: message}
	ws.append(evt)
	select {
	case ws.ch <- evt:
	default:
	}
}

// SendWithEstimate emits a progress event that includes the estimated total build time.
func (s *Store) SendWithEstimate(id uuid.UUID, repo, step, message string) {
	v, ok := s.workspaces.Load(id)
	if !ok {
		return
	}
	ws := v.(*workspace)
	evt := Event{Step: step, Message: message}
	if s.timing != nil {
		evt.EstimatedSecs = int(s.timing.Estimate(repo).Seconds())
	}
	ws.append(evt)
	select {
	case ws.ch <- evt:
	default:
	}
}

// Complete sends a final event and closes the channel.
func (s *Store) Complete(id uuid.UUID, err error) {
	v, ok := s.workspaces.LoadAndDelete(id)
	if !ok {
		return
	}
	ws := v.(*workspace)
	evt := Event{Step: "done", Message: "Workspace ready!", Done: true}
	if err != nil {
		evt.Message = err.Error()
		evt.Error = err.Error()
	}
	ws.append(evt)
	select {
	case ws.ch <- evt:
	default:
	}
	close(ws.ch)
}

// CreateIfAbsent registers a progress channel only if one doesn't already exist.
// Used for activation progress on existing workspaces.
func (s *Store) CreateIfAbsent(id uuid.UUID) <-chan Event {
	if _, _, ok := s.Subscribe(id); ok {
		v, _ := s.workspaces.Load(id)
		return v.(*workspace).ch
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
