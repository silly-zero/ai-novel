package agents

import (
	"strings"
	"testing"
)

func TestHasBlockingGeneratedContentIssuesForBatchAllowsShortNaturalSegment(t *testing.T) {
	if HasBlockingGeneratedContentIssuesForBatch(strings.Repeat("文", 1000)) {
		t.Fatal("expected a short continuous segment to be allowed")
	}
	if !HasBlockingGeneratedContentIssuesForBatch(strings.Repeat("文", 999)) {
		t.Fatal("expected an abnormally short continuous segment to be blocked")
	}
}

func TestHasBlockingGeneratedContentIssuesIgnoresOnlyExcessLength(t *testing.T) {
	if HasBlockingGeneratedContentIssues(strings.Repeat("文", 4001)) {
		t.Fatal("excess-length-only issue was treated as blocking")
	}
	if !HasBlockingGeneratedContentIssues(strings.Repeat("文", 2499)) {
		t.Fatal("short content was not treated as blocking")
	}
	if !HasBlockingGeneratedContentIssues(strings.Repeat("文", 2500) + "【场景卡】") {
		t.Fatal("prompt label was not treated as blocking")
	}
}

func TestValidateGeneratedContentWordCountBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		issueCode string
	}{
		{name: "minimum", content: strings.Repeat("文", minGeneratedContentRunes)},
		{name: "maximum", content: strings.Repeat("文", maxGeneratedContentRunes)},
		{name: "trimmed minimum", content: " \n\t" + strings.Repeat("文", minGeneratedContentRunes) + "\r\n "},
		{name: "too short", content: strings.Repeat("文", minGeneratedContentRunes-1), issueCode: "content_too_short"},
		{name: "too long", content: strings.Repeat("文", maxGeneratedContentRunes+1), issueCode: "content_too_long"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues := ValidateGeneratedContent(test.content)
			if test.issueCode == "" {
				if len(issues) != 0 {
					t.Fatalf("issues = %#v, want none", issues)
				}
				return
			}
			if len(issues) != 1 || issues[0].Code != test.issueCode {
				t.Fatalf("issues = %#v, want only %q", issues, test.issueCode)
			}
		})
	}
}

func TestValidateGeneratedContentRejectsInvalidEncodingAndControlCharacters(t *testing.T) {
	validBody := strings.Repeat("文", minGeneratedContentRunes)
	tests := []struct {
		name      string
		content   string
		issueCode string
	}{
		{name: "invalid utf8", content: validBody + string([]byte{0xff}), issueCode: "invalid_utf8"},
		{name: "nul", content: validBody + "\x00", issueCode: "control_character"},
		{name: "escape", content: validBody + "\x1b", issueCode: "control_character"},
		{name: "allowed whitespace", content: strings.Repeat("文", 1000) + "\n\r\t" + strings.Repeat("文", minGeneratedContentRunes-1000)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues := ValidateGeneratedContent(test.content)
			if test.issueCode == "" {
				if len(issues) != 0 {
					t.Fatalf("issues = %#v, want none", issues)
				}
				return
			}
			if !hasGeneratedContentIssue(issues, test.issueCode) {
				t.Fatalf("issues = %#v, want %q", issues, test.issueCode)
			}
		})
	}
}

func TestValidateGeneratedContentRejectsExactPromptLabels(t *testing.T) {
	body := strings.Repeat("文", minGeneratedContentRunes)
	for _, label := range generatedContentPromptLabels {
		t.Run(label, func(t *testing.T) {
			issues := ValidateGeneratedContent(body + label)
			if !hasGeneratedContentIssue(issues, "prompt_label_leak") {
				t.Fatalf("issues = %#v, want prompt label leak", issues)
			}
		})
	}

	for _, allowed := range []string{
		"场景卡",
		"【场景卡片】",
		"## 场景卡",
		"小说人物提到了背景资料和本章契约。",
	} {
		issues := ValidateGeneratedContent(body + allowed)
		if hasGeneratedContentIssue(issues, "prompt_label_leak") {
			t.Fatalf("content suffix %q caused prompt leak: %#v", allowed, issues)
		}
	}
}

func TestValidateGeneratedContentRejectsOnlyAdjacentExactLongParagraphs(t *testing.T) {
	body := strings.Repeat("正文", 1250)
	paragraph := strings.Repeat("重复", minRepeatedParagraphRunes/2)
	tests := []struct {
		name     string
		content  string
		rejected bool
	}{
		{name: "adjacent long paragraphs", content: body + "\n\n" + paragraph + "\n\n" + paragraph, rejected: true},
		{name: "unicode whitespace blank line", content: body + "\n\n" + paragraph + "\n 　\n" + paragraph, rejected: true},
		{name: "crlf blank line", content: body + "\r\n\r\n" + paragraph + "\r\n\t\r\n" + paragraph, rejected: true},
		{name: "short paragraphs", content: body + "\n\n短句\n\n短句"},
		{name: "nonadjacent paragraphs", content: body + "\n\n" + paragraph + "\n\n中间段落\n\n" + paragraph},
		{name: "near duplicate", content: body + "\n\n" + paragraph + "\n\n" + paragraph + "不同"},
		{name: "different internal whitespace", content: body + "\n\n" + paragraph + "\n\n" + paragraph[:30] + " " + paragraph[30:]},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues := ValidateGeneratedContent(test.content)
			got := hasGeneratedContentIssue(issues, "adjacent_repeated_paragraph")
			if got != test.rejected {
				t.Fatalf("repeated paragraph issue = %v, want %v; issues = %#v", got, test.rejected, issues)
			}
		})
	}
}

func TestValidateGeneratedContentIssuesAreStableAndBounded(t *testing.T) {
	content := strings.Repeat("文", maxGeneratedContentRunes+1) + string([]byte{0xff}) + "\x00\x01【场景卡】【本章契约】"
	issues := ValidateGeneratedContent(content)
	wantCodes := []string{"content_too_long", "invalid_utf8", "control_character", "prompt_label_leak"}
	if len(issues) != len(wantCodes) {
		t.Fatalf("issues = %#v, want %d", issues, len(wantCodes))
	}
	for index, code := range wantCodes {
		if issues[index].Code != code {
			t.Fatalf("issues[%d].Code = %q, want %q", index, issues[index].Code, code)
		}
		if issues[index].Message == "" || strings.Contains(issues[index].Message, "\x00") || strings.Contains(issues[index].Message, "【场景卡】") {
			t.Fatalf("unsafe issue message = %q", issues[index].Message)
		}
	}
}

func hasGeneratedContentIssue(issues []GeneratedContentIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
