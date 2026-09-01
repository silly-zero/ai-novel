package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/cloudwego/eino/compose"
)

type WorkflowStage string

const (
	WorkflowStageArchitect WorkflowStage = "architect"
	WorkflowStagePlot      WorkflowStage = "plot"
	WorkflowStageDirector  WorkflowStage = "director"
	WorkflowStageLibrarian WorkflowStage = "librarian"
	WorkflowStageWriter    WorkflowStage = "writer"
	WorkflowStageReviewer  WorkflowStage = "reviewer"
)

var ErrReviewRetryLimit = errors.New("review retry limit reached")

type reviewRetryLimitError struct {
	reviewArea string
}

func (e *reviewRetryLimitError) Error() string {
	return "review retry limit reached"
}

func (e *reviewRetryLimitError) Is(target error) bool {
	return target == ErrReviewRetryLimit
}

func (e *reviewRetryLimitError) SafeReviewArea() string {
	return e.reviewArea
}

type WorkflowStageError struct {
	Stage WorkflowStage
	cause error
}

func NewWorkflowStageError(stage WorkflowStage, cause error) *WorkflowStageError {
	return &WorkflowStageError{Stage: stage, cause: cause}
}

func (e *WorkflowStageError) Error() string {
	return "workflow stage failed"
}

func (e *WorkflowStageError) Unwrap() error {
	return e.cause
}

func runWorkflowStage(
	ctx context.Context,
	state *agents.GenerationState,
	stage WorkflowStage,
	run func(context.Context, *agents.GenerationState) (*agents.GenerationState, error),
) (*agents.GenerationState, error) {
	result, err := run(ctx, state)
	if err != nil {
		return result, NewWorkflowStageError(stage, err)
	}
	return result, nil
}

// WorkflowEngine 是基于 eino 框架的状态机引擎，用于控制 Agent 之间的流转
type derivedProcessor interface {
	RunCurrent(context.Context, events.ChapterGeneratedEvent) error
}

type WorkflowEngine struct {
	graph        compose.Runnable[*agents.GenerationState, *agents.GenerationState]
	contextGraph compose.Runnable[*agents.GenerationState, *agents.GenerationState]
	architect    *agents.ArchitectAgent
	eventBus     events.Bus
	derived      derivedProcessor
	continuity   *agents.ContinuityExtractor
	writer       *agents.WriterAgent
}

// NewWorkflowEngine 初始化一个新引擎，编排多个 Agent
func NewWorkflowEngine(
	architect *agents.ArchitectAgent,
	plot *agents.PlotAgent,
	director *agents.DirectorAgent,
	librarian *agents.LibrarianAgent,
	writer *agents.WriterAgent,
	reviewer *agents.ReviewerAgent,
	eventBus events.Bus,
	continuity ...*agents.ContinuityExtractor,
) (*WorkflowEngine, error) {

	// 1. 初始化 Eino Graph，输入和输出都是 GenerationState 的指针
	g := compose.NewGraph[*agents.GenerationState, *agents.GenerationState]()

	// 2. 将 Agent 注册为 Graph 中的 Lambda Node
	_ = g.AddLambdaNode("architect", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		return runWorkflowStage(ctx, s, WorkflowStageArchitect, architect.Run)
	}))
	_ = g.AddLambdaNode("plot", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		return runWorkflowStage(ctx, s, WorkflowStagePlot, plot.Run)
	}))
	_ = g.AddLambdaNode("director", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		return runWorkflowStage(ctx, s, WorkflowStageDirector, director.Run)
	}))
	_ = g.AddLambdaNode("librarian", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		if s.ContextPrepared {
			return s, nil
		}
		prepared, err := runWorkflowStage(
			ctx,
			s,
			WorkflowStageLibrarian,
			librarian.Run,
		)
		if err != nil {
			return prepared, err
		}
		prepared.ContextPrepared = true
		return prepared, nil
	}))
	_ = g.AddLambdaNode("writer", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		return runWorkflowStage(ctx, s, WorkflowStageWriter, writer.Run)
	}))
	_ = g.AddLambdaNode("reviewer", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		return runWorkflowStage(ctx, s, WorkflowStageReviewer, reviewer.Run)
	}))

	// 3. 定义图的边 (Edges) - 正常顺序流转
	_ = g.AddEdge(compose.START, "architect")
	_ = g.AddEdge("architect", "plot")
	_ = g.AddEdge("plot", "director")
	_ = g.AddEdge("director", "librarian")
	_ = g.AddEdge("librarian", "writer")
	_ = g.AddEdge("writer", "reviewer")

	// 4. 定义条件分支 (Branch) - Actor-Critic 审查闭环
	// Reviewer 节点执行完毕后，进入此分支判断
	_ = g.AddBranch("reviewer", compose.NewGraphBranch(func(ctx context.Context, state *agents.GenerationState) (string, error) {
		// 如果通过审查，或者重试次数已经达到上限 (3次)，则结束
		if state.IsApproved || state.RetryCount >= 3 {
			return compose.END, nil
		}

		// 没通过审查，增加重试次数，打回给 writer 重新写
		state.RetryCount++
		if state.StreamSink != nil {
			if err := state.StreamSink(ctx, agents.GenerationStreamEvent{
				Type:       agents.GenerationStreamEventRetry,
				RetryCount: state.RetryCount,
				Critique:   state.Critique,
			}); err != nil {
				return "", fmt.Errorf("deliver chapter retry: %w", err)
			}
		}
		return "writer", nil
	}, map[string]bool{
		compose.END: true,
		"writer":    true,
	}))

	// 5. 编译 Graph
	runnable, err := g.Compile(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to compile eino graph: %w", err)
	}

	// 构建仅用于生成上下文的精简图：architect -> plot -> director -> librarian
	gCtx := compose.NewGraph[*agents.GenerationState, *agents.GenerationState]()
	_ = gCtx.AddLambdaNode("architect", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		return runWorkflowStage(ctx, s, WorkflowStageArchitect, architect.Run)
	}))
	_ = gCtx.AddLambdaNode("plot", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		return runWorkflowStage(ctx, s, WorkflowStagePlot, plot.Run)
	}))
	_ = gCtx.AddLambdaNode("director", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		return runWorkflowStage(ctx, s, WorkflowStageDirector, director.Run)
	}))
	_ = gCtx.AddLambdaNode("librarian", compose.InvokableLambda(func(ctx context.Context, s *agents.GenerationState) (*agents.GenerationState, error) {
		prepared, err := runWorkflowStage(
			ctx,
			s,
			WorkflowStageLibrarian,
			librarian.Run,
		)
		if err != nil {
			return prepared, err
		}
		prepared.ContextPrepared = true
		return prepared, nil
	}))
	_ = gCtx.AddEdge(compose.START, "architect")
	_ = gCtx.AddEdge("architect", "plot")
	_ = gCtx.AddEdge("plot", "director")
	_ = gCtx.AddEdge("director", "librarian")
	_ = gCtx.AddEdge("librarian", compose.END)
	ctxRunnable, err := gCtx.Compile(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to compile context graph: %w", err)
	}

	continuityExtractor := (*agents.ContinuityExtractor)(nil)
	if len(continuity) > 0 {
		continuityExtractor = continuity[0]
	}
	return &WorkflowEngine{
		graph:        runnable,
		contextGraph: ctxRunnable,
		architect:    architect,
		eventBus:     eventBus,
		continuity:   continuityExtractor,
		writer:       writer,
	}, nil
}

