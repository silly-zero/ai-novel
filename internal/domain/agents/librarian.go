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
	worldRepo   domain.WorldRepository
}

func NewLibrarianAgent(
	llm LLMService,
	emb memory.Embedder,
	vs memory.VectorStore,
	charRepo domain.CharacterRepository,
	worldRepo domain.WorldRepository,
) *LibrarianAgent {
	return &LibrarianAgent{
		llm:         llm,
		embedder:    emb,
		vectorStore: vs,
		charRepo:    charRepo,
		worldRepo:   worldRepo,
	}
}

func (l *LibrarianAgent) Role() AgentRole {
	return RoleLibrarian
}

// RetrievalPlan 检索计划
type RetrievalPlan struct {
	CharacterNames []string `json:"character_names"` // 需要查询的角色名
	WorldSettings  []string `json:"world_settings"`  // 需要查询的世界观名称 (地理、武学等)
	SearchQueries  []string `json:"search_queries"`  // 针对向量库的优化查询句
}

func (l *LibrarianAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 如果没有基础组件，退回到简单模式
	if l.embedder == nil || l.vectorStore == nil || l.llm == nil {
		state.Context = "（暂无背景资料，请根据大纲自由发挥）"
		return state, nil
	}

	// 2. 制定检索计划 (Query Rewriting)
	plan, err := l.makeRetrievalPlan(ctx, state)
	if err != nil {
		plan = &RetrievalPlan{SearchQueries: []string{state.Outline}}
	}

	contextBuilder := strings.Builder{}

	// 3. 检索角色档案
	seedNames := make(map[string]bool)
	for _, name := range plan.CharacterNames {
		if name != "" {
			seedNames[name] = true
		}
	}

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

	if l.charRepo != nil && len(seedNames) > 0 {
		rels, err := l.charRepo.ListRelationships(ctx, state.NovelID)
		if err == nil && len(rels) > 0 {
			contextBuilder.WriteString("【角色关系网】\n")

			neighborNames := make(map[string]bool)
			added := 0
			for _, rel := range rels {
				if rel == nil || rel.SourceCharacter == nil || rel.TargetCharacter == nil {
					continue
				}

				sName := rel.SourceCharacter.Name
				tName := rel.TargetCharacter.Name
				if sName == "" || tName == "" {
					continue
				}

				if !(seedNames[sName] || seedNames[tName]) {
					continue
				}

				contextBuilder.WriteString(fmt.Sprintf("- %s --(%s)--> %s：%s\n", sName, rel.RelationType, tName, rel.Description))
				neighborNames[sName] = true
				neighborNames[tName] = true
				added++
				if added >= 10 {
					break
				}
			}

			contextBuilder.WriteString("\n")

			contextBuilder.WriteString("【关系相关角色卡】\n")
			addedCards := 0
			for name := range neighborNames {
				if name == "" {
					continue
				}
				char, err := l.charRepo.FindByName(ctx, state.NovelID, name)
				if err == nil && char != nil {
					contextBuilder.WriteString(fmt.Sprintf("- %s: 性格(%s), 外貌(%s), 当前状态(%s)\n",
						char.Name, char.Personality, char.Appearance, char.CurrentStatus))
					addedCards++
					if addedCards >= 8 {
						break
					}
				}
			}
			contextBuilder.WriteString("\n")
		}
	}

	// 4. 检索世界观设定 (结构化数据检索)
	if l.worldRepo != nil && len(plan.WorldSettings) > 0 {
		contextBuilder.WriteString("【世界观设定】\n")
		for _, name := range plan.WorldSettings {
			setting, err := l.worldRepo.FindByName(ctx, state.NovelID, name)
			if err == nil && setting != nil {
				contextBuilder.WriteString(fmt.Sprintf("- [%s] %s: %s\n",
					setting.Category, setting.Name, setting.Description))
			}
		}
		contextBuilder.WriteString("\n")
	}

	// 5. 检索历史记忆 (向量检索)
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
  "world_settings": ["某武学境界", "某地理位置", "某势力名称"],
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
