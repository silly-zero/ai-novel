package agents

import (
	"context"
	"strings"
	"testing"
)

func TestDirectorRunInjectsMainlineBeat(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{"场景卡"}}
	state := &GenerationState{
		Outline: "章节契约",
		MainlineBeat: MainlineEventBeat{
			ChapterIndex: 4,
			CurrentEvent: "主角找到血书",
			NextEvent:    "主角前往地下祭坛",
		},
	}

	got, err := NewDirectorAgent(llm).Run(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.SceneCard != "场景卡" || llm.calls != 1 {
		t.Fatalf("state = %#v, calls = %d", got, llm.calls)
	}
	for _, value := range []string{"第4章", "主角找到血书", "主角前往地下祭坛"} {
		if !strings.Contains(llm.users[0], value) {
			t.Fatalf("director prompt missing %q: %s", value, llm.users[0])
		}
	}
	for _, rule := range []string{"实际推动本章事件", "不得提前完成下一章事件"} {
		if !strings.Contains(llm.systems[0], rule) {
			t.Fatalf("director system prompt missing %q: %s", rule, llm.systems[0])
		}
	}
}

func TestDirectorRunOmitsEmptyMainlineBeat(t *testing.T) {
	llm := &queuedStructuredLLM{responses: []string{"场景卡"}}
	if _, err := NewDirectorAgent(llm).Run(context.Background(), &GenerationState{Outline: "人工章节大纲"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(llm.users[0], "【主线事件节拍】") {
		t.Fatalf("director injected empty beat: %s", llm.users[0])
	}
}
