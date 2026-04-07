package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ai-novel/studio/internal/application/usecases"
	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/ai-novel/studio/internal/infrastructure/config"
	"github.com/ai-novel/studio/internal/infrastructure/database"
	"github.com/ai-novel/studio/internal/infrastructure/eventbus"
	"github.com/ai-novel/studio/internal/infrastructure/llm"
	"github.com/ai-novel/studio/internal/infrastructure/vectorstore"
	"github.com/ai-novel/studio/internal/interfaces/api"
)

func main() {
	ctx := context.Background()

	// 1. 加载配置文件
	cfg, err := config.LoadConfig("configs")
	if err != nil {
		log.Fatalf("加载配置文件失败: %v", err)
	}

	// 2. 初始化基础设施 (数据库)
	// 使用本地定义的结构体转换，确保解耦和稳定性
	dbClient, err := database.NewClient(ctx, &database.PostgresConfig{
		Host:     cfg.Database.Postgres.Host,
		Port:     cfg.Database.Postgres.Port,
		User:     cfg.Database.Postgres.User,
		Password: cfg.Database.Postgres.Password,
		DBName:   cfg.Database.Postgres.DBName,
		SSLMode:  cfg.Database.Postgres.SSLMode,
	})
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer dbClient.Close()

	eventBus := eventbus.NewInternalEventBus()

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
	// vStore := vectorstore.NewMemoryVectorStore()
	vStore := vectorstore.NewEntVectorStore(dbClient.Client)

	// 3. 初始化 Ingestion 业务逻辑并订阅事件
	ingestionUC := usecases.NewIngestionUseCase(llmAdapter, embedder, vStore)
	eventBus.Subscribe("chapter.generated", func(ctx context.Context, event events.Event) error {
		return ingestionUC.HandleChapterGenerated(ctx, event)
	})

	// 4. 初始化各个 Agent
	plot := agents.NewPlotAgent(llmAdapter)
	director := agents.NewDirectorAgent(llmAdapter)
	writer := agents.NewWriterAgent(llmAdapter, eventBus)
	reviewer := agents.NewReviewerAgent(llmAdapter)

	// LibrarianAgent 现在拥有真实的 Embedder 和 VectorStore
	librarian := agents.NewLibrarianAgent(embedder, vStore)

	// 5. 初始化 Eino 工作流引擎
	engine, err := workflows.NewWorkflowEngine(plot, director, librarian, writer, reviewer, eventBus)
	if err != nil {
		log.Fatalf("初始化工作流引擎失败: %v", err)
	}

	// 6. 准备生成任务的初始状态
	// 现在我们可以只提供一个 Idea，让 Plot Agent 自动生成大纲
	initialState := &agents.GenerationState{
		NovelID: "test-novel-001",
		Idea:    "一个普通的少年在山洞中捡到了一枚神秘的戒指，从此踏上了修仙之路。",
	}

	// 7. 启动 API Server (支持流式输出)
	server := api.NewServer(engine, eventBus)
	go func() {
		if err := server.Start(":8080"); err != nil {
			log.Fatalf("API Server 启动失败: %v", err)
		}
	}()

	// 8. 同时保留一个本地 CLI 测试逻辑
	fmt.Println("🚀 正在启动 AI 小说生成工作流 (本地测试)...")
	finalState, err := engine.RunChapterGeneration(ctx, initialState)
	if err != nil {
		log.Fatalf("工作流执行失败: %v", err)
	}

	// 9. 输出结果
	fmt.Println("\n--- 生成结果 ---")
	fmt.Printf("重试次数: %d\n", finalState.RetryCount)
	fmt.Printf("是否通过审查: %v\n", finalState.IsApproved)
	fmt.Println("\n--- 最终正文 ---")
	fmt.Println(finalState.Draft)
}
