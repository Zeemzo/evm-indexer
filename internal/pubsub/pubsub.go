// Package pubsub is a small in-process event bus used to stream freshly
// indexed events to WebSocket clients. Each subscriber owns a buffered
// channel; publishes are non-blocking so one slow consumer can never
// back-pressure the indexing hot path — we drop for that consumer and
// count it instead.
package pubsub

import (
	"sync"
	"sync/atomic"
)

type Broker[T any] struct {
	mu      sync.RWMutex
	subs    map[uint64]chan T
	nextID  uint64
	bufSize int
	dropped atomic.Uint64
	closed  bool
}

func NewBroker[T any](bufSize int) *Broker[T] {
	if bufSize < 1 {
		bufSize = 64
	}
	return &Broker[T]{
		subs:    make(map[uint64]chan T),
		bufSize: bufSize,
	}
}

// Subscribe registers a new consumer. The returned cancel func must be
// called to release the subscription; the channel is closed by it.
func (b *Broker[T]) Subscribe() (<-chan T, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	ch := make(chan T, b.bufSize)
	if b.closed {
		close(ch)
		return ch, func() {}
	}
	b.subs[id] = ch

	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if sub, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(sub)
		}
	}
	return ch, cancel
}

// Publish delivers msg to every subscriber that has buffer space.
// It never blocks.
func (b *Broker[T]) Publish(msg T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- msg:
		default:
			b.dropped.Add(1)
		}
	}
}

// SubscriberCount returns the number of active subscribers.
func (b *Broker[T]) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Dropped returns the total number of messages dropped due to slow
// consumers since startup.
func (b *Broker[T]) Dropped() uint64 {
	return b.dropped.Load()
}

// Close shuts the broker down and closes all subscriber channels.
func (b *Broker[T]) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
