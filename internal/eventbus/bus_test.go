package eventbus

import (
	"sync"
	"testing"
	"time"
)

func TestPublishFanout(t *testing.T) {
	b := New()
	defer b.Close()

	ch1, unsub1 := b.Subscribe(8)
	ch2, unsub2 := b.Subscribe(8)
	defer unsub1()
	defer unsub2()

	b.Publish(Event{Kind: "k1", Data: 42})
	b.Publish(Event{Kind: "k2", Data: "x"})

	for i, ch := range []<-chan Event{ch1, ch2} {
		for _, kind := range []string{"k1", "k2"} {
			select {
			case e := <-ch:
				if e.Kind != kind {
					t.Errorf("sub %d: kind %q want %q", i, e.Kind, kind)
				}
			case <-time.After(time.Second):
				t.Fatalf("sub %d timed out waiting for %s", i, kind)
			}
		}
	}
}

func TestUnsubscribeCloses(t *testing.T) {
	b := New()
	defer b.Close()
	ch, unsub := b.Subscribe(2)
	unsub()
	// Channel must be closed now.
	if _, ok := <-ch; ok {
		t.Fatalf("expected closed channel after unsubscribe")
	}
	// Second unsubscribe is a no-op (no panic).
	unsub()
	// New events don't reach the now-unsubscribed channel (nothing to
	// assert directly; just ensure no panic).
	b.Publish(Event{Kind: "ignored"})
}

func TestSlowSubscriberDropsRatherThanBlocks(t *testing.T) {
	b := New()
	defer b.Close()
	_, unsub := b.Subscribe(1) // tiny buffer
	defer unsub()

	// Publish more events than the buffer can hold. Without the
	// non-blocking send in Publish, this would deadlock the test.
	for i := 0; i < 100; i++ {
		b.Publish(Event{Kind: "k", Data: i})
	}
	// Test passes if we get here without timeout.
}

func TestCloseClosesSubscribers(t *testing.T) {
	b := New()
	ch, _ := b.Subscribe(2)
	b.Publish(Event{Kind: "k"})
	b.Close()
	// Drain — kind=k may or may not arrive depending on race; the
	// terminal must be a closed channel signal.
	for {
		_, ok := <-ch
		if !ok {
			return
		}
	}
}

func TestConcurrentPubSub(t *testing.T) {
	b := New()
	defer b.Close()
	const subs = 8
	const events = 200

	var got [subs]int
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < subs; i++ {
		i := i
		ch, unsub := b.Subscribe(events * 2)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer unsub()
			deadline := time.After(2 * time.Second)
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
					mu.Lock()
					got[i]++
					mu.Unlock()
				case <-deadline:
					return
				}
			}
		}()
	}

	// Give subscribers a moment to register before publishing.
	time.Sleep(10 * time.Millisecond)

	for j := 0; j < events; j++ {
		b.Publish(Event{Kind: "k", Data: j})
	}

	// Wait for at least one subscriber to receive all events, then check
	// the others.
	time.Sleep(200 * time.Millisecond)
	b.Close()
	wg.Wait()

	for i, n := range got {
		if n != events {
			t.Errorf("sub %d got %d/%d events", i, n, events)
		}
	}
}
