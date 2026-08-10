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
	state := &GenerationState{Idea: "调查身世", ChapterIndex: 1, OutlineEnd: 2}

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
