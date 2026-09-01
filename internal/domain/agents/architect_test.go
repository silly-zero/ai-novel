package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestArchitectGeneratesPhasePlanWithoutChapterList(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{`{"phases":[{"id":"phase_1","title":"起点","goal":"主角发现异常","events":["抵达现场","发现线索"],"causal_hook":"线索指向旧案","end_state":"决定继续调查"},{"id":"phase_2","title":"调查","goal":"主角确认线索","events":["追查来源","遭遇阻拦"],"causal_hook":"阻拦暴露幕后势力","end_state":"锁定下一地点"},{"id":"phase_3","title":"转折","goal":"主角进入新危机","events":["抵达入口","发现陷阱"],"causal_hook":"陷阱引出真相","end_state":"带着线索撤离"}]}`}}
	state := &GenerationState{Idea: "调查身世", OutlineMode: "full", ChapterIndex: 1}
	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 1 || !strings.Contains(got.FullOutline, "阶段1｜起点") || strings.Contains(got.FullOutline, "第1章：") {
		t.Fatalf("calls=%d outline=%q", llm.calls, got.FullOutline)
	}
	if got.MainlineBeat.ChapterIndex != 1 || got.MainlineBeat.CurrentEvent != "抵达现场" || got.MainlineBeat.NextEvent != "发现线索" || !got.MainlineBeat.Estimated {
		t.Fatalf("phase plan chapter anchor = %#v", got.MainlineBeat)
	}
	if !strings.Contains(llm.systems[0], "不要按“每章一件事”设计") {
		t.Fatalf("system prompt = %s", llm.systems[0])
	}
}

func TestArchitectExtendsPhasePlanAfterExistingOutline(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{`{"phases":[{"id":"phase_4","title":"新阶段","goal":"主角确认新目标","events":["整理线索","确定方向"],"causal_hook":"新目标引出追踪","end_state":"踏上新路"},{"id":"phase_5","title":"追踪","goal":"主角接近目标","events":["追踪踪迹","遭遇阻碍"],"causal_hook":"阻碍暴露敌人","end_state":"发现敌人所在"},{"id":"phase_6","title":"对抗","goal":"主角与敌人交锋","events":["确认敌情","展开交锋"],"causal_hook":"交锋改变局势","end_state":"冲突尚未结束"}]}`}}
	state := &GenerationState{Idea: "调查身世", ExistingOutline: "阶段1｜旧阶段\n阶段目标：旧目标", OutlineMode: "extend", OutlineStart: 1, OutlineEnd: 10, ChapterIndex: 4}
	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.FullOutline, "阶段1｜旧阶段") || !strings.Contains(got.FullOutline, "阶段2｜新阶段") {
		t.Fatalf("outline=%q", got.FullOutline)
	}
	if !strings.Contains(llm.systems[0], "不要求逐章输出") || !strings.Contains(llm.systems[0], "第1-10章") {
		t.Fatalf("system prompt=%s", llm.systems[0])
	}
}

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
	for _, rule := range []string{"可观察的主线变化", "后续牵引", "不得提前完成后续章节", "恰好逐号输出第 1 章到第 2 章", "不得遗漏、重复、跳号、交换顺序"} {
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

func TestArchitectDoesNotCommitAfterProviderReturnsIntoCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	llm := &queuedStructuredLLM{
		responses: []string{"第1章：主角抵达边城"},
		afterCall: func(int) { cancel() },
	}
	state := &GenerationState{Idea: "调查身世", OutlineStart: 1, OutlineEnd: 1, ChapterIndex: 1, FullOutline: "旧大纲"}
	got, err := NewArchitectAgent(llm).Run(ctx, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if got.FullOutline != "旧大纲" || got.MainlineBeat != (MainlineEventBeat{}) {
		t.Fatalf("state committed after cancellation: %#v", got)
	}
}

func TestArchitectCanonicalizesHarmlessGeneratedOutlineWithoutRepair(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		"以下是大纲：\n- 第 03 章: 主角进入密室  \n- **第０４章：主角追踪祭坛**\n以上。",
	}}
	state := &GenerationState{Idea: "调查身世", OutlineStart: 3, OutlineEnd: 4, ChapterIndex: 3}
	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	want := "第3章：主角进入密室\n第4章：主角追踪祭坛"
	if llm.calls != 1 || got.FullOutline != want ||
		got.MainlineBeat.CurrentEvent != "主角进入密室" {
		t.Fatalf("calls=%d state=%#v", llm.calls, got)
	}
}

