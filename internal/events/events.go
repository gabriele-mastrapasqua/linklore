// Package events implements a tiny in-process pub/sub used to push
// pipeline progress to the UI over Server-Sent Events.
//
// Design choices, all driven by "this is a single-user local app":
//
//   - One broker per process. No cluster, no Redis, no NATS.
//   - Subscribers are per-connection channels with a small buffer; if
//     a subscriber falls behind we drop messages for THAT subscriber
//     instead of blocking the whole broker.
//   - Events are tiny: kind + entity ids. The client receives the
//     event then pulls the fresh fragment via a normal GET — that
//     keeps the broker decoupled from template rendering.
//
// Worker calls Publish; the HTTP handler exposes Subscribe over SSE.
package events

import (
	"sync"
)

// Kind enumerates the event types the UI cares about. New ones don't
// need a server change beyond a new constant — clients just match by
// name.
type Kind string

const (
	// KindLinkUpdated fires whenever a link's status / summary / tags
	// changed. Carries LinkID and CollectionID; the client refetches
	// the row + the collection stats.
	KindLinkUpdated Kind = "link_updated"
	// KindLinkRemoved fires on delete or move-out. Carries LinkID +
	// CollectionID (the OLD collection so the client can drop the row).
	KindLinkRemoved Kind = "link_removed"
	// KindStatsChanged fires when a per-collection counter moved.
	// Carries CollectionID.
	KindStatsChanged Kind = "stats_changed"
)

// Event is the payload broadcast to every subscriber.
type Event struct {
	Kind         Kind
	LinkID       int64
	CollectionID int64
}

// Broker fans events out to all current subscribers. Zero value is NOT
// usable — call New().
type Broker struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
}

// New returns a ready-to-use Broker.
func New() *Broker {
	return &Broker{subscribers: map[chan Event]struct{}{}}
}

// Subscribe returns a buffered channel that receives every published
// event from this point forward. The returned cancel func unsubscribes
// the channel and closes it; callers MUST call cancel exactly once
// (typically via defer) to avoid leaking a goroutine and a buffer.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subscribers[ch]; ok {
			delete(b.subscribers, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Publish broadcasts ev to every subscriber. A subscriber whose buffer
// is full silently drops this event — the UI repaints on the NEXT
// event anyway, so dropping a frame is far better than blocking the
// worker.
func (b *Broker) Publish(ev Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
			// drop — consumer too slow
		}
	}
}

// SubscriberCount is exposed for tests / health checks.
func (b *Broker) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}
