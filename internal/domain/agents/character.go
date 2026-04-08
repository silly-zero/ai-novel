package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domain "github.com/ai-novel/studio/internal/domain/novel"
)

// CharacterAgent 负责从剧情中提取和维护人物档案
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
	existingChars, _ := a.repo.ListCharacters(ctx, state.NovelID)
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

	resp, err := a.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, err
	}

	// 2. 解析并更新数据库
	cleanedJSON := strings.TrimPrefix(strings.TrimSpace(resp), "```json")
	cleanedJSON = strings.TrimSuffix(cleanedJSON, "```")

	var extracted CharacterExtraction
	if err := json.Unmarshal([]byte(cleanedJSON), &extracted); err != nil {
		var updates []CharacterUpdate
		if err2 := json.Unmarshal([]byte(cleanedJSON), &updates); err2 != nil {
			return state, fmt.Errorf("failed to parse character updates: %w", err)
		}
		extracted.Characters = updates
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

		if err := a.repo.SaveCharacter(ctx, char); err == nil {
			nameToChar[char.Name] = char
		}
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

		_ = a.repo.SaveRelationship(ctx, &domain.Relationship{
			NovelID:         state.NovelID,
			SourceCharacter: sourceChar,
			TargetCharacter: targetChar,
			RelationType:    rel.RelationType,
			Description:     rel.Description,
		})
	}

	return state, nil
}
