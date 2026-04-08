package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-novel/studio/internal/domain/memory"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

// LibrarianAgent 是资料管理员，负责根据当前场景，从长期/短期记忆中检索资料
type LibrarianAgent struct {
	llm         LLMService
	embedder    memory.Embedder
	vectorStore memory.VectorStore
	charRepo    domain.CharacterRepository
}

func NewLibrarianAgent(llm LLMService, emb memory.Embedder, vs memory.VectorStore, charRepo domain.CharacterRepository) *LibrarianAgent {
	return &LibrarianAgent{
		llm:         llm,
		embedder:    emb,
		vectorStore: vs,
		charRepo:    charRepo,
	}
}

func (l *LibrarianAgent) Role() AgentRole {
	return RoleLibrarian
}

// RetrievalPlan 检索计划
type RetrievalPlan struct {
	CharacterNames []string `json:"character_names"` // 需要查询的角色名
	SearchQueries  []string `json:"search_queries"`  // 针对向量库的优化查询句
}

func (l *LibrarianAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 如果没有基础组件，退回到简单模式
	if l.embedder == nil || l.vectorStore == nil || l.llm == nil {
		state.Context = "（暂无背景资料，请根据大纲自由发挥）"
		return state, nil
	}

	// 2. 制定检索计划 (Query Rewriting)
	// 让 LLM 分析当前场景，决定搜什么
	plan, err := l.makeRetrievalPlan(ctx, state)
	if err != nil {
		// 容错：如果计划制定失败，直接拿大纲搜
		plan = &RetrievalPlan{SearchQueries: []string{state.Outline}}
	}

	contextBuilder := strings.Builder{}

	// 3. 检索角色档案 (结构化数据检索)
	if l.charRepo != nil && len(plan.CharacterNames) > 0 {
		contextBuilder.WriteString("【相关角色卡】\n")
		for _, name := range plan.CharacterNames {
			char, err := l.charRepo.FindByName(ctx, state.NovelID, name)
			if err == nil && char != nil {
				contextBuilder.WriteString(fmt.Sprintf("- %s: 性格(%s), 外貌(%s), 当前状态(%s)\n",
					char.Name, char.Personality, char.Appearance, char.CurrentStatus))
			}
		}
		contextBuilder.WriteString("\n")
	}

	// 4. 检索历史记忆 (向量检索)
	contextBuilder.WriteString("【前情提要与伏笔】\n")
	allMemories := make(map[string]bool) // 去重
	for _, query := range plan.SearchQueries {
		queryVector, err := l.embedder.EmbedText(ctx, query)
		if err != nil {
			continue
		}
		entries, err := l.vectorStore.Search(ctx, state.NovelID, queryVector, 2)
		if err == nil {
			for _, entry := range entries {
				if !allMemories[entry.Content] {
					contextBuilder.WriteString(fmt.Sprintf("- %s\n", entry.Content))
					allMemories[entry.Content] = true
				}
			}
		}
	}

	state.Context = contextBuilder.String()
	return state, nil
}

func (l *LibrarianAgent) makeRetrievalPlan(ctx context.Context, state *GenerationState) (*RetrievalPlan, error) {
	systemPrompt := `你是一位资深小说资料员。你的任务是分析提供的【场景卡】或【大纲】，制定一个检索计划，以便为主笔提供最准确的背景资料。
请输出 JSON 格式：
{
  "character_names": ["角色A", "角色B"],
  "search_queries": ["角色A和角色B之前的关系如何？", "关于某地点的历史设定是什么？"]
}
务必确保输出是合法的 JSON。`

	userPrompt := fmt.Sprintf("【场景描述】\n%s\n\n【本章大纲】\n%s\n\n请输出检索计划：", state.SceneCard, state.Outline)

	resp, err := l.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}

	var plan RetrievalPlan
	cleanedJSON := strings.TrimPrefix(strings.TrimSpace(resp), "```json")
	cleanedJSON = strings.TrimSuffix(cleanedJSON, "```")
	if err := json.Unmarshal([]byte(cleanedJSON), &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

