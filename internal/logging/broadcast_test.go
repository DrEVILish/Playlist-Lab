package logging

import (
	"testing"
	"time"
)

func TestBroadcaster_PublishesToAllSubscribers(t *testing.T) {
	b := newBroadcaster()
	ch1, unsub1 := b.subscribe()
	defer unsub1()
	ch2, unsub2 := b.subscribe()
	defer unsub2()

	if _, err := b.Write([]byte(`{"msg":"hello"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	for i, ch := range []<-chan string{ch1, ch2} {
		select {
		case line := <-ch:
			if line != `{"msg":"hello"}` {
				t.Errorf("subscriber %d got %q, want the line with its trailing newline trimmed", i, line)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d never received the published line", i)
		}
	}
}

func TestBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := newBroadcaster()
	ch, unsub := b.subscribe()
	unsub()

	if _, err := b.Write([]byte("line\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The channel must be closed, not just quietly empty, so a range loop
	// over it (as the SSE handler's select would effectively do) doesn't
	// hang forever waiting on an unsubscribed channel.
	if _, ok := <-ch; ok {
		t.Fatal("expected the channel to be closed after unsubscribe")
	}
}

func TestBroadcaster_FullSubscriberBufferDropsRatherThanBlocks(t *testing.T) {
	b := newBroadcaster()
	ch, unsub := b.subscribe()
	defer unsub()

	// Fill the subscriber's buffer (capacity 64, see subscribe) without
	// ever reading it, then write one more - Write must return promptly
	// rather than blocking on a full channel, since a stuck viewer must
	// never be able to stall the application's own logging.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_, _ = b.Write([]byte("line\n"))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked on a full subscriber buffer instead of dropping")
	}
	// Drain what did make it through, just to be a well-behaved subscriber.
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestBroadcaster_IgnoresBlankLines(t *testing.T) {
	b := newBroadcaster()
	ch, unsub := b.subscribe()
	defer unsub()

	if _, err := b.Write([]byte("\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := b.Write([]byte("real line\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	select {
	case line := <-ch:
		if line != "real line" {
			t.Fatalf("got %q, want the blank write to have been skipped and this to be the first real line", line)
		}
	case <-time.After(time.Second):
		t.Fatal("never received the real line")
	}
}
