package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestArchitectSelectsBeatFromExistingFullOutline(t *testing.T) {
	llm := &queuedStructuredLLM{}
	state := &GenerationState{
		FullOutline:  "第1章：主角抵达边城\n第2章：主角发现血书",
		ChapterIndex: 1,
	}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 0 || got.MainlineBeat.CurrentEvent != "主角抵达边城" || got.MainlineBeat.NextEvent != "主角发现血书" {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestArchitectSelectsBeatFromExistingOutline(t *testing.T) {
	llm := &queuedStructuredLLM{}
	state := &GenerationState{
		ExistingOutline: "第3章：主角进入密室\n第4章：主角追踪祭坛",
		ChapterIndex:    3,
	}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 0 || got.FullOutline != state.ExistingOutline || got.MainlineBeat.CurrentEvent != "主角进入密室" || got.MainlineBeat.NextEvent != "主角追踪祭坛" {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestArchitectSelectsBeatAfterGeneratingOutline(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		"第1章：主角抵达边城\n第2章：主角发现血书",
	}}
	state := &GenerationState{Idea: "调查身世", ChapterIndex: 1, OutlineStart: 1, OutlineEnd: 2}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 1 || got.MainlineBeat.CurrentEvent != "主角抵达边城" || got.MainlineBeat.NextEvent != "主角发现血书" {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
	for _, rule := range []string{"可观察的主线变化", "后续牵引", "不得提前完成后续章节"} {
		if !strings.Contains(llm.systems[0], rule) {
			t.Fatalf("architect prompt missing %q: %s", rule, llm.systems[0])
		}
	}
}

func TestArchitectSelectsBeatAfterOutlineContinuation(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		"第3章：主角进入密室\n第4章：主角追踪祭坛",
	}}
	state := &GenerationState{
		Idea:            "调查身世",
		ExistingOutline: "第1章：主角抵达边城\n第2章：主角发现血书",
		OutlineStart:    3,
		OutlineEnd:      4,
		ChapterIndex:    3,
	}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.MainlineBeat.CurrentEvent != "主角进入密室" || got.MainlineBeat.NextEvent != "主角追踪祭坛" {
		t.Fatalf("beat = %#v", got.MainlineBeat)
	}
	if !strings.Contains(got.FullOutline, "第2章：主角发现血书\n第3章：主角进入密室") {
		t.Fatalf("FullOutline = %q", got.FullOutline)
	}
}

func TestArchitectContinuesFullOutlineWithoutIdea(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		"第3章：主角进入密室\n第4章：主角追踪祭坛",
	}}
	originalOutline := "第1章：主角抵达边城\n第2章：主角发现血书"
	state := &GenerationState{
		FullOutline:  originalOutline,
		OutlineStart: 3,
		OutlineEnd:   4,
		ChapterIndex: 3,
	}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExistingOutline != originalOutline || !strings.Contains(got.FullOutline, "第2章：主角发现血书\n第3章：主角进入密室") {
		t.Fatalf("state = %#v", got)
	}
	if got.MainlineBeat.CurrentEvent != "主角进入密室" || got.MainlineBeat.NextEvent != "主角追踪祭坛" {
		t.Fatalf("beat = %#v", got.MainlineBeat)
	}
	if !strings.Contains(llm.users[0], "未提供，请严格衔接已有大纲") {
		t.Fatalf("user prompt = %s", llm.users[0])
	}
}

func TestArchitectFailurePreservesOutlineAndClearsStaleBeat(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	llm := &queuedStructuredLLM{errors: []error{providerErr}, responses: []string{""}}
	originalOutline := "第1章：主角抵达边城\n第2章：主角发现血书"
	state := &GenerationState{
		FullOutline:  originalOutline,
		OutlineStart: 3,
		OutlineEnd:   4,
		ChapterIndex: 3,
		MainlineBeat: MainlineEventBeat{ChapterIndex: 99, CurrentEvent: "旧节拍"},
	}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want %v", err, providerErr)
	}
	if got.FullOutline != originalOutline || got.ExistingOutline != "" {
		t.Fatalf("outline state changed on failure: %#v", got)
	}
	if got.MainlineBeat != (MainlineEventBeat{}) {
		t.Fatalf("stale beat retained: %#v", got.MainlineBeat)
	}
}

