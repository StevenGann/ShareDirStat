package events

import (
	"sync"
	"testing"
)

func drain(t *testing.T, sub *Subscription) []Event {
	t.Helper()
	var got []Event
	for {
		select {
		case ev, open := <-sub.C:
			if !open {
				return got
			}
			got = append(got, ev)
		default:
			return got
		}
	}
}

func TestPublishFansOutAndNumbersEvents(t *testing.T) {
	b := NewBroker()
	a, c := b.Subscribe(0), b.Subscribe(0)
	defer a.Close()
	defer c.Close()

	b.Publish(ScanStarted, "one")
	b.Publish(ScanCompleted, "two")

	for name, sub := range map[string]*Subscription{"a": a, "c": c} {
		got := drain(t, sub)
		if len(got) != 2 {
			t.Fatalf("%s: got %d events, want 2", name, len(got))
		}
		if got[0].ID != 1 || got[1].ID != 2 {
			t.Errorf("%s: ids %d,%d; want 1,2", name, got[0].ID, got[1].ID)
		}
		if got[0].Type != ScanStarted || got[1].Type != ScanCompleted {
			t.Errorf("%s: types %q,%q", name, got[0].Type, got[1].Type)
		}
	}
}

// A reconnecting client asks for everything after the last id it saw. The
// replay has to fit: the subscriber buffer is sized against ReplayDepth
// precisely so a full ring is delivered rather than silently truncated to the
// *oldest* events, which would drop the terminal scan.completed the client
// reconnected to find.
func TestSubscribeReplaysEverythingAfterLastID(t *testing.T) {
	b := NewBroker()
	for i := 0; i < ReplayDepth; i++ {
		b.Publish(ScanProgress, i)
	}
	sub := b.Subscribe(1)
	defer sub.Close()

	got := drain(t, sub)
	if len(got) != ReplayDepth-1 {
		t.Fatalf("replayed %d events, want %d", len(got), ReplayDepth-1)
	}
	for i, ev := range got {
		if want := uint64(i + 2); ev.ID != want {
			t.Fatalf("event %d has id %d, want %d (replay must be ordered and gapless)", i, ev.ID, want)
		}
	}
}

// A subscriber that stops reading must be dropped, not allowed to stall the
// publisher: a single backgrounded browser tab would otherwise freeze a scan.
func TestSlowSubscriberIsDroppedNotStalling(t *testing.T) {
	b := NewBroker()
	slow := b.Subscribe(0)
	fast := b.Subscribe(0)
	defer fast.Close()

	for i := 0; i < SubscriberBuffer+10; i++ {
		b.Publish(ScanProgress, i)
		// Keep the fast subscriber drained so only the slow one falls behind.
		select {
		case <-fast.C:
		default:
		}
	}

	if b.Subscribers() != 1 {
		t.Errorf("subscribers = %d, want 1 (the slow one should be gone)", b.Subscribers())
	}
	// Its channel is closed, which is how the SSE handler learns to tell the
	// client to reconnect.
	for range slow.C { //nolint:revive // draining until closed is the assertion
	}
	// Closing an already-dropped subscription must not panic on a closed chan.
	slow.Close()
}

func TestConcurrentPublishSubscribeAndClose(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				b.Publish(ScanProgress, j)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sub := b.Subscribe(0)
				go func() {
					for range sub.C { //nolint:revive // consume until closed
					}
				}()
				sub.Close()
			}
		}()
	}
	wg.Wait()
	if b.Subscribers() != 0 {
		t.Errorf("leaked %d subscribers", b.Subscribers())
	}
}
