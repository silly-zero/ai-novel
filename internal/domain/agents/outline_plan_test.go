package agents

import (
	"strings"
	"testing"
)

func validOutlinePlan() OutlinePlan {
	return OutlinePlan{Phases: []OutlinePhase{
		{ID: "phase_1", Title: "起点", Goal: "主角发现异常", Events: []string{"抵达现场", "发现线索"}, CausalHook: "线索指向旧案", EndState: "决定继续调查"},
		{ID: "phase_2", Title: "调查", Goal: "主角确认线索", Events: []string{"追查来源", "遭遇阻拦"}, CausalHook: "阻拦暴露幕后势力", EndState: "锁定下一地点"},
		{ID: "phase_3", Title: "转折", Goal: "主角进入新危机", Events: []string{"抵达入口", "发现陷阱"}, CausalHook: "陷阱引出真相", EndState: "带着线索撤离"},
	}}
}

func TestValidateOutlinePlanAcceptsAndDefaultsIDs(t *testing.T) {
	plan := validOutlinePlan()
	plan.Phases[0].ID = ""
	if err := validateOutlinePlan(&plan); err != nil {
		t.Fatal(err)
	}
	if plan.Phases[0].ID != "phase_1" {
		t.Fatalf("id = %q", plan.Phases[0].ID)
	}
}

func TestValidateOutlinePlanRejectsInvalidShape(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OutlinePlan)
	}{
		{name: "too few phases", mutate: func(plan *OutlinePlan) { plan.Phases = plan.Phases[:2] }},
		{name: "too many events", mutate: func(plan *OutlinePlan) {
			plan.Phases[0].Events = append(plan.Phases[0].Events, "a", "b", "c", "d", "e")
		}},
		{name: "blank goal", mutate: func(plan *OutlinePlan) { plan.Phases[0].Goal = " " }},
		{name: "duplicate id", mutate: func(plan *OutlinePlan) { plan.Phases[1].ID = plan.Phases[0].ID }},
		{name: "incomplete reference", mutate: func(plan *OutlinePlan) { plan.Phases[0].ReferenceChapterStart = intPtr(1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validOutlinePlan()
			test.mutate(&plan)
			if err := validateOutlinePlan(&plan); err == nil {
				t.Fatal("validateOutlinePlan() error = nil")
			}
		})
	}
}

func TestFormatOutlinePlanProducesEditablePhaseText(t *testing.T) {
	plan := validOutlinePlan()
	start, end := 2, 5
	plan.Phases[0].ReferenceChapterStart = &start
	plan.Phases[0].ReferenceChapterEnd = &end
	text := formatOutlinePlan(plan)
	for _, want := range []string{"阶段1｜起点", "参考章节：2-5", "阶段目标：主角发现异常", "事件链：", "1. 抵达现场", "因果牵引：线索指向旧案", "阶段终点：决定继续调查"} {
		if !contains(text, want) {
			t.Fatalf("formatted outline missing %q: %s", want, text)
		}
	}
}

func TestOutlinePlanPromptUsesReferenceRangeAsHint(t *testing.T) {
	prompt := outlinePlanPrompt("extend", 4, 8)
	for _, want := range []string{"不要按“每章一件事”设计", "续写模式", "第4-8章", "不要求逐章输出"} {
		if !contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func intPtr(value int) *int            { return &value }
func contains(value, want string) bool { return strings.Contains(value, want) }
