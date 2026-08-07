package agents

import (
	"fmt"
	"strings"
)

const (
	maxChapterContractItems     = 5
	maxChapterContractItemRunes = 200
)

func chapterContractPrompt(contract ChapterContract) string {
	if contract.IsEmpty() {
		return "【本章契约】\n（未提供结构化契约，请严格遵循本章大纲。）"
	}
	return "【本章契约】\n" + formatChapterContract(contract)
}

func formatChapterContract(contract ChapterContract) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "章节目标：%s\n", contract.Goal)
	builder.WriteString("必须发生：\n")
	for _, event := range contract.MustHappen {
		fmt.Fprintf(&builder, "- %s\n", event)
	}
	builder.WriteString("禁止发生：\n")
	if len(contract.MustNotHappen) == 0 {
		builder.WriteString("- 无\n")
	} else {
		for _, event := range contract.MustNotHappen {
			fmt.Fprintf(&builder, "- %s\n", event)
		}
	}
	fmt.Fprintf(&builder, "章尾状态：%s", contract.EndState)
	return builder.String()
}

func decodeChapterContract(candidate []byte) (ChapterContract, error) {
	return decodeJSON[ChapterContract](candidate)
}

func validateChapterContract(contract *ChapterContract) error {
	contract.Goal = strings.TrimSpace(contract.Goal)
	contract.EndState = strings.TrimSpace(contract.EndState)
	if contract.Goal == "" {
		return fmt.Errorf("chapter_goal is required")
	}
	if contract.EndState == "" {
		return fmt.Errorf("end_state is required")
	}
	if err := validateChapterContractText("chapter_goal", contract.Goal); err != nil {
		return err
	}
	if err := validateChapterContractText("end_state", contract.EndState); err != nil {
		return err
	}
	var err error
	contract.MustHappen, err = normalizeChapterContractItems("must_happen", contract.MustHappen, true)
	if err != nil {
		return err
	}
	contract.MustNotHappen, err = normalizeChapterContractItems("must_not_happen", contract.MustNotHappen, false)
	return err
}

func normalizeChapterContractItems(name string, items []string, required bool) ([]string, error) {
	if len(items) > maxChapterContractItems {
		return nil, fmt.Errorf("%s must contain at most %d items", name, maxChapterContractItems)
	}
	normalized := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("%s must not contain empty items", name)
		}
		if err := validateChapterContractText(name, item); err != nil {
			return nil, err
		}
		normalized = append(normalized, item)
	}
	if required && len(normalized) == 0 {
		return nil, fmt.Errorf("%s must contain at least 1 item", name)
	}
	return normalized, nil
}

func validateChapterContractText(name, value string) error {
	if len([]rune(value)) > maxChapterContractItemRunes {
		return fmt.Errorf("%s item exceeds %d characters", name, maxChapterContractItemRunes)
	}
	return nil
}
