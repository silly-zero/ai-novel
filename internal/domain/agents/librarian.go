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
		state.CanonConstraints = nil
		return state, nil
	}

	// 2. 制定检索计划 (Query Rewriting)
	plan, err := l.makeRetrievalPlan(ctx, state)
	if err != nil {
		return state, fmt.Errorf("librarian retrieval plan: %w", err)
	}

	contextBuilder := strings.Builder{}
	canonConstraints := make([]CanonConstraint, 0)
	canonSeen := make(map[string]struct{})

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
				writeCharacterLedgerEntry(&contextBuilder, char)
				appendCharacterCanonConstraints(&canonConstraints, canonSeen, char)
			}
		}
		contextBuilder.WriteString("\n")
	}

	if l.charRepo != nil && len(seedNames) > 0 {
		rels, err := l.charRepo.ListRelationships(ctx, state.NovelID)
		if err == nil && len(rels) > 0 {
			sort.SliceStable(rels, func(i, j int) bool {
				return relationshipSortKey(rels[i]) < relationshipSortKey(rels[j])
			})
			contextBuilder.WriteString("【角色关系网】\n")

			neighborNames := make([]string, 0)
			neighborSeen := make(map[string]struct{})
			added := 0
			for _, rel := range rels {
				if rel == nil || rel.SourceCharacter == nil || rel.TargetCharacter == nil {
					continue
				}

				sName := strings.TrimSpace(rel.SourceCharacter.Name)
				tName := strings.TrimSpace(rel.TargetCharacter.Name)
				if sName == "" || tName == "" {
					continue
				}

				if !(seedNames[sName] || seedNames[tName]) {
					continue
				}

				contextBuilder.WriteString(fmt.Sprintf("- %s --(%s)--> %s：%s\n", sName, rel.RelationType, tName, rel.Description))
				if rel.SourceCharacter != nil && rel.TargetCharacter != nil {
					appendRelationshipCanonConstraint(&canonConstraints, canonSeen, rel)
				}
				if _, exists := neighborSeen[sName]; !exists {
					neighborSeen[sName] = struct{}{}
					neighborNames = append(neighborNames, sName)
				}
				if _, exists := neighborSeen[tName]; !exists {
					neighborSeen[tName] = struct{}{}
					neighborNames = append(neighborNames, tName)
				}
				added++
				if added >= 10 {
					break
				}
			}

			contextBuilder.WriteString("\n")

			contextBuilder.WriteString("【关系相关角色卡】\n")
			addedCards := 0
			for _, name := range neighborNames {
				if name == "" {
					continue
				}
				char, err := l.charRepo.FindByName(ctx, state.NovelID, name)
				if err == nil && char != nil {
					writeCharacterLedgerEntry(&contextBuilder, char)
					appendCharacterCanonConstraints(&canonConstraints, canonSeen, char)
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
				writeWorldLedgerEntry(&contextBuilder, setting)
				appendWorldCanonConstraints(&canonConstraints, canonSeen, setting)
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
	state.CanonConstraints = canonConstraints
	return state, nil
}

func writeCharacterLedgerEntry(builder *strings.Builder, character *domain.Character) {
	fmt.Fprintf(
		builder,
		"- %s: 静态档案(性别:%s, 年龄:%d, 外貌:%s, 性格:%s, 背景:%s)",
		character.Name,
		character.Gender,
		character.Age,
		character.Appearance,
		character.Personality,
		character.Background,
	)
	if currentStatus := strings.TrimSpace(character.CurrentStatus); currentStatus != "" {
		fmt.Fprintf(builder, "; 当前状态:%s", currentStatus)
	}
	builder.WriteString("\n")
}

func writeWorldLedgerEntry(builder *strings.Builder, setting *domain.WorldSetting) {
	fmt.Fprintf(builder, "- [%s] %s: 静态说明:%s", setting.Category, setting.Name, setting.Description)
	if currentState := strings.TrimSpace(setting.CurrentState); currentState != "" {
		fmt.Fprintf(builder, "; 当前状态:%s", currentState)
	}
	builder.WriteString("\n")
}

func appendCharacterCanonConstraints(
	constraints *[]CanonConstraint,
	seen map[string]struct{},
	character *domain.Character,
) {
	if character == nil || strings.TrimSpace(character.Name) == "" {
		return
	}
	name := strings.TrimSpace(character.Name)
	staticParts := make([]string, 0, 6)
	if gender := strings.TrimSpace(character.Gender); gender != "" {
		staticParts = append(staticParts, "性别:"+gender)
	}
	if character.Age > 0 {
		staticParts = append(staticParts, fmt.Sprintf("年龄:%d", character.Age))
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "外貌", value: character.Appearance},
		{label: "性格", value: character.Personality},
		{label: "背景", value: character.Background},
	} {
		if value := strings.TrimSpace(field.value); value != "" {
			staticParts = append(staticParts, field.label+":"+value)
		}
	}
	if len(staticParts) > 0 {
		appendCanonConstraint(constraints, seen, CanonConstraint{
			Kind:      "character_static",
			Subject:   name,
			Statement: fmt.Sprintf("角色%s的静态档案：%s", name, strings.Join(staticParts, "；")),
		})
	}
	if status := strings.TrimSpace(character.CurrentStatus); status != "" {
		appendCanonConstraint(constraints, seen, CanonConstraint{
			Kind:      "character_current_status",
			Subject:   name,
			Statement: fmt.Sprintf("角色%s当前状态：%s", name, status),
		})
	}
}

func relationshipSortKey(relationship *domain.Relationship) string {
	if relationship == nil || relationship.SourceCharacter == nil || relationship.TargetCharacter == nil {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(relationship.SourceCharacter.Name),
		strings.TrimSpace(relationship.TargetCharacter.Name),
		strings.TrimSpace(relationship.RelationType),
		strings.TrimSpace(relationship.Description),
		strings.TrimSpace(relationship.ID),
	}, "\x00")
}

func appendRelationshipCanonConstraint(
	constraints *[]CanonConstraint,
	seen map[string]struct{},
	relationship *domain.Relationship,
) {
	if relationship == nil || relationship.SourceCharacter == nil || relationship.TargetCharacter == nil {
		return
	}
	source := strings.TrimSpace(relationship.SourceCharacter.Name)
	target := strings.TrimSpace(relationship.TargetCharacter.Name)
	relationType := strings.TrimSpace(relationship.RelationType)
	if source == "" || target == "" || relationType == "" {
		return
	}
	statement := fmt.Sprintf("角色关系：%s与%s是%s", source, target, relationType)
	if description := strings.TrimSpace(relationship.Description); description != "" {
		statement += "；" + description
	}
	appendCanonConstraint(constraints, seen, CanonConstraint{
		Kind:      "character_relationship",
		Subject:   fmt.Sprintf("%s->%s[%s]", source, target, relationType),
		Statement: statement,
	})
}

func appendWorldCanonConstraints(
	constraints *[]CanonConstraint,
	seen map[string]struct{},
	setting *domain.WorldSetting,
) {
	if setting == nil || strings.TrimSpace(setting.Name) == "" {
		return
	}
	name := strings.TrimSpace(setting.Name)
	if description := strings.TrimSpace(setting.Description); description != "" {
		appendCanonConstraint(constraints, seen, CanonConstraint{
			Kind:      "world_static",
			Subject:   name,
			Statement: fmt.Sprintf("世界设定%s的静态说明：%s", name, description),
		})
	}
	if currentState := strings.TrimSpace(setting.CurrentState); currentState != "" {
		appendCanonConstraint(constraints, seen, CanonConstraint{
			Kind:      "world_current_state",
			Subject:   name,
			Statement: fmt.Sprintf("世界设定%s当前状态：%s", name, currentState),
		})
	}
}

func appendCanonConstraint(
	constraints *[]CanonConstraint,
	seen map[string]struct{},
	constraint CanonConstraint,
) {
	if strings.TrimSpace(constraint.Statement) == "" {
		return
	}
	key := constraint.Kind + "\x00" + constraint.Subject
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*constraints = append(*constraints, constraint)
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
