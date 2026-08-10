package agents

import (
	"context"
	"fmt"
)

// ArchitectAgent 是架构师智能体，负责根据 Idea 构建全书的章节大纲映射
type ArchitectAgent struct {
	llm LLMService
}

func NewArchitectAgent(llm LLMService) *ArchitectAgent {
	return &ArchitectAgent{llm: llm}
}

func (a *ArchitectAgent) Role() AgentRole {
	return RoleArchitect
}

func (a *ArchitectAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	state.MainlineBeat = MainlineEventBeat{}

	// 已有大纲且未指定续写范围：直接复用，不触发生成/续写
	if state.ExistingOutline != "" && state.OutlineStart <= 0 && state.OutlineEnd <= 0 {
		if state.FullOutline == "" {
			state.FullOutline = state.ExistingOutline
		}
		state.MainlineBeat = selectMainlineEventBeat(state.FullOutline, state.ChapterIndex)
		return state, nil
	}

	// FullOutline 已有且未指定续写范围：跳过生成
	if state.FullOutline != "" && state.OutlineStart <= 0 && state.OutlineEnd <= 0 {
		state.MainlineBeat = selectMainlineEventBeat(state.FullOutline, state.ChapterIndex)
		return state, nil
	}

	// 续写时优先使用显式 ExistingOutline，否则使用 FullOutline，但只在生成成功后提交新状态。
	existingOutline := state.ExistingOutline
	if existingOutline == "" && state.FullOutline != "" && (state.OutlineStart > 0 || state.OutlineEnd > 0) {
		existingOutline = state.FullOutline
	}

	if state.Idea == "" && existingOutline == "" {
		return state, fmt.Errorf("architect agent requires an idea or existing outline but both are empty")
	}

	start := state.OutlineStart
	if start <= 0 {
		start = 1
	}
	end := state.OutlineEnd
	if end <= 0 {
		end = 10
	}

	systemPrompt := fmt.Sprintf(`你是一位资深小说架构师。你的任务是根据用户提供的小说【想法(Idea)】和可能存在的【已有大纲】，构思或续写小说的【大纲】。
要求：
- 专门规划第 %d 章到第 %d 章的简要剧情。
- 每章用一句话写清本章实际发生的、可观察的主线变化，以及该变化造成的后续牵引；不要只写主题、氛围或笼统目标。
- 相邻章节必须形成因果推进，当前章不得提前完成后续章节的主线事件。
- 确保故事节奏合理，有伏笔和高潮预设。
- 格式如下：
第%d章：[简要描述]
...
第%d章：[简要描述]

请直接输出新增的这部分大纲，不要有开场白，也不要重复已有大纲的内容。`, start, end, start, end)

	idea := state.Idea
	if idea == "" {
		idea = "（未提供，请严格衔接已有大纲）"
	}
	userPrompt := fmt.Sprintf("【小说想法】\n%s", idea)
	if existingOutline != "" {
		userPrompt += fmt.Sprintf("\n\n【已有大纲参考】\n%s", existingOutline)
	}
	userPrompt += "\n\n请开始构思或续写大纲："

	fullOutline, err := a.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("architect agent failed: %w", err)
	}

	if existingOutline != "" {
		state.ExistingOutline = existingOutline
		state.FullOutline = existingOutline + "\n" + fullOutline
	} else {
		state.FullOutline = fullOutline
	}
	state.MainlineBeat = selectMainlineEventBeat(state.FullOutline, state.ChapterIndex)

	return state, nil
}
