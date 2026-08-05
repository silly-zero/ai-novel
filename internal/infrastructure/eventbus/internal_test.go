package eventbus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-novel/studio/internal/domain/events"
)

type testEvent struct {
	topic string
}

func (e testEvent) Topic() string         { return e.topic }
func (e testEvent) OccurredAt() time.Time { return time.Unix(1, 0) }

func TestPublishWaitsForAllHandlers(t *testing.T) {
	bus := NewInternalEventBus()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	for i := 0; i < 2; i++ {
		bus.Subscribe("test", func(context.Context, events.Event) error {
			started <- struct{}{}
			<-release
			return nil
		})
	}

	result := make(chan error, 1)
	go func() {
		result <- bus.Publish(context.Background(), testEvent{topic: "test"})
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("handler did not start")
		}
	}

	select {
	case err := <-result:
		t.Fatalf("Publish returned before handlers completed: %v", err)
	default:
	}
	close(release)

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Publish returned unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Publish did not wait for handlers")
	}
}

func TestPublishRunsHandlersConcurrently(t *testing.T) {
	bus := NewInternalEventBus()
	var mu sync.Mutex
	active := 0
	maxActive := 0
	release := make(chan struct{})
	for i := 0; i < 2; i++ {
		bus.Subscribe("test", func(context.Context, events.Event) error {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			<-release
			mu.Lock()
			active--
			mu.Unlock()
			return nil
		})
	}

	result := make(chan error, 1)
	go func() { result <- bus.Publish(context.Background(), testEvent{topic: "test"}) }()
	deadline := time.After(time.Second)
	for {
		mu.Lock()
		concurrent := maxActive == 2
		mu.Unlock()
		if concurrent {
			break
		}
		select {
		case <-deadline:
			t.Fatal("handlers did not run concurrently")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("Publish returned unexpected error: %v", err)
	}
}

func TestPublishIsolatesHandlerFailuresAndPanics(t *testing.T) {
	bus := NewInternalEventBus()
	completed := make(chan struct{}, 3)
	bus.Subscribe("test", func(context.Context, events.Event) error {
		completed <- struct{}{}
		return errors.New("handler failed")
	})
	bus.Subscribe("test", func(context.Context, events.Event) error {
		completed <- struct{}{}
		panic("handler panic")
	})
	bus.Subscribe("test", func(context.Context, events.Event) error {
		completed <- struct{}{}
		return nil
	})

	err := bus.Publish(context.Background(), testEvent{topic: "test"})
	if err == nil || !strings.Contains(err.Error(), "handler failed") ||
		!strings.Contains(err.Error(), "handler panic") {
		t.Fatalf("Publish error missing handler failures: %v", err)
	}
	if got := len(completed); got != 3 {
		t.Fatalf("completed handlers = %d, want 3", got)
	}
}

func TestPublishDoesNotPartiallyFanOutWhenContextCancelled(t *testing.T) {
	bus := NewInternalEventBus()
	called := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		bus.Subscribe("test", func(ctx context.Context, _ events.Event) error {
			if ctx.Err() == nil {
				t.Error("handler received a non-cancelled context")
			}
			called <- struct{}{}
			return ctx.Err()
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bus.Publish(ctx, testEvent{topic: "test"}); err == nil {
		t.Fatal("Publish returned nil for cancelled handlers")
	}
	if got := len(called); got != 2 {
		t.Fatalf("called handlers = %d, want 2", got)
	}
}
