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

func (a *CharacterAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 获取当前章节的所有角色档案 (用于提供给 LLM 参考)
	existingChars, _ := a.repo.ListCharacters(ctx, state.NovelID)
	charContext := "【现有角色档案】\n"
	for _, c := range existingChars {
		charContext += fmt.Sprintf("- %s: %s\n", c.Name, c.Personality)
	}

	systemPrompt := `你是一位专业的小说人设分析师。你的任务是从提供的【小说正文】中，分析并更新【人物档案】。
要求：
1. 识别文中出现的所有重要角色。
2. 对于已有角色，根据文中描述更新其“外貌”、“性格”、“当前状态”或“背景”。
3. 对于新出现的角色，创建完整的人设卡。
4. 输出格式为 JSON 数组：
[
  {
    "name": "角色名",
    "gender": "性别",
    "age": 20,
    "appearance": "外貌描写",
    "personality": "性格特征",
    "background": "背景故事",
    "current_status": "当前在文中的状态或处境"
  }
]`

	userPrompt := fmt.Sprintf("%s\n\n【本章正文】\n%s\n\n请分析并输出角色更新结果：", charContext, state.Draft)

	resp, err := a.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, err
	}

	// 2. 解析并更新数据库
	var updates []CharacterUpdate
	cleanedJSON := strings.TrimPrefix(strings.TrimSpace(resp), "```json")
	cleanedJSON = strings.TrimSuffix(cleanedJSON, "```")

	if err := json.Unmarshal([]byte(cleanedJSON), &updates); err != nil {
		return state, fmt.Errorf("failed to parse character updates: %w", err)
	}

	for _, up := range updates {
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

		_ = a.repo.SaveCharacter(ctx, char)
	}

	return state, nil
}
