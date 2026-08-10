package agents

import (
	"strings"
	"testing"
)

func TestSelectMainlineEventBeatUsesExactChapterAndNextEvent(t *testing.T) {
	outline := `第 1 章：主角抵达边城
第2章: 主角发现失踪案与旧王朝有关
第 10 章：主角进入地下祭坛
第11章：主角确认幕后势力`

	beat := selectMainlineEventBeat(outline, 10)
	if beat.ChapterIndex != 10 || beat.CurrentEvent != "主角进入地下祭坛" || beat.NextEvent != "主角确认幕后势力" {
		t.Fatalf("beat = %#v", beat)
	}
}

func TestSelectMainlineEventBeatAllowsMissingNextChapter(t *testing.T) {
	beat := selectMainlineEventBeat("第3章：主角夺回信物", 3)
	if beat.ChapterIndex != 3 || beat.CurrentEvent != "主角夺回信物" || beat.NextEvent != "" {
		t.Fatalf("beat = %#v", beat)
	}
}

func TestSelectMainlineEventBeatFallsBackForInvalidCurrentChapter(t *testing.T) {
	tests := []struct {
		name    string
		outline string
		index   int
	}{
		{name: "missing chapter", outline: "第2章：其他事件", index: 1},
		{name: "blank event", outline: "第1章：   ", index: 1},
		{name: "duplicate chapter", outline: "第1章：事件一\n第1章：事件二", index: 1},
		{name: "duplicate with blank event", outline: "第1章：事件一\n第1章：   ", index: 1},
		{name: "duplicate with oversized event", outline: "第1章：事件一\n第1章：" + strings.Repeat("事", maxMainlineEventRunes+1), index: 1},
		{name: "nonstandard outline", outline: "第一章 主角出发", index: 1},
		{name: "invalid index", outline: "第1章：主角出发", index: 0},
		{name: "oversized event", outline: "第1章：" + strings.Repeat("事", maxMainlineEventRunes+1), index: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if beat := selectMainlineEventBeat(test.outline, test.index); beat != (MainlineEventBeat{}) {
				t.Fatalf("beat = %#v, want empty", beat)
			}
		})
	}
}

func TestInspectMainlineEventBeatReportsCurrentChapterIssues(t *testing.T) {
	tests := []struct {
		name    string
		outline string
		index   int
		code    string
	}{
		{name: "invalid index", outline: "第1章：主角出发", index: 0, code: mainlineBeatIssueInvalidChapterIndex},
		{name: "missing current", outline: "第2章：主角抵达边城", index: 1, code: mainlineBeatIssueMissingCurrent},
		{name: "duplicate current", outline: "第1章：事件一\n第1章：事件二", index: 1, code: mainlineBeatIssueDuplicateCurrent},
		{name: "duplicate valid and blank", outline: "第1章：事件一\n第1章：", index: 1, code: mainlineBeatIssueDuplicateCurrent},
		{name: "blank current", outline: "第1章：   ", index: 1, code: mainlineBeatIssueBlankCurrent},
		{name: "oversized current", outline: "第1章：" + strings.Repeat("事", maxMainlineEventRunes+1), index: 1, code: mainlineBeatIssueOversizedCurrent},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection := inspectMainlineEventBeat(test.outline, test.index)
			if selection.IssueCode != test.code {
				t.Fatalf("issue = %q, want %q", selection.IssueCode, test.code)
			}
			if !selection.HasStructuredOutline {
				t.Fatal("structured outline was not detected")
			}
			if selection.Beat != (MainlineEventBeat{}) {
				t.Fatalf("beat = %#v, want empty", selection.Beat)
			}
		})
	}
}

func TestInspectMainlineEventBeatKeepsNonstandardOutlineCompatible(t *testing.T) {
	for _, index := range []int{0, 1} {
		selection := inspectMainlineEventBeat("人工大纲：主角调查身世", index)
		if selection.HasStructuredOutline || selection.IssueCode != "" || selection.Beat != (MainlineEventBeat{}) {
			t.Fatalf("index %d selection = %#v", index, selection)
		}
	}
}

func TestInspectMainlineEventBeatOmitsInvalidNextWithoutFailingCurrent(t *testing.T) {
	outlines := []string{
		"第1章：主角出发\n第2章：",
		"第1章：主角出发\n第2章：事件一\n第2章：事件二",
		"第1章：主角出发\n第2章：" + strings.Repeat("事", maxMainlineEventRunes+1),
	}

	for _, outline := range outlines {
		selection := inspectMainlineEventBeat(outline, 1)
		if selection.IssueCode != "" || selection.Beat.CurrentEvent != "主角出发" || selection.Beat.NextEvent != "" {
			t.Fatalf("selection = %#v", selection)
		}
	}
}

func TestSelectMainlineEventBeatOmitsAmbiguousOrInvalidNextEvent(t *testing.T) {
	tests := []string{
		"第1章：主角出发\n第2章：事件一\n第2章：事件二",
		"第1章：主角出发\n第2章：" + strings.Repeat("事", maxMainlineEventRunes+1),
	}

	for _, outline := range tests {
		beat := selectMainlineEventBeat(outline, 1)
		if beat.CurrentEvent != "主角出发" || beat.NextEvent != "" {
			t.Fatalf("beat = %#v", beat)
		}
	}
}