func TestArchitectStructuredOutlineRequiresCurrentChapter(t *testing.T) {
	tests := []struct {
		name    string
		outline string
		issue   string
	}{
		{name: "missing", outline: "第2章：主角发现血书", issue: mainlineBeatIssueMissingCurrent},
		{name: "duplicate", outline: "第1章：事件一\n第1章：事件二", issue: mainlineBeatIssueDuplicateCurrent},
		{name: "blank", outline: "第1章：", issue: mainlineBeatIssueBlankCurrent},
		{name: "oversized", outline: "第1章：" + strings.Repeat("事", maxMainlineEventRunes+1), issue: mainlineBeatIssueOversizedCurrent},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			llm := &queuedStructuredLLM{}
			state := &GenerationState{FullOutline: test.outline, ChapterIndex: 1}
			got, err := NewArchitectAgent(llm).Run(context.Background(), state)
			if err == nil || !strings.Contains(err.Error(), test.issue) {
				t.Fatalf("error = %v, want issue %q", err, test.issue)
			}
			if strings.Contains(err.Error(), test.outline) {
				t.Fatalf("error leaked outline: %v", err)
			}
			if llm.calls != 0 || got.FullOutline != test.outline || got.MainlineBeat != (MainlineEventBeat{}) {
				t.Fatalf("state = %#v, calls = %d", got, llm.calls)
			}
		})
	}
}

func TestArchitectContinuationRejectsInvalidExistingCurrentBeforeGeneration(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{"第3章：主角进入密室\n第4章：主角追踪祭坛"}}
	state := &GenerationState{
		ExistingOutline: "第1章：主角出发",
		OutlineStart:    3,
		OutlineEnd:      4,
		ChapterIndex:    2,
	}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), mainlineBeatIssueMissingCurrent) {
		t.Fatalf("error = %v", err)
	}
	if llm.calls != 0 || got.FullOutline != "" || got.MainlineBeat != (MainlineEventBeat{}) {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestArchitectContinuationGeneratesCurrentMissingFromExistingRange(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		"第3章：主角进入密室\n第4章：主角追踪祭坛",
	}}
	state := &GenerationState{
		ExistingOutline: "第1章：主角抵达边城\n第2章：主角发现血书",
		OutlineStart:    3,
		OutlineEnd:      4,
		ChapterIndex:    3,
	}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 1 || got.MainlineBeat.CurrentEvent != "主角进入密室" {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}

func TestArchitectContinuesNonstandardManualOutline(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		"第3章：主角进入密室\n第4章：主角追踪祭坛",
	}}
	manualOutline := "人工大纲：主角调查身世"
	state := &GenerationState{
		ExistingOutline: manualOutline,
		OutlineStart:    3,
		OutlineEnd:      4,
		ChapterIndex:    1,
	}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 1 || got.ExistingOutline != manualOutline || !strings.Contains(got.FullOutline, "第3章：主角进入密室") {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
	if got.MainlineBeat != (MainlineEventBeat{}) {
		t.Fatalf("beat = %#v, want empty", got.MainlineBeat)
	}
}

func TestArchitectExistingOutlineTakesPrecedenceOverFullOutline(t *testing.T) {
	state := &GenerationState{
		ExistingOutline: "第1章：显式已有大纲事件",
		FullOutline:     "第1章：缓存大纲事件",
		ChapterIndex:    1,
	}
	got, err := NewArchitectAgent(&queuedStructuredLLM{}).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.FullOutline != state.ExistingOutline || got.MainlineBeat.CurrentEvent != "显式已有大纲事件" {
		t.Fatalf("state = %#v", got)
	}
}

