package agents

import (
	"context"
	"fmt"
)

// DirectorAgent 是主编/导演智能体，负责拆解大纲，生成场景卡
type DirectorAgent struct {
	llm LLMService
}

func NewDirectorAgent(llm LLMService) *DirectorAgent {
	return &DirectorAgent{llm: llm}
}

func (d *DirectorAgent) Role() AgentRole {
	return RoleDirector
}

func (d *DirectorAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	if state.SceneCard != "" {
		return state, nil
	}

	systemPrompt := `你是一位资深小说主编。你的任务是根据提供的【本章大纲】，拆解出本章的【场景卡(Scene Cards)】。
场景卡应该包含：
1. 本章发生的时间、地点。
2. 出场人物及其当前状态。
3. 核心矛盾与情节推进点。
4. 给作者（主笔）的写作建议。
- 5. 分章节奏约束：本章必须停在“阶段性节点”，不能把整件大事一次写完；结尾要保留下一章的悬念或未完成目标。

请直接输出场景卡的文本内容，不要有多余的寒暄。`

	userPrompt := fmt.Sprintf("【本章大纲】\n%s\n", state.Outline)
	if state.EditorNotes != "" {
		userPrompt += fmt.Sprintf("\n【作者指令（人工干预）】\n%s\n", state.EditorNotes)
	}
	userPrompt += "\n请输出场景卡："

	sceneCard, err := d.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("director agent failed: %w", err)
	}

	state.SceneCard = sceneCard
	return state, nil
}
