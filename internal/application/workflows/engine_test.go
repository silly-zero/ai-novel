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
	streamCalls   int
	reviewCalls   int
	plotCalls     int
	directorCalls int
	passOn        int
	draft         string
}

func (f *workflowLLMFake) Generate(_ context.Context, systemPrompt, _ string) (string, error) {
	if strings.Contains(systemPrompt, "资深网文编剧") {
		f.plotCalls++
		return `{"chapter_goal":"完成调查","must_happen":["找到线索"],"must_not_happen":["揭晓真相"],"end_state":"决定继续追查"}`, nil
	}
	if strings.Contains(systemPrompt, "资深小说主编") {
		f.directorCalls++
		return "场景卡", nil
	}
	if strings.Contains(systemPrompt, "审查员") {
		f.reviewCalls++
		passed := f.passOn > 0 && f.reviewCalls >= f.passOn
		if passed {
			return `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"satisfied":true,"evidence":"主角完成了调查目标。"},"must_happen":[{"satisfied":true,"evidence":"主角找到了关键线索。"}],"must_not_happen":[{"satisfied":true,"evidence":"正文未揭晓真相"}],"end_state":{"satisfied":true,"evidence":"他决定继续追查。"}},"critique":""}`, nil
		}
		return `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"satisfied":true,"evidence":"主角完成了调查目标。"},"must_happen":[{"satisfied":false,"evidence":"正文没有找到线索"}],"must_not_happen":[{"satisfied":true,"evidence":"正文未揭晓真相"}],"end_state":{"satisfied":true,"evidence":"他决定继续追查。"}},"critique":""}`, nil
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
		core := "主角完成了调查目标。主角找到了关键线索。他决定继续追查。"
		draft = core + strings.Repeat("文", 2500-len([]rune(core)))
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

func TestRunChapterGenerationStopsAtInvalidStructuredOutline(t *testing.T) {
	llm := &workflowLLMFake{}
	engine := newReviewerLoopEngine(t, llm)
	outline := "第2章：不应出现在错误消息中的秘密事件"
	state := &agents.GenerationState{
		FullOutline:  outline,
		ChapterIndex: 1,
	}

	finalState, err := engine.RunChapterGeneration(context.Background(), state)
	var stageErr *WorkflowStageError
	if !errors.As(err, &stageErr) || stageErr.Stage != WorkflowStageArchitect {
		t.Fatalf("error = %v, want architect stage", err)
	}
	var diagnosticCoder interface{ SafeDiagnosticCode() string }
	if !errors.As(err, &diagnosticCoder) || diagnosticCoder.SafeDiagnosticCode() != "current_chapter_missing" {
		t.Fatalf("error did not preserve Architect issue code: %v", err)
	}
	if !strings.Contains(err.Error(), "workflow stage failed") {
		t.Fatalf("unexpected safe error = %q", err.Error())
	}
	if strings.Contains(err.Error(), outline) || strings.Contains(err.Error(), "current_chapter_missing") {
		t.Fatalf("error leaked outline: %v", err)
	}
	if finalState != nil {
		t.Fatalf("final state = %#v, want nil", finalState)
	}
	if llm.plotCalls != 0 || llm.directorCalls != 0 || llm.streamCalls != 0 || llm.reviewCalls != 0 {
		t.Fatalf("downstream calls = plot %d director %d writer %d reviewer %d", llm.plotCalls, llm.directorCalls, llm.streamCalls, llm.reviewCalls)
	}
}

func TestRunChapterGenerationKeepsNonstandardManualOutlineCompatible(t *testing.T) {
	llm := &workflowLLMFake{passOn: 1}
	engine := newReviewerLoopEngine(t, llm)
	state := &agents.GenerationState{
		FullOutline:  "人工大纲：主角调查身世",
		ChapterIndex: 1,
	}

	finalState, err := engine.RunChapterGeneration(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if finalState == nil || !finalState.IsApproved || finalState.MainlineBeat != (agents.MainlineEventBeat{}) {
		t.Fatalf("final state = %#v", finalState)
	}
	if llm.plotCalls != 1 || llm.directorCalls != 1 || llm.streamCalls != 1 || llm.reviewCalls != 1 {
		t.Fatalf("calls = plot %d director %d writer %d reviewer %d", llm.plotCalls, llm.directorCalls, llm.streamCalls, llm.reviewCalls)
	}
}

func TestPreparedContextIsReusedByChapterGeneration(t *testing.T) {
	llm := &workflowLLMFake{passOn: 1}
	engine := newReviewerLoopEngine(t, llm)
	state := &agents.GenerationState{
		FullOutline:  "人工大纲：主角调查身世",
		ChapterIndex: 1,
	}

	prepared, err := engine.PrepareContext(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.ContextPrepared {
		t.Fatal("prepared context was not marked ready")
	}
	prepared.Context = "本次已冻结的背景"

	finalState, err := engine.RunChapterGeneration(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if finalState.Context != "本次已冻结的背景" || !finalState.ContextPrepared {
		t.Fatalf("final context state = %#v", finalState)
	}
	if llm.plotCalls != 1 || llm.directorCalls != 1 {
		t.Fatalf("context agents reran: plot=%d director=%d", llm.plotCalls, llm.directorCalls)
	}
}

func TestDirectChapterGenerationStillPreparesContext(t *testing.T) {
	llm := &workflowLLMFake{passOn: 1}
	engine := newReviewerLoopEngine(t, llm)
	state := &agents.GenerationState{
		FullOutline:  "人工大纲：主角调查身世",
		ChapterIndex: 1,
		Context:      "外部旧背景",
	}

	finalState, err := engine.RunChapterGeneration(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !finalState.ContextPrepared || finalState.Context != "（暂无背景资料，请根据大纲自由发挥）" {
		t.Fatalf("final context state = %#v", finalState)
	}
}

func TestFailedContextPreparationDoesNotSetReadyMarker(t *testing.T) {
	llm := &workflowLLMFake{}
	engine := newReviewerLoopEngine(t, llm)
	state := &agents.GenerationState{
		FullOutline: "人工大纲：主角调查身世",
	}

	if _, err := engine.PrepareContext(context.Background(), state); err == nil {
		t.Fatal("PrepareContext() error = nil")
	}
	if state.ContextPrepared {
		t.Fatal("failed preparation marked context ready")
	}
}

func assertReviewRetryLimit(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrReviewRetryLimit) {
		t.Fatalf("error = %v, want review retry limit", err)
	}
	var stageErr *WorkflowStageError
	if !errors.As(err, &stageErr) || stageErr.Stage != WorkflowStageReviewer {
		t.Fatalf("error = %v, want reviewer stage", err)
	}
	if err.Error() != "workflow stage failed" {
		t.Fatalf("unsafe retry-limit error = %q", err.Error())
	}
}

func TestWorkflowStageErrorDoesNotRenderCause(t *testing.T) {
	cause := errors.New("CANARY_PROMPT CANARY_DRAFT CANARY_RESPONSE CANARY_EVIDENCE")
	err := NewWorkflowStageError(WorkflowStageWriter, cause)
	if err.Error() != "workflow stage failed" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("stage error did not preserve cause chain")
	}
	if strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("stage error leaked cause: %s", err)
	}
}

func TestRunChapterGenerationStopsAfterThreeRewrites(t *testing.T) {
	llm := &workflowLLMFake{}
	engine := newReviewerLoopEngine(t, llm)
	var retries []int
	state := &agents.GenerationState{
		ChapterIndex:    1,
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
	assertReviewRetryLimit(t, err)
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
		ChapterIndex:    1,
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
	assertReviewRetryLimit(t, err)
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
		ChapterIndex:    1,
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
				generated.ChapterID != "11" || generated.ChapterIndex != 4 ||
				generated.Content != "正文" {
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
		ChapterIndex: 4,
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
