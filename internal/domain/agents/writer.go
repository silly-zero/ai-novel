package agents

import (
	"context"
	"fmt"
)

// WriterAgent 是负责文本撰写的主笔智能体
type WriterAgent struct {
	llm LLMService
}

// NewWriterAgent 构造函数
func NewWriterAgent(llm LLMService) *WriterAgent {
	return &WriterAgent{
		llm: llm,
	}
}

func (w *WriterAgent) Role() AgentRole {
	return RoleWriter
}

func (w *WriterAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 1. 构建 System Prompt：赋予 Writer 角色设定和文风要求
	systemPrompt := `你是一位顶尖的网络小说作家。你的任务是根据主编提供的【场景卡】和【背景资料】，撰写生动、有感染力的小说正文。
要求：
- 细节描写丰富，动作与神态刻画生动。
- 严格遵循背景资料中的世界观和角色设定，避免 OOC。
- 如果有【修改意见(Critique)】，请务必针对意见对原稿进行重写修正。`

	// 2. 构建 User Prompt：拼装当前状态中的各类上下文
	userPrompt := fmt.Sprintf("【场景卡】\n%s\n\n【背景资料】\n%s\n", state.SceneCard, state.Context)
	
	if state.Critique != "" {
		userPrompt += fmt.Sprintf("\n【前一版草稿】\n%s\n\n【审查员的修改意见】\n%s\n\n请根据以上意见，重新撰写本章正文：", state.Draft, state.Critique)
	} else {
		userPrompt += "\n请开始撰写本章正文："
	}

	// 3. 调用大模型进行文本生成
	// TODO: 在实际应用中，这里可能需要支持 SSE 流式输出以便前端展示“打字机”效果
	draft, err := w.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("writer agent failed to generate text: %w", err)
	}

	// 4. 更新状态机中的 Draft 字段
	state.Draft = draft
	
	// 清理上一轮的 Critique，表示 Writer 已经做出了修改响应
	state.Critique = ""

	return state, nil
}
