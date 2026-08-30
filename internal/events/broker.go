// Package events implements the Server-Sent Events fan-out used by the UI
// (specification §9.4). Publishers never block: a subscriber that cannot
// keep up is dropped, and the client reconnects with Last-Event-ID.
package events

import (
	"strconv"
	"sync"
)

// Type names the kind of an event.
type Type string

// Event types (§9.4).
const (
	ScanStarted   Type = "scan.started"
	ScanProgress  Type = "scan.progress"
	ScanCompleted Type = "scan.completed"
	ScanCancelled Type = "scan.cancelled"
	ScanFailed    Type = "scan.failed"
	ScanPaused    Type = "scan.paused"
	ScanResumed   Type = "scan.resumed"
	ShareState    Type = "share.state"
	NodeDeleted   Type = "node.deleted"
)

// Event is one message on the stream.
type Event struct {
	ID   uint64 `json:"id"`
	Type Type   `json:"type"`
	Data any    `json:"data"`
}

// IDString renders the event id for the SSE id: field.
func (e Event) IDString() string { return strconv.FormatUint(e.ID, 10) }

// Publisher is the subset of the broker that producers need.
type Publisher interface {
	Publish(t Type, data any)
}

// ReplayDepth is the number of recent events retained for Last-Event-ID
// reconnection (§9.4).
const ReplayDepth = 100

// SubscriberBuffer is the per-subscriber queue depth. Progress events are
// frequent; a slow client is dropped rather than stalling a scan. It is at
// least ReplayDepth so that a full replay always fits: a smaller buffer
// silently discards the *newest* replayed events (the ring is replayed
// oldest-first), which is exactly the terminal scan.completed a reconnecting
// client needs, and leaves the new subscription starting out already full.
const SubscriberBuffer = ReplayDepth

type subscriber struct {
	ch      chan Event
	dropped bool
}

// Broker fans events out to SSE subscribers and keeps a replay ring.
type Broker struct {
	mu     sync.Mutex
	nextID uint64
	subs   map[*subscriber]struct{}
	ring   []Event
}

// NewBroker creates an empty broker.
func NewBroker() *Broker {
	return &Broker{subs: make(map[*subscriber]struct{}), ring: make([]Event, 0, ReplayDepth)}
}

// Publish delivers an event to every subscriber. It never blocks.
func (b *Broker) Publish(t Type, data any) {
	b.mu.Lock()
	b.nextID++
	ev := Event{ID: b.nextID, Type: t, Data: data}
	if len(b.ring) == ReplayDepth {
		copy(b.ring, b.ring[1:])
		b.ring[len(b.ring)-1] = ev
	} else {
		b.ring = append(b.ring, ev)
	}
	for s := range b.subs {
		select {
		case s.ch <- ev:
		default:
			// Subscriber is not keeping up: drop it and let the browser
			// reconnect with Last-Event-ID rather than stall the producer.
			s.dropped = true
			close(s.ch)
			delete(b.subs, s)
		}
	}
	b.mu.Unlock()
}

// Subscription is a live event stream.
type Subscription struct {
	C      <-chan Event
	broker *Broker
	sub    *subscriber
}

// Subscribe registers a new subscriber. When lastID is non-zero, events
// still held in the replay ring that are newer than lastID are delivered
// first.
func (b *Broker) Subscribe(lastID uint64) *Subscription {
	s := &subscriber{ch: make(chan Event, SubscriberBuffer)}
	b.mu.Lock()
	defer b.mu.Unlock()
	if lastID > 0 {
		for _, ev := range b.ring {
			if ev.ID <= lastID {
				continue
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
	b.subs[s] = struct{}{}
	return &Subscription{C: s.ch, broker: b, sub: s}
}

// Close unregisters the subscription.
func (s *Subscription) Close() {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	if _, ok := s.broker.subs[s.sub]; ok {
		delete(s.broker.subs, s.sub)
		close(s.sub.ch)
	}
}

// Subscribers returns the current subscriber count (metrics and tests).
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
