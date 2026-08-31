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

	systemPrompt := `你是一位资深网文编剧。你的任务是根据【小说想法】、【全书大纲】、【章节序号】和【上一章接力状态】，制定本章必须遵守的结构化剧情契约。
要求：
- 本章只推进一个阶段（铺垫、试探、受挫或反转之一），不能在单章内完整解决一个应跨多章的大事件。
- chapter_goal：本章必须达到的阶段性推进节点，不要求所属宏观事件在本章闭环；必须是单一、可观察、包含明确主体、动作/发现/决定/状态变化，能在正文中直接判断完成与否的状态变化或现场结果；不能只写空泛意图，也不能把计划以后完成当成本章目标。
- must_happen：正文必须实际发生的关键推进，1-5条，按发生顺序排列。
- must_not_happen：本章禁止发生或禁止提前解决的事件，0-5条；用于保护后续主线和核心矛盾。
- end_state：正文结束时的精确现场状态，可以是持续冲突、动作进行中或信息刚刚变化，不要求另造新任务。
- 如果存在上一章接力状态，契约开端必须承接 NextAction 或合理处理 OpenLoops。
- 如果存在【主线事件节拍】，chapter_goal 和 must_happen 必须实际推进本章事件；下一章预定事件只能形成因果牵引，不得在本章提前完成。
- 如果主线计划与上一章实际接力存在偏差，以上一章接力为事实基础，在不跳跃、不重启冲突的前提下调整推进方式。
只返回合法 JSON，不要 Markdown 或解释：
{"chapter_goal":"...","must_happen":["..."],"must_not_happen":["..."],"end_state":"..."}`

	idea := state.Idea
	if state.EventChapterCount > 0 {
		systemPrompt += fmt.Sprintf(`
连续情节模式补充要求：
- 本次将连续生成并自然拆分为 %d 章，chapter_goal 表示这段跨章情节整体要达到的结果，不是第一章必须完成的独立事件。
- must_happen 必须按因果顺序覆盖多个连续推进阶段，允许同一场景或冲突跨越章节边界。
- end_state 是整段连续情节结束时的状态；中间章节不要求另造新任务或独立闭环。
- 禁止通过回到起点、重新介绍人物地点或重复进入过程来制造章节感。`, state.EventChapterCount)
	}

	if idea == "" {
		idea = "（未提供）"
	}
	fullOutline := state.FullOutline
	if fullOutline == "" {
		fullOutline = "（未提供）"
	}

	userPrompt := fmt.Sprintf("【小说想法】\n%s\n\n【全书大纲】\n%s\n\n【当前章节序号】\n第%d章\n\n%s\n\n%s\n\n请输出本章剧情契约：",
		idea, fullOutline, state.ChapterIndex, mainlineBeatPrompt(state.MainlineBeat), continuityPrompt(state.PreviousContinuity))

	contract, err := generateStructuredObjectResponse(
		ctx,
		p.llm,
		"plot",
		systemPrompt,
		userPrompt,
		decodeChapterContract,
		validateChapterContract,
	)
	if err != nil {
		return state, fmt.Errorf("plot agent failed to generate chapter contract: %w", err)
	}

	state.ChapterContract = contract
	state.Outline = formatChapterContract(contract)
	return state, nil
}
