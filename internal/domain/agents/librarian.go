package agents

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/ai-novel/studio/internal/domain/memory"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

type LibrarianConfig struct {
	SearchOptions      memory.SearchOptions
	MaxQueries         int
	MaxContextMemories int
}

// LibrarianAgent 是资料管理员，负责根据当前场景，从长期/短期记忆中检索资料
type LibrarianAgent struct {
	llm         LLMService
	embedder    memory.Embedder
	vectorStore memory.VectorStore
	charRepo    domain.CharacterRepository
	worldRepo   domain.WorldRepository
	config      LibrarianConfig
}

func NewLibrarianAgent(
	llm LLMService,
	emb memory.Embedder,
	vs memory.VectorStore,
	charRepo domain.CharacterRepository,
	worldRepo domain.WorldRepository,
	config LibrarianConfig,
) *LibrarianAgent {
	return &LibrarianAgent{
		llm:         llm,
		embedder:    emb,
		vectorStore: vs,
		charRepo:    charRepo,
		worldRepo:   worldRepo,
		config:      config,
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

	if state.Context != "" {
		return state, nil
	}

	// 2. 制定检索计划 (Query Rewriting)
	plan, err := l.makeRetrievalPlan(ctx, state)
	if err != nil {
		return state, fmt.Errorf("librarian retrieval plan: %w", err)
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
	queries := uniqueLimitedStrings(plan.SearchQueries, l.config.MaxQueries)
	queryVectors, err := l.embedder.EmbedBatch(ctx, queries)
	if err != nil {
		return state, fmt.Errorf("librarian embed queries: %w", err)
	}
	if len(queryVectors) != len(queries) {
		return state, fmt.Errorf("librarian embed queries: got %d vectors for %d queries", len(queryVectors), len(queries))
	}

	bestByContent := make(map[string]rankedMemory)
	memoryOrder := 0
	for index, queryVector := range queryVectors {
		results, searchErr := l.vectorStore.Search(ctx, state.NovelID, queryVector, l.config.SearchOptions)
		if searchErr != nil {
			return state, fmt.Errorf("librarian search query %d: %w", index, searchErr)
		}
		for _, result := range results {
			if result.Entry == nil || math.IsNaN(float64(result.Score)) ||
				math.IsInf(float64(result.Score), 0) || result.Score < l.config.SearchOptions.MinSimilarity {
				continue
			}
			content := strings.TrimSpace(result.Entry.Content)
			if content == "" {
				continue
			}
			current, exists := bestByContent[content]
			if !exists {
				bestByContent[content] = rankedMemory{result: result, order: memoryOrder}
				memoryOrder++
			} else if memoryResultLess(result, current.result) {
				current.result = result
				bestByContent[content] = current
			}
		}
	}

	memories := make([]rankedMemory, 0, len(bestByContent))
	for _, result := range bestByContent {
		memories = append(memories, result)
	}
	sort.SliceStable(memories, func(i, j int) bool {
		if memoryResultLess(memories[i].result, memories[j].result) {
			return true
		}
		if memoryResultLess(memories[j].result, memories[i].result) {
			return false
		}
		return memories[i].order < memories[j].order
	})
	if len(memories) > l.config.MaxContextMemories {
		memories = memories[:l.config.MaxContextMemories]
	}
	if len(memories) > 0 {
		contextBuilder.WriteString("【前情提要与伏笔】\n")
		for _, ranked := range memories {
			fmt.Fprintf(&contextBuilder, "- %s\n", strings.TrimSpace(ranked.result.Entry.Content))
		}
	}

	state.Context = contextBuilder.String()
	return state, nil
}

type rankedMemory struct {
	result memory.SearchResult
	order  int
}

func uniqueLimitedStrings(values []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	result := make([]string, 0, min(len(values), limit))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func memoryResultLess(left, right memory.SearchResult) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	return left.Entry.ID < right.Entry.ID
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

	userPrompt := fmt.Sprintf("【场景描述】\n%s\n\n【本章大纲】\n%s\n\n%s\n", state.SceneCard, state.Outline, continuityPrompt(state.PreviousContinuity))
	if state.EditorNotes != "" {
		userPrompt += fmt.Sprintf("\n【作者指令（人工干预）】\n%s\n", state.EditorNotes)
	}
	userPrompt += "\n请输出检索计划："

	plan, err := generateStructuredResponse(
		ctx,
		l.llm,
		"librarian",
		systemPrompt,
		userPrompt,
		decodeJSON[RetrievalPlan],
		validateRetrievalPlan,
	)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

func validateRetrievalPlan(plan *RetrievalPlan) error {
	for _, field := range []struct {
		name   string
		values *[]string
	}{
		{name: "character_names", values: &plan.CharacterNames},
		{name: "world_settings", values: &plan.WorldSettings},
		{name: "search_queries", values: &plan.SearchQueries},
	} {
		for index := range *field.values {
			(*field.values)[index] = strings.TrimSpace((*field.values)[index])
			if (*field.values)[index] == "" {
				return fmt.Errorf("%s[%d] must not be blank", field.name, index)
			}
		}
	}
	if len(plan.SearchQueries) == 0 {
		return fmt.Errorf("search_queries must contain at least one query")
	}
	return nil
}