func TestArchitectRepairsSemanticOutlineFailure(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		"第3章：主角进入密室",
		"第3章：主角进入密室\n第4章：主角追踪祭坛",
	}}
	state := &GenerationState{Idea: "调查身世", OutlineStart: 3, OutlineEnd: 4, ChapterIndex: 3}
	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 2 || got.MainlineBeat.CurrentEvent != "主角进入密室" ||
		!strings.Contains(got.FullOutline, "第4章：主角追踪祭坛") {
		t.Fatalf("calls=%d state=%#v", llm.calls, got)
	}
	if !strings.Contains(llm.systems[1], "全角冒号") ||
		!strings.Contains(llm.users[1], outlineIssueMissingChapter) {
		t.Fatalf("repair prompt missing constraints: system=%s user=%s", llm.systems[1], llm.users[1])
	}
	for _, rule := range []string{"恰好逐号输出完整区间", "第3章、第4章"} {
		if !strings.Contains(llm.systems[1], rule) {
			t.Fatalf("repair prompt missing %q: %s", rule, llm.systems[1])
		}
	}
}

func TestArchitectRepairReceivesCompleteBoundedOutline(t *testing.T) {
	longEvent := strings.Repeat("事", 70)
	lines := make([]string, 0, 10)
	for index := 1; index <= 10; index++ {
		lines = append(lines, fmt.Sprintf("第%d章：%s-%d", index, longEvent, index))
	}
	malformed := strings.Join(lines[:9], "\n")
	fixed := strings.Join(lines, "\n")
	if len([]rune(malformed)) <= 512 {
		t.Fatal("test outline must exceed the old repair limit")
	}
	llm := &queuedStructuredLLM{responses: []string{malformed, fixed}}
	state := &GenerationState{Idea: "调查身世", ChapterIndex: 1}

	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.users[1], lines[8]) || strings.Contains(llm.users[1], "...") {
		t.Fatal("repair prompt truncated a bounded outline")
	}
	if got.FullOutline != fixed {
		t.Fatalf("FullOutline length=%d, want=%d", len([]rune(got.FullOutline)), len([]rune(fixed)))
	}
}

func TestArchitectOutlineErrorExposesOnlySafeIssueCode(t *testing.T) {
	err := architectOutlineError(outlineIssueMissingChapter)
	var coded interface{ SafeDiagnosticCode() string }
	if !errors.As(err, &coded) || coded.SafeDiagnosticCode() != outlineIssueMissingChapter {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("error leaked unexpected content: %s", err)
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
			llm := &queuedStructuredLLM{responses: []string{test.response, test.response}}
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
			if llm.calls != 2 || got.FullOutline != originalOutline || got.ExistingOutline != "" || got.MainlineBeat != (MainlineEventBeat{}) {
				t.Fatalf("state = %#v, calls = %d", got, llm.calls)
			}
		})
	}
}

func TestArchitectRejectsInvalidRangesBeforeGeneration(t *testing.T) {
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

func TestArchitectAllowsOverlappingReferenceRange(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{"第2章：主角继续追查\n第3章：主角发现新线索"}}
	state := &GenerationState{
		FullOutline:  "第1章：主角出发\n第2章：主角发现血书",
		OutlineStart: 2,
		OutlineEnd:   3,
		ChapterIndex: 2,
	}
	got, err := NewArchitectAgent(llm).Run(context.Background(), state)
	if err != nil || llm.calls != 1 {
		t.Fatalf("error=%v calls=%d state=%#v", err, llm.calls, got)
	}
	if !strings.Contains(llm.systems[0], "第 2 章到第 3 章") || !strings.Contains(got.FullOutline, "第2章：主角继续追查") {
		t.Fatalf("system=%q state=%#v", llm.systems[0], got)
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
