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

const (
	maxReviewerCritiqueRunes      = 1000
	maxReviewerFeedbackRunes      = 8192
	reviewerContinuityWindowRunes = 500
)

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
	Passed               bool                         `json:"passed"`
	ContinuityPassed     bool                         `json:"-"`
	ContractPassed       bool                         `json:"-"`
	CanonPassed          bool                         `json:"-"`
	MainlinePassed       bool                         `json:"-"`
	Violations           []string                     `json:"-"`
	MainlineAssessment   MainlineAssessment           `json:"mainline_assessment"`
	CanonAssessment      []CanonConsistencyAssessment `json:"canon_assessment"`
	ContinuityAssessment ContinuityAssessment         `json:"continuity_assessment"`
	ContractAssessment   ChapterContractAssessment    `json:"contract_assessment"`
	Critique             string                       `json:"critique"`
	contractChecked      bool
}

func (r *ReviewerAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	if state.Draft == "" {
		return state, fmt.Errorf("draft is empty, nothing to review")
	}

	if issues := ValidateGeneratedContent(state.Draft); len(issues) > 0 {
		state.ContractAssessment = ChapterContractAssessment{}
		state.ContinuityAssessment = ContinuityAssessment{}
		state.CanonAssessment = nil
		state.MainlineAssessment = MainlineAssessment{}
		state.IsApproved = false
		state.Critique = generatedContentIssuesCritique(issues)
		return state, nil
	}

	systemPrompt := `你是一位严厉的小说主编和审查员。你的任务是审查作者提交的【小说草稿】，并对比【场景卡】和【背景资料】，检查是否存在以下问题：
1. 剧情偏离：是否漏写了场景卡中要求的重要情节？
2. 角色 OOC：角色的行为、语言是否与背景资料中的设定相冲突？
3. 行文质量：是否存在逻辑硬伤、水字数、或者描写过于干瘪？
4. 字数要求：正文总字数（按中文字符计）是否在 2500-4000 字之间？
5. 分章节奏：是否把一个应跨多章的大事件在本章“一次性写完”？如果是，必须判定不通过，并要求改成“本章只推进一个阶段，结尾保留悬念/未完成目标”。
6. 连贯性硬门槛：如果存在上一章接力状态，章首是否承接 NextAction 或合理处理 OpenLoops？OpenLoops 可以被解决、升级或转化，不要求原样复述。
7. 本章结尾是否留下具体、可行动的未完成目标供下一章继续？没有上一章接力时 chapter_head 必须返回 null，但仍检查 chapter_tail。
8. 章节契约实际状态：如果存在【本章契约】，必须按原顺序逐项评估 chapter_goal、每条 must_happen、每条 must_not_happen 和 end_state。chapter_goal、must_happen、end_state 为 satisfied=true 时，evidence 必须逐字引用全稿中的单段连续原文；为 false 时写未达成原因。must_not_happen 的 satisfied=false 表示禁止事项实际发生，evidence 必须逐字引用违规原文；为 true 表示禁止事项未发生，只写简短理由，不得虚构正文证据。
9. 主线事件节拍：如果存在【主线事件节拍】，必须返回 mainline_assessment。current_event.satisfied=true 时 evidence 必须逐字引用全稿连续原文；satisfied=false 时写未完成原因。存在下一章预定事件时，next_event.satisfied=true 表示本章没有提前完成，只写理由；satisfied=false 表示提前完成，evidence 必须逐字引用违规原文。若不存在下一章预定事件，next_event 必须为 null。正文必须实际发生本章事件，不能只口头提及或推迟。
10. 角色与世界账本一致性：如果存在【冻结账本约束】，必须按原顺序逐项判断正文是否冲突。constraint_index 必须等于冻结约束前的 1-based 序号。satisfied=true 表示正文与该约束一致；satisfied=false 表示正文实际发生冲突，evidence 必须逐字引用全稿中的单段连续原文。角色和世界当前状态可以被正文合理推进，不要把正常状态变化误判为冲突。

请输出合法 JSON：
{
	"passed": true或false,
	"continuity_assessment": {
		"chapter_head": {"satisfied": true或false, "evidence": "章首原文或缺失原因"},
		"chapter_tail": {"satisfied": true或false, "evidence": "章尾原文或缺失原因"}
	},
	"contract_assessment": {
		"goal": {"satisfied": true或false, "evidence": "正文依据或缺失原因"},
		"must_happen": [{"satisfied": true或false, "evidence": "正文依据或缺失原因"}],
		"must_not_happen": [{"satisfied": true或false, "evidence": "正文依据或发生位置"}],
		"end_state": {"satisfied": true或false, "evidence": "正文依据或缺失原因"}
	},
	"canon_assessment": [{"constraint_index": 1, "satisfied": true或false, "evidence": "一致性理由或正文冲突原文"}],
	"mainline_assessment": {
		"current_event": {"satisfied": true或false, "evidence": "当前主线正文原文或未完成原因"},
		"next_event": {"satisfied": true或false, "evidence": "未提前完成理由或提前完成正文原文"}
	},
	"critique": "如果常规审查、连续性或账本一致性不通过，写明具体修改意见；否则可留空。"
}
如果没有上一章接力状态，continuity_assessment.chapter_head 必须为 null。continuity_assessment 的 satisfied=true evidence 必须逐字引用草稿中的单段连续原文，不得概括、改写或拼接；章首证据必须来自开头，章尾证据必须来自结尾。contract_assessment 的正向通过证据和禁止事项失败证据必须逐字引用全稿中的单段连续原文；未达成原因和禁止事项未发生的理由不要求出现在正文。如果没有结构化章节契约，contract_assessment 可以为 null。如果存在有效【主线事件节拍】，mainline_assessment 必须存在；当前事件正向 evidence 和下一事件提前完成 evidence 必须逐字引用正文。如果没有有效主线节拍，mainline_assessment 可以为 null。如果没有冻结账本约束，canon_assessment 可以为 null；否则数组数量和顺序必须与约束完全一致，每项 constraint_index 必须等于对应冻结约束的 1-based 序号，冲突项的 evidence 必须逐字引用正文。评估数组数量和顺序必须与对应输入完全一致。只返回 JSON，不要输出 Markdown 或解释。`

	userPrompt := fmt.Sprintf("【场景卡】\n%s\n\n【背景资料】\n%s\n\n%s\n\n%s\n\n%s\n\n%s\n\n【小说草稿】\n%s\n\n请给出你的审查结果：",
		state.SceneCard, state.Context, chapterContractPrompt(state.ChapterContract), mainlineBeatPrompt(state.MainlineBeat), continuityPrompt(state.PreviousContinuity), canonConstraintsPrompt(state.CanonConstraints), state.Draft)

	result, err := generateStructuredResponse(
		ctx,
		r.llm,
		"reviewer",
		systemPrompt,
		userPrompt,
		func(candidate []byte) (ReviewResult, error) {
			return decodeReviewResultForState(candidate, state)
		},
		validateReviewResult,
	)
	if err != nil {
		return state, fmt.Errorf("reviewer agent failed to analyze draft: %w", err)
	}

	critique := boundedText(result.Critique, maxReviewerCritiqueRunes)
	if result.contractChecked && !result.ContractPassed {
		critique = boundedText(
			contractFailureCritique(result.Violations, critique),
			maxReviewerFeedbackRunes,
		)
	}
	if !result.CanonPassed {
		critique = boundedText(
			canonFailureCritique(state.CanonConstraints, result.CanonAssessment, critique),
			maxReviewerFeedbackRunes,
		)
	}
	if !result.MainlinePassed {
		critique = boundedText(
			mainlineFailureCritique(state.MainlineBeat, result.MainlineAssessment, critique),
			maxReviewerFeedbackRunes,
		)
	}
	state.ContractAssessment = result.ContractAssessment
	state.ContinuityAssessment = result.ContinuityAssessment
	state.CanonAssessment = result.CanonAssessment
	state.MainlineAssessment = result.MainlineAssessment
	state.IsApproved = result.Passed && result.ContinuityPassed && result.ContractPassed && result.CanonPassed && result.MainlinePassed
	state.Critique = critique
	return state, nil
}

