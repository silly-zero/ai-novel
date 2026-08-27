package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domain "github.com/ai-novel/studio/internal/domain/novel"
)

const (
	maxCharacterTextRunes  = 2000
	maxCharacterStateRunes = 1000
)

func decodeCharacterExtraction(candidate []byte) (CharacterExtraction, error) {
	trimmed := bytes.TrimSpace(candidate)
	if len(trimmed) == 0 {
		return CharacterExtraction{}, fmt.Errorf("response is empty")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return CharacterExtraction{}, fmt.Errorf("character extraction must not be null")
	}
	if trimmed[0] == '[' {
		var updates []CharacterUpdate
		if err := json.Unmarshal(trimmed, &updates); err != nil {
			return CharacterExtraction{}, err
		}
		if updates == nil {
			return CharacterExtraction{}, fmt.Errorf("character array must not be null")
		}
		return CharacterExtraction{Characters: updates}, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return CharacterExtraction{}, err
	}
	charactersJSON, hasCharacters := raw["characters"]
	relationshipsJSON, hasRelationships := raw["relationships"]
	if !hasCharacters && !hasRelationships {
		return CharacterExtraction{}, fmt.Errorf("characters or relationships is required")
	}

	var extracted CharacterExtraction
	if hasCharacters {
		if bytes.Equal(bytes.TrimSpace(charactersJSON), []byte("null")) {
			return CharacterExtraction{}, fmt.Errorf("characters must be an array")
		}
		if err := json.Unmarshal(charactersJSON, &extracted.Characters); err != nil {
			return CharacterExtraction{}, fmt.Errorf("characters must be an array: %w", err)
		}
	}
	if hasRelationships {
		if bytes.Equal(bytes.TrimSpace(relationshipsJSON), []byte("null")) {
			return CharacterExtraction{}, fmt.Errorf("relationships must be an array")
		}
		if err := json.Unmarshal(relationshipsJSON, &extracted.Relationships); err != nil {
			return CharacterExtraction{}, fmt.Errorf("relationships must be an array: %w", err)
		}
	}
	return extracted, nil
}

func validateCharacterExtraction(extracted *CharacterExtraction) error {
	for index := range extracted.Characters {
		character := &extracted.Characters[index]
		character.Name = strings.TrimSpace(character.Name)
		character.Gender = strings.TrimSpace(character.Gender)
		character.Appearance = strings.TrimSpace(character.Appearance)
		character.Personality = strings.TrimSpace(character.Personality)
		character.Background = strings.TrimSpace(character.Background)
		character.CurrentStatus = strings.TrimSpace(character.CurrentStatus)
		if character.Name == "" {
			return fmt.Errorf("characters[%d].name must not be blank", index)
		}
		if character.CurrentStatus == "" {
			return fmt.Errorf("characters[%d].current_status must not be blank", index)
		}
		for fieldName, value := range map[string]string{
			"gender":         character.Gender,
			"appearance":     character.Appearance,
			"personality":    character.Personality,
			"background":     character.Background,
			"current_status": character.CurrentStatus,
		} {
			limit := maxCharacterTextRunes
			if fieldName == "current_status" {
				limit = maxCharacterStateRunes
			}
			if len([]rune(value)) > limit {
				return fmt.Errorf("characters[%d].%s exceeds %d characters", index, fieldName, limit)
			}
		}
	}
	for index := range extracted.Relationships {
		relation := &extracted.Relationships[index]
		relation.Source = strings.TrimSpace(relation.Source)
		relation.Target = strings.TrimSpace(relation.Target)
		relation.RelationType = strings.TrimSpace(relation.RelationType)
		relation.Description = strings.TrimSpace(relation.Description)
		if relation.Operation == "" {
			relation.Operation = domain.RelationshipOperationUpsert
		}
		if relation.Operation != domain.RelationshipOperationUpsert && relation.Operation != domain.RelationshipOperationRemove {
			return fmt.Errorf("relationships[%d].operation must be upsert or remove", index)
		}
		if relation.Source == "" {
			return fmt.Errorf("relationships[%d].source must not be blank", index)
		}
		if relation.Target == "" {
			return fmt.Errorf("relationships[%d].target must not be blank", index)
		}
		if relation.RelationType == "" {
			return fmt.Errorf("relationships[%d].relation_type must not be blank", index)
		}
	}
	return nil
}

type CharacterAgent struct {
	llm  LLMService
	repo domain.CharacterRepository
}

func NewCharacterAgent(llm LLMService, repo domain.CharacterRepository) *CharacterAgent {
	return &CharacterAgent{
		llm:  llm,
		repo: repo,
	}
}

func (a *CharacterAgent) Role() AgentRole {
	return RoleCharacter
}

// CharacterUpdate 结构化输出
type CharacterUpdate struct {
	Name             string `json:"name"`
	Gender           string `json:"gender"`
	Age              int    `json:"age"`
	Appearance       string `json:"appearance"`
	Personality      string `json:"personality"`
	Background       string `json:"background"`
	CurrentStatus    string `json:"current_status"`
	IdentityEvidence string `json:"identity_evidence"`
	StaticEvidence   string `json:"static_evidence"`
	StateEvidence    string `json:"state_evidence"`
}

