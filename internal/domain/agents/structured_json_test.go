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

func TestReviewerStructuredRepairProducesReviewResult(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"passed":"false","critique":"修改"}`,
		`{"passed":false,"continuity_passed":false,"critique":" 补充场景冲突 "}`,
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

func passingContractAssessmentJSON() string {
	return `{"goal":{"satisfied":true,"evidence":"目标已完成"},"must_happen":[{"satisfied":true,"evidence":"已进入密门"},{"satisfied":true,"evidence":"已发现血书"}],"must_not_happen":[{"satisfied":true,"evidence":"未揭晓反派"}],"end_state":{"satisfied":true,"evidence":"决定追踪祭坛"}}`
}

func TestReviewerContractGate(t *testing.T) {
	passing := `{"passed":true,"continuity_passed":true,"contract_assessment":` + passingContractAssessmentJSON() + `,"critique":""}`
	failing := `{"passed":true,"continuity_passed":true,"contract_assessment":{"goal":{"satisfied":true,"evidence":"目标已完成"},"must_happen":[{"satisfied":true,"evidence":"已进入密门"},{"satisfied":false,"evidence":"正文没有发现血书"}],"must_not_happen":[{"satisfied":true,"evidence":"未揭晓反派"}],"end_state":{"satisfied":true,"evidence":"决定追踪祭坛"}},"critique":"补强情节"}`

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
				Draft:           strings.Repeat("文", 2500),
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
			if got.ContractAssessment.Goal.Evidence != "目标已完成" {
				t.Fatalf("assessment = %#v", got.ContractAssessment)
			}
			if llm.calls != 1 {
				t.Fatalf("reviewer calls = %d, want 1 for a structurally valid assessment", llm.calls)
			}
		})
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
	invalid := `{"passed":true,"continuity_passed":true,"contract_assessment":{"goal":{"satisfied":true,"evidence":"依据"},"must_happen":[],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`
	valid := `{"passed":true,"continuity_passed":true,"contract_assessment":` + passingContractAssessmentJSON() + `,"critique":""}`
	llm := &queuedStructuredLLM{responses: []string{invalid, valid}}
	state := &GenerationState{
		Draft:           strings.Repeat("文", 2500),
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
		`{"passed":true,"continuity_passed":true,"critique":""}`,
		`{"passed":true,"continuity_passed":true,"contract_assessment":null,"critique":""}`,
		`{"passed":true,"continuity_passed":true,"contract_assessment":{"goal":{"evidence":"依据"},"must_happen":[{"satisfied":true,"evidence":"依据"},{"satisfied":true,"evidence":"依据"}],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`,
		`{"passed":true,"continuity_passed":true,"contract_assessment":{"goal":{"satisfied":true,"evidence":"依据"},"must_happen":[],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`,
		`{"passed":true,"continuity_passed":true,"contract_assessment":{"goal":{"satisfied":true,"evidence":" "},"must_happen":[{"satisfied":true,"evidence":"依据"},{"satisfied":true,"evidence":"依据"}],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`,
		fmt.Sprintf(`{"passed":true,"continuity_passed":true,"contract_assessment":{"goal":{"satisfied":true,"evidence":%q},"must_happen":[{"satisfied":true,"evidence":"依据"},{"satisfied":true,"evidence":"依据"}],"must_not_happen":[{"satisfied":true,"evidence":"依据"}],"end_state":{"satisfied":true,"evidence":"依据"}},"critique":""}`, tooLongEvidence),
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

func TestReviewerWithoutContractKeepsLegacyResponseCompatibility(t *testing.T) {
	result, err := parseStructuredResponse(
		`{"passed":true,"continuity_passed":true,"critique":""}`,
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
		`{"passed":true,"continuity_passed":true,"critique":null}`,
		`{"passed":false,"continuity_passed":false,"critique":" "}`,
		`{"passed":true,"continuity_passed":false,"critique":" "}`,
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
		`[{"category":" ","name":"青云山"}]`,
		`[{"category":"地理","name":" "}]`,
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
	llm := &queuedStructuredLLM{responses: []string{`[{"name":" 林云 "}]`}}

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
