package usecases

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/ai-novel/studio/internal/domain/memory"
)

// IngestionUseCase 负责处理记忆注入逻辑
type IngestionUseCase struct {
	llm      agents.LLMService
	embedder memory.Embedder
	vStore   memory.VectorStore
}

func NewIngestionUseCase(llm agents.LLMService, emb memory.Embedder, vs memory.VectorStore) *IngestionUseCase {
	return &IngestionUseCase{
		llm:      llm,
		embedder: emb,
		vStore:   vs,
	}
}

// HandleChapterGenerated 订阅并处理章节生成事件
func (uc *IngestionUseCase) HandleChapterGenerated(ctx context.Context, event events.Event) error {
	e, ok := event.(events.ChapterGeneratedEvent)
	if !ok {
		return nil
	}

	log.Printf("[Ingestion] 开始处理章节记忆注入: NovelID=%s, ChapterID=%s", e.NovelID, e.ChapterID)

	// 1. 提取剧情摘要与关键设定 (利用 LLM 压缩信息，避免向量库冗余)
	summary, err := uc.extractSummary(ctx, e.Content)
	if err != nil {
		return fmt.Errorf("failed to extract summary: %w", err)
	}

	// 2. 向量化摘要
	vector, err := uc.embedder.EmbedText(ctx, summary)
	if err != nil {
		return fmt.Errorf("failed to embed summary: %w", err)
	}

	// 3. 存入向量数据库
	entry := &memory.MemoryEntry{
		ID:        fmt.Sprintf("mem_%d", time.Now().UnixNano()),
		NovelID:   e.NovelID,
		Content:   summary,
		Embedding: vector,
		Metadata: map[string]interface{}{
			"chapter_id": e.ChapterID,
			"type":       "plot_summary",
		},
	}

	if err := uc.vStore.Add(ctx, []*memory.MemoryEntry{entry}); err != nil {
		return fmt.Errorf("failed to add memory entry: %w", err)
	}

	log.Printf("[Ingestion] 章节记忆注入成功: %s", summary)
	return nil
}

func (uc *IngestionUseCase) extractSummary(ctx context.Context, content string) (string, error) {
	systemPrompt := "你是一位专业的小说编辑。请从提供的章节正文中，提取出对后续剧情有影响的关键信息（包括但不限于：新出现的角色、重要道具、伏笔、角色关系变动、核心剧情进展）。请用简练的一句话概括。"
	userPrompt := fmt.Sprintf("正文内容：\n%s\n\n请提取关键信息：", content)

	summary, err := uc.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}

	return summary, nil
}
