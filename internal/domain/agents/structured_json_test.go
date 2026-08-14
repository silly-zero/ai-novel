package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type queuedStructuredLLM struct {
	responses []string
	errors    []error
	calls     int
	systems   []string
	users     []string
	afterCall func(int)
}

func (f *queuedStructuredLLM) Generate(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	index := f.calls
	f.calls++
	f.systems = append(f.systems, systemPrompt)
	f.users = append(f.users, userPrompt)
	if f.afterCall != nil {
		f.afterCall(f.calls)
	}
	if index < len(f.errors) && f.errors[index] != nil {
		return "", f.errors[index]
	}
	if index >= len(f.responses) {
		return "", fmt.Errorf("unexpected Generate call %d", f.calls)
	}
	return f.responses[index], nil
}

func (*queuedStructuredLLM) StreamGenerate(
	context.Context,
	string,
	string,
	func(string) error,
) error {
	return nil
}

type structuredTestPayload struct {
	Message string   `json:"message"`
	Items   []string `json:"items"`
}

func TestParseStructuredResponseAcceptsCommonJSONForms(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "plain", input: `{"message":"ok","items":["a"]}`},
		{name: "json fence", input: "```json\n{\"message\":\"ok\",\"items\":[\"a\"]}\n```"},
		{name: "uppercase fence", input: "说明：\n```JSON\n{\"message\":\"ok\",\"items\":[\"a\"]}\n```\n完成"},
		{name: "plain fence", input: "```\n{\"message\":\"ok\",\"items\":[\"a\"]}\n```"},
		{name: "surrounding prose", input: `结果如下：{"message":"ok","items":["a"]}以上。`},
		{name: "delimiters in string", input: `忽略 {not json}，使用：{"message":"括号 { [ ] } 与转义引号 \" 正常","items":["a"]}。`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := parseStructuredResponse(
				test.input,
				decodeJSON[structuredTestPayload],
				func(payload *structuredTestPayload) error {
					if payload.Message == "" {
						return errors.New("message is required")
					}
					return nil
				},
			)
			if err != nil {
				t.Fatalf("parseStructuredResponse returned error: %v", err)
			}
			if result.Message == "" || len(result.Items) != 1 {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestParseStructuredResponseTriesLaterCandidate(t *testing.T) {
	result, err := parseStructuredResponse(
		`前置 {"message":""} 后置 {"message":"valid","items":[]}`,
		decodeJSON[structuredTestPayload],
		func(payload *structuredTestPayload) error {
			if payload.Message == "" {
				return errors.New("message is required")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("parseStructuredResponse returned error: %v", err)
	}
	if result.Message != "valid" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBalancedJSONCandidatesRecoversAfterUnclosedProseDelimiter(t *testing.T) {
	result, err := parseStructuredResponse(
		`前置 {未闭合文本，随后是 {"message":"valid","items":[]}`,
		decodeJSON[structuredTestPayload],
		nil,
	)
	if err != nil {
		t.Fatalf("parseStructuredResponse returned error: %v", err)
	}
	if result.Message != "valid" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBalancedJSONCandidatesRecoversBeforeMismatchedDelimiter(t *testing.T) {
	result, err := parseStructuredResponse(
		`前置 {随后是 {"message":"valid","items":[]} ]`,
		decodeJSON[structuredTestPayload],
		nil,
	)
	if err != nil {
		t.Fatalf("parseStructuredResponse returned error: %v", err)
	}
	if result.Message != "valid" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBalancedJSONCandidatesTriesNestedRecoveryCandidates(t *testing.T) {
	result, err := parseStructuredResponse(
		`前置 {未闭合，随后是 [{"message":"valid","items":[]}]`,
		decodeJSON[structuredTestPayload],
		nil,
	)
	if err != nil {
		t.Fatalf("parseStructuredResponse returned error: %v", err)
	}
	if result.Message != "valid" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBalancedJSONCandidatesDoesNotAcceptNestedObjectFromValidOuterValue(t *testing.T) {
	_, err := parseStructuredResponse(
		`{"outer":{"message":"nested","items":[]}}`,
		decodeJSON[structuredTestPayload],
		func(payload *structuredTestPayload) error {
			if payload.Message == "" {
				return errors.New("message is required")
			}
			return nil
		},
	)
	if err == nil {
		t.Fatal("nested object from valid outer value was accepted")
	}
}

func TestBalancedJSONCandidatesHandlesLargeUnclosedInput(t *testing.T) {
	input := strings.Repeat("{", 100_000)
	if candidates := balancedJSONCandidates(input); len(candidates) != 0 {
		t.Fatalf("candidates = %d, want 0", len(candidates))
	}
}

func TestGenerateStructuredResponseRepairsOnce(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"message":`,
		`{"message":"fixed","items":[]}`,
	}}

	result, err := generateStructuredResponse(
		context.Background(),
		llm,
		"test-agent",
		"schema",
		"task",
		decodeJSON[structuredTestPayload],
		nil,
	)
	if err != nil {
		t.Fatalf("generateStructuredResponse returned error: %v", err)
	}
	if result.Message != "fixed" || llm.calls != 2 {
		t.Fatalf("result = %#v, calls = %d", result, llm.calls)
	}
	if !strings.Contains(llm.systems[1], "只返回一个") ||
		!strings.Contains(llm.users[1], "<previous_response>") {
		t.Fatalf("repair prompts = %#v / %#v", llm.systems[1], llm.users[1])
	}
}

func TestCharacterStructuredResponseRepairsMissingCurrentStatus(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"characters":[{"name":"林云"}]}`,
		`{"characters":[{"name":"林云","current_status":"本章结束时留在青云山"}]}`,
	}}

	result, err := generateStructuredResponse(
		context.Background(),
		llm,
		"character",
		"schema",
		"task",
		decodeCharacterExtraction,
		validateCharacterExtraction,
	)
	if err != nil {
		t.Fatalf("generateStructuredResponse returned error: %v", err)
	}
	if llm.calls != 2 || len(result.Characters) != 1 || result.Characters[0].CurrentStatus != "本章结束时留在青云山" {
		t.Fatalf("result = %#v, calls = %d", result, llm.calls)
	}
}

func TestWorldStructuredResponseRepairsMissingCurrentState(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`[{"category":"地理","name":"青云山","description":"终年云雾环绕的修炼宗门"}]`,
		`[{"category":"地理","name":"青云山","description":"终年云雾环绕的修炼宗门","current_state":"本章结束时山门封闭"}]`,
	}}

	result, err := generateStructuredResponse(
		context.Background(),
		llm,
		"world",
		"schema",
		"task",
		decodeJSON[[]WorldSettingUpdate],
		validateWorldSettingUpdatesForExisting(nil),
	)
	if err != nil {
		t.Fatalf("generateStructuredResponse returned error: %v", err)
	}
	if llm.calls != 2 || len(result) != 1 || result[0].CurrentState != "本章结束时山门封闭" {
		t.Fatalf("result = %#v, calls = %d", result, llm.calls)
	}
}

func TestGenerateStructuredResponseReturnsBoundedErrorAfterRepair(t *testing.T) {
	largeResponse := strings.Repeat("模型输出异常", 5000)
	llm := &queuedStructuredLLM{responses: []string{largeResponse, largeResponse}}

	_, err := generateStructuredResponse(
		context.Background(),
		llm,
		"world",
		"schema",
		"task",
		decodeJSON[structuredTestPayload],
		nil,
	)
	if err == nil {
		t.Fatal("generateStructuredResponse returned nil error")
	}
	if llm.calls != 2 {
		t.Fatalf("calls = %d, want 2", llm.calls)
	}
	if !strings.Contains(err.Error(), "world structured response invalid after 2 attempts") {
		t.Fatalf("error = %v", err)
	}
	if len([]rune(err.Error())) > 700 || strings.Contains(err.Error(), largeResponse) {
		t.Fatalf("error is not bounded: %d runes", len([]rune(err.Error())))
	}
}

func TestGenerateStructuredResponseDoesNotRepairRequestErrors(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	llm := &queuedStructuredLLM{responses: []string{""}, errors: []error{providerErr}}

	_, err := generateStructuredResponse(
		context.Background(),
		llm,
		"reviewer",
		"schema",
		"task",
		decodeJSON[structuredTestPayload],
		nil,
	)
	if !errors.Is(err, providerErr) || llm.calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, llm.calls)
	}
}

func TestGenerateStructuredResponseReturnsRepairRequestError(t *testing.T) {
	providerErr := errors.New("repair unavailable")
	llm := &queuedStructuredLLM{
		responses: []string{`{"message":`, ""},
		errors:    []error{nil, providerErr},
	}

	_, err := generateStructuredResponse(
		context.Background(),
		llm,
		"reviewer",
		"schema",
		"task",
		decodeJSON[structuredTestPayload],
		nil,
	)
	if !errors.Is(err, providerErr) || llm.calls != 2 {
		t.Fatalf("error = %v, calls = %d", err, llm.calls)
	}
}

func TestGenerateStructuredResponseStopsBeforeRepairWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	llm := &queuedStructuredLLM{responses: []string{`{"message":`}}
	llm.afterCall = func(call int) {
		if call == 1 {
			cancel()
		}
	}

	_, err := generateStructuredResponse(
		ctx,
		llm,
		"librarian",
		"schema",
		"task",
		decodeJSON[structuredTestPayload],
		nil,
	)
	if !errors.Is(err, context.Canceled) || llm.calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, llm.calls)
	}
}

