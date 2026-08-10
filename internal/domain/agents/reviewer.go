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
	Passed             bool                      `json:"passed"`
	ContinuityPassed   bool                      `json:"continuity_passed"`
	ContractPassed     bool                      `json:"-"`
	Violations         []string                  `json:"-"`
	ContractAssessment ChapterContractAssessment `json:"contract_assessment"`
	Critique           string                    `json:"critique"`
	contractChecked    bool
}

func (r *ReviewerAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	if state.Draft == "" {
		return state, fmt.Errorf("draft is empty, nothing to review")
	}

	wordCount := len([]rune(strings.TrimSpace(state.Draft)))
	if wordCount < 2500 || wordCount > 4000 {
		state.ContractAssessment = ChapterContractAssessment{}
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
8. 章节契约实际状态：如果存在【本章契约】，必须按原顺序逐项评估 chapter_goal、每条 must_happen、每条 must_not_happen 和 end_state。每项返回 satisfied 和来自正文的具体 evidence。must_not_happen 的 satisfied=true 表示禁止事项没有发生。
9. 主线事件节拍：如果存在【主线事件节拍】，正文必须实际发生本章事件，不能只口头提及或推迟；如果提前完成下一章预定事件，必须判定 passed=false 并给出具体修改意见。

请输出合法 JSON：
{
	"passed": true或false,
	"continuity_passed": true或false,
	"contract_assessment": {
		"goal": {"satisfied": true或false, "evidence": "正文依据或缺失原因"},
		"must_happen": [{"satisfied": true或false, "evidence": "正文依据或缺失原因"}],
		"must_not_happen": [{"satisfied": true或false, "evidence": "正文依据或发生位置"}],
		"end_state": {"satisfied": true或false, "evidence": "正文依据或缺失原因"}
	},
	"critique": "如果常规审查或连续性不通过，写明具体修改意见；否则可留空。"
}
如果没有结构化章节契约，contract_assessment 可以为 null。评估数组数量和顺序必须与契约完全一致。只返回 JSON，不要输出 Markdown 或解释。`

	userPrompt := fmt.Sprintf("【场景卡】\n%s\n\n【背景资料】\n%s\n\n%s\n\n%s\n\n%s\n\n【小说草稿】\n%s\n\n请给出你的审查结果：",
		state.SceneCard, state.Context, chapterContractPrompt(state.ChapterContract), mainlineBeatPrompt(state.MainlineBeat), continuityPrompt(state.PreviousContinuity), state.Draft)

	result, err := generateStructuredResponse(
		ctx,
		r.llm,
		"reviewer",
		systemPrompt,
		userPrompt,
		func(candidate []byte) (ReviewResult, error) {
			return decodeReviewResultWithContract(candidate, state.ChapterContract)
		},
		validateReviewResult,
	)
	if err != nil {
		return state, fmt.Errorf("reviewer agent failed to analyze draft: %w", err)
	}

	critique := strings.TrimSpace(result.Critique)
	if result.contractChecked && !result.ContractPassed {
		critique = contractFailureCritique(result.Violations, critique)
	}
	state.ContractAssessment = result.ContractAssessment
	state.IsApproved = result.Passed && result.ContinuityPassed && result.ContractPassed
	state.Critique = critique
	return state, nil
}

func decodeReviewResult(candidate []byte) (ReviewResult, error) {
	return decodeReviewResultWithContract(candidate, ChapterContract{})
}

func decodeReviewResultWithContract(
	candidate []byte,
	contract ChapterContract,
) (ReviewResult, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(candidate, &raw); err != nil {
		return ReviewResult{}, err
	}
	passed, err := decodeRequiredReviewBool(raw, "passed")
	if err != nil {
		return ReviewResult{}, err
	}
	continuityPassed, err := decodeRequiredReviewBool(raw, "continuity_passed")
	if err != nil {
		return ReviewResult{}, err
	}

	contractChecked := !contract.IsEmpty()
	contractPassed := true
	var assessment ChapterContractAssessment
	var violations []string
	if contractChecked {
		assessmentJSON, ok := raw["contract_assessment"]
		if !ok || bytes.Equal(bytes.TrimSpace(assessmentJSON), []byte("null")) {
			return ReviewResult{}, fmt.Errorf(
				"contract_assessment is required when a chapter contract is present",
			)
		}
		assessment, err = decodeChapterContractAssessment(assessmentJSON, contract)
		if err != nil {
			return ReviewResult{}, err
		}
		violations = chapterContractViolations(contract, assessment)
		contractPassed = len(violations) == 0
	}

	var critique string
	if critiqueJSON, ok := raw["critique"]; ok {
		if bytes.Equal(bytes.TrimSpace(critiqueJSON), []byte("null")) {
			return ReviewResult{}, fmt.Errorf("critique must be a string")
		}
		if err := json.Unmarshal(critiqueJSON, &critique); err != nil {
			return ReviewResult{}, fmt.Errorf("critique must be a string")
		}
	}
	return ReviewResult{
		Passed:             passed,
		ContinuityPassed:   continuityPassed,
		ContractPassed:     contractPassed,
		Violations:         violations,
		ContractAssessment: assessment,
		Critique:           critique,
		contractChecked:    contractChecked,
	}, nil
}

func decodeRequiredReviewBool(
	raw map[string]json.RawMessage,
	name string,
) (bool, error) {
	valueJSON, ok := raw[name]
	if !ok || bytes.Equal(bytes.TrimSpace(valueJSON), []byte("null")) {
		return false, fmt.Errorf("%s is required and must be a boolean", name)
	}
	var value bool
	if err := json.Unmarshal(valueJSON, &value); err != nil {
		return false, fmt.Errorf("%s is required and must be a boolean", name)
	}
	return value, nil
}

func decodeChapterContractAssessment(
	candidate []byte,
	contract ChapterContract,
) (ChapterContractAssessment, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(candidate, &raw); err != nil {
		return ChapterContractAssessment{}, fmt.Errorf(
			"contract_assessment must be an object",
		)
	}
	for _, name := range []string{"goal", "must_happen", "must_not_happen", "end_state"} {
		value, ok := raw[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ChapterContractAssessment{}, fmt.Errorf(
				"contract_assessment.%s is required",
				name,
			)
		}
	}
	var wire chapterContractAssessmentWire
	if err := json.Unmarshal(candidate, &wire); err != nil {
		return ChapterContractAssessment{}, fmt.Errorf(
			"contract_assessment is invalid: %w",
			err,
		)
	}
	return normalizeChapterContractAssessment(wire, contract)
}

func validateReviewResult(result *ReviewResult) error {
	result.Critique = strings.TrimSpace(result.Critique)
	if (!result.Passed || !result.ContinuityPassed) && result.Critique == "" {
		return fmt.Errorf("critique is required when review or continuity fails")
	}
	return nil
}