type RelationshipUpdate struct {
	Source       string                       `json:"source"`
	Target       string                       `json:"target"`
	RelationType string                       `json:"relation_type"`
	Description  string                       `json:"description"`
	Operation    domain.RelationshipOperation `json:"operation"`
	Evidence     string                       `json:"evidence"`
}

type CharacterExtraction struct {
	Characters    []CharacterUpdate    `json:"characters"`
	Relationships []RelationshipUpdate `json:"relationships"`
}

func validateCharacterExtractionForDraft(
	extracted *CharacterExtraction,
	existing []*domain.Character,
	draft string,
) error {
	if err := validateCharacterExtraction(extracted); err != nil {
		return err
	}
	existingByName := make(map[string]*domain.Character, len(existing))
	for _, character := range existing {
		existingByName[strings.TrimSpace(character.Name)] = character
	}
	for index := range extracted.Characters {
		update := &extracted.Characters[index]
		if err := validateLedgerEvidence(fmt.Sprintf("characters[%d].identity_evidence", index), update.IdentityEvidence, draft, true); err != nil {
			return err
		}
		existingCharacter := existingByName[update.Name]
		hasStaticUpdate := update.Gender != "" || update.Age != 0 || update.Appearance != "" || update.Personality != "" || update.Background != ""
		staticRequired := (existingCharacter == nil && hasStaticUpdate) ||
			(existingCharacter != nil && strings.TrimSpace(existingCharacter.Gender) == "" && update.Gender != "") ||
			(existingCharacter != nil && existingCharacter.Age == 0 && update.Age != 0) ||
			(existingCharacter != nil && strings.TrimSpace(existingCharacter.Appearance) == "" && update.Appearance != "") ||
			(existingCharacter != nil && strings.TrimSpace(existingCharacter.Personality) == "" && update.Personality != "") ||
			(existingCharacter != nil && strings.TrimSpace(existingCharacter.Background) == "" && update.Background != "")
		if err := validateLedgerEvidence(fmt.Sprintf("characters[%d].static_evidence", index), update.StaticEvidence, draft, staticRequired); err != nil {
			return err
		}
		if err := validateLedgerEvidence(fmt.Sprintf("characters[%d].state_evidence", index), update.StateEvidence, draft, true); err != nil {
			return err
		}
	}
	for index := range extracted.Relationships {
		if err := validateLedgerEvidence(fmt.Sprintf("relationships[%d].evidence", index), extracted.Relationships[index].Evidence, draft, true); err != nil {
			return err
		}
	}
	return nil
}

