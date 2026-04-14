package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ReviewerAgent 是负责质量把关的审查员智能体
type ReviewerAgent struct {
	llm LLMService
}

// NewReviewerAgent 构造函数
func NewReviewerAgent(llm LLMService) *ReviewerAgent {
	return &ReviewerAgent{
		llm: llm,
	}
}

func (r *ReviewerAgent) Role() AgentRole {
	return RoleReviewer
}

// ReviewResult 审查结果的结构化定义
type ReviewResult struct {
	Passed   bool   `json:"passed"`   // 是否通过
	Critique string `json:"critique"` // 如果不通过，具体的修改意见
}

func (r *ReviewerAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	// 如果草稿为空，直接报错返回
	if state.Draft == "" {
		return state, fmt.Errorf("draft is empty, nothing to review")
	}

	wordCount := len([]rune(strings.TrimSpace(state.Draft)))
	if wordCount < 2500 || wordCount > 4000 {
		state.IsApproved = false
		if wordCount < 2500 {
			state.Critique = fmt.Sprintf("字数不达标：当前约 %d 字。请补写细节与推进剧情，使正文总字数达到 2500-4000 字（按中文字符计），同时保持与场景卡一致。", wordCount)
		} else {
			state.Critique = fmt.Sprintf("字数超标：当前约 %d 字。请删减冗余描写与重复表达，使正文总字数控制在 2500-4000 字（按中文字符计），同时保持与场景卡一致。", wordCount)
		}
		return state, nil
	}

	systemPrompt := `你是一位严厉的小说主编和审查员。你的任务是审查作者提交的【小说草稿】，并对比【场景卡】和【背景资料】，检查是否存在以下问题：
1. 剧情偏离：是否漏写了场景卡中要求的重要情节？
2. 角色 OOC：角色的行为、语言是否与背景资料中的设定相冲突？
3. 行文质量：是否存在逻辑硬伤、水字数、或者描写过于干瘪？
4. 字数要求：正文总字数（按中文字符计）是否在 2500-4000 字之间？

请你严格审查，并输出 JSON 格式的审查结果：
{
	"passed": true或false,
	"critique": "如果不通过，在这里写明具体的、可执行的修改意见。如果通过，请留空。"
}
务必确保输出是合法的 JSON 字符串。`

	userPrompt := fmt.Sprintf("【场景卡】\n%s\n\n【背景资料】\n%s\n\n【小说草稿】\n%s\n\n请给出你的审查结果：", 
		state.SceneCard, state.Context, state.Draft)

	// 调用大模型进行审查
	response, err := r.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("reviewer agent failed to analyze draft: %w", err)
	}

	// 解析结构化输出
	var result ReviewResult
	// 简单的清理，防止大模型返回带有 markdown 标记的 JSON (如 ```json ... ```)
	cleanedJSON := strings.TrimPrefix(strings.TrimSpace(response), "```json")
	cleanedJSON = strings.TrimSuffix(cleanedJSON, "```")
	
	if err := json.Unmarshal([]byte(cleanedJSON), &result); err != nil {
		// 如果解析失败，保守起见认为审查不通过，并把原始响应作为 critique
		state.IsApproved = false
		state.Critique = fmt.Sprintf("审查员格式化输出失败，原始意见：%s", response)
		return state, nil
	}

	// 更新状态机
	state.IsApproved = result.Passed
	if !result.Passed {
		state.Critique = result.Critique
	}

	return state, nil
}
