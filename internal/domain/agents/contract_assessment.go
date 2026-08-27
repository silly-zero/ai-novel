package agents

import (
	"encoding/json"
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
		return ChapterContractAssessment{}, newReviewerValidationError(
			"reviewer_required_field",
			"required",
			"contract_assessment.goal",
		)
	}
	if wire.EndState == nil {
		return ChapterContractAssessment{}, newReviewerValidationError(
			"reviewer_required_field",
			"required",
			"contract_assessment.end_state",
		)
	}
	if len(wire.MustHappen) != len(contract.MustHappen) {
		return ChapterContractAssessment{}, newReviewerExpectedValidationError(
			"reviewer_array_structure",
			"exact_count",
			"contract_assessment.must_happen",
			len(contract.MustHappen),
		)
	}
	if len(wire.MustNotHappen) != len(contract.MustNotHappen) {
		return ChapterContractAssessment{}, newReviewerExpectedValidationError(
			"reviewer_array_structure",
			"exact_count",
			"contract_assessment.must_not_happen",
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
		return ContractRequirementAssessment{}, newReviewerValidationError(
			"reviewer_required_field",
			"required",
			name+".satisfied",
		)
	}
	if item.Evidence == nil {
		return ContractRequirementAssessment{}, newReviewerValidationError(
			"reviewer_required_field",
			"required",
			name+".evidence",
		)
	}
	evidence := strings.TrimSpace(*item.Evidence)
	if evidence == "" {
		return ContractRequirementAssessment{}, newReviewerValidationError(
			"reviewer_validation_other",
			"nonblank",
			name+".evidence",
		)
	}
	if len([]rune(evidence)) > maxContractAssessmentEvidenceRunes {
		return ContractRequirementAssessment{}, newReviewerExpectedValidationError(
			"reviewer_validation_other",
			"max_runes",
			name+".evidence",
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
		return newReviewerValidationError(
			"reviewer_evidence_draft",
			"exact_substring",
			name+".evidence",
		)
	}
	return nil
}

type canonConsistencyAssessmentWire struct {
	ConstraintIndex *int    `json:"constraint_index"`
	Satisfied       *bool   `json:"satisfied"`
	Evidence        *string `json:"evidence"`
}

func decodeCanonConsistencyAssessments(
	candidate []byte,
	constraints []CanonConstraint,
	draft string,
) ([]CanonConsistencyAssessment, error) {
	var wire []canonConsistencyAssessmentWire
	if err := json.Unmarshal(candidate, &wire); err != nil {
		return nil, newReviewerValidationError(
			"reviewer_json_shape_type",
			"array",
			"canon_assessment",
		)
	}
	if len(wire) != len(constraints) {
		return nil, newReviewerExpectedValidationError(
			"reviewer_array_structure",
			"exact_count",
			"canon_assessment",
			len(constraints),
		)
	}
	assessments := make([]CanonConsistencyAssessment, 0, len(wire))
	for index, item := range wire {
		name := fmt.Sprintf("canon_assessment[%d]", index)
		if item.ConstraintIndex == nil {
			return nil, newReviewerValidationError(
				"reviewer_required_field",
				"required",
				name+".constraint_index",
			)
		}
		expectedIndex := index + 1
		if *item.ConstraintIndex != expectedIndex {
			return nil, newReviewerExpectedValidationError(
				"reviewer_array_structure",
				"expected_index",
				name+".constraint_index",
				expectedIndex,
			)
		}
		if item.Satisfied == nil {
			return nil, newReviewerValidationError(
				"reviewer_required_field",
				"required",
				name+".satisfied",
			)
		}
		if item.Evidence == nil {
			return nil, newReviewerValidationError(
				"reviewer_required_field",
				"required",
				name+".evidence",
			)
		}
		evidence := strings.TrimSpace(*item.Evidence)
		if evidence == "" {
			return nil, newReviewerValidationError(
				"reviewer_validation_other",
				"nonblank",
				name+".evidence",
			)
		}
		if len([]rune(evidence)) > maxContractAssessmentEvidenceRunes {
			return nil, newReviewerExpectedValidationError(
				"reviewer_validation_other",
				"max_runes",
				name+".evidence",
				maxContractAssessmentEvidenceRunes,
			)
		}
		if !*item.Satisfied {
			if err := validateContractEvidenceInDraft(name, evidence, draft); err != nil {
				return nil, err
			}
		}
		assessments = append(assessments, CanonConsistencyAssessment{
			ConstraintIndex: expectedIndex,
			Satisfied:       *item.Satisfied,
			Evidence:        evidence,
		})
	}
	return assessments, nil
}

func canonConstraintsPassed(assessments []CanonConsistencyAssessment) bool {
	for _, assessment := range assessments {
		if !assessment.Satisfied {
			return false
		}
	}
	return true
}

func canonFailureCritique(
	constraints []CanonConstraint,
	assessments []CanonConsistencyAssessment,
	critique string,
) string {
	violations := make([]string, 0)
	for index, assessment := range assessments {
		if assessment.Satisfied || index >= len(constraints) {
			continue
		}
		constraint := constraints[index]
		violations = append(violations, fmt.Sprintf(
			"%s/%s 与账本冲突：%s；正文依据：%s",
			constraint.Kind,
			constraint.Subject,
			constraint.Statement,
			assessment.Evidence,
		))
	}
	critique = strings.TrimSpace(critique)
	if len(violations) == 0 {
		return critique
	}
	result := "角色或世界账本冲突：\n- " + strings.Join(violations, "\n- ")
	if critique != "" {
		result += "\n其他修改意见：" + critique
	}
	return result
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