func (a *CharacterAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	ref := domain.ChapterStateRef{
		NovelID:      state.NovelID,
		ChapterID:    state.ChapterID,
		ChapterIndex: state.ChapterIndex,
		GenerationID: state.GenerationID,
	}
	ref.Normalize()
	if err := ref.Validate(); err != nil {
		return state, fmt.Errorf("character chapter state: %w", err)
	}
	// 1. 获取当前章节之前的所有角色档案 (用于提供给 LLM 参考)
	existingChars, err := a.repo.ListCharactersBeforeChapter(ctx, ref.NovelID, ref.ChapterIndex)
	if err != nil {
		return state, fmt.Errorf("list existing characters: %w", err)
	}
	existingRelationships, err := a.repo.ListRelationshipsBeforeChapter(ctx, ref.NovelID, ref.ChapterIndex)
	if err != nil {
		return state, fmt.Errorf("list existing relationships: %w", err)
	}
	var charContext strings.Builder
	charContext.WriteString("【现有角色账本】\n")
	for _, c := range existingChars {
		fmt.Fprintf(&charContext,
			"- %s: 静态档案(性别:%s, 年龄:%d, 外貌:%s, 性格:%s, 背景:%s); 当前状态:%s\n",
			c.Name, c.Gender, c.Age, c.Appearance, c.Personality, c.Background, c.CurrentStatus,
		)
	}
	charContext.WriteString("\n【当前章之前的角色关系】\n")
	for _, relationship := range existingRelationships {
		if relationship == nil || relationship.SourceCharacter == nil || relationship.TargetCharacter == nil {
			continue
		}
		fmt.Fprintf(
			&charContext,
			"- %s --(%s)--> %s：%s\n",
			relationship.SourceCharacter.Name,
			relationship.RelationType,
			relationship.TargetCharacter.Name,
			relationship.Description,
		)
	}

	systemPrompt := `你是一位专业的小说人物状态分析师。你的任务是从【本章正文】提取角色账本更新。
要求：
1. 只输出正文中有明确依据的重要角色和关系。
2. 静态档案（性别、年龄、外貌、性格、背景）是长期事实；已有角色的静态字段不要改写，若正文没有新信息可留空。
3. current_status 是本章结束时的动态快照，必须具体描述角色的位置、处境、目标、立场或持有物；已有角色也必须根据正文更新它。
4. 新角色可以填写静态档案，但必须填写 current_status。
5. 未提到的关系不要输出，不代表删除；只有正文明确建立、更新或解除关系时才输出。
6. operation 为 upsert 或 remove。旧格式未提供 operation 时按 upsert 处理；关系改变时先 remove 旧类型，再 upsert 新类型。
7. 所有 evidence 必须逐字复制自【本章正文】中的一段连续原文，长度不超过1000字；不得改写、概括或引用现有账本。
8. 每个角色必须提供 identity_evidence 和 state_evidence；仅当新增或补齐静态字段时提供 static_evidence，否则留空。
9. 每个关系 upsert/remove 都必须提供 evidence；正文没有明确证据时不要输出该更新。
10. 不要用空字符串清除已有信息。
11. 只输出合法 JSON，不要输出 Markdown 或解释。
格式：
{
  "characters": [
    {
      "name": "角色名",
      "gender": "新角色的性别，否则留空",
      "age": 20,
      "appearance": "新信息，否则留空",
      "personality": "新信息，否则留空",
      "background": "新信息，否则留空",
      "current_status": "本章结束时的具体动态状态",
      "identity_evidence": "正文中的角色原文",
      "static_evidence": "支持静态字段的正文原文，没有静态更新则留空",
      "state_evidence": "支持当前状态的正文原文"
    }
  ],
  "relationships": [
    {
      "source": "角色A",
      "target": "角色B",
      "relation_type": "师徒/敌人/盟友/亲属/恋人/交易等",
      "description": "一句话说明正文依据",
      "operation": "upsert 或 remove",
      "evidence": "支持关系变化的正文原文"
    }
  ]
}`

	userPrompt := fmt.Sprintf("%s\n\n【本章正文】\n%s\n\n请分析并输出角色账本更新结果：", charContext.String(), state.Draft)

	extracted, err := generateStructuredObjectResponse(
		ctx,
		a.llm,
		"character",
		systemPrompt,
		userPrompt,
		decodeCharacterExtraction,
		func(extracted *CharacterExtraction) error {
			return validateCharacterExtractionForDraft(extracted, existingChars, state.Draft)
		},
	)
	if err != nil {
		return state, err
	}

	extractedNames := make(map[string]struct{}, len(extracted.Characters))
	for _, update := range extracted.Characters {
		extractedNames[update.Name] = struct{}{}
	}
	existingRelationshipCharacters := make(map[string]*domain.Character)
	for _, rel := range extracted.Relationships {
		for _, name := range []string{rel.Source, rel.Target} {
			if _, exists := extractedNames[name]; exists {
				continue
			}
			if existingRelationshipCharacters[name] != nil {
				continue
			}
			character, err := a.repo.FindByName(ctx, ref.NovelID, name)
			if err != nil {
				return state, fmt.Errorf("resolve relationship character %q: %w", name, err)
			}
			existingRelationshipCharacters[name] = character
		}
	}

	characterSnapshots := make([]*domain.Character, 0, len(extracted.Characters))
	for _, update := range extracted.Characters {
		characterSnapshots = append(characterSnapshots, &domain.Character{
			Name:          update.Name,
			Gender:        update.Gender,
			Age:           update.Age,
			Appearance:    update.Appearance,
			Personality:   update.Personality,
			Background:    update.Background,
			CurrentStatus: update.CurrentStatus,
		})
	}
	canonicalCharacters, err := a.repo.ReplaceChapterCharacters(ctx, ref, characterSnapshots)
	if err != nil {
		return state, fmt.Errorf("replace chapter character states: %w", err)
	}
	nameToChar := make(map[string]*domain.Character, len(canonicalCharacters))
	for _, character := range canonicalCharacters {
		nameToChar[character.Name] = character
	}

	relationshipChanges := make([]domain.RelationshipChange, 0, len(extracted.Relationships))
	for _, rel := range extracted.Relationships {
		sourceChar := nameToChar[rel.Source]
		if sourceChar == nil {
			sourceChar = existingRelationshipCharacters[rel.Source]
		}

		targetChar := nameToChar[rel.Target]
		if targetChar == nil {
			targetChar = existingRelationshipCharacters[rel.Target]
		}
		if sourceChar == nil || targetChar == nil {
			return state, fmt.Errorf("relationship %q -> %q has unresolved endpoint", rel.Source, rel.Target)
		}
		relationshipChanges = append(relationshipChanges, domain.RelationshipChange{
			SourceCharacter: sourceChar,
			TargetCharacter: targetChar,
			RelationType:    rel.RelationType,
			Description:     rel.Description,
			Operation:       rel.Operation,
		})
	}
	if _, err := a.repo.ReplaceChapterRelationships(ctx, ref, relationshipChanges); err != nil {
		return state, fmt.Errorf("replace chapter relationships: %w", err)
	}
	return state, nil
}
