package usecases

import (
	"context"
	"log"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
)

type characterStateAgent interface {
	Run(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
}

// CharacterUseCase 负责处理人物档案的维护逻辑
type CharacterUseCase struct {
	agent characterStateAgent
}

func NewCharacterUseCase(agent *agents.CharacterAgent) *CharacterUseCase {
	return &CharacterUseCase{
		agent: agent,
	}
}

// HandleChapterGenerated 订阅并处理章节生成事件，提取角色信息
func (uc *CharacterUseCase) HandleChapterGenerated(ctx context.Context, event events.Event) error {
	e, ok := event.(events.ChapterGeneratedEvent)
	if !ok {
		return nil
	}

	log.Printf(
		"[Character] 开始分析章节角色: generation_id=%s novel_id=%s chapter_id=%s",
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
		"[Character] 角色档案处理完成: generation_id=%s novel_id=%s chapter_id=%s",
		e.GenerationID,
		e.NovelID,
		e.ChapterID,
	)
	return nil
}