func TestLibrarianStructuredPlanRepairsInvalidResponse(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"character_names":[],"world_settings":[],"search_queries":[]}`,
		`结果：{"character_names":[" 林云 "],"world_settings":[],"search_queries":[" 青云山历史 "]}`,
	}}
	agent := NewLibrarianAgent(llm, nil, nil, nil, nil, LibrarianConfig{})

	plan, err := agent.makeRetrievalPlan(context.Background(), &GenerationState{Outline: "大纲"})
	if err != nil {
		t.Fatalf("makeRetrievalPlan returned error: %v", err)
	}
	if llm.calls != 2 || plan.CharacterNames[0] != "林云" || plan.SearchQueries[0] != "青云山历史" {
		t.Fatalf("plan = %#v, calls = %d", plan, llm.calls)
	}
}

func TestReviewerInjectsMainlineBeatAndUsesExistingFailureProtocol(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"passed":false,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":"文"},"next_event":{"satisfied":true,"evidence":"下一事件尚未完成"}},"critique":"本章只提到血书线索，没有让主角实际找到血书"}`,
	}}
	state := &GenerationState{
		Draft: strings.Repeat("文", 2500),
		MainlineBeat: MainlineEventBeat{
			ChapterIndex: 4,
			CurrentEvent: "主角找到血书",
			NextEvent:    "主角前往地下祭坛",
		},
	}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsApproved || got.Critique != "本章只提到血书线索，没有让主角实际找到血书" {
		t.Fatalf("state = %#v", got)
	}
	for _, value := range []string{"第4章", "主角找到血书", "主角前往地下祭坛"} {
		if !strings.Contains(llm.users[0], value) {
			t.Fatalf("reviewer prompt missing %q: %s", value, llm.users[0])
		}
	}
	for _, rule := range []string{"实际发生本章事件", "提前完成", "satisfied=false"} {
		if !strings.Contains(llm.systems[0], rule) {
			t.Fatalf("reviewer system prompt missing %q: %s", rule, llm.systems[0])
		}
	}
}

func TestReviewerStructuredFailureDoesNotBecomeQualityRetry(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{"not json", "still not json"}}
	agent := NewReviewerAgent(llm)
	oldAssessment := ChapterContractAssessment{
		Goal: ContractRequirementAssessment{
			Satisfied: true,
			Evidence:  "旧评估依据",
		},
	}
	state := &GenerationState{
		Draft:              strings.Repeat("文", 2500),
		ChapterContract:    validChapterContract(),
		ContractAssessment: oldAssessment,
		Critique:           "existing critique",
		IsApproved:         true,
		RetryCount:         2,
	}

	_, err := agent.Run(context.Background(), state)
	if err == nil {
		t.Fatal("ReviewerAgent.Run returned nil error")
	}
	if state.Critique != "existing critique" || state.RetryCount != 2 || llm.calls != 2 {
		t.Fatalf("state = %#v, calls = %d", state, llm.calls)
	}
	if state.ContractAssessment.Goal != oldAssessment.Goal || !state.IsApproved {
		t.Fatalf("review state changed after invalid responses: assessment = %#v, approved = %v", state.ContractAssessment, state.IsApproved)
	}
}

