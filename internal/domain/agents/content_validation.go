package agents

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	minGeneratedContentRunes  = 2500
	maxGeneratedContentRunes  = 4000
	minRepeatedParagraphRunes = 100
)

var (
	generatedContentPromptLabels = []string{
		"【场景卡】",
		"【背景资料】",
		"【本章契约】",
		"【主线事件节拍】",
		"【上一章接力状态】",
	}
	generatedContentParagraphBoundary = regexp.MustCompile(`(?:\r\n|\n|\r)[\p{Zs}\t]*(?:\r\n|\n|\r)`)
)

type GeneratedContentIssue struct {
	Code    string
	Message string
}

func ValidateGeneratedContent(content string) []GeneratedContentIssue {
	issues := make([]GeneratedContentIssue, 0, 5)
	wordCount := len([]rune(strings.TrimSpace(content)))
	if wordCount < minGeneratedContentRunes {
		issues = append(issues, GeneratedContentIssue{
			Code:    "content_too_short",
			Message: fmt.Sprintf("字数不达标：当前约 %d 字。请补写细节并推进剧情，使正文达到 2500–4000 字。", wordCount),
		})
	} else if wordCount > maxGeneratedContentRunes {
		issues = append(issues, GeneratedContentIssue{
			Code:    "content_too_long",
			Message: fmt.Sprintf("字数超标：当前约 %d 字。请删减冗余描写和重复表达，使正文保持在 2500–4000 字。", wordCount),
		})
	}

	if !utf8.ValidString(content) {
		issues = append(issues, GeneratedContentIssue{
			Code:    "invalid_utf8",
			Message: "正文包含无效字符编码，请重新生成有效的 UTF-8 文本。",
		})
	}

	if containsDisallowedControlCharacter(content) {
		issues = append(issues, GeneratedContentIssue{
			Code:    "control_character",
			Message: "正文包含异常控制字符，请删除后重新生成。",
		})
	}

	if containsGeneratedContentPromptLabel(content) {
		issues = append(issues, GeneratedContentIssue{
			Code:    "prompt_label_leak",
			Message: "正文泄漏了内部提示标签，请删除提示内容，只保留小说正文。",
		})
	}

	if containsAdjacentRepeatedLongParagraphs(content) {
		issues = append(issues, GeneratedContentIssue{
			Code:    "adjacent_repeated_paragraph",
			Message: "正文包含紧邻的长段落重复，请删除重复段落并补齐叙事。",
		})
	}

	return issues
}

func ValidateGeneratedContentForState(state *GenerationState) []GeneratedContentIssue {
	if state == nil || state.EventChapterCount == 0 {
		if state == nil {
			return []GeneratedContentIssue{{Code: "content_too_short", Message: "正文为空。"}}
		}
		return ValidateGeneratedContent(state.Draft)
	}
	issues := validateGeneratedContentSafety(state.Draft)
	wordCount := len([]rune(strings.TrimSpace(state.Draft)))
	minimum := minGeneratedContentRunes
	maximum := maxGeneratedContentRunes
	if state.EventSegmentIndex == 0 {
		minimum *= state.EventChapterCount
		maximum *= state.EventChapterCount
	}
	if wordCount < minimum {
		issues = append([]GeneratedContentIssue{{
			Code:    "content_too_short",
			Message: fmt.Sprintf("连续情节字数不达标：当前约 %d 字。请补写到 %d–%d 字。", wordCount, minimum, maximum),
		}}, issues...)
	} else if wordCount > maximum {
		issues = append([]GeneratedContentIssue{{
			Code:    "content_too_long",
			Message: fmt.Sprintf("连续情节字数超标：当前约 %d 字。请删减到 %d–%d 字。", wordCount, minimum, maximum),
		}}, issues...)
	}
	return issues
}

func validateGeneratedContentSafety(content string) []GeneratedContentIssue {
	issues := make([]GeneratedContentIssue, 0, 4)
	if !utf8.ValidString(content) {
		issues = append(issues, GeneratedContentIssue{Code: "invalid_utf8", Message: "正文包含无效字符编码，请重新生成有效的 UTF-8 文本。"})
	}
	if containsDisallowedControlCharacter(content) {
		issues = append(issues, GeneratedContentIssue{Code: "control_character", Message: "正文包含异常控制字符，请删除后重新生成。"})
	}
	if containsGeneratedContentPromptLabel(content) {
		issues = append(issues, GeneratedContentIssue{Code: "prompt_label_leak", Message: "正文泄漏了内部提示标签，请删除提示内容，只保留小说正文。"})
	}
	if containsAdjacentRepeatedLongParagraphs(content) {
		issues = append(issues, GeneratedContentIssue{Code: "adjacent_repeated_paragraph", Message: "正文包含紧邻的长段落重复，请删除重复段落并补齐叙事。"})
	}
	return issues
}

func HasBlockingGeneratedContentIssues(content string) bool {
	for _, issue := range ValidateGeneratedContent(content) {
		switch issue.Code {
		case "content_too_long":
			continue
		default:
			return true
		}
	}
	return false
}

func containsDisallowedControlCharacter(content string) bool {
	for _, character := range content {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func containsGeneratedContentPromptLabel(content string) bool {
	for _, label := range generatedContentPromptLabels {
		if strings.Contains(content, label) {
			return true
		}
	}
	return false
}

func containsAdjacentRepeatedLongParagraphs(content string) bool {
	paragraphs := generatedContentParagraphBoundary.Split(content, -1)
	previous := ""
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if paragraph == previous && len([]rune(paragraph)) >= minRepeatedParagraphRunes {
			return true
		}
		previous = paragraph
	}
	return false
}
