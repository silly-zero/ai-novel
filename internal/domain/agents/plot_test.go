package agents

import (
	"context"
	"strings"
	"testing"
)

func TestPlotRunGeneratesStructuredChapterContract(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"chapter_goal":" 查明密门来源 ","must_happen":[" 进入密门 ","发现血书"],"must_not_happen":["揭晓最终真相"],"end_state":" 前往地下祭坛 "}`,
	}}
	plot := NewPlotAgent(llm)
	state := &GenerationState{
		Idea:         "主角调查身世",
		FullOutline:  "主角最终发现旧王朝真相",
		ChapterIndex: 4,
		PreviousContinuity: ContinuityPacket{
			LastBeat:   "主角推开密门。",
			OpenLoops:  []string{"血书来自何人"},
			NextAction: "立即进入密门。",
		},
	}

	got, err := plot.Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChapterContract.Goal != "查明密门来源" ||
		got.ChapterContract.MustHappen[0] != "进入密门" ||
		got.ChapterContract.EndState != "前往地下祭坛" {
		t.Fatalf("contract = %#v", got.ChapterContract)
	}
	for _, value := range []string{"章节目标：查明密门来源", "必须发生", "禁止发生", "章尾状态：前往地下祭坛"} {
		if !strings.Contains(got.Outline, value) {
			t.Fatalf("outline missing %q: %s", value, got.Outline)
		}
	}
	for _, value := range []string{"第4章", "主角推开密门。", "血书来自何人", "立即进入密门。"} {
		if !strings.Contains(llm.users[0], value) {
			t.Fatalf("plot prompt missing %q: %s", value, llm.users[0])
		}
	}
}

func TestPlotRunRepairsInvalidContract(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{
		`{"chapter_goal":"","must_happen":[],"must_not_happen":[],"end_state":""}`,
		`{"chapter_goal":"进入密门","must_happen":["找到血书"],"must_not_happen":[],"end_state":"决定追踪祭坛"}`,
	}}
	state := &GenerationState{Idea: "调查身世", ChapterIndex: 2}

	got, err := NewPlotAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 2 || got.ChapterContract.Goal != "进入密门" {
		t.Fatalf("calls = %d, contract = %#v", llm.calls, got.ChapterContract)
	}
}

func TestPlotRunFailurePreservesExistingState(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{"not json", "still invalid"}}
	state := &GenerationState{
		Idea:            "调查身世",
		ChapterContract: validChapterContract(),
	}

	got, err := NewPlotAgent(llm).Run(context.Background(), state)
	if err == nil {
		t.Fatal("expected structured contract error")
	}
	if got.Outline != "" || got.ChapterContract.Goal != validChapterContract().Goal {
		t.Fatalf("state changed after failure: %#v", got)
	}
}

func TestPlotRunKeepsManualOutlineWithoutInventingContract(t *testing.T) {
	llm := &queuedStructuredLLM{}
	state := &GenerationState{Outline: "人工章节大纲"}

	got, err := NewPlotAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 0 || got.Outline != "人工章节大纲" || !got.ChapterContract.IsEmpty() {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
}
