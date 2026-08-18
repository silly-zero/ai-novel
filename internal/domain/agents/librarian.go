package agents

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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

type characterCatalog struct {
	byName   map[string]*domain.Character
	byID     map[string]*domain.Character
	allNames map[string]struct{}
	ordered  []*domain.Character
}

type worldCatalog struct {
	byName   map[string]*domain.WorldSetting
	allNames map[string]struct{}
	ordered  []*domain.WorldSetting
}

func (l *LibrarianAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	if state.ChapterIndex <= 0 {
		return state, fmt.Errorf("librarian chapter index must be positive")
	}
	// 1. 如果没有基础组件，退回到简单模式
	if l.embedder == nil || l.vectorStore == nil || l.llm == nil {
		state.Context = "（暂无背景资料，请根据大纲自由发挥）"
		state.CanonConstraints = nil
		return state, nil
	}
	characterCatalog, err := l.loadCharacterCatalog(ctx, state.NovelID, state.ChapterIndex)
	if err != nil {
		return state, err
	}
	worldCatalog, err := l.loadWorldCatalog(ctx, state.NovelID, state.ChapterIndex)
	if err != nil {
		return state, err
	}

	// 2. 制定检索计划 (Query Rewriting)
	plan, err := l.makeRetrievalPlan(ctx, state)
	if err != nil {
		return state, fmt.Errorf("librarian retrieval plan: %w", err)
	}

	contextBuilder := strings.Builder{}
	canonConstraints := make([]CanonConstraint, 0)
	canonSeen := make(map[string]struct{})

	fragments := currentChapterRetrievalFragments(state)
	characters := resolveCharacters(plan.CharacterNames, characterCatalog.byName)
	settings := resolveWorldSettings(plan.WorldSettings, worldCatalog.byName)
	characters, settings = appendDeterministicEntities(
		characters,
		settings,
		fragments,
		characterCatalog,
		worldCatalog,
	)
	seedIDs := make(map[string]bool, len(characters))
	for _, character := range characters {
		if id := strings.TrimSpace(character.ID); id != "" {
			seedIDs[id] = true
		}
	}

	if len(characters) > 0 {
		contextBuilder.WriteString("【相关角色卡】\n")
		for _, character := range characters {
			writeCharacterLedgerEntry(&contextBuilder, character)
			appendCharacterCanonConstraints(&canonConstraints, canonSeen, character)
		}
		contextBuilder.WriteString("\n")
	}

	if l.charRepo != nil && len(seedIDs) > 0 {
		rels, err := l.charRepo.ListRelationshipsBeforeChapter(ctx, state.NovelID, state.ChapterIndex)
		if err != nil {
			return state, fmt.Errorf("librarian list relationships: %w", err)
		}
		if len(rels) > 0 {
			sort.SliceStable(rels, func(i, j int) bool {
				return relationshipSortKey(rels[i]) < relationshipSortKey(rels[j])
			})
			relationshipBuilder := strings.Builder{}
			neighborIDs := make([]string, 0)
			neighborSeen := make(map[string]struct{})
			relationshipSeen := make(map[string]struct{})
			added := 0
			for _, rel := range rels {
				if rel == nil || rel.SourceCharacter == nil || rel.TargetCharacter == nil {
					continue
				}

				sID := strings.TrimSpace(rel.SourceCharacter.ID)
				tID := strings.TrimSpace(rel.TargetCharacter.ID)
				if sID == "" || tID == "" || !(seedIDs[sID] || seedIDs[tID]) {
					continue
				}

				source := characterCatalog.byID[sID]
				target := characterCatalog.byID[tID]
				if source == nil || target == nil {
					continue
				}
				sName := strings.TrimSpace(source.Name)
				tName := strings.TrimSpace(target.Name)
				relationshipKey := strings.Join([]string{
					sID,
					tID,
					strings.TrimSpace(rel.RelationType),
					strings.TrimSpace(rel.Description),
				}, "\x00")
				if _, exists := relationshipSeen[relationshipKey]; exists {
					continue
				}
				relationshipSeen[relationshipKey] = struct{}{}

				relationshipBuilder.WriteString(fmt.Sprintf("- %s --(%s)--> %s：%s\n", sName, rel.RelationType, tName, rel.Description))
				appendRelationshipCanonConstraint(&canonConstraints, canonSeen, &domain.Relationship{
					ID:              rel.ID,
					NovelID:         rel.NovelID,
					SourceCharacter: source,
					TargetCharacter: target,
					RelationType:    rel.RelationType,
					Description:     rel.Description,
				})
				for _, character := range []*domain.Character{source, target} {
					id := strings.TrimSpace(character.ID)
					if seedIDs[id] {
						continue
					}
					if _, exists := neighborSeen[id]; !exists {
						neighborSeen[id] = struct{}{}
						neighborIDs = append(neighborIDs, id)
					}
				}
				added++
				if added >= 10 {
					break
				}
			}

			if added > 0 {
				contextBuilder.WriteString("【角色关系网】\n")
				contextBuilder.WriteString(relationshipBuilder.String())
				contextBuilder.WriteString("\n")

				contextBuilder.WriteString("【关系相关角色卡】\n")
				addedCards := 0
				for _, id := range neighborIDs {
					character := characterCatalog.byID[id]
					if character != nil {
						writeCharacterLedgerEntry(&contextBuilder, character)
						appendCharacterCanonConstraints(&canonConstraints, canonSeen, character)
						addedCards++
						if addedCards >= 8 {
							break
						}
					}
				}
				contextBuilder.WriteString("\n")
			}
		}
	}

	if len(settings) > 0 {
		contextBuilder.WriteString("【世界观设定】\n")
		for _, setting := range settings {
			writeWorldLedgerEntry(&contextBuilder, setting)
			appendWorldCanonConstraints(&canonConstraints, canonSeen, setting)
		}
		contextBuilder.WriteString("\n")
	}

	// 5. 检索历史记忆 (向量检索)
	queryCandidates := make([]string, 0, 1+len(plan.SearchQueries))
	if anchor := deterministicRetrievalQuery(fragments); anchor != "" {
		queryCandidates = append(queryCandidates, anchor)
	}
	queryCandidates = append(queryCandidates, plan.SearchQueries...)
	queries := uniqueLimitedStrings(queryCandidates, l.config.MaxQueries)

	bestByContent := make(map[string]rankedMemory)
	memoryOrder := 0
	if len(queries) > 0 {
		queryVectors, err := l.embedder.EmbedBatch(ctx, queries)
		if err != nil {
			return state, fmt.Errorf("librarian embed queries: %w", err)
		}
		if len(queryVectors) != len(queries) {
			return state, fmt.Errorf("librarian embed queries: got %d vectors for %d queries", len(queryVectors), len(queries))
		}

		searchOptions := l.config.SearchOptions
		searchOptions.BeforeChapterIndex = state.ChapterIndex
		for index, queryVector := range queryVectors {
			results, searchErr := l.vectorStore.Search(ctx, state.NovelID, queryVector, searchOptions)
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

func (l *LibrarianAgent) loadCharacterCatalog(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) (characterCatalog, error) {
	catalog := characterCatalog{
		byName:   make(map[string]*domain.Character),
		byID:     make(map[string]*domain.Character),
		allNames: make(map[string]struct{}),
	}
	if l.charRepo == nil {
		return catalog, nil
	}
	characters, err := l.charRepo.ListCharactersBeforeChapter(ctx, novelID, chapterIndex)
	if err != nil {
		return catalog, fmt.Errorf("librarian list characters: %w", err)
	}
	counts := make(map[string]int)
	for _, character := range characters {
		if character != nil {
			if name := strings.TrimSpace(character.Name); name != "" {
				counts[name]++
				catalog.allNames[name] = struct{}{}
				catalog.ordered = append(catalog.ordered, character)
			}
		}
	}
	sort.SliceStable(catalog.ordered, func(i, j int) bool {
		leftName := strings.TrimSpace(catalog.ordered[i].Name)
		rightName := strings.TrimSpace(catalog.ordered[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return catalog.ordered[i].ID < catalog.ordered[j].ID
	})
	for _, character := range catalog.ordered {
		name := strings.TrimSpace(character.Name)
		if counts[name] == 1 {
			catalog.byName[name] = character
			if id := strings.TrimSpace(character.ID); id != "" {
				catalog.byID[id] = character
			}
		}
	}
	return catalog, nil
}

func (l *LibrarianAgent) loadWorldCatalog(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) (worldCatalog, error) {
	catalog := worldCatalog{
		byName:   make(map[string]*domain.WorldSetting),
		allNames: make(map[string]struct{}),
	}
	if l.worldRepo == nil {
		return catalog, nil
	}
	settings, err := l.worldRepo.ListWorldSettingsBeforeChapter(ctx, novelID, chapterIndex)
	if err != nil {
		return catalog, fmt.Errorf("librarian list world settings: %w", err)
	}
	counts := make(map[string]int)
	for _, setting := range settings {
		if setting != nil {
			if name := strings.TrimSpace(setting.Name); name != "" {
				counts[name]++
				catalog.allNames[name] = struct{}{}
				catalog.ordered = append(catalog.ordered, setting)
			}
		}
	}
	sort.SliceStable(catalog.ordered, func(i, j int) bool {
		leftName := strings.TrimSpace(catalog.ordered[i].Name)
		rightName := strings.TrimSpace(catalog.ordered[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return catalog.ordered[i].ID < catalog.ordered[j].ID
	})
	for _, setting := range catalog.ordered {
		name := strings.TrimSpace(setting.Name)
		if counts[name] == 1 {
			catalog.byName[name] = setting
		}
	}
	return catalog, nil
}

func resolveCharacters(names []string, byName map[string]*domain.Character) []*domain.Character {
	characters := make([]*domain.Character, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		character := byName[name]
		if character == nil {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		characters = append(characters, character)
	}
	return characters
}

func resolveWorldSettings(names []string, byName map[string]*domain.WorldSetting) []*domain.WorldSetting {
	settings := make([]*domain.WorldSetting, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		setting := byName[name]
		if setting == nil {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		settings = append(settings, setting)
	}
	return settings
}

const deterministicRetrievalQueryMaxRunes = 1000

func deterministicRetrievalQuery(fragments []string) string {
	query := strings.Join(fragments, "；")
	if utf8.RuneCountInString(query) <= deterministicRetrievalQueryMaxRunes {
		return query
	}
	runes := []rune(query)
	return string(runes[:deterministicRetrievalQueryMaxRunes])
}

func currentChapterRetrievalFragments(state *GenerationState) []string {
	fragments := make([]string, 0, 8+len(state.ChapterContract.MustHappen)+len(state.PreviousContinuity.OpenLoops))
	appendFragment := func(value string) {
		if value = strings.TrimSpace(value); value != "" {
			fragments = append(fragments, value)
		}
	}
	if state.ChapterContract.IsEmpty() {
		appendFragment(state.Outline)
	} else {
		appendFragment(state.ChapterContract.Goal)
		for _, event := range state.ChapterContract.MustHappen {
			appendFragment(event)
		}
		appendFragment(state.ChapterContract.EndState)
	}
	appendFragment(state.MainlineBeat.CurrentEvent)
	appendFragment(state.PreviousContinuity.LastBeat)
	for _, openLoop := range state.PreviousContinuity.OpenLoops {
		appendFragment(openLoop)
	}
	appendFragment(state.PreviousContinuity.NextAction)
	appendFragment(state.ManualContext)
	return fragments
}

type retrievalEntityCandidate struct {
	name      string
	character *domain.Character
	setting   *domain.WorldSetting
}

func appendDeterministicEntities(
	characters []*domain.Character,
	settings []*domain.WorldSetting,
	fragments []string,
	characterCatalog characterCatalog,
	worldCatalog worldCatalog,
) ([]*domain.Character, []*domain.WorldSetting) {
	candidates := deterministicEntityCandidates(characterCatalog, worldCatalog)
	characterSeen := make(map[string]struct{}, len(characters))
	for _, character := range characters {
		characterSeen[strings.TrimSpace(character.Name)] = struct{}{}
	}
	settingSeen := make(map[string]struct{}, len(settings))
	for _, setting := range settings {
		settingSeen[strings.TrimSpace(setting.Name)] = struct{}{}
	}

	for _, fragment := range fragments {
		for offset := 0; offset < len(fragment); {
			matched := false
			for _, candidate := range candidates {
				if !entityNameMatchesAt(fragment, offset, candidate.name) {
					continue
				}
				if candidate.character != nil {
					if _, exists := characterSeen[candidate.name]; !exists {
						characterSeen[candidate.name] = struct{}{}
						characters = append(characters, candidate.character)
					}
				} else if _, exists := settingSeen[candidate.name]; !exists {
					settingSeen[candidate.name] = struct{}{}
					settings = append(settings, candidate.setting)
				}
				offset += len(candidate.name)
				matched = true
				break
			}
			if !matched {
				_, size := utf8.DecodeRuneInString(fragment[offset:])
				offset += size
			}
		}
	}
	return characters, settings
}

func deterministicEntityCandidates(characterCatalog characterCatalog, worldCatalog worldCatalog) []retrievalEntityCandidate {
	candidates := make([]retrievalEntityCandidate, 0, len(characterCatalog.byName)+len(worldCatalog.byName))
	for name, character := range characterCatalog.byName {
		if _, ambiguous := worldCatalog.allNames[name]; ambiguous || unsafeSingleRuneName(name) {
			continue
		}
		candidates = append(candidates, retrievalEntityCandidate{name: name, character: character})
	}
	for name, setting := range worldCatalog.byName {
		if _, ambiguous := characterCatalog.allNames[name]; ambiguous || unsafeSingleRuneName(name) {
			continue
		}
		candidates = append(candidates, retrievalEntityCandidate{name: name, setting: setting})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftLength := utf8.RuneCountInString(candidates[i].name)
		rightLength := utf8.RuneCountInString(candidates[j].name)
		if leftLength != rightLength {
			return leftLength > rightLength
		}
		return candidates[i].name < candidates[j].name
	})
	return candidates
}

func unsafeSingleRuneName(name string) bool {
	if utf8.RuneCountInString(name) != 1 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return r > unicode.MaxASCII
}

func entityNameMatchesAt(text string, offset int, name string) bool {
	if !strings.HasPrefix(text[offset:], name) {
		return false
	}
	first, _ := utf8.DecodeRuneInString(name)
	if isASCIIIdentifierRune(first) && offset > 0 {
		left, _ := utf8.DecodeLastRuneInString(text[:offset])
		if isASCIIIdentifierRune(left) {
			return false
		}
	}
	last, _ := utf8.DecodeLastRuneInString(name)
	end := offset + len(name)
	if isASCIIIdentifierRune(last) && end < len(text) {
		right, _ := utf8.DecodeRuneInString(text[end:])
		if isASCIIIdentifierRune(right) {
			return false
		}
	}
	return true
}

func isASCIIIdentifierRune(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
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
	systemPrompt := `你是一位资深小说资料员。你的任务是分析提供的【当前章正向资料】，制定一个检索计划，以便为主笔提供最准确的背景资料。
请输出 JSON 格式：
{
  "character_names": ["角色A", "角色B"],
  "world_settings": ["某武学境界", "某地理位置", "某势力名称"],
  "search_queries": ["角色A和角色B之前的关系如何？", "关于某地点的历史设定是什么？"]
}
没有合适的向量查询时，search_queries 可以是空数组。
务必确保输出是合法的 JSON。`

	fragments := currentChapterRetrievalFragments(state)
	userPrompt := "【当前章正向资料】\n"
	if len(fragments) == 0 {
		userPrompt += "（未提供）\n"
	} else {
		for _, fragment := range fragments {
			userPrompt += "- " + fragment + "\n"
		}
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
	return nil
}
