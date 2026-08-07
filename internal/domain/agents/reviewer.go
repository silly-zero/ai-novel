package agents

import (
	"bytes"
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
	Passed           bool     `json:"passed"`
	ContinuityPassed bool     `json:"continuity_passed"`
	ContractPassed   bool     `json:"contract_passed"`
	Violations       []string `json:"violations"`
	Critique         string   `json:"critique"`
	contractChecked  bool
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
5. 分章节奏：是否把一个应跨多章的大事件在本章“一次性写完”？如果是，必须判定不通过，并要求改成“本章只推进一个阶段，结尾保留悬念/未完成目标”。
6. 连贯性硬门槛：如果存在上一章接力状态，章首是否承接 NextAction 或合理处理 OpenLoops？OpenLoops 可以被解决、升级或转化，不要求原样复述；若无因断裂或凭空重启，必须判定 continuity_passed=false。
7. 本章结尾是否留下具体、可行动的未完成目标供下一章继续？第一章跳过上一章承接检查，但仍检查本章结尾。
8. 章节契约硬门槛：如果存在【本章契约】，逐项检查 chapter_goal、全部 must_happen、全部 must_not_happen 和 end_state；漏写必须事件、写入禁止事件或章尾未达到指定状态时，contract_passed=false，并在 violations 中列出具体违约项。

请你严格审查，并输出 JSON 格式的审查结果：
{
	"passed": true或false,
	"continuity_passed": true或false,
	"contract_passed": true或false,
	"violations": ["具体违约项；没有则为空数组"],
	"critique": "如果任一检查不通过，在这里写明具体的、可执行的修改意见。如果全部通过，请留空。"
}
务必确保输出是合法的 JSON 字符串。`

	userPrompt := fmt.Sprintf("【场景卡】\n%s\n\n【背景资料】\n%s\n\n%s\n\n%s\n\n【小说草稿】\n%s\n\n请给出你的审查结果：",
		state.SceneCard, state.Context, chapterContractPrompt(state.ChapterContract), continuityPrompt(state.PreviousContinuity), state.Draft)

	requireContract := !state.ChapterContract.IsEmpty()
	result, err := generateStructuredResponse(
		ctx,
		r.llm,
		"reviewer",
		systemPrompt,
		userPrompt,
		func(candidate []byte) (ReviewResult, error) {
			return decodeReviewResultWithContract(candidate, requireContract)
		},
		validateReviewResult,
	)
	if err != nil {
		return state, fmt.Errorf("reviewer agent failed to analyze draft: %w", err)
	}

	contractPassed := !requireContract || result.ContractPassed
	state.IsApproved = result.Passed && result.ContinuityPassed && contractPassed
	state.Critique = strings.TrimSpace(result.Critique)

	return state, nil
}

func decodeReviewResult(candidate []byte) (ReviewResult, error) {
	return decodeReviewResultWithContract(candidate, false)
}

func decodeReviewResultWithContract(candidate []byte, requireContract bool) (ReviewResult, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(candidate, &raw); err != nil {
		return ReviewResult{}, err
	}
	passedJSON, ok := raw["passed"]
	if !ok || string(bytes.TrimSpace(passedJSON)) == "null" {
		return ReviewResult{}, fmt.Errorf("passed is required and must be a boolean")
	}
	var passed bool
	if err := json.Unmarshal(passedJSON, &passed); err != nil {
		return ReviewResult{}, fmt.Errorf("passed is required and must be a boolean")
	}
	var continuityPassed bool
	continuityJSON, ok := raw["continuity_passed"]
	if !ok || string(bytes.TrimSpace(continuityJSON)) == "null" {
		return ReviewResult{}, fmt.Errorf("continuity_passed is required and must be a boolean")
	}
	if err := json.Unmarshal(continuityJSON, &continuityPassed); err != nil {
		return ReviewResult{}, fmt.Errorf("continuity_passed is required and must be a boolean")
	}
	var contractPassed bool
	var violations []string
	contractChecked := requireContract
	if requireContract {
		contractJSON, ok := raw["contract_passed"]
		if !ok || string(bytes.TrimSpace(contractJSON)) == "null" {
			return ReviewResult{}, fmt.Errorf("contract_passed is required when a chapter contract is present")
		}
		if err := json.Unmarshal(contractJSON, &contractPassed); err != nil {
			return ReviewResult{}, fmt.Errorf("contract_passed must be a boolean")
		}
		violationsJSON, ok := raw["violations"]
		if !ok || string(bytes.TrimSpace(violationsJSON)) == "null" {
			return ReviewResult{}, fmt.Errorf("violations is required when a chapter contract is present")
		}
		if err := json.Unmarshal(violationsJSON, &violations); err != nil {
			return ReviewResult{}, fmt.Errorf("violations must be an array of strings")
		}
	}
	var critique string
	if critiqueJSON, ok := raw["critique"]; ok {
		if string(bytes.TrimSpace(critiqueJSON)) == "null" {
			return ReviewResult{}, fmt.Errorf("critique must be a string")
		}
		if err := json.Unmarshal(critiqueJSON, &critique); err != nil {
			return ReviewResult{}, fmt.Errorf("critique must be a string")
		}
	}
	return ReviewResult{
		Passed:           passed,
		ContinuityPassed: continuityPassed,
		ContractPassed:   contractPassed,
		Violations:       violations,
		Critique:         critique,
		contractChecked:  contractChecked,
	}, nil
}

func validateReviewResult(result *ReviewResult) error {
	result.Critique = strings.TrimSpace(result.Critique)
	violations := make([]string, 0, len(result.Violations))
	for _, violation := range result.Violations {
		violation = strings.TrimSpace(violation)
		if violation == "" {
			return fmt.Errorf("violations must not contain empty items")
		}
		violations = append(violations, violation)
	}
	result.Violations = violations
	contractFailed := result.contractChecked && !result.ContractPassed
	if (!result.Passed || !result.ContinuityPassed || contractFailed) && result.Critique == "" {
		return fmt.Errorf("critique is required when review, continuity, or contract fails")
	}
	if contractFailed && len(result.Violations) == 0 {
		return fmt.Errorf("violations is required when contract_passed is false")
	}
	if result.contractChecked && result.ContractPassed && len(result.Violations) > 0 {
		return fmt.Errorf("violations must be empty when contract_passed is true")
	}
	return nil
}
