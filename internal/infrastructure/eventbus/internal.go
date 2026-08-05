package eventbus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ai-novel/studio/internal/domain/events"
)

type subscription struct {
	id      string
	handler events.Handler
}

type InternalEventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]subscription
	counter     uint64
}

// NewInternalEventBus 构造函数
func NewInternalEventBus() *InternalEventBus {
	return &InternalEventBus{
		subscribers: make(map[string][]subscription),
	}
}

// Subscribe 订阅某个主题的事件，返回订阅 ID
func (b *InternalEventBus) Subscribe(topic string, handler events.Handler) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := fmt.Sprintf("sub_%d", atomic.AddUint64(&b.counter, 1))
	b.subscribers[topic] = append(b.subscribers[topic], subscription{
		id:      id,
		handler: handler,
	})
	return id
}

// Unsubscribe 取消订阅
func (b *InternalEventBus) Unsubscribe(topic string, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs, ok := b.subscribers[topic]
	if !ok {
		return
	}

	newSubs := make([]subscription, 0, len(subs))
	for _, sub := range subs {
		if sub.id != id {
			newSubs = append(newSubs, sub)
		}
	}
	b.subscribers[topic] = newSubs
}

// Publish 并行调用订阅者，并在全部处理完成后返回聚合错误。
func (b *InternalEventBus) Publish(ctx context.Context, event events.Event) error {
	b.mu.RLock()
	subs, ok := b.subscribers[event.Topic()]
	b.mu.RUnlock()

	if !ok {
		return nil
	}

	subs = append([]subscription(nil), subs...)
	errorsBySubscriber := make([]error, len(subs))
	var wg sync.WaitGroup
	for i, sub := range subs {
		if sub.handler == nil {
			continue
		}

		wg.Add(1)
		go func(index int, subscription subscription) {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					errorsBySubscriber[index] = fmt.Errorf(
						"subscriber %s handling %s panicked: %v",
						subscription.id,
						event.Topic(),
						recovered,
					)
				}
			}()

			if err := subscription.handler(ctx, event); err != nil {
				errorsBySubscriber[index] = fmt.Errorf(
					"subscriber %s handling %s: %w",
					subscription.id,
					event.Topic(),
					err,
				)
			}
		}(i, sub)
	}
	wg.Wait()

	return errors.Join(errorsBySubscriber...)
}