func TestValidateGeneratedOutlineSegment(t *testing.T) {
	tests := []struct {
		name    string
		outline string
		start   int
		end     int
		issue   string
	}{
		{name: "valid", outline: "第3章：主角进入密室\n第4章：主角追踪祭坛", start: 3, end: 4},
		{name: "valid CRLF", outline: "第3章：主角进入密室\r\n第4章：主角追踪祭坛", start: 3, end: 4},
		{name: "valid terminal newline", outline: "第3章：主角进入密室\n第4章：主角追踪祭坛\n", start: 3, end: 4},
		{name: "bare CR", outline: "第3章：主角进入密室\r第4章：主角追踪祭坛", start: 3, end: 4, issue: outlineIssueMalformedLine},
		{name: "invalid range", outline: "第3章：主角进入密室", start: 4, end: 3, issue: outlineIssueInvalidRange},
		{name: "malformed line", outline: "以下是大纲\n第3章：主角进入密室", start: 3, end: 3, issue: outlineIssueMalformedLine},
		{name: "spaced chapter marker", outline: "第 3 章：主角进入密室", start: 3, end: 3, issue: outlineIssueMalformedLine},
		{name: "leading zero chapter", outline: "第03章：主角进入密室", start: 3, end: 3, issue: outlineIssueMalformedLine},
		{name: "ascii colon", outline: "第3章:主角进入密室", start: 3, end: 3, issue: outlineIssueMalformedLine},
		{name: "leading whitespace", outline: " 第3章：主角进入密室", start: 3, end: 3, issue: outlineIssueMalformedLine},
		{name: "trailing whitespace", outline: "第3章：主角进入密室 ", start: 3, end: 3, issue: outlineIssueMalformedLine},
		{name: "extra blank line", outline: "第3章：主角进入密室\n\n第4章：主角追踪祭坛", start: 3, end: 4, issue: outlineIssueMalformedLine},
		{name: "out of range", outline: "第2章：主角发现血书\n第3章：主角进入密室", start: 3, end: 4, issue: outlineIssueOutOfRange},
		{name: "duplicate", outline: "第3章：事件一\n第3章：事件二", start: 3, end: 4, issue: outlineIssueDuplicateChapter},
		{name: "out of order", outline: "第4章：主角追踪祭坛\n第3章：主角进入密室", start: 3, end: 4, issue: outlineIssueOutOfOrder},
		{name: "blank event", outline: "第3章：\n第4章：主角追踪祭坛", start: 3, end: 4, issue: outlineIssueBlankEvent},
		{name: "oversized event", outline: "第3章：" + strings.Repeat("事", maxMainlineEventRunes+1), start: 3, end: 3, issue: outlineIssueOversizedEvent},
		{name: "missing chapter", outline: "第3章：主角进入密室", start: 3, end: 4, issue: outlineIssueMissingChapter},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if issue := validateGeneratedOutlineSegment(test.outline, test.start, test.end); issue != test.issue {
				t.Fatalf("issue = %q, want %q", issue, test.issue)
			}
		})
	}
}

func TestValidateOutlineRangeDoesNotOverlap(t *testing.T) {
	if issue := validateOutlineRangeDoesNotOverlap("人工大纲：主角调查身世", 3, 4); issue != "" {
		t.Fatalf("manual outline issue = %q", issue)
	}
	if issue := validateOutlineRangeDoesNotOverlap("第1章：主角出发\n第3章：主角进入密室", 3, 4); issue != outlineIssueRangeOverlap {
		t.Fatalf("overlap issue = %q", issue)
	}
	if issue := validateOutlineRangeDoesNotOverlap("第1章：主角出发\n第2章：主角发现血书", 3, 4); issue != "" {
		t.Fatalf("non-overlap issue = %q", issue)
	}
}

func TestMainlineBeatPromptFormatsAvailableBoundaries(t *testing.T) {
	prompt := mainlineBeatPrompt(MainlineEventBeat{
		ChapterIndex: 4,
		CurrentEvent: "主角找到血书",
		NextEvent:    "主角前往地下祭坛",
	})
	for _, value := range []string{"第4章", "主角找到血书", "主角前往地下祭坛", "本章不得提前完成"} {
		if !strings.Contains(prompt, value) {
			t.Fatalf("prompt missing %q: %s", value, prompt)
		}
	}
	if got := mainlineBeatPrompt(MainlineEventBeat{}); got != "" {
		t.Fatalf("empty beat prompt = %q", got)
	}
	if got := mainlineBeatPrompt(MainlineEventBeat{
		ChapterIndex: 1,
		CurrentEvent: strings.Repeat("事", maxMainlineEventRunes+1),
	}); got != "" {
		t.Fatalf("oversized current event prompt = %q", got)
	}
	prompt = mainlineBeatPrompt(MainlineEventBeat{
		ChapterIndex: 1,
		CurrentEvent: "主角出发",
		NextEvent:    strings.Repeat("事", maxMainlineEventRunes+1),
	})
	if !strings.Contains(prompt, "主角出发") || strings.Contains(prompt, "下一章预定事件") {
		t.Fatalf("oversized next event prompt = %q", prompt)
	}
}
