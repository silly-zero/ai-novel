package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/ai-novel/studio/internal/infrastructure/config"
	"github.com/ai-novel/studio/internal/infrastructure/eventbus"
	"github.com/ai-novel/studio/internal/infrastructure/llm"
	"github.com/ai-novel/studio/internal/infrastructure/vectorstore"
)

func main() {
	ctx := context.Background()

	// 1. 加载配置文件
	cfg, err := config.LoadConfig("configs")
	if err != nil {
		log.Fatalf("加载配置文件失败: %v", err)
	}

	// 2. 初始化基础设施
	eventBus := eventbus.NewInternalEventBus()

	// 订阅事件测试 (模拟 Ingestion 或 日志)
	eventBus.Subscribe("chapter.generated", func(ctx context.Context, event events.Event) error {
		e := event.(events.ChapterGeneratedEvent)
		fmt.Printf("\n📢 [事件订阅成功] 检测到新章节生成！小说ID: %s, 字数: %d\n", e.NovelID, len(e.Content))
		return nil
	})

	if cfg.LLM.OpenAI.APIKey == "你的Key" || cfg.LLM.OpenAI.APIKey == "" {
		log.Println("警告: LLM API Key 未配置，请在 configs/config.yaml 中设置")
		return
	}

	// 初始化 OpenAI ChatModel 适配器
	llmAdapter, err := llm.NewOpenAIAdapter(ctx, cfg.LLM.OpenAI.APIKey, cfg.LLM.OpenAI.BaseURL, cfg.LLM.OpenAI.Model)
	if err != nil {
		log.Fatalf("初始化 LLM 失败: %v", err)
	}

	// 初始化 OpenAI Embedder 适配器
	embedder, err := llm.NewOpenAIEmbedder(ctx, cfg.LLM.OpenAI.APIKey, cfg.LLM.OpenAI.BaseURL, cfg.LLM.OpenAI.EmbeddingModel)
	if err != nil {
		log.Fatalf("初始化 Embedder 失败: %v", err)
	}

	// 初始化内存向量库 (作为临时存储)
	vStore := vectorstore.NewMemoryVectorStore()

	// 3. 初始化各个 Agent
	director := agents.NewDirectorAgent(llmAdapter)
	writer := agents.NewWriterAgent(llmAdapter)
	reviewer := agents.NewReviewerAgent(llmAdapter)

	// LibrarianAgent 现在拥有真实的 Embedder 和 VectorStore
	librarian := agents.NewLibrarianAgent(embedder, vStore)

	// 3. 初始化 Eino 工作流引擎
	engine, err := workflows.NewWorkflowEngine(director, librarian, writer, reviewer, eventBus)
	if err != nil {
		log.Fatalf("初始化工作流引擎失败: %v", err)
	}

	// 4. 准备生成任务的初始状态
	initialState := &agents.GenerationState{
		NovelID: "test-novel-001",
		Outline: "这一章描写主角林动初次下山，在客栈遇到了一位神秘的黑衣人，两人因为一卷秘籍产生了争执。",
	}

	// 5. 运行工作流！
	fmt.Println("🚀 正在启动 AI 小说生成工作流...")
	finalState, err := engine.RunChapterGeneration(ctx, initialState)
	if err != nil {
		log.Fatalf("工作流执行失败: %v", err)
	}

	// 6. 输出结果
	fmt.Println("\n--- 生成结果 ---")
	fmt.Printf("重试次数: %d\n", finalState.RetryCount)
	fmt.Printf("是否通过审查: %v\n", finalState.IsApproved)
	fmt.Println("\n--- 最终正文 ---")
	fmt.Println(finalState.Draft)
}
