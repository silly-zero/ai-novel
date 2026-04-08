package usecases

import (
	"context"
	"log"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
)

// WorldUseCase 负责处理世界观设定的维护逻辑
type WorldUseCase struct {
	agent *agents.WorldAgent
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

	log.Printf("[World] 开始分析章节中的世界观信息: NovelID=%s", e.NovelID)

	state := &agents.GenerationState{
		NovelID: e.NovelID,
		Draft:   e.Content,
	}

	_, err := uc.agent.Run(ctx, state)
	if err != nil {
		return err
	}

	log.Printf("[World] 世界观设定更新完成")
	return nil
}
