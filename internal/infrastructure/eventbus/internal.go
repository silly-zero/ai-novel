package eventbus

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/ai-novel/studio/internal/domain/events"
)

type subscription struct {
	id      string
	handler events.Handler
}

type dispatchJob struct {
	ctx     context.Context
	event   events.Event
	handler events.Handler
}

// InternalEventBus 基于内存 Channel 的轻量级异步事件总线实现
type InternalEventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]subscription
	counter     uint64
	queue       chan dispatchJob
}

const (
	defaultQueueSize  = 4096
	defaultWorkerSize = 8
)

// NewInternalEventBus 构造函数
func NewInternalEventBus() *InternalEventBus {
	b := &InternalEventBus{
		subscribers: make(map[string][]subscription),
		queue:       make(chan dispatchJob, defaultQueueSize),
	}
	for i := 0; i < defaultWorkerSize; i++ {
		go b.worker()
	}
	return b
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

// Publish 异步发布一个事件
func (b *InternalEventBus) Publish(ctx context.Context, event events.Event) error {
	b.mu.RLock()
	subs, ok := b.subscribers[event.Topic()]
	b.mu.RUnlock()

	if !ok {
		return nil
	}

	subs = append([]subscription(nil), subs...)
	topic := event.Topic()
	for _, sub := range subs {
		if sub.handler == nil {
			continue
		}
		job := dispatchJob{ctx: ctx, event: event, handler: sub.handler}
		if topic == "token.generated" {
			select {
			case b.queue <- job:
			default:
			}
			continue
		}
		select {
		case b.queue <- job:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

func (b *InternalEventBus) worker() {
	for job := range b.queue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[EventBus] handler panic: %v", r)
				}
			}()
			if err := job.handler(job.ctx, job.event); err != nil {
				log.Printf("[EventBus] 处理事件主题 %s 失败: %v", job.event.Topic(), err)
			}
		}()
	}
}
