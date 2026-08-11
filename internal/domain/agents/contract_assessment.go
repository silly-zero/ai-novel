package agents

import (
	"fmt"
	"strings"
)

const maxContractAssessmentEvidenceRunes = 300

type contractRequirementAssessmentWire struct {
	Satisfied *bool   `json:"satisfied"`
	Evidence  *string `json:"evidence"`
}

type chapterContractAssessmentWire struct {
	Goal          *contractRequirementAssessmentWire  `json:"goal"`
	MustHappen    []contractRequirementAssessmentWire `json:"must_happen"`
	MustNotHappen []contractRequirementAssessmentWire `json:"must_not_happen"`
	EndState      *contractRequirementAssessmentWire  `json:"end_state"`
}

func normalizeChapterContractAssessment(
	wire chapterContractAssessmentWire,
	contract ChapterContract,
) (ChapterContractAssessment, error) {
	if wire.Goal == nil {
		return ChapterContractAssessment{}, fmt.Errorf("contract_assessment.goal is required")
	}
	if wire.EndState == nil {
		return ChapterContractAssessment{}, fmt.Errorf("contract_assessment.end_state is required")
	}
	if len(wire.MustHappen) != len(contract.MustHappen) {
		return ChapterContractAssessment{}, fmt.Errorf(
			"contract_assessment.must_happen must contain exactly %d items",
			len(contract.MustHappen),
		)
	}
	if len(wire.MustNotHappen) != len(contract.MustNotHappen) {
		return ChapterContractAssessment{}, fmt.Errorf(
			"contract_assessment.must_not_happen must contain exactly %d items",
			len(contract.MustNotHappen),
		)
	}

	goal, err := normalizeContractRequirementAssessment("contract_assessment.goal", *wire.Goal)
	if err != nil {
		return ChapterContractAssessment{}, err
	}
	endState, err := normalizeContractRequirementAssessment("contract_assessment.end_state", *wire.EndState)
	if err != nil {
		return ChapterContractAssessment{}, err
	}
	mustHappen, err := normalizeContractRequirementAssessments(
		"contract_assessment.must_happen",
		wire.MustHappen,
	)
	if err != nil {
		return ChapterContractAssessment{}, err
	}
	mustNotHappen, err := normalizeContractRequirementAssessments(
		"contract_assessment.must_not_happen",
		wire.MustNotHappen,
	)
	if err != nil {
		return ChapterContractAssessment{}, err
	}
	return ChapterContractAssessment{
		Goal:          goal,
		MustHappen:    mustHappen,
		MustNotHappen: mustNotHappen,
		EndState:      endState,
	}, nil
}

func normalizeContractRequirementAssessments(
	name string,
	items []contractRequirementAssessmentWire,
) ([]ContractRequirementAssessment, error) {
	normalized := make([]ContractRequirementAssessment, 0, len(items))
	for index, item := range items {
		normalizedItem, err := normalizeContractRequirementAssessment(
			fmt.Sprintf("%s[%d]", name, index),
			item,
		)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, normalizedItem)
	}
	return normalized, nil
}

func normalizeContractRequirementAssessment(
	name string,
	item contractRequirementAssessmentWire,
) (ContractRequirementAssessment, error) {
	if item.Satisfied == nil {
		return ContractRequirementAssessment{}, fmt.Errorf("%s.satisfied is required", name)
	}
	if item.Evidence == nil {
		return ContractRequirementAssessment{}, fmt.Errorf("%s.evidence is required", name)
	}
	evidence := strings.TrimSpace(*item.Evidence)
	if evidence == "" {
		return ContractRequirementAssessment{}, fmt.Errorf("%s.evidence must not be empty", name)
	}
	if len([]rune(evidence)) > maxContractAssessmentEvidenceRunes {
		return ContractRequirementAssessment{}, fmt.Errorf(
			"%s.evidence exceeds %d characters",
			name,
			maxContractAssessmentEvidenceRunes,
		)
	}
	return ContractRequirementAssessment{
		Satisfied: *item.Satisfied,
		Evidence:  evidence,
	}, nil
}

func validateChapterContractAssessmentEvidence(
	assessment ChapterContractAssessment,
	draft string,
) error {
	if assessment.Goal.Satisfied {
		if err := validateContractEvidenceInDraft(
			"contract_assessment.goal",
			assessment.Goal.Evidence,
			draft,
		); err != nil {
			return err
		}
	}
	for index, item := range assessment.MustHappen {
		if item.Satisfied {
			if err := validateContractEvidenceInDraft(
				fmt.Sprintf("contract_assessment.must_happen[%d]", index),
				item.Evidence,
				draft,
			); err != nil {
				return err
			}
		}
	}
	for index, item := range assessment.MustNotHappen {
		if !item.Satisfied {
			if err := validateContractEvidenceInDraft(
				fmt.Sprintf("contract_assessment.must_not_happen[%d]", index),
				item.Evidence,
				draft,
			); err != nil {
				return err
			}
		}
	}
	if assessment.EndState.Satisfied {
		return validateContractEvidenceInDraft(
			"contract_assessment.end_state",
			assessment.EndState.Evidence,
			draft,
		)
	}
	return nil
}

func validateContractEvidenceInDraft(name, evidence, draft string) error {
	if !strings.Contains(draft, evidence) {
		return fmt.Errorf("%s.evidence must be an exact draft substring", name)
	}
	return nil
}

func chapterContractViolations(
	contract ChapterContract,
	assessment ChapterContractAssessment,
) []string {
	violations := make([]string, 0)
	if !assessment.Goal.Satisfied {
		violations = append(violations, contractViolation(
			"chapter_goal",
			contract.Goal,
			assessment.Goal.Evidence,
		))
	}
	for index, item := range assessment.MustHappen {
		if !item.Satisfied {
			violations = append(violations, contractViolation(
				fmt.Sprintf("must_happen[%d]", index),
				contract.MustHappen[index],
				item.Evidence,
			))
		}
	}
	for index, item := range assessment.MustNotHappen {
		if !item.Satisfied {
			violations = append(violations, contractViolation(
				fmt.Sprintf("must_not_happen[%d]", index),
				contract.MustNotHappen[index],
				item.Evidence,
			))
		}
	}
	if !assessment.EndState.Satisfied {
		violations = append(violations, contractViolation(
			"end_state",
			contract.EndState,
			assessment.EndState.Evidence,
		))
	}
	return violations
}

func contractViolation(kind, requirement, evidence string) string {
	return fmt.Sprintf("%s 未满足：%s；依据：%s", kind, requirement, evidence)
}

func contractFailureCritique(violations []string, critique string) string {
	critique = strings.TrimSpace(critique)
	if len(violations) == 0 {
		return critique
	}
	result := "章节契约未满足：\n- " + strings.Join(violations, "\n- ")
	if critique != "" {
		result += "\n其他修改意见：" + critique
	}
	return result
}
