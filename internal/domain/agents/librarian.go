package agents

import (
	"context"
	"fmt"
	"github.com/ai-novel/studio/internal/domain/memory"
)

// LibrarianAgent 是资料管理员，负责根据当前场景，从长期/短期记忆中检索资料
type LibrarianAgent struct {
	longTermMemory memory.LongTermMemory
}

func NewLibrarianAgent(ltm memory.LongTermMemory) *LibrarianAgent {
	return &LibrarianAgent{longTermMemory: ltm}
}

func (l *LibrarianAgent) Role() AgentRole {
	return RoleLibrarian
}

func (l *LibrarianAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 在实际应用中，这里可能会先让 LLM 根据 SceneCard 提取检索关键词 (Query)
	// 为了简化，目前我们直接把大纲或场景卡的一部分作为查询条件
	query := state.Outline

	var contextStr string

	// 如果有长期记忆的实现，进行 RAG 检索
	if l.longTermMemory != nil {
		entries, err := l.longTermMemory.Retrieve(ctx, state.NovelID, query, 3)
		if err != nil {
			return state, fmt.Errorf("librarian failed to retrieve memory: %w", err)
		}
		
		contextStr = "【历史设定与前情提要】\n"
		for _, entry := range entries {
			contextStr += fmt.Sprintf("- %s\n", entry.Content)
		}
	} else {
		contextStr = "（暂无背景资料，请根据大纲自由发挥）"
	}

	state.Context = contextStr
	return state, nil
}