func TestReviewerWordCountPrecheckClearsOldContractAssessment(t *testing.T) {
	llm := &queuedStructuredLLM{}
	state := &GenerationState{
		Draft: strings.Repeat("文", 2499),
		ContractAssessment: ChapterContractAssessment{
			Goal: ContractRequirementAssessment{
				Satisfied: true,
				Evidence:  "旧评估依据",
			},
		},
		IsApproved: true,
	}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContractAssessment.Goal != (ContractRequirementAssessment{}) ||
		len(got.ContractAssessment.MustHappen) != 0 ||
		len(got.ContractAssessment.MustNotHappen) != 0 ||
		got.ContractAssessment.EndState != (ContractRequirementAssessment{}) {
		t.Fatalf("ContractAssessment = %#v, want empty", got.ContractAssessment)
	}
	if got.IsApproved || llm.calls != 0 {
		t.Fatalf("approved = %v, calls = %d", got.IsApproved, llm.calls)
	}
}

func TestReviewerEmptyDraftStillReturnsError(t *testing.T) {
	llm := &queuedStructuredLLM{}
	state := &GenerationState{IsApproved: true, Critique: "旧修改意见"}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "draft is empty") {
		t.Fatalf("error = %v, want empty draft error", err)
	}
	if got != state || !got.IsApproved || got.Critique != "旧修改意见" || llm.calls != 0 {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestReviewerValidDraftStillRunsContinuityReview(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"passed":true,"continuity_assessment":{"chapter_head":{"satisfied":false,"evidence":"章首没有承接上一章行动"},"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"critique":"章首没有承接上一章行动"}`,
	}}
	state := &GenerationState{
		Draft: strings.Repeat("文", 2500),
		PreviousContinuity: ContinuityPacket{
			LastBeat:   "主角推开密门。",
			OpenLoops:  []string{"密门后是谁"},
			NextAction: "主角立即进入密门。",
		},
	}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsApproved || got.Critique != "章首没有承接上一章行动" || llm.calls != 1 {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
	for _, value := range []string{"主角推开密门。", "密门后是谁", "主角立即进入密门。"} {
		if !strings.Contains(llm.users[0], value) {
			t.Fatalf("reviewer prompt missing %q: %s", value, llm.users[0])
		}
	}
}

func TestReviewerDeterministicPrecheckRejectsBeforeLLM(t *testing.T) {
	llm := &queuedStructuredLLM{}
	state := &GenerationState{
		Draft: strings.Repeat("文", 2500) + "【场景卡】\x00",
		ContractAssessment: ChapterContractAssessment{
			Goal: ContractRequirementAssessment{
				Satisfied: true,
				Evidence:  "旧评估依据",
			},
		},
		Critique:   "旧修改意见",
		IsApproved: true,
	}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContractAssessment.Goal != (ContractRequirementAssessment{}) ||
		len(got.ContractAssessment.MustHappen) != 0 ||
		len(got.ContractAssessment.MustNotHappen) != 0 ||
		got.ContractAssessment.EndState != (ContractRequirementAssessment{}) {
		t.Fatalf("ContractAssessment = %#v, want empty", got.ContractAssessment)
	}
	if got.IsApproved || llm.calls != 0 {
		t.Fatalf("approved = %v, calls = %d", got.IsApproved, llm.calls)
	}
	for _, value := range []string{"异常控制字符", "内部提示标签"} {
		if !strings.Contains(got.Critique, value) {
			t.Fatalf("critique missing %q: %s", value, got.Critique)
		}
	}
	if strings.Contains(got.Critique, "【场景卡】") || len([]rune(got.Critique)) > 300 {
		t.Fatalf("unsafe or unbounded critique = %q", got.Critique)
	}
}

func TestReviewerBoundsModelCritiqueBeforeRetry(t *testing.T) {
	longCritique := "敏感原文" + strings.Repeat("改", maxReviewerCritiqueRunes+100)
	llm := &queuedStructuredLLM{responses: []string{
		fmt.Sprintf(`{"passed":false,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"critique":%q}`, longCritique),
	}}

	got, err := NewReviewerAgent(llm).Run(context.Background(), &GenerationState{
		Draft: strings.Repeat("文", 2500),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(got.Critique)) != maxReviewerCritiqueRunes+1 ||
		!strings.HasSuffix(got.Critique, "…") {
		t.Fatalf("critique length = %d, suffix = %q", len([]rune(got.Critique)), got.Critique[len(got.Critique)-3:])
	}
}

func TestReviewerStructuredRepairProducesReviewResult(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"passed":"false","critique":"修改"}`,
		`{"passed":false,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":false,"evidence":"章尾没有留下具体行动"}},"critique":" 补充场景冲突 "}`,
	}}
	agent := NewReviewerAgent(llm)
	state := &GenerationState{Draft: strings.Repeat("文", 2500)}

	result, err := agent.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("ReviewerAgent.Run returned error: %v", err)
	}
	if result.IsApproved || result.Critique != "补充场景冲突" || result.RetryCount != 0 {
		t.Fatalf("state = %#v", result)
	}
}

func contractEvidenceDraft() string {
	core := "主角确认密门与身世有关。他跨入密门，在石台上发现旧王朝血书。反派身份仍未揭晓，他决定追踪血书指向的地下祭坛。"
	return strings.Repeat("文", 2500-len([]rune(core))) + core
}

func passingContractAssessmentJSON() string {
	return `{"goal":{"satisfied":true,"evidence":"主角确认密门与身世有关。"},"must_happen":[{"satisfied":true,"evidence":"他跨入密门"},{"satisfied":true,"evidence":"发现旧王朝血书"}],"must_not_happen":[{"satisfied":true,"evidence":"正文未揭晓最终反派身份"}],"end_state":{"satisfied":true,"evidence":"他决定追踪血书指向的地下祭坛。"}}`
}

