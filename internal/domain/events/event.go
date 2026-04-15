package events

import (
	"context"
	"time"
)

// Event 是所有领域事件的通用接口
type Event interface {
	Topic() string         // 事件主题
	OccurredAt() time.Time // 发生时间
}

// Handler 定义了如何处理某种类型的事件
type Handler func(ctx context.Context, event Event) error

// Bus 领域事件总线接口 (Repository/Port)
type Bus interface {
	// Publish 发布一个事件
	Publish(ctx context.Context, event Event) error

	// Subscribe 订阅某个主题的事件，返回订阅 ID 用于取消订阅
	Subscribe(topic string, handler Handler) string

	// Unsubscribe 取消订阅
	Unsubscribe(topic string, id string)
}

// ChapterGeneratedEvent 章节生成成功的领域事件
type ChapterGeneratedEvent struct {
	NovelID   string
	ChapterID string
	Content   string
	Timestamp time.Time
}

func (e ChapterGeneratedEvent) Topic() string         { return "chapter.generated" }
func (e ChapterGeneratedEvent) OccurredAt() time.Time { return e.Timestamp }

// TokenGeneratedEvent 实时生成中的 Token 事件 (用于流式展示)
type TokenGeneratedEvent struct {
	NovelID   string
	ChapterID string
	Token     string
	Timestamp time.Time
}

func (e TokenGeneratedEvent) Topic() string         { return "token.generated" }
func (e TokenGeneratedEvent) OccurredAt() time.Time { return e.Timestamp }

// ChapterRetryEvent 章节被审查打回后进入重写轮次
type ChapterRetryEvent struct {
	NovelID    string
	ChapterID  string
	RetryCount int
	Critique   string
	Timestamp  time.Time
}

func (e ChapterRetryEvent) Topic() string         { return "chapter.retry" }
func (e ChapterRetryEvent) OccurredAt() time.Time { return e.Timestamp }
