package agents

import (
	"context"
	"fmt"
	"strings"
)

// WriterAgent 是负责文本撰写的主笔智能体
type WriterAgent struct {
	llm LLMService
}

// NewWriterAgent 构造函数
func NewWriterAgent(llm LLMService) *WriterAgent {
	return &WriterAgent{llm: llm}
}

func (w *WriterAgent) Role() AgentRole {
	return RoleWriter
}

func (w *WriterAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 构建 System Prompt：赋予 Writer 角色设定和文风要求
	systemPrompt := `你是一位顶尖的网络小说作家。你的任务是根据主编提供的【场景卡】和【背景资料】，撰写生动、有感染力的小说正文。
系统要求：
- 细节描写丰富，动作与神态刻画生动。
- 严格遵循背景资料中的世界观和角色设定，避免 OOC。
- 正文总字数（按中文字符计）通常控制在 2500-5000 字；以情节完整、自然和连贯为优先，不要为了凑字数删掉关键内容。
- 如果有【修改意见(Critique)】，请务必针对意见对原稿进行重写修正。
- 如果存在上一章接力状态，开头必须承接 NextAction 或合理处理 OpenLoops；不得无因重启冲突。
- 如果存在本章契约，必须在正文中达到 ChapterGoal（本章阶段性推进节点）、逐条呈现全部 MustHappen、不得执行 MustNotHappen，并让章尾处于 EndState；ChapterGoal 不能只被提及或计划，应该出现能够证明本章推进已发生的具体动作、发现、决定或状态变化。
- 如果存在主线事件节拍，正文必须实际呈现本章事件，不能只承诺以后发生；下一章预定事件只能被铺垫，不得在本章提前完成。
- 本章只推进一个阶段；结尾保持当前情节的因果连续性，可以停在动作、对话、信息变化或悬念节点，不要求另造无关的新任务。`

	if state.EventChapterCount > 0 {
		segmentIndex := state.EventSegmentIndex
		if segmentIndex <= 0 {
			segmentIndex = 1
		}
		systemPrompt += fmt.Sprintf(`
- 当前为连续写作批次：这是同一核心情节的第 %d 个写作段，预计本批次约 %d 章；章数只是写作窗口，不是事件总跨度或闭环承诺。当前段通常写 2500-5000 字，但不得为了凑足窗口压缩过渡、硬性截断或添加无关内容。
- 不要输出章节标题、章节编号、“本章完”、分隔线或任何人为切章标记；系统会在连续情节完成后按自然转折整理章节。
- 同一冲突、调查、战斗、谈判或修炼过程必须连续发展；不得在中途重新介绍人物、地点、能力或重演已经发生的进入过程。
- 当前段只推进同一情节的一段自然阶段；不要强行解决整个宏观事件，也不要为了段落结束另造无关任务。批次结束时允许事件处于自然未完状态，下一批必须从本批次最后现场继续。`, segmentIndex, state.EventChapterCount)
	}

	userPrompt := generationContextPrompt(state)
	if state.Critique != "" {
		userPrompt += fmt.Sprintf("\n\n【前一版草稿】\n%s\n\n【审查员的修改意见】\n%s\n\n请根据以上意见，重新撰写本章正文：", state.Draft, state.Critique)
	} else {
		userPrompt += "\n\n请开始撰写本章正文："
	}

	// 3. 调用大模型进行流式文本生成
	var fullDraft strings.Builder
	err := w.llm.StreamGenerate(ctx, systemPrompt, userPrompt, func(content string) error {
		fullDraft.WriteString(content)

		if state.StreamSink != nil {
			if err := state.StreamSink(ctx, GenerationStreamEvent{
				Type:  GenerationStreamEventToken,
				Token: content,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return state, fmt.Errorf("writer agent stream canceled: %w", ctxErr)
		}
		return state, fmt.Errorf("writer agent stream failed: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return state, fmt.Errorf("writer agent stream canceled: %w", ctxErr)
	}

	// 4. 更新状态机中的 Draft 字段
	state.Draft = fullDraft.String()
	state.ContractAssessment = ChapterContractAssessment{}
	state.ContinuityAssessment = ContinuityAssessment{}
	state.CanonAssessment = nil
	state.MainlineAssessment = MainlineAssessment{}
	state.IsApproved = false

	// 清理上一轮的 Critique，表示 Writer 已经做出了修改响应
	state.Critique = ""

	return state, nil
}
