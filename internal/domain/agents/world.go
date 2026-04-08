package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domain "github.com/ai-novel/studio/internal/domain/novel"
)

// WorldAgent 负责从剧情中提取和维护世界观设定
type WorldAgent struct {
	llm  LLMService
	repo domain.WorldRepository
}

func NewWorldAgent(llm LLMService, repo domain.WorldRepository) *WorldAgent {
	return &WorldAgent{
		llm:  llm,
		repo: repo,
	}
}

func (a *WorldAgent) Role() AgentRole {
	return "World"
}

// WorldSettingUpdate 结构化输出
type WorldSettingUpdate struct {
	Category    string `json:"category"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (a *WorldAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 获取现有设定 (用于参考)
	existingSettings, _ := a.repo.ListAll(ctx, state.NovelID)
	settingContext := "【现有世界观设定】\n"
	for _, s := range existingSettings {
		settingContext += fmt.Sprintf("- [%s] %s: %s\n", s.Category, s.Name, s.Description)
	}

	systemPrompt := `你是一位专业的小说世界观架构师。你的任务是从提供的【小说正文】中，分析并更新【世界观设定】。
要求：
1. 识别文中出现的地理位置、武学等级、势力名称、特殊宝物或核心规则。
2. 对于已有设定，根据文中描述更新其描述。
3. 对于新出现的设定，创建完整的条目。
4. 输出格式为 JSON 数组：
[
  {
    "category": "分类(地理/武学/势力/宝物/规则)",
    "name": "设定名称",
    "description": "详细描述"
  }
]`

	userPrompt := fmt.Sprintf("%s\n\n【本章正文】\n%s\n\n请分析并输出世界观更新结果：", settingContext, state.Draft)

	resp, err := a.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, err
	}

	// 2. 解析并更新数据库
	var updates []WorldSettingUpdate
	cleanedJSON := strings.TrimPrefix(strings.TrimSpace(resp), "```json")
	cleanedJSON = strings.TrimSuffix(cleanedJSON, "```")
	
	if err := json.Unmarshal([]byte(cleanedJSON), &updates); err != nil {
		return state, fmt.Errorf("failed to parse world setting updates: %w", err)
	}

	for _, up := range updates {
		setting, err := a.repo.FindByName(ctx, state.NovelID, up.Name)
		if err != nil {
			setting = &domain.WorldSetting{
				NovelID: state.NovelID,
				Name:    up.Name,
			}
		}
		
		setting.Category = up.Category
		setting.Description = up.Description
		
		_ = a.repo.SaveSetting(ctx, setting)
	}

	return state, nil
}
