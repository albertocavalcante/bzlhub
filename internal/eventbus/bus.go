// Package eventbus is a small in-process pub/sub for canopy events.
//
// The bus is intentionally minimal: a fanout dispatcher with per-subscriber
// buffered channels. Publishers never block — a slow subscriber drops
// events past its buffer, surfaced via a per-subscriber drop counter.
// Cross-process distribution (e.g., NATS, Redis) is a future concern;
// canopy v0 runs as a single binary so a local bus is enough.
package eventbus

import (
	"sync"
)

// Event is one published item. Kind groups events by semantic category
// (e.g., "module_indexed"); Data is a kind-specific payload typed at
// the publisher and asserted at the subscriber. Keeping Data as `any`
// avoids forcing a registry of event types into this package — the
// canopy/server package owns the schema.
type Event struct {
	Kind string
	Data any
}

// Bus is a fanout pub/sub. The zero value is NOT usable; call New.
type Bus struct {
	mu      sync.Mutex
	subs    map[*subscriber]struct{}
	closed  bool
}

// New returns an empty Bus.
func New() *Bus {
	return &Bus{subs: make(map[*subscriber]struct{})}
}

// Publish sends e to every current subscriber. A subscriber whose channel
// buffer is full receives nothing for this event; its Dropped counter
// increments. Publish never blocks and returns immediately.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	// Snapshot subscribers so we don't hold the mutex while writing to
	// channels — a slow non-blocking send is still cheap, but we want
	// strict non-locking once the snapshot is taken.
	targets := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		targets = append(targets, s)
	}
	b.mu.Unlock()

	for _, s := range targets {
		select {
		case s.ch <- e:
		default:
			s.dropped.add(1)
		}
	}
}

// Subscribe returns a buffered channel of events plus an unsubscribe
// function. bufferSize controls the per-subscriber buffer; 0 → 64.
//
// The returned channel is closed when the caller invokes the
// unsubscribe function. Callers should always defer unsubscribe.
func (b *Bus) Subscribe(bufferSize int) (<-chan Event, func()) {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	s := &subscriber{ch: make(chan Event, bufferSize)}

	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		if _, still := b.subs[s]; still {
			delete(b.subs, s)
			close(s.ch)
		}
		b.mu.Unlock()
	}
	return s.ch, unsubscribe
}

// Close marks the bus closed; future Publish calls are no-ops. Existing
// subscribers see their channels closed.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for s := range b.subs {
		close(s.ch)
	}
	b.subs = nil
}

// subscriber holds one subscription's state.
type subscriber struct {
	ch      chan Event
	dropped atomicUint64
}

// atomicUint64 is a tiny stand-in for sync/atomic.Uint64 that avoids
// pulling in atomic alignment concerns on 32-bit GOARCHes. The dropped
// counter is best-effort (reads are also unsynchronized; that's
// acceptable — it's a debug hint, not load-bearing).
type atomicUint64 struct {
	mu sync.Mutex
	n  uint64
}

func (a *atomicUint64) add(delta uint64) {
	a.mu.Lock()
	a.n += delta
	a.mu.Unlock()
}
