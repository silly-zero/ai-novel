package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	minOutlinePlanPhases      = 3
	maxOutlinePlanPhases      = 8
	minOutlinePhaseEvents     = 2
	maxOutlinePhaseEvents     = 6
	maxOutlinePhaseTitleRunes = 80
	maxOutlinePhaseFieldRunes = 300
)

type OutlinePlan struct {
	Phases []OutlinePhase `json:"phases"`
}

type OutlinePhase struct {
	ID                    string   `json:"id,omitempty"`
	Title                 string   `json:"title"`
	Goal                  string   `json:"goal"`
	Events                []string `json:"events"`
	CausalHook            string   `json:"causal_hook"`
	EndState              string   `json:"end_state"`
	ReferenceChapterStart *int     `json:"reference_chapter_start,omitempty"`
	ReferenceChapterEnd   *int     `json:"reference_chapter_end,omitempty"`
}

func decodeOutlinePlan(candidate []byte) (OutlinePlan, error) {
	var plan OutlinePlan
	if err := json.Unmarshal(candidate, &plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func validateOutlinePlan(plan *OutlinePlan) error {
	if plan == nil || len(plan.Phases) < minOutlinePlanPhases || len(plan.Phases) > maxOutlinePlanPhases {
		return errors.New("outline phase count is invalid")
	}
	seenIDs := make(map[string]struct{}, len(plan.Phases))
	for index := range plan.Phases {
		phase := &plan.Phases[index]
		if strings.TrimSpace(phase.ID) == "" {
			phase.ID = "phase_" + strconv.Itoa(index+1)
		}
		if _, exists := seenIDs[phase.ID]; exists {
			return errors.New("outline phase id is duplicated")
		}
		seenIDs[phase.ID] = struct{}{}
		if len([]rune(strings.TrimSpace(phase.Title))) == 0 || len([]rune(phase.Title)) > maxOutlinePhaseTitleRunes {
			return errors.New("outline phase title is invalid")
		}
		for _, value := range []string{phase.Goal, phase.CausalHook, phase.EndState} {
			if len([]rune(strings.TrimSpace(value))) == 0 || len([]rune(value)) > maxOutlinePhaseFieldRunes {
				return errors.New("outline phase field is invalid")
			}
		}
		if len(phase.Events) < minOutlinePhaseEvents || len(phase.Events) > maxOutlinePhaseEvents {
			return errors.New("outline phase event count is invalid")
		}
		for _, event := range phase.Events {
			if len([]rune(strings.TrimSpace(event))) == 0 || len([]rune(event)) > maxOutlinePhaseFieldRunes {
				return errors.New("outline phase event is invalid")
			}
		}
		if (phase.ReferenceChapterStart == nil) != (phase.ReferenceChapterEnd == nil) {
			return errors.New("outline phase reference range is incomplete")
		}
		if phase.ReferenceChapterStart != nil && (*phase.ReferenceChapterStart <= 0 || *phase.ReferenceChapterEnd < *phase.ReferenceChapterStart) {
			return errors.New("outline phase reference range is invalid")
		}
	}
	return nil
}

func formatOutlinePlan(plan OutlinePlan) string {
	return formatOutlinePlanStartingAt(plan, 1)
}

func formatOutlinePlanStartingAt(plan OutlinePlan, firstIndex int) string {
	if firstIndex <= 0 {
		firstIndex = 1
	}
	var builder strings.Builder
	for index, phase := range plan.Phases {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		fmt.Fprintf(&builder, "阶段%d｜%s\n", firstIndex+index, strings.TrimSpace(phase.Title))
		if phase.ReferenceChapterStart != nil {
			fmt.Fprintf(&builder, "参考章节：%d-%d\n", *phase.ReferenceChapterStart, *phase.ReferenceChapterEnd)
		}
		fmt.Fprintf(&builder, "阶段目标：%s\n事件链：\n", strings.TrimSpace(phase.Goal))
		for eventIndex, event := range phase.Events {
			fmt.Fprintf(&builder, "%d. %s\n", eventIndex+1, strings.TrimSpace(event))
		}
		fmt.Fprintf(&builder, "因果牵引：%s\n阶段终点：%s", strings.TrimSpace(phase.CausalHook), strings.TrimSpace(phase.EndState))
	}
	return builder.String()
}

func outlinePlanPrompt(mode string, start, end int) string {
	var builder strings.Builder
	builder.WriteString(`你是一位资深小说架构师。请根据小说想法和已有大纲，规划可跨越多个章节的情节阶段。
不要按“每章一件事”设计，也不要输出逐章列表。请输出唯一的 JSON 对象：{"phases":[...]}。
每个阶段必须包含：id、title、goal、events、causal_hook、end_state；events 按因果顺序写 2-6 个具体推进事件。
阶段目标、事件链和阶段终点必须具体可观察；同一阶段允许跨越多章，阶段终点是情节阶段的自然结果，不要求等于某一章结尾。
阶段数量必须为 3-8 个。不要输出 Markdown、解释或 JSON 以外的文字。`)
	if mode == "extend" {
		builder.WriteString("\n这是续写模式：只规划已有大纲最后阶段之后的新阶段，不重复已有阶段或事件。")
	} else {
		builder.WriteString("\n这是全书规划模式：从 Idea 组织完整主线，优先覆盖主要冲突、成长和因果转折。")
	}
	if start > 0 && end > 0 {
		fmt.Fprintf(&builder, "\n参考章节范围为第%d-%d章，只用于估计阶段跨度和节奏，不要求逐章输出，也不要求每个阶段对应一章。", start, end)
	}
	return builder.String()
}

func outlinePlanFromText(value string) (OutlinePlan, bool) {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	plan := OutlinePlan{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "阶段") && strings.Contains(line, "｜") {
			parts := strings.SplitN(line, "｜", 2)
			if len(parts) == 2 {
				plan.Phases = append(plan.Phases, OutlinePhase{Title: strings.TrimSpace(parts[1])})
			}
		}
	}
	return plan, len(plan.Phases) > 0
}