func canonConstraintsPrompt(constraints []CanonConstraint) string {
	if len(constraints) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("【冻结账本约束】\n")
	for index, constraint := range constraints {
		fmt.Fprintf(
			&builder,
			"%d. [%s] %s：%s\n",
			index+1,
			constraint.Kind,
			constraint.Subject,
			constraint.Statement,
		)
	}
	return builder.String()
}

func generatedContentIssuesCritique(issues []GeneratedContentIssue) string {
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		messages = append(messages, issue.Message)
	}
	return strings.Join(messages, "\n")
}

func decodeReviewResult(candidate []byte) (ReviewResult, error) {
	return decodeReviewResultForState(candidate, &GenerationState{
		Draft: strings.Repeat("文", 2500),
	})
}

func decodeReviewResultWithContract(
	candidate []byte,
	contract ChapterContract,
) (ReviewResult, error) {
	return decodeReviewResultForState(candidate, &GenerationState{
		Draft:           strings.Repeat("文", 2500),
		ChapterContract: contract,
	})
}

func decodeReviewResultForState(
	candidate []byte,
	state *GenerationState,
) (ReviewResult, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(candidate, &raw); err != nil {
		return ReviewResult{}, err
	}
	passed, err := decodeRequiredReviewBool(raw, "passed")
	if err != nil {
		return ReviewResult{}, err
	}
	continuityAssessment, continuityPassed, err := decodeContinuityAssessment(
		raw,
		state.Draft,
		!state.PreviousContinuity.IsEmpty(),
	)
	if err != nil {
		return ReviewResult{}, err
	}

	contractChecked := !state.ChapterContract.IsEmpty()
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
		assessment, err = decodeChapterContractAssessment(
			assessmentJSON,
			state.ChapterContract,
		)
		if err != nil {
			return ReviewResult{}, err
		}
		if err := validateChapterContractAssessmentEvidence(
			assessment,
			state.Draft,
		); err != nil {
			return ReviewResult{}, err
		}
		violations = chapterContractViolations(state.ChapterContract, assessment)
		contractPassed = len(violations) == 0
	}

	canonPassed := true
	var canonAssessment []CanonConsistencyAssessment
	if len(state.CanonConstraints) > 0 {
		canonJSON, ok := raw["canon_assessment"]
		if !ok || bytes.Equal(bytes.TrimSpace(canonJSON), []byte("null")) {
			return ReviewResult{}, fmt.Errorf(
				"canon_assessment is required when canon constraints are present",
			)
		}
		canonAssessment, err = decodeCanonConsistencyAssessments(
			canonJSON,
			state.CanonConstraints,
			state.Draft,
		)
		if err != nil {
			return ReviewResult{}, err
		}
		canonPassed = canonConstraintsPassed(canonAssessment)
	}

	mainlinePassed := true
	var mainlineAssessment MainlineAssessment
	if mainlineEventBeatIsValid(state.MainlineBeat) {
		mainlineJSON, ok := raw["mainline_assessment"]
		if !ok || bytes.Equal(bytes.TrimSpace(mainlineJSON), []byte("null")) {
			return ReviewResult{}, fmt.Errorf(
				"mainline_assessment is required when a mainline beat is present",
			)
		}
		mainlineAssessment, mainlinePassed, err = decodeMainlineAssessment(
			mainlineJSON,
			state.MainlineBeat,
			state.Draft,
		)
		if err != nil {
			return ReviewResult{}, err
		}
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
		Passed:               passed,
		ContinuityPassed:     continuityPassed,
		ContractPassed:       contractPassed,
		CanonPassed:          canonPassed,
		MainlinePassed:       mainlinePassed,
		Violations:           violations,
		CanonAssessment:      canonAssessment,
		MainlineAssessment:   mainlineAssessment,
		ContinuityAssessment: continuityAssessment,
		ContractAssessment:   assessment,
		Critique:             critique,
		contractChecked:      contractChecked,
	}, nil
}

