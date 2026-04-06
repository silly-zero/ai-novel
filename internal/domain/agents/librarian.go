package agents

import (
	"context"
	"fmt"

	"github.com/ai-novel/studio/internal/domain/memory"
)

// LibrarianAgent 是资料管理员，负责根据当前场景，从长期/短期记忆中检索资料
type LibrarianAgent struct {
	embedder    memory.Embedder
	vectorStore memory.VectorStore
}

func NewLibrarianAgent(emb memory.Embedder, vs memory.VectorStore) *LibrarianAgent {
	return &LibrarianAgent{
		embedder:    emb,
		vectorStore: vs,
	}
}

func (l *LibrarianAgent) Role() AgentRole {
	return RoleLibrarian
}

func (l *LibrarianAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 如果没有配置向量库或 Embedder，退回到简单模式
	if l.embedder == nil || l.vectorStore == nil {
		state.Context = "（暂无背景资料，请根据大纲自由发挥）"
		return state, nil
	}

	// 2. 将当前的场景描述（大纲）转换为向量
	query := state.Outline
	queryVector, err := l.embedder.EmbedText(ctx, query)
	if err != nil {
		return state, fmt.Errorf("librarian failed to embed query: %w", err)
	}

	// 3. 从向量库中检索最相关的记忆
	entries, err := l.vectorStore.Search(ctx, state.NovelID, queryVector, 3)
	if err != nil {
		return state, fmt.Errorf("librarian failed to search vector store: %w", err)
	}

	// 4. 组装背景资料 Context
	contextStr := "【历史设定与前情提要】\n"
	if len(entries) == 0 {
		contextStr += "- 暂无相关历史记忆。\n"
	}
	for _, entry := range entries {
		contextStr += fmt.Sprintf("- %s\n", entry.Content)
	}

	state.Context = contextStr
	return state, nil
}
