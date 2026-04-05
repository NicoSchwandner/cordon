package audit

import (
	"sync"

	"github.com/NicoSchwandner/cordon/internal/domain"
)

// Broadcaster fans out audit entries to all connected subscribers.
type Broadcaster struct {
	mu   sync.RWMutex
	subs map[chan domain.AuditEntry]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subs: make(map[chan domain.AuditEntry]struct{}),
	}
}

// Subscribe returns a channel that receives audit entries. The caller must
// call Unsubscribe when done to avoid leaking goroutines.
func (b *Broadcaster) Subscribe() chan domain.AuditEntry {
	ch := make(chan domain.AuditEntry, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Broadcaster) Unsubscribe(ch chan domain.AuditEntry) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	close(ch)
}

// Publish sends an audit entry to all subscribers. Non-blocking — drops
// entries for slow consumers rather than blocking the pipeline.
func (b *Broadcaster) Publish(entry domain.AuditEntry) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- entry:
		default:
			// slow consumer, drop entry
		}
	}
}
