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

func validateWorldSettingUpdatesForDraft(
	existingSettings []*domain.WorldSetting,
	draft string,
) func(*[]WorldSettingUpdate) error {
	existingByName := make(map[string]*domain.WorldSetting, len(existingSettings))
	for _, setting := range existingSettings {
		existingByName[strings.TrimSpace(setting.Name)] = setting
	}
	return func(updates *[]WorldSettingUpdate) error {
		if err := validateWorldSettingUpdatesForExisting(existingSettings)(updates); err != nil {
			return err
		}
		for index := range *updates {
			update := &(*updates)[index]
			if err := validateLedgerEvidence(fmt.Sprintf("world_settings[%d].identity_evidence", index), update.IdentityEvidence, draft, true); err != nil {
				return err
			}
			existing := existingByName[update.Name]
			staticRequired := (existing == nil && (update.Category != "" || update.Description != "")) ||
				(existing != nil && strings.TrimSpace(existing.Category) == "" && update.Category != "") ||
				(existing != nil && strings.TrimSpace(existing.Description) == "" && update.Description != "")
			if err := validateLedgerEvidence(fmt.Sprintf("world_settings[%d].static_evidence", index), update.StaticEvidence, draft, staticRequired); err != nil {
				return err
			}
			if err := validateLedgerEvidence(fmt.Sprintf("world_settings[%d].state_evidence", index), update.StateEvidence, draft, true); err != nil {
				return err
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
	Category         string `json:"category"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	CurrentState     string `json:"current_state"`
	IdentityEvidence string `json:"identity_evidence"`
	StaticEvidence   string `json:"static_evidence"`
	StateEvidence    string `json:"state_evidence"`
}

func (a *WorldAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	ref := domain.ChapterStateRef{
		NovelID:      state.NovelID,
		ChapterID:    state.ChapterID,
		ChapterIndex: state.ChapterIndex,
		GenerationID: state.GenerationID,
	}
	ref.Normalize()
	if err := ref.Validate(); err != nil {
		return state, fmt.Errorf("world chapter state: %w", err)
	}
	// 1. 获取当前章节之前的设定 (用于参考)
	existingSettings, err := a.repo.ListWorldSettingsBeforeChapter(ctx, ref.NovelID, ref.ChapterIndex)
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
5. 所有 evidence 必须逐字复制自【本章正文】中的一段连续原文，长度不超过1000字；不得改写、概括或引用现有账本。
6. 每个设定必须提供 identity_evidence 和 state_evidence；仅当新增或补齐 category/description 时提供 static_evidence，否则留空。
7. 正文没有明确证据时不要输出该更新。
8. 不要用空字符串清除已有信息。
9. 只输出合法 JSON 数组，不要输出 Markdown 或解释：
[
  {
    "category": "分类(地理/武学/势力/宝物/规则)",
    "name": "设定名称",
    "description": "新设定的静态说明，否则留空",
    "current_state": "本章结束时的具体动态状态",
    "identity_evidence": "正文中的设定原文",
    "static_evidence": "支持category/description的正文原文，没有静态更新则留空",
    "state_evidence": "支持当前状态的正文原文"
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
		validateWorldSettingUpdatesForDraft(existingSettings, state.Draft),
	)
	if err != nil {
		return state, err
	}

	settings := make([]*domain.WorldSetting, 0, len(updates))
	for _, update := range updates {
		settings = append(settings, &domain.WorldSetting{
			Category:     update.Category,
			Name:         update.Name,
			Description:  update.Description,
			CurrentState: update.CurrentState,
		})
	}
	if _, err := a.repo.ReplaceChapterWorldSettings(ctx, ref, settings); err != nil {
		return state, fmt.Errorf("replace chapter world states: %w", err)
	}
	return state, nil
}
