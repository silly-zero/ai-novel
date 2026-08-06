package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/ai-novel/studio/internal/application/usecases"
	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/ai-novel/studio/internal/domain/memory"
	"github.com/ai-novel/studio/internal/infrastructure/config"
	"github.com/ai-novel/studio/internal/infrastructure/database"
	"github.com/ai-novel/studio/internal/infrastructure/eventbus"
	"github.com/ai-novel/studio/internal/infrastructure/llm"
	"github.com/ai-novel/studio/internal/infrastructure/vectorstore"
	"github.com/ai-novel/studio/internal/interfaces/api"
)

func main() {
	if err := run(); err != nil {
		log.Printf("服务运行失败: %v", err)
		os.Exit(1)
	}
}

func run() error {
	rootCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	cfg, err := config.LoadConfig("configs")
	if err != nil {
		return fmt.Errorf("加载配置文件失败: %w", err)
	}

	startupCtx, cancelStartup := context.WithTimeout(rootCtx, cfg.App.StartupTimeout)
	defer cancelStartup()

	dbClient, err := database.NewClient(startupCtx, &database.PostgresConfig{
		Host:              cfg.Database.Postgres.Host,
		Port:              cfg.Database.Postgres.Port,
		User:              cfg.Database.Postgres.User,
		Password:          cfg.Database.Postgres.Password,
		DBName:            cfg.Database.Postgres.DBName,
		SSLMode:           cfg.Database.Postgres.SSLMode,
		EnableForeignKeys: cfg.Database.Postgres.EnableForeignKeys,
	})
	if err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}
	defer func() {
		if closeErr := dbClient.Close(); closeErr != nil {
			log.Printf("关闭数据库失败: %v", closeErr)
		}
	}()

	eventBus := eventbus.NewInternalEventBus()

	chatConfig := cfg.LLM.Chat
	llmAdapter, err := llm.NewOpenAIAdapter(startupCtx, llm.ChatConfig{
		APIKey:    chatConfig.APIKey,
		BaseURL:   chatConfig.BaseURL,
		Model:     chatConfig.Model,
		MaxTokens: chatConfig.MaxTokens,
		Timeout:   chatConfig.Timeout,
	})
	if err != nil {
		return fmt.Errorf("初始化 LLM 失败: %w", err)
	}

	embeddingConfig := cfg.LLM.Embedding
	embedder, err := llm.NewOpenAIEmbedder(startupCtx, llm.EmbeddingConfig{
		APIKey:  embeddingConfig.APIKey,
		BaseURL: embeddingConfig.BaseURL,
		Model:   embeddingConfig.Model,
		Timeout: embeddingConfig.Timeout,
	})
	if err != nil {
		return fmt.Errorf("初始化 Embedder 失败: %w", err)
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
	writer := agents.NewWriterAgent(llmAdapter)
	reviewer := agents.NewReviewerAgent(llmAdapter)
	librarian := agents.NewLibrarianAgent(llmAdapter, embedder, vStore, charRepo, worldRepo, agents.LibrarianConfig{
		SearchOptions: memory.SearchOptions{
			CandidateLimit: cfg.RAG.CandidateLimit,
			ResultLimit:    cfg.RAG.ResultLimit,
			MinSimilarity:  cfg.RAG.MinSimilarity,
		},
		MaxQueries:         cfg.RAG.MaxQueries,
		MaxContextMemories: cfg.RAG.MaxContextMemories,
	})

	engine, err := workflows.NewWorkflowEngine(architect, plot, director, librarian, writer, reviewer, eventBus)
	if err != nil {
		return fmt.Errorf("初始化工作流引擎失败: %w", err)
	}

	var localTestWG sync.WaitGroup
	if os.Getenv("AI_NOVEL_RUN_LOCAL_TEST") == "1" {
		localTestWG.Go(func() {
			generationID, genErr := agents.NewGenerationID()
			if genErr != nil {
				log.Printf("生成本地测试运行 ID 失败: %v", genErr)
				return
			}
			if _, runErr := engine.RunChapterGeneration(rootCtx, &agents.GenerationState{
				GenerationID: generationID,
				NovelID:      "test-novel-001",
				ChapterIndex: 1,
				Idea:         "一个普通的少年在山洞中捡到了一枚神秘的戒指，从此踏上了修仙之路。",
			}); runErr != nil && rootCtx.Err() == nil {
				log.Printf("本地测试运行失败: %v", runErr)
			}
		})
	}

	server := api.NewServer(engine, dbClient.Client, api.ServerConfig{
		ListenAddr:               cfg.App.ListenAddr,
		CorsOrigins:              cfg.App.CorsOrigins,
		MaxConcurrentGenerations: cfg.App.MaxConcurrentGenerations,
		ReadHeaderTimeout:        cfg.App.ReadHeaderTimeout,
		ReadTimeout:              cfg.App.ReadTimeout,
		WriteTimeout:             cfg.App.WriteTimeout,
		IdleTimeout:              cfg.App.IdleTimeout,
		GenerationTimeout:        cfg.App.GenerationTimeout,
	})
	httpServer := server.HTTPServer()
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("API Server started at http://%s", httpServer.Addr)
		serveErr <- httpServer.ListenAndServe()
	}()

	var runtimeErr error
	select {
	case <-rootCtx.Done():
	case listenErr := <-serveErr:
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			runtimeErr = fmt.Errorf("API Server 运行失败: %w", listenErr)
		}
		stopSignals()
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancelShutdown()
	if shutdownErr := server.Shutdown(shutdownCtx, httpServer); shutdownErr != nil {
		runtimeErr = errors.Join(runtimeErr, fmt.Errorf("API Server 停止失败: %w", shutdownErr))
	}
	localTestDone := make(chan struct{})
	go func() {
		localTestWG.Wait()
		close(localTestDone)
	}()
	select {
	case <-localTestDone:
	case <-shutdownCtx.Done():
		runtimeErr = errors.Join(runtimeErr, fmt.Errorf("等待本地测试停止失败: %w", shutdownCtx.Err()))
	}

	return runtimeErr
}
