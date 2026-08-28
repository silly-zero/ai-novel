package agents

import (
	"encoding/json"
	"fmt"
	"strings"
)

type mainlineAssessmentWire struct {
	CurrentEvent *contractRequirementAssessmentWire `json:"current_event"`
	NextEvent    *contractRequirementAssessmentWire `json:"next_event"`
}

func decodeMainlineAssessment(
	candidate []byte,
	beat MainlineEventBeat,
	draft string,
) (MainlineAssessment, bool, error) {
	var wire mainlineAssessmentWire
	if err := json.Unmarshal(candidate, &wire); err != nil {
		return MainlineAssessment{}, false, newReviewerValidationError(
			"reviewer_json_shape_type",
			"object",
			"mainline_assessment",
		)
	}
	if wire.CurrentEvent == nil {
		return MainlineAssessment{}, false, newReviewerValidationError(
			"reviewer_required_field",
			"required",
			"mainline_assessment.current_event",
		)
	}
	current, err := normalizeContractRequirementAssessment(
		"mainline_assessment.current_event",
		*wire.CurrentEvent,
	)
	if err != nil {
		return MainlineAssessment{}, false, err
	}
	if current.Satisfied {
		if err := validateContractEvidenceInDraft(
			"mainline_assessment.current_event",
			current.Evidence,
			draft,
			true,
			reviewerAreaMainlineCurrentEvent,
			"section=mainline_beat; field=current_event",
		); err != nil {
			return MainlineAssessment{}, false, err
		}
	}

	assessment := MainlineAssessment{CurrentEvent: current}
	if strings.TrimSpace(beat.NextEvent) == "" {
		if wire.NextEvent != nil {
			return MainlineAssessment{}, false, newReviewerValidationError(
				reviewerIssueNullability,
				"must_be_null",
				"mainline_assessment.next_event",
			)
		}
		return assessment, current.Satisfied, nil
	}
	if wire.NextEvent == nil {
		return MainlineAssessment{}, false, newReviewerValidationError(
			"reviewer_required_field",
			"required",
			"mainline_assessment.next_event",
		)
	}
	next, err := normalizeContractRequirementAssessment(
		"mainline_assessment.next_event",
		*wire.NextEvent,
	)
	if err != nil {
		return MainlineAssessment{}, false, err
	}
	if !next.Satisfied {
		if err := validateContractEvidenceInDraft(
			"mainline_assessment.next_event",
			next.Evidence,
			draft,
			false,
			reviewerAreaMainlineNextEarlyCompletion,
			"section=mainline_beat; field=next_event",
		); err != nil {
			return MainlineAssessment{}, false, err
		}
	}
	assessment.NextEvent = &next
	return assessment, current.Satisfied && next.Satisfied, nil
}

func mainlineFailureCritique(
	beat MainlineEventBeat,
	assessment MainlineAssessment,
	critique string,
) string {
	violations := make([]string, 0, 2)
	if !assessment.CurrentEvent.Satisfied {
		violations = append(violations, fmt.Sprintf(
			"本章主线事件未完成：%s；依据：%s",
			strings.TrimSpace(beat.CurrentEvent),
			assessment.CurrentEvent.Evidence,
		))
	}
	if assessment.NextEvent != nil && !assessment.NextEvent.Satisfied {
		violations = append(violations, fmt.Sprintf(
			"提前完成下一章主线事件：%s；正文依据：%s",
			strings.TrimSpace(beat.NextEvent),
			assessment.NextEvent.Evidence,
		))
	}
	critique = strings.TrimSpace(critique)
	if len(violations) == 0 {
		return critique
	}
	result := "主线事件节拍未满足：\n- " + strings.Join(violations, "\n- ")
	if critique != "" {
		result += "\n其他修改意见：" + critique
	}
	return result
}
