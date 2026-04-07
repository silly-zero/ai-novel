package agents

import (
	"context"
	"fmt"
)

// PlotAgent 是编剧智能体，负责从 Idea 生成详细大纲
type PlotAgent struct {
	llm LLMService
}

func NewPlotAgent(llm LLMService) *PlotAgent {
	return &PlotAgent{llm: llm}
}

func (p *PlotAgent) Role() AgentRole {
	return RolePlot
}

func (p *PlotAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 如果已经有大纲了，就不重复生成
	if state.Outline != "" {
		return state, nil
	}

	// 2. 如果 Idea 为空，报错
	if state.Idea == "" {
		return state, fmt.Errorf("plot agent requires an idea but it's empty")
	}

	systemPrompt := `你是一位资深网文编剧。你的任务是根据用户提供的一句话【小说想法(Idea)】，构思并输出本章的【详细剧情大纲】。
大纲要求：
- 逻辑自洽，充满冲突。
- 包含起承转合，明确高潮点。
- 字数在 200-400 字之间。
- 直接输出大纲内容，不要有多余的描述。`

	userPrompt := fmt.Sprintf("【小说想法】\n%s\n\n请输出本章详细大纲：", state.Idea)

	outline, err := p.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("plot agent failed to generate outline: %w", err)
	}

	// 3. 将生成的大纲写入状态
	state.Outline = outline
	return state, nil
}