func decodeContinuityAssessment(
	raw map[string]json.RawMessage,
	draft string,
	requireHead bool,
) (ContinuityAssessment, bool, error) {
	candidate, ok := raw["continuity_assessment"]
	if !ok || bytes.Equal(bytes.TrimSpace(candidate), []byte("null")) {
		return ContinuityAssessment{}, false, fmt.Errorf(
			"continuity_assessment is required",
		)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(candidate, &fields); err != nil {
		return ContinuityAssessment{}, false, fmt.Errorf(
			"continuity_assessment must be an object",
		)
	}

	headJSON, headPresent := fields["chapter_head"]
	if !headPresent {
		return ContinuityAssessment{}, false, fmt.Errorf(
			"continuity_assessment.chapter_head is required",
		)
	}
	headIsNull := bytes.Equal(bytes.TrimSpace(headJSON), []byte("null"))
	if requireHead && headIsNull {
		return ContinuityAssessment{}, false, fmt.Errorf(
			"continuity_assessment.chapter_head is required when previous continuity is present",
		)
	}
	if !requireHead && !headIsNull {
		return ContinuityAssessment{}, false, fmt.Errorf(
			"continuity_assessment.chapter_head must be null without previous continuity",
		)
	}

	tailJSON, tailPresent := fields["chapter_tail"]
	if !tailPresent || bytes.Equal(bytes.TrimSpace(tailJSON), []byte("null")) {
		return ContinuityAssessment{}, false, fmt.Errorf(
			"continuity_assessment.chapter_tail is required",
		)
	}
	tail, err := decodeContinuityEvidence(
		"continuity_assessment.chapter_tail",
		tailJSON,
		draft,
		false,
	)
	if err != nil {
		return ContinuityAssessment{}, false, err
	}

	assessment := ContinuityAssessment{ChapterTail: tail}
	passed := tail.Satisfied
	if requireHead {
		head, err := decodeContinuityEvidence(
			"continuity_assessment.chapter_head",
			headJSON,
			draft,
			true,
		)
		if err != nil {
			return ContinuityAssessment{}, false, err
		}
		assessment.ChapterHead = &head
		passed = passed && head.Satisfied
	}
	return assessment, passed, nil
}

func decodeContinuityEvidence(
	name string,
	candidate []byte,
	draft string,
	head bool,
) (ContractRequirementAssessment, error) {
	var wire contractRequirementAssessmentWire
	if err := json.Unmarshal(candidate, &wire); err != nil {
		return ContractRequirementAssessment{}, fmt.Errorf("%s must be an object", name)
	}
	item, err := normalizeContractRequirementAssessment(name, wire)
	if err != nil {
		return ContractRequirementAssessment{}, err
	}
	if !item.Satisfied {
		return item, nil
	}

	draftRunes := []rune(draft)
	windowRunes := draftRunes
	if len(windowRunes) > reviewerContinuityWindowRunes {
		if head {
			windowRunes = windowRunes[:reviewerContinuityWindowRunes]
		} else {
			windowRunes = windowRunes[len(windowRunes)-reviewerContinuityWindowRunes:]
		}
	}
	if !strings.Contains(string(windowRunes), item.Evidence) {
		position := "chapter tail"
		if head {
			position = "chapter head"
		}
		return ContractRequirementAssessment{}, fmt.Errorf(
			"%s.evidence must be an exact draft substring within the %s window",
			name,
			position,
		)
	}
	return item, nil
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
	if (!result.Passed || !result.ContinuityPassed || !result.CanonPassed || !result.MainlinePassed) && result.Critique == "" {
		return fmt.Errorf("critique is required when review, continuity, canon consistency, or mainline beat fails")
	}
	return nil
}
