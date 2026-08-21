package agents

import (
	"fmt"
	"strings"
)

const maxLedgerEvidenceRunes = 1000

func validateLedgerEvidence(field, evidence, draft string, required bool) error {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		if required {
			return fmt.Errorf("%s must not be blank", field)
		}
		return nil
	}
	if len([]rune(evidence)) > maxLedgerEvidenceRunes {
		return fmt.Errorf("%s exceeds %d characters", field, maxLedgerEvidenceRunes)
	}
	if !strings.Contains(draft, evidence) {
		return fmt.Errorf("%s must be an exact draft substring", field)
	}
	return nil
}
