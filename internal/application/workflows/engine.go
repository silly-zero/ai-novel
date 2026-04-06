package workflows

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/cloudwego/eino/compose"
)

// WorkflowEngine 是基于 eino 框架的状态机引擎，用于控制 Agent 之间的流转
type WorkflowEngine struct {
	graph    compose.Runnable[*agents.GenerationState, *agents.GenerationState]
	eventBus events.Bus
}

// NewWorkflowEngine 初始化一个新引擎，编排多个 Agent
func NewWorkflowEngine(
	director *agents.DirectorAgent,
	librarian *agents.LibrarianAgent,
	writer *agents.WriterAgent,
	reviewer *agents.ReviewerAgent,
	eventBus events.Bus,
) (*WorkflowEngine, error) {

	// 1. 初始化 Eino Graph，输入和输出都是 GenerationState 的指针
	g := compose.NewGraph[*agents.GenerationState, *agents.GenerationState]()

	// 2. 将 Agent 注册为 Graph 中的 Lambda Node
	_ = g.AddLambdaNode("director", compose.InvokableLambda(director.Run))
	_ = g.AddLambdaNode("librarian", compose.InvokableLambda(librarian.Run))
	_ = g.AddLambdaNode("writer", compose.InvokableLambda(writer.Run))
	_ = g.AddLambdaNode("reviewer", compose.InvokableLambda(reviewer.Run))

	// 3. 定义图的边 (Edges) - 正常顺序流转
	_ = g.AddEdge(compose.START, "director")
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

	return &WorkflowEngine{
		graph:    runnable,
		eventBus: eventBus,
	}, nil
}

// RunChapterGeneration 开始执行章节生成工作流
func (e *WorkflowEngine) RunChapterGeneration(ctx context.Context, state *agents.GenerationState) (*agents.GenerationState, error) {
	// 调用 Eino 编译好的 Runnable
	finalState, err := e.graph.Invoke(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("workflow execution failed: %w", err)
	}

	if !finalState.IsApproved {
		return finalState, fmt.Errorf("failed to generate acceptable chapter after %d retries. Last critique: %s", finalState.RetryCount, finalState.Critique)
	}

	// 重点：章节生成成功后，发布领域事件！
	if e.eventBus != nil {
		_ = e.eventBus.Publish(ctx, events.ChapterGeneratedEvent{
			NovelID:   finalState.NovelID,
			ChapterID: finalState.ChapterID,
			Content:   finalState.Draft,
			Timestamp: time.Now(),
		})
	}

	return finalState, nil
}
