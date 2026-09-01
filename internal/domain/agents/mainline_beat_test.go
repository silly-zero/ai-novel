package agents

import (
	"strings"
	"testing"
)

func TestCurrentMainlineEventBeatMapsPhaseEventsToChapter(t *testing.T) {
	outline := `阶段1｜模拟觉醒与苟道开局
阶段目标：确立生存策略
事件链：
1. 觉醒人生模拟器
2. 锁定隐秘副本
3. 大学时期积蓄力量
因果牵引：主角寻找变数
阶段终点：掌握初始能力

阶段2｜隐秘副本与分身收徒
参考章节：4-6
阶段目标：获得修炼据点
事件链：
1. 进入副本
2. 通过考核
3. 获得传承
因果牵引：获得真实修炼环境
阶段终点：实力质变`

	first := currentMainlineEventBeat(outline, 1)
	if first.CurrentEvent != "觉醒人生模拟器" || first.PhaseTitle != "模拟觉醒与苟道开局" || !first.Estimated {
		t.Fatalf("chapter 1 beat = %#v", first)
	}
	third := currentMainlineEventBeat(outline, 3)
	if third.CurrentEvent != "大学时期积蓄力量" || third.NextEvent != "" || third.PhaseTitle != "模拟觉醒与苟道开局" {
		t.Fatalf("chapter 3 beat = %#v", third)
	}
	fourth := currentMainlineEventBeat(outline, 4)
	if fourth.CurrentEvent != "进入副本" || fourth.PhaseTitle != "隐秘副本与分身收徒" || !fourth.Estimated {
		t.Fatalf("chapter 4 beat = %#v", fourth)
	}
}

func TestCurrentMainlineEventBeatPrefersExactChapterOverPhaseEstimate(t *testing.T) {
	outline := `第3章：主角进入秘境
阶段1｜早期阶段
阶段目标：不应覆盖精确章节
事件链：
1. 回到宿舍
2. 重新觉醒
因果牵引：继续推进
阶段终点：进入下一阶段`
	beat := currentMainlineEventBeat(outline, 3)
	if beat.CurrentEvent != "主角进入秘境" || beat.Estimated || beat.PhaseTitle != "" {
		t.Fatalf("beat = %#v", beat)
	}
}
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

func TestNormalizeGeneratedOutlineSegmentAcceptsHarmlessFormatting(t *testing.T) {
	tests := []struct {
		name    string
		outline string
	}{
		{name: "canonical", outline: "第3章：主角进入密室\n第4章：主角追踪祭坛"},
		{name: "CRLF", outline: "第3章：主角进入密室\r\n第4章：主角追踪祭坛"},
		{name: "bare CR", outline: "第3章：主角进入密室\r第4章：主角追踪祭坛"},
		{name: "terminal newlines", outline: "第3章：主角进入密室\n第4章：主角追踪祭坛\n\n"},
		{name: "preamble and postamble", outline: "以下是大纲：\n第3章：主角进入密室\n第4章：主角追踪祭坛\n以上。"},
		{name: "fenced", outline: "```text\n第3章：主角进入密室\n第4章：主角追踪祭坛\n```"},
		{name: "spaced markers", outline: "第 3 章： 主角进入密室\n第 4 章：主角追踪祭坛"},
		{name: "leading zeros", outline: "第03章：主角进入密室\n第04章：主角追踪祭坛"},
		{name: "ASCII colons", outline: "第3章: 主角进入密室\n第4章:主角追踪祭坛"},
		{name: "outer whitespace", outline: "  第3章：主角进入密室  \n\t第4章：主角追踪祭坛\t"},
		{name: "blank separators", outline: "第3章：主角进入密室\n\n第4章：主角追踪祭坛"},
		{name: "bullets", outline: "- 第3章：主角进入密室\n* 第4章：主角追踪祭坛"},
		{name: "numbered list", outline: "1. 第3章：主角进入密室\n2、 第4章：主角追踪祭坛"},
		{name: "bold", outline: "**第3章：主角进入密室**\n**第4章：主角追踪祭坛**"},
		{name: "BOM and full width digits", outline: string(rune(0xFEFF)) + "第３章：主角进入密室\n第４章：主角追踪祭坛"},
	}
	const want = "第3章：主角进入密室\n第4章：主角追踪祭坛"
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, issue := normalizeGeneratedOutlineSegment(test.outline, 3, 4)
			if issue != "" || got != want {
				t.Fatalf("canonical=%q issue=%q, want %q", got, issue, want)
			}
		})
	}
}

