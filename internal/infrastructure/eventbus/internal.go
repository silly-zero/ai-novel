package eventbus

import (
	"context"
	"log"
	"sync"

	"github.com/ai-novel/studio/internal/domain/events"
)

// InternalEventBus 基于内存 Channel 的轻量级异步事件总线实现
type InternalEventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]events.Handler
}

// NewInternalEventBus 构造函数
func NewInternalEventBus() *InternalEventBus {
	return &InternalEventBus{
		subscribers: make(map[string][]events.Handler),
	}
}

// Subscribe 订阅某个主题的事件
func (b *InternalEventBus) Subscribe(topic string, handler events.Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[topic] = append(b.subscribers[topic], handler)
}

// Publish 异步发布一个事件
func (b *InternalEventBus) Publish(ctx context.Context, event events.Event) error {
	b.mu.RLock()
	handlers, ok := b.subscribers[event.Topic()]
	b.mu.RUnlock()

	if !ok {
		return nil
	}

	// 异步并发执行所有 Handler，不阻塞主流程
	for _, handler := range handlers {
		go func(h events.Handler) {
			if err := h(ctx, event); err != nil {
				log.Printf("[EventBus] 处理事件主题 %s 失败: %v", event.Topic(), err)
			}
		}(handler)
	}

	return nil
}
