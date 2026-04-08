package usecases

import (
	"context"
	"log"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
)

// CharacterUseCase 负责处理人物档案的维护逻辑
type CharacterUseCase struct {
	agent *agents.CharacterAgent
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

	log.Printf("[Character] 开始分析章节中的角色信息: NovelID=%s", e.NovelID)

	state := &agents.GenerationState{
		NovelID: e.NovelID,
		Draft:   e.Content,
	}

	_, err := uc.agent.Run(ctx, state)
	if err != nil {
		return err
	}

	log.Printf("[Character] 角色档案更新完成")
	return nil
}