func hasBlockingGeneratedContentIssuesForState(state *agents.GenerationState) bool {
	for _, issue := range agents.ValidateGeneratedContentForState(state) {
		if issue.Code != "content_too_long" {
			return true
		}
	}
	return false
}

func (e *WorkflowEngine) RunChapterGeneration(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	// 调用 Eino 编译好的 Runnable
	finalState, err := e.graph.Invoke(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("workflow execution failed: %w", err)
	}

	if !finalState.IsApproved {
		if finalState.RetryCount >= 3 && strings.TrimSpace(finalState.Draft) != "" &&
			!hasBlockingGeneratedContentIssuesForState(finalState) {
			finalState.SaveEligible = true
		}
		return finalState, NewWorkflowStageError(
			WorkflowStageReviewer,
			&reviewRetryLimitError{reviewArea: finalState.ReviewFailureArea},
		)
	}

	return finalState, nil
}

func (e *WorkflowEngine) RunContinuousSegment(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	if state == nil {
		return nil, errors.New("continuous segment state is nil")
	}
	if e.writer == nil {
		return nil, errors.New("continuous segment writer is not initialized")
	}
	writerState, err := runWorkflowStage(ctx, state, WorkflowStageWriter, e.writer.Run)
	if err != nil {
		return nil, fmt.Errorf("continuous segment generation failed: %w", err)
	}
	if writerState == nil || strings.TrimSpace(writerState.Draft) == "" {
		return writerState, errors.New("continuous segment draft is empty")
	}
	if agents.HasBlockingGeneratedContentIssuesForBatch(writerState.Draft) {
		return writerState, fmt.Errorf("continuous segment content failed validation")
	}
	return writerState, nil
}
func (e *WorkflowEngine) PrepareOutline(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	if e.architect == nil {
		return nil, errors.New("architect agent is not initialized")
	}
	return runWorkflowStage(ctx, state, WorkflowStageArchitect, e.architect.Run)
}

func (e *WorkflowEngine) SetDerivedProcessor(processor derivedProcessor) {
	e.derived = processor
}

func (e *WorkflowEngine) PublishChapterGenerated(
	ctx context.Context,
	state *agents.GenerationState,
) error {
	if state == nil {
		return nil
	}
	event := events.ChapterGeneratedEvent{
		GenerationID: state.GenerationID,
		NovelID:      state.NovelID,
		ChapterID:    state.ChapterID,
		ChapterIndex: state.ChapterIndex,
		Content:      state.Draft,
		Timestamp:    time.Now(),
	}
	if e.derived != nil {
		return e.derived.RunCurrent(ctx, event)
	}
	if e.eventBus == nil {
		return nil
	}
	return e.eventBus.Publish(ctx, event)
}

func (e *WorkflowEngine) ExtractContinuity(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	if e.continuity == nil {
		return nil, fmt.Errorf("continuity extractor is not initialized")
	}
	return e.continuity.Extract(ctx, state)
}

func (e *WorkflowEngine) PrepareContext(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	if e.contextGraph == nil {
		return nil, fmt.Errorf("context graph is not initialized")
	}
	res, err := e.contextGraph.Invoke(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("context preparation failed: %w", err)
	}
	return res, nil
}