func TestReviewerContractGate(t *testing.T) {
	passing := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":` + passingContractAssessmentJSON() + `,"critique":""}`
	failing := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"satisfied":true,"evidence":"主角确认密门与身世有关。"},"must_happen":[{"satisfied":true,"evidence":"他跨入密门"},{"satisfied":false,"evidence":"正文没有发现血书"}],"must_not_happen":[{"satisfied":true,"evidence":"正文未揭晓最终反派身份"}],"end_state":{"satisfied":true,"evidence":"他决定追踪血书指向的地下祭坛。"}},"critique":"补强情节"}`

	for _, test := range []struct {
		name         string
		response     string
		wantApproved bool
		wantCritique []string
	}{
		{name: "passes complete contract", response: passing, wantApproved: true},
		{
			name:         "rejects derived contract violation",
			response:     failing,
			wantCritique: []string{"must_happen[1]", "主角发现旧王朝血书", "正文没有发现血书", "补强情节"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			llm := &queuedStructuredLLM{responses: []string{test.response}}
			state := &GenerationState{
				Draft:           contractEvidenceDraft(),
				ChapterContract: validChapterContract(),
			}

			got, err := NewReviewerAgent(llm).Run(context.Background(), state)
			if err != nil {
				t.Fatal(err)
			}
			if got.IsApproved != test.wantApproved {
				t.Fatalf("state = %#v", got)
			}
			for _, value := range test.wantCritique {
				if !strings.Contains(got.Critique, value) {
					t.Fatalf("critique missing %q: %s", value, got.Critique)
				}
			}
			if got.ContractAssessment.Goal.Evidence != "主角确认密门与身世有关。" {
				t.Fatalf("assessment = %#v", got.ContractAssessment)
			}
			if llm.calls != 1 {
				t.Fatalf("reviewer calls = %d, want 1 for a structurally valid assessment", llm.calls)
			}
		})
	}
}

func TestValidateChapterContractAssessmentEvidenceMatrix(t *testing.T) {
	draft := "主角确认了密门来源。随后他发现一封血书。最终他决定前往地下祭坛。反派摘下面具，坦白了真实身份。"
	valid := ChapterContractAssessment{
		Goal: ContractRequirementAssessment{
			Satisfied: true,
			Evidence:  "主角确认了密门来源。",
		},
		MustHappen: []ContractRequirementAssessment{
			{Satisfied: true, Evidence: "他发现一封血书。"},
		},
		MustNotHappen: []ContractRequirementAssessment{
			{Satisfied: true, Evidence: "正文没有揭晓最终反派身份"},
		},
		EndState: ContractRequirementAssessment{
			Satisfied: true,
			Evidence:  "他决定前往地下祭坛。",
		},
	}
	if err := validateChapterContractAssessmentEvidence(valid, draft); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		assessment ChapterContractAssessment
		want       string
	}{
		{
			name: "goal true requires exact quote",
			assessment: func() ChapterContractAssessment {
				assessment := valid
				assessment.Goal.Evidence = "主角查明了密门来源。"
				return assessment
			}(),
			want: "contract_assessment.goal.evidence",
		},
		{
			name: "must happen true rejects spliced quote",
			assessment: func() ChapterContractAssessment {
				assessment := valid
				assessment.MustHappen = append([]ContractRequirementAssessment(nil), valid.MustHappen...)
				assessment.MustHappen[0].Evidence = "随后他发现血书。"
				return assessment
			}(),
			want: "contract_assessment.must_happen[0].evidence",
		},
		{
			name: "end state true requires exact quote",
			assessment: func() ChapterContractAssessment {
				assessment := valid
				assessment.EndState.Evidence = "他准备前往地下祭坛。"
				return assessment
			}(),
			want: "contract_assessment.end_state.evidence",
		},
		{
			name: "forbidden event false requires violation quote",
			assessment: func() ChapterContractAssessment {
				assessment := valid
				assessment.MustNotHappen = []ContractRequirementAssessment{{
					Evidence: "反派已经公开身份",
				}}
				return assessment
			}(),
			want: "contract_assessment.must_not_happen[0].evidence",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateChapterContractAssessmentEvidence(test.assessment, draft)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateChapterContractAssessmentEvidenceAllowsReasons(t *testing.T) {
	draft := "反派摘下面具，坦白了真实身份。"
	assessment := ChapterContractAssessment{
		Goal: ContractRequirementAssessment{
			Evidence: "正文没有确认密门来源",
		},
		MustHappen: []ContractRequirementAssessment{
			{Evidence: "正文没有发现血书"},
		},
		MustNotHappen: []ContractRequirementAssessment{
			{Satisfied: true, Evidence: "正文没有提前揭晓幕后主使"},
			{Evidence: "反派摘下面具，坦白了真实身份。"},
		},
		EndState: ContractRequirementAssessment{
			Evidence: "正文没有决定下一步行动",
		},
	}

	if err := validateChapterContractAssessmentEvidence(assessment, draft); err != nil {
		t.Fatal(err)
	}
}

func TestReviewerRepairsInvalidContractEvidenceOnce(t *testing.T) {
	invalid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"satisfied":true,"evidence":"概括性的目标已完成"},"must_happen":[{"satisfied":true,"evidence":"他跨入密门"},{"satisfied":true,"evidence":"发现旧王朝血书"}],"must_not_happen":[{"satisfied":true,"evidence":"正文未揭晓最终反派身份"}],"end_state":{"satisfied":true,"evidence":"他决定追踪血书指向的地下祭坛。"}},"critique":""}`
	valid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":` + passingContractAssessmentJSON() + `,"critique":""}`
	llm := &queuedStructuredLLM{responses: []string{invalid, valid}}

	got, err := NewReviewerAgent(llm).Run(context.Background(), &GenerationState{
		Draft:           contractEvidenceDraft(),
		ChapterContract: validChapterContract(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsApproved || llm.calls != 2 {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestChapterContractViolationsCoversEveryRequirementKind(t *testing.T) {
	contract := validChapterContract()
	assessment := ChapterContractAssessment{
		Goal: ContractRequirementAssessment{Evidence: "目标未完成"},
		MustHappen: []ContractRequirementAssessment{
			{Satisfied: true, Evidence: "已进入密门"},
			{Evidence: "没有发现血书"},
		},
		MustNotHappen: []ContractRequirementAssessment{
			{Evidence: "正文提前揭晓了反派"},
		},
		EndState: ContractRequirementAssessment{Evidence: "没有决定追踪祭坛"},
	}

	violations := chapterContractViolations(contract, assessment)
	joined := strings.Join(violations, "\n")
	for _, value := range []string{
		"chapter_goal",
		contract.Goal,
		"must_happen[1]",
		contract.MustHappen[1],
		"must_not_happen[0]",
		contract.MustNotHappen[0],
		"end_state",
		contract.EndState,
		"目标未完成",
		"没有发现血书",
		"正文提前揭晓了反派",
		"没有决定追踪祭坛",
	} {
		if !strings.Contains(joined, value) {
			t.Fatalf("violations missing %q: %s", value, joined)
		}
	}
}

func TestReviewerRepairsStructurallyInvalidContractAssessmentOnce(t *testing.T) {
	invalid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"satisfied":true,"evidence":"依据"},"must_happen":[],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`
	valid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":` + passingContractAssessmentJSON() + `,"critique":""}`
	llm := &queuedStructuredLLM{responses: []string{invalid, valid}}
	state := &GenerationState{
		Draft:           contractEvidenceDraft(),
		ChapterContract: validChapterContract(),
	}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsApproved || llm.calls != 2 {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestReviewerContractValidationRejectsInvalidResults(t *testing.T) {
	tooLongEvidence := strings.Repeat("据", maxContractAssessmentEvidenceRunes+1)
	tests := []string{
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"evidence":"依据"},"must_happen":[{"satisfied":true,"evidence":"依据"},{"satisfied":true,"evidence":"依据"}],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"satisfied":true,"evidence":"依据"},"must_happen":[],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"satisfied":true,"evidence":" "},"must_happen":[{"satisfied":true,"evidence":"依据"},{"satisfied":true,"evidence":"依据"}],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`,
		fmt.Sprintf(`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":{"goal":{"satisfied":true,"evidence":%q},"must_happen":[{"satisfied":true,"evidence":"依据"},{"satisfied":true,"evidence":"依据"}],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`, tooLongEvidence),
	}
	for _, response := range tests {
		_, err := parseStructuredResponse(
			response,
			func(candidate []byte) (ReviewResult, error) {
				return decodeReviewResultWithContract(candidate, validChapterContract())
			},
			validateReviewResult,
		)
		if err == nil {
			t.Fatalf("contract review response %q was accepted", response)
		}
	}
}

func TestReviewerCanonGate(t *testing.T) {
	conflictEvidence := "林云忽然说自己从未见过苏青。"
	draft := strings.Repeat("文", 2500-len([]rune(conflictEvidence))) + conflictEvidence
	constraints := []CanonConstraint{
		{Kind: "character_relationship", Subject: "林云->苏青", Statement: "角色关系：林云与苏青是盟友"},
		{Kind: "world_current_state", Subject: "青云山", Statement: "世界设定青云山当前状态：山门封闭"},
	}

	passing := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"satisfied":true,"evidence":"正文延续了盟友关系"},{"constraint_index":2,"satisfied":true,"evidence":"正文明确描写守卫打开山门，属于合理状态推进"}],"critique":""}`
	got, err := NewReviewerAgent(&queuedStructuredLLM{responses: []string{passing}}).Run(
		context.Background(),
		&GenerationState{Draft: draft, CanonConstraints: constraints},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsApproved || len(got.CanonAssessment) != 2 {
		t.Fatalf("state = %#v", got)
	}

	failing := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"satisfied":false,"evidence":"林云忽然说自己从未见过苏青。"},{"constraint_index":2,"satisfied":true,"evidence":"正文没有违反山门状态"}],"critique":"修正角色关系"}`
	got, err = NewReviewerAgent(&queuedStructuredLLM{responses: []string{failing}}).Run(
		context.Background(),
		&GenerationState{Draft: draft, CanonConstraints: constraints},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsApproved || !strings.Contains(got.Critique, "character_relationship/林云->苏青") ||
		!strings.Contains(got.Critique, "林云忽然说自己从未见过苏青。") {
		t.Fatalf("state = %#v", got)
	}
}

