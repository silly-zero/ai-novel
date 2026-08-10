package workflows

import (
	"context"
	"errors"
	"strings"
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

type workflowLLMFake struct {
	streamCalls int
	reviewCalls int
	passOn      int
	draft       string
}

func (f *workflowLLMFake) Generate(_ context.Context, systemPrompt, _ string) (string, error) {
	if strings.Contains(systemPrompt, "审查员") {
		f.reviewCalls++
		passed := f.passOn > 0 && f.reviewCalls >= f.passOn
		if passed {
			return `{"passed":true,"continuity_passed":true,"contract_assessment":{"goal":{"satisfied":true,"evidence":"目标已完成"},"must_happen":[{"satisfied":true,"evidence":"线索已找到"}],"must_not_happen":[{"satisfied":true,"evidence":"真相未揭晓"}],"end_state":{"satisfied":true,"evidence":"决定继续追查"}},"critique":""}`, nil
		}
		return `{"passed":true,"continuity_passed":true,"contract_assessment":{"goal":{"satisfied":true,"evidence":"目标已完成"},"must_happen":[{"satisfied":false,"evidence":"正文没有找到线索"}],"must_not_happen":[{"satisfied":true,"evidence":"真相未揭晓"}],"end_state":{"satisfied":true,"evidence":"决定继续追查"}},"critique":""}`, nil
	}
	return "unused", nil
}

func (f *workflowLLMFake) StreamGenerate(
	_ context.Context,
	_, _ string,
	onChunk func(string) error,
) error {
	f.streamCalls++
	draft := f.draft
	if draft == "" {
		draft = strings.Repeat("文", 2500)
	}
	return onChunk(draft)
}

func newReviewerLoopEngine(t *testing.T, llm *workflowLLMFake) *WorkflowEngine {
	t.Helper()
	engine, err := NewWorkflowEngine(
		agents.NewArchitectAgent(llm),
		agents.NewPlotAgent(llm),
		agents.NewDirectorAgent(llm),
		agents.NewLibrarianAgent(nil, nil, nil, nil, nil, agents.LibrarianConfig{}),
		agents.NewWriterAgent(llm),
		agents.NewReviewerAgent(llm),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestRunChapterGenerationStopsAfterThreeRewrites(t *testing.T) {
	llm := &workflowLLMFake{}
	engine := newReviewerLoopEngine(t, llm)
	var retries []int
	state := &agents.GenerationState{
		ExistingOutline: "大纲",
		Outline:         "本章大纲",
		ChapterContract: agents.ChapterContract{
			Goal:          "完成调查",
			MustHappen:    []string{"找到线索"},
			MustNotHappen: []string{"揭晓真相"},
			EndState:      "决定继续追查",
		},
		SceneCard: "场景卡",
		Context:   "背景",
		StreamSink: func(_ context.Context, event agents.GenerationStreamEvent) error {
			if event.Type == agents.GenerationStreamEventRetry {
				retries = append(retries, event.RetryCount)
			}
			return nil
		},
	}

	finalState, err := engine.RunChapterGeneration(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "after 3 retries") {
		t.Fatalf("error = %v, want retry-limit failure", err)
	}
	if strings.Contains(err.Error(), "正文没有找到线索") {
		t.Fatalf("retry-limit error leaked critique: %v", err)
	}
	if finalState == nil || finalState.RetryCount != 3 || finalState.IsApproved {
		t.Fatalf("final state = %#v", finalState)
	}
	if llm.streamCalls != 4 || llm.reviewCalls != 4 {
		t.Fatalf("writer calls = %d, reviewer calls = %d, want 4 each", llm.streamCalls, llm.reviewCalls)
	}
	if len(retries) != 3 || retries[0] != 1 || retries[1] != 2 || retries[2] != 3 {
		t.Fatalf("retry events = %v, want [1 2 3]", retries)
	}
}

func TestRunChapterGenerationRetriesDeterministicFailuresWithoutReviewerLLM(t *testing.T) {
	llm := &workflowLLMFake{draft: strings.Repeat("文", 2500) + "【场景卡】"}
	engine := newReviewerLoopEngine(t, llm)
	var retries []int
	state := &agents.GenerationState{
		ExistingOutline: "大纲",
		Outline:         "本章大纲",
		SceneCard:       "场景卡",
		Context:         "背景",
		StreamSink: func(_ context.Context, event agents.GenerationStreamEvent) error {
			if event.Type == agents.GenerationStreamEventRetry {
				retries = append(retries, event.RetryCount)
			}
			return nil
		},
	}

	finalState, err := engine.RunChapterGeneration(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "after 3 retries") {
		t.Fatalf("error = %v, want retry-limit failure", err)
	}
	if strings.Contains(err.Error(), "内部提示标签") {
		t.Fatalf("retry-limit error leaked deterministic critique: %v", err)
	}
	if finalState == nil || finalState.RetryCount != 3 || finalState.IsApproved {
		t.Fatalf("final state = %#v", finalState)
	}
	if llm.streamCalls != 4 || llm.reviewCalls != 0 {
		t.Fatalf("writer calls = %d, reviewer calls = %d, want 4 and 0", llm.streamCalls, llm.reviewCalls)
	}
	if len(retries) != 3 || retries[0] != 1 || retries[1] != 2 || retries[2] != 3 {
		t.Fatalf("retry events = %v, want [1 2 3]", retries)
	}
	if !strings.Contains(finalState.Critique, "内部提示标签") {
		t.Fatalf("critique = %q", finalState.Critique)
	}
}

func TestRunChapterGenerationStopsRewritingAfterApproval(t *testing.T) {
	llm := &workflowLLMFake{passOn: 2}
	engine := newReviewerLoopEngine(t, llm)
	var retries int
	state := &agents.GenerationState{
		ExistingOutline: "大纲",
		Outline:         "本章大纲",
		ChapterContract: agents.ChapterContract{
			Goal:          "完成调查",
			MustHappen:    []string{"找到线索"},
			MustNotHappen: []string{"揭晓真相"},
			EndState:      "决定继续追查",
		},
		SceneCard: "场景卡",
		Context:   "背景",
		StreamSink: func(_ context.Context, event agents.GenerationStreamEvent) error {
			if event.Type == agents.GenerationStreamEventRetry {
				retries++
			}
			return nil
		},
	}

	finalState, err := engine.RunChapterGeneration(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !finalState.IsApproved || finalState.RetryCount != 1 || retries != 1 {
		t.Fatalf("final state = %#v, retry events = %d", finalState, retries)
	}
	if llm.streamCalls != 2 || llm.reviewCalls != 2 {
		t.Fatalf("writer calls = %d, reviewer calls = %d, want 2 each", llm.streamCalls, llm.reviewCalls)
	}
}

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
