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
	// 已有大纲且未指定续写范围：直接复用，不触发生成/续写
	if state.ExistingOutline != "" && state.OutlineStart <= 0 && state.OutlineEnd <= 0 {
		if state.FullOutline == "" {
			state.FullOutline = state.ExistingOutline
		}
		return state, nil
	}

	// FullOutline 已有且未指定续写范围：跳过生成
	if state.FullOutline != "" && state.OutlineStart <= 0 && state.OutlineEnd <= 0 {
		return state, nil
	}

	// 允许把 FullOutline 当作 ExistingOutline 来续写（如果调用方没显式传 ExistingOutline）
	if state.ExistingOutline == "" && state.FullOutline != "" && (state.OutlineStart > 0 || state.OutlineEnd > 0) {
		state.ExistingOutline = state.FullOutline
		state.FullOutline = ""
	}

	// 2. 如果 Idea 为空，报错
	if state.Idea == "" {
		return state, fmt.Errorf("architect agent requires an idea but it's empty")
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
- 每章用一句话概括核心冲突或进展。
- 确保故事节奏合理，有伏笔和高潮预设。
- 格式如下：
第%d章：[简要描述]
...
第%d章：[简要描述]

请直接输出新增的这部分大纲，不要有开场白，也不要重复已有大纲的内容。`, start, end, start, end)

	userPrompt := fmt.Sprintf("【小说想法】\n%s", state.Idea)
	if state.ExistingOutline != "" {
		userPrompt += fmt.Sprintf("\n\n【已有大纲参考】\n%s", state.ExistingOutline)
	}
	userPrompt += "\n\n请开始构思或续写大纲："

	fullOutline, err := a.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("architect agent failed: %w", err)
	}

	if state.ExistingOutline != "" {
		state.FullOutline = state.ExistingOutline + "\n" + fullOutline
	} else {
		state.FullOutline = fullOutline
	}

	return state, nil
}
