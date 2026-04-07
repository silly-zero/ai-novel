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
	// 1. 如果已经有全书大纲了，跳过
	if state.FullOutline != "" {
		return state, nil
	}

	// 2. 如果 Idea 为空，报错
	if state.Idea == "" {
		return state, fmt.Errorf("architect agent requires an idea but it's empty")
	}

	systemPrompt := `你是一位资深小说架构师。你的任务是根据用户提供的小说【想法(Idea)】，构思整部小说的【全书大纲】。
要求：
- 规划前 10 章的简要剧情。
- 每章用一句话概括核心冲突或进展。
- 确保故事节奏合理，有伏笔和高潮预设。
- 格式如下：
第1章：[简要描述]
第2章：[简要描述]
...
第10章：[简要描述]

请直接输出大纲，不要有开场白。`

	userPrompt := fmt.Sprintf("【小说想法】\n%s\n\n请构思全书大纲：", state.Idea)

	fullOutline, err := a.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("architect agent failed: %w", err)
	}

	state.FullOutline = fullOutline
	return state, nil
}