func TestReviewerCanonValidationRejectsInvalidResults(t *testing.T) {
	draft := strings.Repeat("文", 2500)
	constraint := CanonConstraint{Kind: "character_static", Subject: "林云", Statement: "角色林云的性格：谨慎"}
	tooLong := strings.Repeat("据", maxContractAssessmentEvidenceRunes+1)
	responses := []string{
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":null,"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[],"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"satisfied":true,"evidence":"理由"}],"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":2,"satisfied":true,"evidence":"理由"}],"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"evidence":"理由"}],"critique":""}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"satisfied":true,"evidence":" "}],"critique":""}`,
		fmt.Sprintf(`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"satisfied":true,"evidence":%q}],"critique":""}`, tooLong),
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"satisfied":false,"evidence":"正文外的冲突概括"}],"critique":"冲突"}`,
	}
	for _, response := range responses {
		_, err := parseStructuredResponse(
			response,
			func(candidate []byte) (ReviewResult, error) {
				return decodeReviewResultForState(candidate, &GenerationState{
					Draft:            draft,
					CanonConstraints: []CanonConstraint{constraint},
				})
			},
			validateReviewResult,
		)
		if err == nil {
			t.Fatalf("canon response %q was accepted", response)
		}
	}
}

func TestReviewerRepairsInvalidCanonEvidenceOnce(t *testing.T) {
	conflictEvidence := "林云宣称自己从未认识苏青。"
	draft := strings.Repeat("文", 2500-len([]rune(conflictEvidence))) + conflictEvidence
	invalid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"satisfied":false,"evidence":"林云否认盟友关系"}],"critique":"修正关系"}`
	valid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"satisfied":false,"evidence":"林云宣称自己从未认识苏青。"}],"critique":"修正关系"}`
	llm := &queuedStructuredLLM{responses: []string{invalid, valid}}
	state := &GenerationState{
		Draft: draft,
		CanonConstraints: []CanonConstraint{{
			Kind:      "character_relationship",
			Subject:   "林云->苏青",
			Statement: "角色关系：林云与苏青是盟友",
		}},
	}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsApproved || llm.calls != 2 || got.CanonAssessment[0].Evidence != "林云宣称自己从未认识苏青。" {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestReviewerInvalidCanonResponsesPreserveReviewState(t *testing.T) {
	invalid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"canon_assessment":[{"constraint_index":1,"satisfied":false,"evidence":"正文外的冲突概括"}],"critique":"修正关系"}`
	llm := &queuedStructuredLLM{responses: []string{invalid, invalid}}
	oldContract := ChapterContractAssessment{Goal: ContractRequirementAssessment{Satisfied: true, Evidence: "旧契约依据"}}
	oldContinuity := ContinuityAssessment{ChapterTail: ContractRequirementAssessment{Satisfied: true, Evidence: "旧章尾依据"}}
	oldCanon := []CanonConsistencyAssessment{{ConstraintIndex: 1, Satisfied: true, Evidence: "旧账本依据"}}
	state := &GenerationState{
		Draft:                strings.Repeat("文", 2500),
		ContractAssessment:   oldContract,
		ContinuityAssessment: oldContinuity,
		CanonConstraints: []CanonConstraint{{
			Kind:      "character_relationship",
			Subject:   "林云->苏青[盟友]",
			Statement: "角色关系：林云与苏青是盟友",
		}},
		CanonAssessment: append([]CanonConsistencyAssessment(nil), oldCanon...),
		Critique:        "旧修改意见",
		IsApproved:      true,
	}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "invalid after 2 attempts") {
		t.Fatalf("error = %v, want bounded two-attempt structured error", err)
	}
	if llm.calls != 2 {
		t.Fatalf("calls = %d, want 2", llm.calls)
	}
	if len([]rune(err.Error())) > 2*structuredResponsePreviewRunes+500 {
		t.Fatalf("error is unexpectedly large: %d runes", len([]rune(err.Error())))
	}
	if got != state || got.Draft != state.Draft || got.Critique != "旧修改意见" ||
		got.ContractAssessment.Goal != oldContract.Goal ||
		got.ContinuityAssessment.ChapterTail != oldContinuity.ChapterTail ||
		len(got.CanonAssessment) != 1 || got.CanonAssessment[0] != oldCanon[0] ||
		!got.IsApproved {
		t.Fatalf("review state changed after invalid canon responses: %#v", got)
	}
}

func TestReviewerMainlineGate(t *testing.T) {
	draft := "主角终于夺得血书。" + strings.Repeat("文", 2490) + "主角决定前往祭坛。"
	beat := MainlineEventBeat{ChapterIndex: 4, CurrentEvent: "主角找到血书", NextEvent: "主角前往地下祭坛"}
	passing := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":"主角终于夺得血书。"},"next_event":{"satisfied":true,"evidence":"本章仅作出前往祭坛的决定，尚未抵达"}},"critique":""}`
	got, err := NewReviewerAgent(&queuedStructuredLLM{responses: []string{passing}}).Run(context.Background(), &GenerationState{Draft: draft, MainlineBeat: beat})
	if err != nil || !got.IsApproved || !got.MainlineAssessment.CurrentEvent.Satisfied {
		t.Fatalf("passing state = %#v, err = %v", got, err)
	}

	failing := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":false,"evidence":"本章尚未让主角找到血书"},"next_event":{"satisfied":true,"evidence":"下一事件尚未完成"}},"critique":"补写主线事件"}`
	got, err = NewReviewerAgent(&queuedStructuredLLM{responses: []string{failing}}).Run(context.Background(), &GenerationState{Draft: draft, MainlineBeat: beat})
	if err != nil || got.IsApproved || !strings.Contains(got.Critique, "主角找到血书") {
		t.Fatalf("current-event failure state = %#v, err = %v", got, err)
	}

	nextFailureEvidence := "主角已抵达地下祭坛。"
	nextDraft := strings.Repeat("文", 2500-len([]rune(nextFailureEvidence))) + nextFailureEvidence
	nextFailing := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":"文"},"next_event":{"satisfied":false,"evidence":"主角已抵达地下祭坛。"}},"critique":"不得提前完成下一章事件"}`
	got, err = NewReviewerAgent(&queuedStructuredLLM{responses: []string{nextFailing}}).Run(context.Background(), &GenerationState{Draft: nextDraft, MainlineBeat: beat})
	if err != nil || got.IsApproved || !strings.Contains(got.Critique, "提前完成下一章主线事件") {
		t.Fatalf("next-event failure state = %#v, err = %v", got, err)
	}
}

