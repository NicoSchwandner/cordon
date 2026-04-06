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

// workspace holds subscribers and event history for a single creation.
type workspace struct {
	mu          sync.Mutex
	subscribers map[int]chan Event
	nextID      int
	history     []Event
	closed      bool
}

func newWorkspace() *workspace {
	return &workspace{subscribers: make(map[int]chan Event)}
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

// subscribe creates a new per-subscriber channel. Returns channel and an ID for unsubscribing.
func (w *workspace) subscribe() (<-chan Event, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.nextID
	w.nextID++
	ch := make(chan Event, 20)
	w.subscribers[id] = ch
	return ch, id
}

func (w *workspace) unsubscribe(id int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.subscribers, id)
}

// broadcast sends an event to all subscribers. Non-blocking per subscriber.
func (w *workspace) broadcast(evt Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range w.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

// closeAll closes all subscriber channels.
func (w *workspace) closeAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	for id, ch := range w.subscribers {
		close(ch)
		delete(w.subscribers, id)
	}
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

// Create registers a workspace for progress tracking.
func (s *Store) Create(id uuid.UUID) {
	ws := newWorkspace()
	s.workspaces.Store(id, ws)
}

// Subscribe returns the event history and a dedicated channel for this subscriber.
// Each caller gets its own channel so multiple SSE clients all receive every event.
// The returned unsubscribe function must be called when the subscriber disconnects.
func (s *Store) Subscribe(id uuid.UUID) ([]Event, <-chan Event, func(), bool) {
	v, ok := s.workspaces.Load(id)
	if !ok {
		return nil, nil, nil, false
	}
	ws := v.(*workspace)
	history := ws.snapshot()
	ch, subID := ws.subscribe()
	unsub := func() { ws.unsubscribe(subID) }
	return history, ch, unsub, true
}

// Send emits a progress event to all subscribers. Non-blocking per subscriber.
func (s *Store) Send(id uuid.UUID, step, message string) {
	v, ok := s.workspaces.Load(id)
	if !ok {
		return
	}
	ws := v.(*workspace)
	evt := Event{Step: step, Message: message}
	ws.append(evt)
	ws.broadcast(evt)
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
	ws.broadcast(evt)
}

// Complete sends a final event and closes all subscriber channels.
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
	ws.broadcast(evt)
	ws.closeAll()
}

// CreateIfAbsent registers a workspace for progress tracking only if not already tracked.
func (s *Store) CreateIfAbsent(id uuid.UUID) {
	if _, loaded := s.workspaces.LoadOrStore(id, newWorkspace()); loaded {
		return
	}
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