func TestArchitectRejectsInvalidGeneratedOutlineWithoutMutatingState(t *testing.T) {
	tests := []struct {
		name     string
		response string
		issue    string
	}{
		{name: "missing", response: "第3章：主角进入密室", issue: outlineIssueMissingChapter},
		{name: "duplicate", response: "第3章：事件一\n第3章：事件二", issue: outlineIssueDuplicateChapter},
		{name: "out of range", response: "第2章：主角发现血书\n第3章：主角进入密室", issue: outlineIssueOutOfRange},
		{name: "out of order", response: "第4章：主角追踪祭坛\n第3章：主角进入密室", issue: outlineIssueOutOfOrder},
		{name: "blank", response: "第3章：\n第4章：主角追踪祭坛", issue: outlineIssueBlankEvent},
	}
	originalOutline := "第1章：主角抵达边城\n第2章：主角发现血书"

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			llm := &queuedStructuredLLM{responses: []string{test.response}}
			state := &GenerationState{
				FullOutline:  originalOutline,
				OutlineStart: 3,
				OutlineEnd:   4,
				ChapterIndex: 3,
				MainlineBeat: MainlineEventBeat{ChapterIndex: 99, CurrentEvent: "旧节拍"},
			}
			got, err := NewArchitectAgent(llm).Run(context.Background(), state)
			if err == nil || !strings.Contains(err.Error(), test.issue) {
				t.Fatalf("error = %v, want issue %q", err, test.issue)
			}
			if strings.Contains(err.Error(), test.response) {
				t.Fatalf("error leaked generated outline: %v", err)
			}
			if llm.calls != 1 || got.FullOutline != originalOutline || got.ExistingOutline != "" || got.MainlineBeat != (MainlineEventBeat{}) {
				t.Fatalf("state = %#v, calls = %d", got, llm.calls)
			}
		})
	}
}

func TestArchitectRejectsInvalidAndOverlappingRangesBeforeGeneration(t *testing.T) {
	tests := []struct {
		name  string
		state GenerationState
		issue string
	}{
		{name: "missing start without input", state: GenerationState{OutlineEnd: 2}, issue: outlineIssueInvalidRange},
		{name: "reversed without input", state: GenerationState{OutlineStart: 4, OutlineEnd: 3}, issue: outlineIssueInvalidRange},
		{name: "missing start", state: GenerationState{Idea: "调查身世", OutlineEnd: 2}, issue: outlineIssueInvalidRange},
		{name: "missing end", state: GenerationState{Idea: "调查身世", OutlineStart: 2}, issue: outlineIssueInvalidRange},
		{name: "reversed", state: GenerationState{Idea: "调查身世", OutlineStart: 4, OutlineEnd: 3}, issue: outlineIssueInvalidRange},
		{name: "overlap", state: GenerationState{FullOutline: "第1章：主角出发\n第2章：主角发现血书", OutlineStart: 2, OutlineEnd: 3, ChapterIndex: 2}, issue: outlineIssueRangeOverlap},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			llm := &queuedStructuredLLM{}
			got, err := NewArchitectAgent(llm).Run(context.Background(), &test.state)
			if err == nil || !strings.Contains(err.Error(), test.issue) {
				t.Fatalf("error = %v, want issue %q", err, test.issue)
			}
			if llm.calls != 0 || got.MainlineBeat != (MainlineEventBeat{}) {
				t.Fatalf("state = %#v, calls = %d", got, llm.calls)
			}
		})
	}
}

func TestArchitectNewOutlineRangeMustContainCurrentChapter(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{"第3章：主角进入密室\n第4章：主角追踪祭坛"}}
	state := &GenerationState{
		Idea:         "调查身世",
		OutlineStart: 3,
		OutlineEnd:   4,
		ChapterIndex: 1,
	}
	_, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), mainlineBeatIssueMissingCurrent) {
		t.Fatalf("error = %v", err)
	}
	if llm.calls != 0 {
		t.Fatalf("LLM calls = %d, want 0", llm.calls)
	}
}

func TestArchitectInvalidOutlineKeepsEmptyBeat(t *testing.T) {
	state := &GenerationState{FullOutline: "人工大纲：主角调查身世", ChapterIndex: 1}
	got, err := NewArchitectAgent(&queuedStructuredLLM{}).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.MainlineBeat != (MainlineEventBeat{}) {
		t.Fatalf("beat = %#v, want empty", got.MainlineBeat)
	}
}