func TestReviewerMainlineValidationRejectsInvalidResults(t *testing.T) {
	draft := strings.Repeat("文", 2500)
	beat := MainlineEventBeat{ChapterIndex: 4, CurrentEvent: "找到血书"}
	responses := []string{
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"critique":"缺少主线评估"}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":null,"critique":"缺少主线评估"}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":"正文外"},"next_event":null},"critique":"证据错误"}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":""},"next_event":null},"critique":"证据为空"}`,
	}
	for _, response := range responses {
		_, err := parseStructuredResponse(response, func(candidate []byte) (ReviewResult, error) {
			return decodeReviewResultForState(candidate, &GenerationState{Draft: draft, MainlineBeat: beat})
		}, validateReviewResult)
		if err == nil {
			t.Fatalf("mainline response was accepted: %s", response)
		}
	}

	withoutBeat, err := parseStructuredResponse(
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"critique":""}`,
		decodeReviewResult,
		validateReviewResult,
	)
	if err != nil || !withoutBeat.MainlinePassed {
		t.Fatalf("legacy result = %#v, err = %v", withoutBeat, err)
	}
}

func TestReviewerRepairsInvalidMainlineEvidenceOnce(t *testing.T) {
	beat := MainlineEventBeat{ChapterIndex: 4, CurrentEvent: "找到血书"}
	draft := strings.Repeat("文", 2495) + "主角找到血书。"
	invalid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":"主角完成了事件"},"next_event":null},"critique":""}`
	valid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":"主角找到血书。"},"next_event":null},"critique":""}`
	llm := &queuedStructuredLLM{responses: []string{invalid, valid}}
	got, err := NewReviewerAgent(llm).Run(context.Background(), &GenerationState{Draft: draft, MainlineBeat: beat})
	if err != nil || llm.calls != 2 || !got.IsApproved {
		t.Fatalf("repair state = %#v, err = %v, calls = %d", got, err, llm.calls)
	}
}
func TestReviewerInvalidMainlineResponsesPreserveReviewState(t *testing.T) {
	state := &GenerationState{
		Draft: strings.Repeat("文", 2500),
		MainlineBeat: MainlineEventBeat{
			ChapterIndex: 4,
			CurrentEvent: "找到血书",
		},
		MainlineAssessment: MainlineAssessment{
			CurrentEvent: ContractRequirementAssessment{
				Satisfied: true,
				Evidence:  "旧主线证据",
			},
		},
		ContractAssessment: ChapterContractAssessment{
			Goal: ContractRequirementAssessment{Satisfied: true, Evidence: "旧契约证据"},
		},
		ContinuityAssessment: ContinuityAssessment{
			ChapterTail: ContractRequirementAssessment{Satisfied: true, Evidence: "旧连续性证据"},
		},
		CanonAssessment: []CanonConsistencyAssessment{{
			ConstraintIndex: 1,
			Satisfied:       true,
			Evidence:        "旧账本证据",
		}},
		Critique:   "旧修改意见",
		IsApproved: true,
	}
	invalid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":"正文外"},"next_event":null},"critique":""}`
	llm := &queuedStructuredLLM{responses: []string{invalid, invalid}}

	got, err := NewReviewerAgent(llm).Run(context.Background(), state)
	if err == nil {
		t.Fatal("Run() error = nil, want structured response failure")
	}
	if llm.calls != 2 {
		t.Fatalf("calls = %d, want 2", llm.calls)
	}
	if len([]rune(err.Error())) > 2*structuredResponsePreviewRunes+500 {
		t.Fatalf("error is unexpectedly large: %d runes", len([]rune(err.Error())))
	}
	if got != state || got.MainlineAssessment.CurrentEvent != state.MainlineAssessment.CurrentEvent ||
		got.ContractAssessment.Goal != state.ContractAssessment.Goal ||
		got.ContinuityAssessment.ChapterTail != state.ContinuityAssessment.ChapterTail ||
		len(got.CanonAssessment) != 1 || got.CanonAssessment[0] != state.CanonAssessment[0] ||
		got.Critique != "旧修改意见" || !got.IsApproved {
		t.Fatalf("review state changed after invalid mainline responses: %#v", got)
	}
}