func TestValidateGeneratedOutlineSegmentRejectsSemanticIssues(t *testing.T) {
	tests := []struct {
		name    string
		outline string
		start   int
		end     int
		issue   string
	}{
		{name: "invalid range", outline: "第3章：主角进入密室", start: 4, end: 3, issue: outlineIssueInvalidRange},
		{name: "Chinese chapter number", outline: "第三章：主角进入密室", start: 3, end: 3, issue: outlineIssueMalformedLine},
		{name: "malformed marker", outline: "第3章 主角进入密室", start: 3, end: 3, issue: outlineIssueMalformedLine},
		{name: "text inside chapter block", outline: "第3章：主角进入密室\n中场说明\n第4章：主角追踪祭坛", start: 3, end: 4, issue: outlineIssueMalformedLine},
		{name: "out of range", outline: "以下是大纲\n- 第2章：主角发现血书\n- 第3章：主角进入密室", start: 3, end: 4, issue: outlineIssueOutOfRange},
		{name: "duplicate", outline: "**第3章：事件一**\n**第3章：事件二**", start: 3, end: 4, issue: outlineIssueDuplicateChapter},
		{name: "out of order", outline: "第4章:主角追踪祭坛\n第3章:主角进入密室", start: 3, end: 4, issue: outlineIssueOutOfOrder},
		{name: "blank event", outline: "第3章：   \n第4章：主角追踪祭坛", start: 3, end: 4, issue: outlineIssueBlankEvent},
		{name: "oversized event", outline: "第3章：" + strings.Repeat("事", maxMainlineEventRunes+1), start: 3, end: 3, issue: outlineIssueOversizedEvent},
		{name: "missing chapter", outline: "第3章：主角进入密室", start: 3, end: 4, issue: outlineIssueMissingChapter},
		{name: "zero chapter", outline: "第0章：主角进入密室", start: 1, end: 1, issue: outlineIssueInvalidChapter},
		{name: "overflow chapter", outline: "第999999999999999999999999章：主角进入密室", start: 1, end: 1, issue: outlineIssueInvalidChapter},
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
	for _, value := range []string{"第4章", "主角找到血书", "主角前往地下祭坛", "当前章节主线锚点"} {
		if !strings.Contains(prompt, value) {
			t.Fatalf("prompt missing %q: %s", value, prompt)
		}
	}
	if got := mainlineBeatPrompt(MainlineEventBeat{}); got != "" {
		t.Fatalf("empty beat prompt = %q", got)
	}
	firstPrompt := mainlineBeatPrompt(MainlineEventBeat{
		ChapterIndex:  1,
		CurrentEvent:  "觉醒人生模拟器",
		PhaseTitle:    "开局",
		PhaseGoal:     "确立生存策略",
		PhaseEndState: "掌握初始能力",
		Estimated:     true,
	})
	for _, value := range []string{"阶段事件方向（不代表必须在本章完成）", "开局", "确立生存策略", "阶段边界参考"} {
		if !strings.Contains(firstPrompt, value) {
			t.Fatalf("estimated prompt missing %q: %s", value, firstPrompt)
		}
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
	if !strings.Contains(prompt, "主角出发") || strings.Contains(prompt, "下一章预定推进节点") {
		t.Fatalf("oversized next event prompt = %q", prompt)
	}
}
