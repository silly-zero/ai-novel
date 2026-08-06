package workflows

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
)

type workflowEventBusFake struct {
	publish func(context.Context, events.Event) error
}

func (b *workflowEventBusFake) Publish(ctx context.Context, event events.Event) error {
	return b.publish(ctx, event)
}

func (b *workflowEventBusFake) Subscribe(string, events.Handler) string { return "" }
func (b *workflowEventBusFake) Unsubscribe(string, string)              {}

func TestPublishChapterGeneratedUsesCallerContext(t *testing.T) {
	publishCtx, cancelPublish := context.WithTimeout(context.Background(), time.Second)
	defer cancelPublish()

	bus := &workflowEventBusFake{
		publish: func(ctx context.Context, event events.Event) error {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 {
				t.Fatal("publish context has no active deadline")
			}
			generated, ok := event.(events.ChapterGeneratedEvent)
			if !ok {
				t.Fatalf("event type = %T", event)
			}
			if generated.GenerationID != "generation-1" ||
				generated.NovelID != "7" ||
				generated.ChapterID != "11" || generated.Content != "正文" {
				t.Fatalf("event = %#v", generated)
			}
			return nil
		},
	}
	engine := &WorkflowEngine{eventBus: bus}

	if err := engine.PublishChapterGenerated(publishCtx, &agents.GenerationState{
		GenerationID: "generation-1",
		NovelID:      "7",
		ChapterID:    "11",
		Draft:        "正文",
	}); err != nil {
		t.Fatalf("PublishChapterGenerated returned error: %v", err)
	}
}

func TestPublishChapterGeneratedPropagatesCallerCancellation(t *testing.T) {
	publishCtx, cancelPublish := context.WithCancel(context.Background())
	cancelPublish()

	bus := &workflowEventBusFake{
		publish: func(ctx context.Context, _ events.Event) error {
			return ctx.Err()
		},
	}
	engine := &WorkflowEngine{eventBus: bus}

	if err := engine.PublishChapterGenerated(publishCtx, &agents.GenerationState{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PublishChapterGenerated error = %v, want context canceled", err)
	}
}
