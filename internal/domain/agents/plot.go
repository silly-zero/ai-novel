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

	if state.FullOutline == "" && state.Idea == "" {
		return state, fmt.Errorf("plot agent requires full outline or idea but both are empty")
	}

	systemPrompt := `你是一位资深网文编剧。你的任务是根据【小说想法】和【全书大纲】，为指定的【章节序号】撰写详细的本章剧情大纲。
大纲要求：
- 逻辑自洽，充满冲突。
- 包含起承转合，明确高潮点。
- 字数在 200-400 字之间。
- 直接输出大纲内容，不要有多余的描述。`

	idea := state.Idea
	if idea == "" {
		idea = "（未提供）"
	}
	fullOutline := state.FullOutline
	if fullOutline == "" {
		fullOutline = "（未提供）"
	}

	userPrompt := fmt.Sprintf("【小说想法】\n%s\n\n【全书大纲】\n%s\n\n【当前章节序号】\n第%d章\n\n请输出本章详细大纲：",
		idea, fullOutline, state.ChapterIndex)

	outline, err := p.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("plot agent failed to generate outline: %w", err)
	}

	// 3. 将生成的大纲写入状态
	state.Outline = outline
	return state, nil
}
