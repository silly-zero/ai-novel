package agents

import (
	"context"
	"fmt"
	"strings"

	domain "github.com/ai-novel/studio/internal/domain/novel"
)

const (
	maxWorldDescriptionRunes = 2000
	maxWorldStateRunes       = 1000
)

func validateWorldSettingUpdates(updates *[]WorldSettingUpdate) error {
	if *updates == nil {
		return fmt.Errorf("world setting array must not be null")
	}
	for index := range *updates {
		update := &(*updates)[index]
		update.Category = strings.TrimSpace(update.Category)
		update.Name = strings.TrimSpace(update.Name)
		update.Description = strings.TrimSpace(update.Description)
		update.CurrentState = strings.TrimSpace(update.CurrentState)
		if update.Category == "" {
			return fmt.Errorf("world_settings[%d].category must not be blank", index)
		}
		if update.Name == "" {
			return fmt.Errorf("world_settings[%d].name must not be blank", index)
		}
		if update.CurrentState == "" {
			return fmt.Errorf("world_settings[%d].current_state must not be blank", index)
		}
		if len([]rune(update.Description)) > maxWorldDescriptionRunes {
			return fmt.Errorf("world_settings[%d].description exceeds %d characters", index, maxWorldDescriptionRunes)
		}
		if len([]rune(update.CurrentState)) > maxWorldStateRunes {
			return fmt.Errorf("world_settings[%d].current_state exceeds %d characters", index, maxWorldStateRunes)
		}
	}
	return nil
}

func validateWorldSettingUpdatesForExisting(
	existingSettings []*domain.WorldSetting,
) func(*[]WorldSettingUpdate) error {
	existingDescriptions := make(map[string]string, len(existingSettings))
	for _, setting := range existingSettings {
		existingDescriptions[strings.TrimSpace(setting.Name)] = strings.TrimSpace(setting.Description)
	}
	return func(updates *[]WorldSettingUpdate) error {
		if err := validateWorldSettingUpdates(updates); err != nil {
			return err
		}
		for index, update := range *updates {
			description, exists := existingDescriptions[update.Name]
			if (!exists || description == "") && update.Description == "" {
				return fmt.Errorf("world_settings[%d].description must not be blank for a new or incomplete setting", index)
			}
		}
		return nil
	}
}

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
	Category     string `json:"category"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	CurrentState string `json:"current_state"`
}

func (a *WorldAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 获取现有设定 (用于参考)
	existingSettings, err := a.repo.ListAll(ctx, state.NovelID)
	if err != nil {
		return state, fmt.Errorf("list existing world settings: %w", err)
	}
	var settingContext strings.Builder
	settingContext.WriteString("【现有世界观账本】\n")
	for _, s := range existingSettings {
		fmt.Fprintf(&settingContext, "- [%s] %s: 静态说明:%s; 当前状态:%s\n", s.Category, s.Name, s.Description, s.CurrentState)
	}

	systemPrompt := `你是一位专业的小说世界状态分析师。你的任务是从【本章正文】提取世界观账本更新。
要求：
1. 只输出正文中有明确依据的地理、武学、势力、宝物或规则。
2. description 是设定的静态基线；已有设定不要改写，若正文没有新静态信息可留空。
3. current_state 是本章结束时的动态快照，必须描述该设定当前的位置归属、开放封闭、控制者、损毁变化或生效状态。
4. 新设定必须填写 description 和 current_state；已有设定必须根据正文填写 current_state。
5. 不要用空字符串清除已有信息。
6. 只输出合法 JSON 数组，不要输出 Markdown 或解释：
[
  {
    "category": "分类(地理/武学/势力/宝物/规则)",
    "name": "设定名称",
    "description": "新设定的静态说明，否则留空",
    "current_state": "本章结束时的具体动态状态"
  }
]`

	userPrompt := fmt.Sprintf("%s\n\n【本章正文】\n%s\n\n请分析并输出世界观账本更新结果：", settingContext.String(), state.Draft)

	updates, err := generateStructuredResponse(
		ctx,
		a.llm,
		"world",
		systemPrompt,
		userPrompt,
		decodeJSON[[]WorldSettingUpdate],
		validateWorldSettingUpdatesForExisting(existingSettings),
	)
	if err != nil {
		return state, err
	}

	for _, up := range updates {
		setting, err := a.repo.FindByName(ctx, state.NovelID, up.Name)
		if err != nil {
			setting = &domain.WorldSetting{
				NovelID: state.NovelID,
				Name:    up.Name,
			}
		}

		if strings.TrimSpace(setting.Category) == "" {
			setting.Category = up.Category
		}
		if strings.TrimSpace(setting.Description) == "" {
			setting.Description = up.Description
		}
		setting.CurrentState = up.CurrentState

		if err := a.repo.SaveSetting(ctx, setting); err != nil {
			return state, fmt.Errorf("save world setting %q: %w", up.Name, err)
		}
	}

	return state, nil
}
