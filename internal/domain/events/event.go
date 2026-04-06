package events

import (
	"context"
	"time"
)

// Event 是所有领域事件的通用接口
type Event interface {
	Topic() string      // 事件主题
	OccurredAt() time.Time // 发生时间
}

// Handler 定义了如何处理某种类型的事件
type Handler func(ctx context.Context, event Event) error

// Bus 领域事件总线接口 (Repository/Port)
type Bus interface {
	// Publish 发布一个事件
	Publish(ctx context.Context, event Event) error
	
	// Subscribe 订阅某个主题的事件
	Subscribe(topic string, handler Handler)
}

// ChapterGeneratedEvent 章节生成成功的领域事件
type ChapterGeneratedEvent struct {
	NovelID   string
	ChapterID string
	Content   string
	Timestamp time.Time
}

func (e ChapterGeneratedEvent) Topic() string      { return "chapter.generated" }
func (e ChapterGeneratedEvent) OccurredAt() time.Time { return e.Timestamp }
