package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domain "github.com/ai-novel/studio/internal/domain/novel"
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
		extracted.Characters[index].Name = strings.TrimSpace(extracted.Characters[index].Name)
		if extracted.Characters[index].Name == "" {
			return fmt.Errorf("characters[%d].name must not be blank", index)
		}
	}
	for index := range extracted.Relationships {
		relation := &extracted.Relationships[index]
		relation.Source = strings.TrimSpace(relation.Source)
		relation.Target = strings.TrimSpace(relation.Target)
		relation.RelationType = strings.TrimSpace(relation.RelationType)
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
	Name          string `json:"name"`
	Gender        string `json:"gender"`
	Age           int    `json:"age"`
	Appearance    string `json:"appearance"`
	Personality   string `json:"personality"`
	Background    string `json:"background"`
	CurrentStatus string `json:"current_status"`
}

type RelationshipUpdate struct {
	Source       string `json:"source"`
	Target       string `json:"target"`
	RelationType string `json:"relation_type"`
	Description  string `json:"description"`
}

type CharacterExtraction struct {
	Characters    []CharacterUpdate    `json:"characters"`
	Relationships []RelationshipUpdate `json:"relationships"`
}

func (a *CharacterAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 获取当前章节的所有角色档案 (用于提供给 LLM 参考)
	existingChars, err := a.repo.ListCharacters(ctx, state.NovelID)
	if err != nil {
		return state, fmt.Errorf("list existing characters: %w", err)
	}
	charContext := "【现有角色档案】\n"
	for _, c := range existingChars {
		charContext += fmt.Sprintf("- %s: %s\n", c.Name, c.Personality)
	}

	systemPrompt := `你是一位专业的小说人设分析师。你的任务是从提供的【小说正文】中，分析并更新【人物档案】与【角色关系网】。
要求：
1. 识别文中出现的所有重要角色。
2. 对于已有角色，根据文中描述更新其“外貌”、“性格”、“当前状态”或“背景”。
3. 对于新出现的角色，创建完整的人设卡。
4. 提取关键角色关系（如师徒、敌对、盟友、亲属、交易等），仅输出确定的信息。
5. 输出必须是合法 JSON，格式如下：
{
  "characters": [
    {
      "name": "角色名",
      "gender": "性别",
      "age": 20,
      "appearance": "外貌描写",
      "personality": "性格特征",
      "background": "背景故事",
      "current_status": "当前在文中的状态或处境"
    }
  ],
  "relationships": [
    {
      "source": "角色A",
      "target": "角色B",
      "relation_type": "师徒/敌人/盟友/亲属/恋人/交易等",
      "description": "一句话说明关系依据"
    }
  ]
}`

	userPrompt := fmt.Sprintf("%s\n\n【本章正文】\n%s\n\n请分析并输出角色更新结果：", charContext, state.Draft)

	extracted, err := generateStructuredResponse(
		ctx,
		a.llm,
		"character",
		systemPrompt,
		userPrompt,
		decodeCharacterExtraction,
		validateCharacterExtraction,
	)
	if err != nil {
		return state, err
	}

	nameToChar := make(map[string]*domain.Character)

	for _, up := range extracted.Characters {
		// 查找或创建
		char, err := a.repo.FindByName(ctx, state.NovelID, up.Name)
		if err != nil {
			char = &domain.Character{
				NovelID: state.NovelID,
				Name:    up.Name,
			}
		}

		// 更新字段
		char.Gender = up.Gender
		char.Age = up.Age
		char.Appearance = up.Appearance
		char.Personality = up.Personality
		char.Background = up.Background
		char.CurrentStatus = up.CurrentStatus

		if err := a.repo.SaveCharacter(ctx, char); err != nil {
			return state, fmt.Errorf("save character %q: %w", up.Name, err)
		}
		nameToChar[char.Name] = char
	}

	for _, rel := range extracted.Relationships {
		if rel.Source == "" || rel.Target == "" || rel.RelationType == "" {
			continue
		}

		sourceChar := nameToChar[rel.Source]
		if sourceChar == nil {
			c, err := a.repo.FindByName(ctx, state.NovelID, rel.Source)
			if err == nil {
				sourceChar = c
			}
		}

		targetChar := nameToChar[rel.Target]
		if targetChar == nil {
			c, err := a.repo.FindByName(ctx, state.NovelID, rel.Target)
			if err == nil {
				targetChar = c
			}
		}

		if sourceChar == nil || targetChar == nil {
			continue
		}

		if err := a.repo.SaveRelationship(ctx, &domain.Relationship{
			NovelID:         state.NovelID,
			SourceCharacter: sourceChar,
			TargetCharacter: targetChar,
			RelationType:    rel.RelationType,
			Description:     rel.Description,
		}); err != nil {
			return state, fmt.Errorf(
				"save relationship %q -> %q (%s): %w",
				rel.Source,
				rel.Target,
				rel.RelationType,
				err,
			)
		}
	}

	return state, nil
}
