package main

import (
	"context"
	"log"
	"os"

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
		Host:              cfg.Database.Postgres.Host,
		Port:              cfg.Database.Postgres.Port,
		User:              cfg.Database.Postgres.User,
		Password:          cfg.Database.Postgres.Password,
		DBName:            cfg.Database.Postgres.DBName,
		SSLMode:           cfg.Database.Postgres.SSLMode,
		EnableForeignKeys: cfg.Database.Postgres.EnableForeignKeys,
	})
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer dbClient.Close()

	eventBus := eventbus.NewInternalEventBus()

	var engine *workflows.WorkflowEngine
	if cfg.LLM.OpenAI.APIKey == "你的Key" || cfg.LLM.OpenAI.APIKey == "" {
		log.Println("警告: LLM API Key 未配置，将禁用生成相关接口")
	} else {
		llmAdapter, err := llm.NewOpenAIAdapter(ctx, cfg.LLM.OpenAI.APIKey, cfg.LLM.OpenAI.BaseURL, cfg.LLM.OpenAI.Model)
		if err != nil {
			log.Fatalf("初始化 LLM 失败: %v", err)
		}

		embedder, err := llm.NewOpenAIEmbedder(ctx, cfg.LLM.OpenAI.APIKey, cfg.LLM.OpenAI.BaseURL, cfg.LLM.OpenAI.EmbeddingModel)
		if err != nil {
			log.Fatalf("初始化 Embedder 失败: %v", err)
		}

		vStore := vectorstore.NewEntVectorStore(dbClient.Client)

		ingestionUC := usecases.NewIngestionUseCase(llmAdapter, embedder, vStore)
		eventBus.Subscribe("chapter.generated", func(ctx context.Context, event events.Event) error {
			return ingestionUC.HandleChapterGenerated(ctx, event)
		})

		charRepo := database.NewCharacterRepository(dbClient.Client)
		charAgent := agents.NewCharacterAgent(llmAdapter, charRepo)
		charUC := usecases.NewCharacterUseCase(charAgent)
		eventBus.Subscribe("chapter.generated", func(ctx context.Context, event events.Event) error {
			return charUC.HandleChapterGenerated(ctx, event)
		})

		worldRepo := database.NewWorldRepository(dbClient.Client)
		worldAgent := agents.NewWorldAgent(llmAdapter, worldRepo)
		worldUC := usecases.NewWorldUseCase(worldAgent)
		eventBus.Subscribe("chapter.generated", func(ctx context.Context, event events.Event) error {
			return worldUC.HandleChapterGenerated(ctx, event)
		})

		architect := agents.NewArchitectAgent(llmAdapter)
		plot := agents.NewPlotAgent(llmAdapter)
		director := agents.NewDirectorAgent(llmAdapter)
		writer := agents.NewWriterAgent(llmAdapter, eventBus)
		reviewer := agents.NewReviewerAgent(llmAdapter)
		librarian := agents.NewLibrarianAgent(llmAdapter, embedder, vStore, charRepo, worldRepo)

		engine, err = workflows.NewWorkflowEngine(architect, plot, director, librarian, writer, reviewer, eventBus)
		if err != nil {
			log.Fatalf("初始化工作流引擎失败: %v", err)
		}

		if os.Getenv("AI_NOVEL_RUN_LOCAL_TEST") == "1" {
			go func() {
				_, _ = engine.RunChapterGeneration(ctx, &agents.GenerationState{
					NovelID:      "test-novel-001",
					ChapterIndex: 1,
					Idea:         "一个普通的少年在山洞中捡到了一枚神秘的戒指，从此踏上了修仙之路。",
				})
			}()
		}
	}

	server := api.NewServer(engine, eventBus, dbClient.Client)
	if err := server.Start(":8081"); err != nil {
		log.Fatalf("API Server 启动失败: %v", err)
	}
}
