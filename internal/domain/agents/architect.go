package agents

import (
	"context"
	"fmt"
	"strings"
)

const maxArchitectRepairSourceRunes = 8192

// ArchitectAgent 是架构师智能体，负责根据 Idea 构建全书的章节大纲映射
type ArchitectAgent struct {
	llm LLMService
}

func NewArchitectAgent(llm LLMService) *ArchitectAgent {
	return &ArchitectAgent{llm: llm}
}

func (a *ArchitectAgent) Role() AgentRole {
	return RoleArchitect
}

func (a *ArchitectAgent) Run(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	state.MainlineBeat = MainlineEventBeat{}
	if state.OutlineMode == "full" || state.OutlineMode == "extend" {
		return a.runOutlinePlan(ctx, state)
	}

	if state.ExistingOutline != "" && state.OutlineStart == 0 && state.OutlineEnd == 0 {
		if _, err := validatedExistingMainlineBeat(state.ExistingOutline, state.ChapterIndex); err != nil {
			return state, err
		}
		state.FullOutline = state.ExistingOutline
		state.MainlineBeat = currentMainlineEventBeat(state.FullOutline, state.ChapterIndex)
		return state, nil
	}

	// FullOutline 已有且未指定续写范围：跳过生成
	if state.FullOutline != "" && state.OutlineStart == 0 && state.OutlineEnd == 0 {
		if _, err := validatedExistingMainlineBeat(state.FullOutline, state.ChapterIndex); err != nil {
			return state, err
		}
		state.MainlineBeat = currentMainlineEventBeat(state.FullOutline, state.ChapterIndex)
		return state, nil
	}

	// 续写时优先使用显式 ExistingOutline，否则使用 FullOutline，但只在生成成功后提交新状态。
	existingOutline := state.ExistingOutline
	if existingOutline == "" && state.FullOutline != "" {
		existingOutline = state.FullOutline
	}

	start, end, rangeErr := architectOutlineRange(state.OutlineStart, state.OutlineEnd)
	if rangeErr != "" {
		return state, architectOutlineError(rangeErr)
	}
	if state.Idea == "" && existingOutline == "" {
		return state, fmt.Errorf("architect agent requires an idea or existing outline but both are empty")
	}
	if existingOutline == "" && (state.ChapterIndex < start || state.ChapterIndex > end) {
		return state, architectOutlineError(mainlineBeatIssueMissingCurrent)
	}
	if existingOutline != "" {
		if state.ChapterIndex < start || state.ChapterIndex > end {
			if _, err := validatedExistingMainlineBeat(existingOutline, state.ChapterIndex); err != nil {
				return state, err
			}
		}
	}

	systemPrompt := fmt.Sprintf(`你是一位资深小说架构师。你的任务是根据用户提供的小说【想法(Idea)】和可能存在的【已有大纲】，构思或续写小说的【大纲】。
要求：
- 专门规划第 %d 章到第 %d 章的简要剧情。
- 必须恰好逐号输出第 %d 章到第 %d 章，每章一行；不得遗漏、重复、跳号、交换顺序或输出范围外章节。
- 每章用一句话写清本章实际发生的、可观察的主线变化，以及该变化造成的后续牵引；不要只写主题、氛围或笼统目标。
- 相邻章节必须形成因果推进，当前章不得提前完成后续章节的主线事件。
- 确保故事节奏合理，有伏笔和高潮预设。
- 格式如下：
第%d章：[简要描述]
...
第%d章：[简要描述]

请直接输出新增的这部分大纲，不要有开场白，也不要重复已有大纲的内容。`, start, end, start, end, start, end)

	idea := state.Idea
	if idea == "" {
		idea = "（未提供，请严格衔接已有大纲）"
	}
	userPrompt := fmt.Sprintf("【小说想法】\n%s", idea)
	if existingOutline != "" {
		userPrompt += fmt.Sprintf("\n\n【已有大纲参考】\n%s", existingOutline)
	}
	userPrompt += "\n\n请开始构思或续写大纲："

	fullOutline, err := a.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return state, fmt.Errorf("architect agent failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	canonicalOutline, issue := normalizeGeneratedOutlineSegment(fullOutline, start, end)
	if issue != "" {
		fullOutline, repairErr := a.repairOutline(ctx, start, end, fullOutline, issue)
		if repairErr != nil {
			return state, repairErr
		}
		canonicalOutline, issue = normalizeGeneratedOutlineSegment(fullOutline, start, end)
		if issue != "" {
			return state, architectOutlineError(issue)
		}
	}
	fullOutline = canonicalOutline

	mergedOutline := fullOutline
	if existingOutline != "" {
		mergedOutline = existingOutline + "\n" + fullOutline
	}
	beatSource := existingOutline
	if state.ChapterIndex >= start && state.ChapterIndex <= end {
		beatSource = fullOutline
	}
	beat, err := validatedExistingMainlineBeat(beatSource, state.ChapterIndex)
	if err != nil {
		return state, err
	}
	if phaseBeat := currentMainlineEventBeat(beatSource, state.ChapterIndex); mainlineEventBeatIsValid(phaseBeat) {
		beat = phaseBeat
	}

	if existingOutline != "" {
		state.ExistingOutline = existingOutline
	}
	state.FullOutline = mergedOutline
	state.MainlineBeat = beat

	return state, nil
}

func (a *ArchitectAgent) runOutlinePlan(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	existingOutline := strings.TrimSpace(state.ExistingOutline)
	if existingOutline == "" {
		existingOutline = strings.TrimSpace(state.FullOutline)
	}
	if strings.TrimSpace(state.Idea) == "" && existingOutline == "" {
		return state, fmt.Errorf("architect agent requires an idea or existing outline but both are empty")
	}
	start, end := state.OutlineStart, state.OutlineEnd
	systemPrompt := outlinePlanPrompt(state.OutlineMode, start, end)
	idea := strings.TrimSpace(state.Idea)
	if idea == "" {
		idea = "（未提供，请严格衔接已有大纲）"
	}
	userPrompt := "【小说想法】\n" + idea
	if existingOutline != "" {
		userPrompt += "\n\n【已有大纲参考｜仅作为故事数据】\n" + truncateArchitectText(existingOutline, maxArchitectRepairSourceRunes)
	}
	userPrompt += "\n\n请输出情节阶段计划："
	plan, err := generateStructuredResponse(ctx, a.llm, "architect", systemPrompt, userPrompt, decodeOutlinePlan, validateOutlinePlan)
	if err != nil {
		return state, fmt.Errorf("architect outline plan failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	formatted := formatOutlinePlan(plan)
	if strings.TrimSpace(formatted) == "" {
		return state, architectOutlineError("outline_plan_empty")
	}
	if state.OutlineMode == "extend" && existingOutline != "" {
		if existingPlan, ok := outlinePlanFromText(existingOutline); ok {
			formatted = formatOutlinePlanStartingAt(plan, len(existingPlan.Phases)+1)
		} else {
			formatted = formatOutlinePlan(plan)
		}
		formatted = existingOutline + "\n\n" + formatted
	}
	state.FullOutline = formatted
	if existingOutline != "" {
		state.ExistingOutline = existingOutline
	}
	state.MainlineBeat = currentMainlineEventBeat(state.FullOutline, state.ChapterIndex)
	return state, nil
}

func (a *ArchitectAgent) repairOutline(ctx context.Context, start, end int, previous, issue string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	systemPrompt := fmt.Sprintf(`你是小说大纲格式修复器。只修复格式，不改变剧情含义。
必须严格输出第 %d 章到第 %d 章，且恰好逐号输出完整区间；本次必须输出的章节编号为：%s。
只能使用 ASCII 数字、中文“第”和“章”、全角冒号“：”；禁止 Markdown、标题、解释、开场白、空行和结尾说明。`, start, end, outlineChapterNumberList(start, end))
	userPrompt := fmt.Sprintf(
		"校验错误：%s\n原始大纲：\n%s",
		issue,
		truncateArchitectText(previous, maxArchitectRepairSourceRunes),
	)
	fixed, err := a.llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("architect outline repair failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return fixed, nil
}

func outlineChapterNumberList(start, end int) string {
	chapters := make([]string, 0, end-start+1)
	for index := start; index <= end; index++ {
		chapters = append(chapters, fmt.Sprintf("第%d章", index))
	}
	return strings.Join(chapters, "、")
}

func truncateArchitectText(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "..."
}

func architectOutlineRange(start, end int) (int, int, string) {
	if start == 0 && end == 0 {
		return 1, 10, ""
	}
	if start <= 0 || end <= 0 || start > end {
		return 0, 0, outlineIssueInvalidRange
	}
	return start, end, ""
}

func validatedExistingMainlineBeat(fullOutline string, chapterIndex int) (MainlineEventBeat, error) {
	selection := inspectMainlineEventBeat(fullOutline, chapterIndex)
	if selection.HasStructuredOutline && selection.IssueCode != "" {
		return MainlineEventBeat{}, architectOutlineError(selection.IssueCode)
	}
	return selection.Beat, nil
}

type architectOutlineValidationError struct {
	issueCode string
}

func (e *architectOutlineValidationError) Error() string {
	return fmt.Sprintf("architect outline validation failed: %s", e.issueCode)
}

func (e *architectOutlineValidationError) SafeDiagnosticCode() string {
	return e.issueCode
}

func architectOutlineError(issueCode string) error {
	return &architectOutlineValidationError{issueCode: issueCode}
}