func TestReviewerMainlineEvidenceRuneLimit(t *testing.T) {
	beat := MainlineEventBeat{ChapterIndex: 4, CurrentEvent: "主线事件"}
	for _, test := range []struct {
		name     string
		evidence string
		wantErr  bool
	}{
		{name: "300 runes", evidence: strings.Repeat("界", 300)},
		{name: "301 runes", evidence: strings.Repeat("界", 301), wantErr: true},
		{name: "300 unicode runes", evidence: strings.Repeat("界", 299) + "🙂"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fmt.Sprintf(
				`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"contract_assessment":null,"mainline_assessment":{"current_event":{"satisfied":true,"evidence":%q},"next_event":null},"critique":""}`,
				test.evidence,
			)
			_, err := decodeReviewResultForState([]byte(response), &GenerationState{
				Draft:        test.evidence + strings.Repeat("文", 2500-len([]rune(test.evidence))),
				MainlineBeat: beat,
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, test.wantErr)
			}
		})
	}
}

func TestReviewerWithoutContractKeepsLegacyResponseCompatibility(t *testing.T) {
	result, err := parseStructuredResponse(
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"critique":""}`,
		decodeReviewResult,
		validateReviewResult,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || !result.ContinuityPassed || result.contractChecked || !result.ContractPassed {
		t.Fatalf("result = %#v", result)
	}
}

func TestReviewerContinuityEvidenceGateApprovesValidHeadAndTail(t *testing.T) {
	headEvidence := "主角跨进密门，身后石门轰然闭合。"
	tailEvidence := "他握紧火把，决定沿着血迹继续追查。"
	draft := headEvidence + strings.Repeat("文", 2500-len([]rune(headEvidence))-len([]rune(tailEvidence))) + tailEvidence
	response := fmt.Sprintf(
		`{"passed":true,"continuity_assessment":{"chapter_head":{"satisfied":true,"evidence":%q},"chapter_tail":{"satisfied":true,"evidence":%q}},"contract_assessment":null,"critique":""}`,
		headEvidence,
		tailEvidence,
	)
	state := &GenerationState{
		Draft: draft,
		PreviousContinuity: ContinuityPacket{
			LastBeat:   "主角推开密门。",
			NextAction: "主角进入密门。",
		},
	}

	got, err := NewReviewerAgent(&queuedStructuredLLM{responses: []string{response}}).Run(
		context.Background(),
		state,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsApproved || !got.ContinuityAssessment.ChapterHead.Satisfied ||
		!got.ContinuityAssessment.ChapterTail.Satisfied {
		t.Fatalf("state = %#v", got)
	}
}

func TestReviewerContinuityEvidenceGateAllowsMissingHeadWithoutPreviousContinuity(t *testing.T) {
	tailEvidence := "他决定天亮后继续追查。"
	draft := strings.Repeat("文", 2500-len([]rune(tailEvidence))) + tailEvidence
	response := fmt.Sprintf(
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":%q}},"contract_assessment":null,"critique":""}`,
		tailEvidence,
	)

	got, err := NewReviewerAgent(&queuedStructuredLLM{responses: []string{response}}).Run(
		context.Background(),
		&GenerationState{Draft: draft},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsApproved || got.ContinuityAssessment.ChapterHead != nil {
		t.Fatalf("state = %#v", got)
	}
}

