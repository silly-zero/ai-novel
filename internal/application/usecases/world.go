package usecases

import (
	"context"
	"log"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
)

type worldStateAgent interface {
	Run(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
}

// WorldUseCase 负责处理世界观设定的维护逻辑
type WorldUseCase struct {
	agent worldStateAgent
}

func NewWorldUseCase(agent *agents.WorldAgent) *WorldUseCase {
	return &WorldUseCase{
		agent: agent,
	}
}

// HandleChapterGenerated 订阅并处理章节生成事件，提取世界观信息
func (uc *WorldUseCase) HandleChapterGenerated(ctx context.Context, event events.Event) error {
	e, ok := event.(events.ChapterGeneratedEvent)
	if !ok {
		return nil
	}

	log.Printf(
		"[World] 开始分析章节世界观: generation_id=%s novel_id=%s chapter_id=%s",
		e.GenerationID,
		e.NovelID,
		e.ChapterID,
	)

	state := &agents.GenerationState{
		GenerationID: e.GenerationID,
		NovelID:      e.NovelID,
		ChapterID:    e.ChapterID,
		ChapterIndex: e.ChapterIndex,
		Draft:        e.Content,
	}

	_, err := uc.agent.Run(ctx, state)
	if err != nil {
		return err
	}

	log.Printf(
		"[World] 世界观处理完成: generation_id=%s novel_id=%s chapter_id=%s",
		e.GenerationID,
		e.NovelID,
		e.ChapterID,
	)
	return nil
}
