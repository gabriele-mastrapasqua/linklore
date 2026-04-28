package events

import (
	"sync"
	"testing"
	"time"
)

func TestSubscribeReceives(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	defer cancel()
	if b.SubscriberCount() != 1 {
		t.Errorf("subs = %d", b.SubscriberCount())
	}
	go b.Publish(Event{Kind: KindLinkUpdated, LinkID: 7, CollectionID: 3})

	select {
	case ev := <-ch:
		if ev.Kind != KindLinkUpdated || ev.LinkID != 7 || ev.CollectionID != 3 {
			t.Errorf("got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event delivered")
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	cancel()
	// channel must be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel not closed")
		}
	default:
		t.Fatal("expected closed channel to read immediately")
	}
	if b.SubscriberCount() != 0 {
		t.Errorf("subs after cancel = %d", b.SubscriberCount())
	}
}

func TestPublishDropsToSlowSubscriber(t *testing.T) {
	b := New()
	_, cancel := b.Subscribe() // never read
	defer cancel()
	// Buffer is 32; publishing far more must not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Publish(Event{Kind: KindStatsChanged, CollectionID: int64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
}

func TestNilBrokerPublishIsNoOp(t *testing.T) {
	var b *Broker
	// Should not panic.
	b.Publish(Event{Kind: KindLinkUpdated})
}

func TestConcurrentSubscribersAllReceive(t *testing.T) {
	b := New()
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	cancels := make([]func(), n)
	for i := 0; i < n; i++ {
		ch, cancel := b.Subscribe()
		cancels[i] = cancel
		go func() {
			defer wg.Done()
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Errorf("subscriber missed event")
			}
		}()
	}
	b.Publish(Event{Kind: KindLinkUpdated, LinkID: 42})
	wg.Wait()
	for _, c := range cancels {
		c()
	}
}
