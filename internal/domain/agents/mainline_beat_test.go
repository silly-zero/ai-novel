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