func TestReviewerContinuityEvidenceGateRejectsInvalidEvidence(t *testing.T) {
	headEvidence := "章首承接动作。"
	tailEvidence := "章尾继续行动。"
	middleEvidence := "只在正文中段出现的证据。"
	draft := headEvidence +
		strings.Repeat("文", reviewerContinuityWindowRunes) +
		middleEvidence +
		strings.Repeat("文", reviewerContinuityWindowRunes) +
		tailEvidence
	previous := ContinuityPacket{LastBeat: "上一章结尾", NextAction: "继续行动"}

	tests := []struct {
		name     string
		headJSON string
		tailJSON string
		previous ContinuityPacket
		want     string
	}{
		{
			name:     "requires head with previous continuity",
			headJSON: "null",
			tailJSON: fmt.Sprintf(`{"satisfied":true,"evidence":%q}`, tailEvidence),
			previous: previous,
			want:     "chapter_head is required",
		},
		{
			name:     "rejects head evidence outside head window",
			headJSON: fmt.Sprintf(`{"satisfied":true,"evidence":%q}`, middleEvidence),
			tailJSON: fmt.Sprintf(`{"satisfied":true,"evidence":%q}`, tailEvidence),
			previous: previous,
			want:     "chapter head window",
		},
		{
			name:     "rejects tail evidence outside tail window",
			headJSON: fmt.Sprintf(`{"satisfied":true,"evidence":%q}`, headEvidence),
			tailJSON: fmt.Sprintf(`{"satisfied":true,"evidence":%q}`, middleEvidence),
			previous: previous,
			want:     "chapter tail window",
		},
		{
			name:     "requires null head without previous continuity",
			headJSON: fmt.Sprintf(`{"satisfied":true,"evidence":%q}`, headEvidence),
			tailJSON: fmt.Sprintf(`{"satisfied":true,"evidence":%q}`, tailEvidence),
			want:     "must be null",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := fmt.Sprintf(
				`{"passed":true,"continuity_assessment":{"chapter_head":%s,"chapter_tail":%s},"contract_assessment":null,"critique":""}`,
				test.headJSON,
				test.tailJSON,
			)
			_, err := decodeReviewResultForState([]byte(response), &GenerationState{
				Draft:              draft,
				PreviousContinuity: test.previous,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReviewerContinuityEvidenceUsesRuneWindows(t *testing.T) {
	headEvidence := "🙂章首承接。"
	tailEvidence := "章尾行动🙂"
	draft := headEvidence +
		strings.Repeat("界", 2500-len([]rune(headEvidence))-len([]rune(tailEvidence))) +
		tailEvidence
	response := fmt.Sprintf(
		`{"passed":true,"continuity_assessment":{"chapter_head":{"satisfied":true,"evidence":%q},"chapter_tail":{"satisfied":true,"evidence":%q}},"contract_assessment":null,"critique":""}`,
		headEvidence,
		tailEvidence,
	)

	result, err := decodeReviewResultForState([]byte(response), &GenerationState{
		Draft: draft,
		PreviousContinuity: ContinuityPacket{
			LastBeat:   "上一章结尾",
			NextAction: "继续行动",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ContinuityPassed {
		t.Fatalf("result = %#v", result)
	}
}

func TestReviewerRepairsInvalidContinuityEvidenceOnce(t *testing.T) {
	tailEvidence := "他决定继续追查。"
	draft := strings.Repeat("文", 2500-len([]rune(tailEvidence))) + tailEvidence
	invalid := `{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"并不存在的章尾原文"}},"contract_assessment":null,"critique":""}`
	valid := fmt.Sprintf(
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":%q}},"contract_assessment":null,"critique":""}`,
		tailEvidence,
	)
	llm := &queuedStructuredLLM{responses: []string{invalid, valid}}

	got, err := NewReviewerAgent(llm).Run(context.Background(), &GenerationState{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsApproved || llm.calls != 2 {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestReviewerRejectsLegacyContinuityBooleanWithoutAssessment(t *testing.T) {
	_, err := decodeReviewResultForState(
		[]byte(`{"passed":true,"continuity_passed":true,"contract_assessment":null,"critique":""}`),
		&GenerationState{Draft: strings.Repeat("文", 2500)},
	)
	if err == nil || !strings.Contains(err.Error(), "continuity_assessment is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestCharacterAndWorldValidatorsRejectBeforePersistence(t *testing.T) {
	characterRepo := &characterRepositoryFake{}
	characterLLM := &queuedStructuredLLM{responses: []string{
		`{"characters":[{"name":" "}]}`,
		`{"characters":[{"name":" "}]}`,
	}}
	_, err := NewCharacterAgent(characterLLM, characterRepo).Run(
		context.Background(),
		&GenerationState{NovelID: "7"},
	)
	if err == nil || characterRepo.saveCharacterCalls != 0 {
		t.Fatalf("character error = %v, saves = %d", err, characterRepo.saveCharacterCalls)
	}

	worldRepo := &worldRepositoryFake{}
	worldLLM := &queuedStructuredLLM{responses: []string{"null", "null"}}
	_, err = NewWorldAgent(worldLLM, worldRepo).Run(
		context.Background(),
		&GenerationState{NovelID: "7"},
	)
	if err == nil || worldRepo.saveCalls != 0 {
		t.Fatalf("world error = %v, saves = %d", err, worldRepo.saveCalls)
	}
}

func TestStructuredValidatorsRejectMissingRequiredFields(t *testing.T) {
	for _, response := range []string{
		`{"critique":"missing passed"}`,
		`{"passed":null,"critique":"invalid null"}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":true,"evidence":"文"}},"critique":null}`,
		`{"passed":false,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":false,"evidence":"章尾没有留下具体行动"}},"critique":" "}`,
		`{"passed":true,"continuity_assessment":{"chapter_head":null,"chapter_tail":{"satisfied":false,"evidence":"章尾没有留下具体行动"}},"critique":" "}`,
	} {
		_, err := parseStructuredResponse(response, decodeReviewResult, validateReviewResult)
		if err == nil {
			t.Fatalf("review response %q was accepted", response)
		}
	}

	for _, response := range []string{
		`{}`,
		`{"oops":"value"}`,
		`{"characters":null,"relationships":[]}`,
		`{"characters":[],"relationships":null}`,
		`{"characters":[{"name":"林云","current_status":" "}],"relationships":[]}`,
		fmt.Sprintf(`{"characters":[{"name":"林云","current_status":%q}],"relationships":[]}`, strings.Repeat("状", maxCharacterStateRunes+1)),
		`{"characters":[],"relationships":[{"source":"林云","target":"苏青","relation_type":" "}]}`,
	} {
		_, err := parseStructuredResponse(
			response,
			decodeCharacterExtraction,
			validateCharacterExtraction,
		)
		if err == nil {
			t.Fatalf("character response %q was accepted", response)
		}
	}

	for _, response := range []string{
		`[{"category":" ","name":"青云山","current_state":"山门封闭"}]`,
		`[{"category":"地理","name":" ","current_state":"山门封闭"}]`,
		`[{"category":"地理","name":"青云山","current_state":" "}]`,
		fmt.Sprintf(`[{"category":"地理","name":"青云山","current_state":%q}]`, strings.Repeat("状", maxWorldStateRunes+1)),
	} {
		_, err := parseStructuredResponse(
			response,
			decodeJSON[[]WorldSettingUpdate],
			validateWorldSettingUpdates,
		)
		if err == nil {
			t.Fatalf("world response %q was accepted", response)
		}
	}
}

func TestCharacterAgentKeepsLegacyArrayResponse(t *testing.T) {
	repo := &characterRepositoryFake{}
	llm := &queuedStructuredLLM{responses: []string{`[{"name":" 林云 ","current_status":"在城门等待"}]`}}

	_, err := NewCharacterAgent(llm, repo).Run(
		context.Background(),
		&GenerationState{NovelID: "7"},
	)
	if err != nil {
		t.Fatalf("CharacterAgent.Run returned error: %v", err)
	}
	if repo.saveCharacterCalls != 1 || repo.lastCharacterName != "林云" {
		t.Fatalf("saves = %d, name = %q", repo.saveCharacterCalls, repo.lastCharacterName)
	}
}

func TestWorldAgentAcceptsEmptyArray(t *testing.T) {
	repo := &worldRepositoryFake{}
	llm := &queuedStructuredLLM{responses: []string{"[]"}}

	_, err := NewWorldAgent(llm, repo).Run(
		context.Background(),
		&GenerationState{NovelID: "7"},
	)
	if err != nil {
		t.Fatalf("WorldAgent.Run returned error: %v", err)
	}
	if repo.saveCalls != 0 {
		t.Fatalf("saves = %d", repo.saveCalls)
	}
}
