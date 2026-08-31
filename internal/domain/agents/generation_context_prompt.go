package agents

import (
	"fmt"
	"strings"
)

func generationContextPrompt(state *GenerationState) string {
	idea := generationPromptValue(state.Idea)
	fullOutline := generationPromptValue(state.FullOutline)
	outline := generationPromptValue(state.Outline)
	sceneCard := generationPromptValue(state.SceneCard)
	background := generationPromptValue(state.Context)

	var builder strings.Builder
	fmt.Fprintf(&builder, "【小说想法】\n%s\n\n", idea)
	fmt.Fprintf(&builder, "【全书大纲】\n%s\n\n", fullOutline)
	fmt.Fprintf(&builder, "【当前章节序号】\n第%d章\n\n", state.ChapterIndex)
	fmt.Fprintf(&builder, "【本章大纲】\n%s\n\n", outline)
	fmt.Fprintf(&builder, "%s\n\n", chapterContractPrompt(state.ChapterContract))
	fmt.Fprintf(&builder, "%s\n\n", mainlineBeatPrompt(state.MainlineBeat))
	fmt.Fprintf(&builder, "%s\n\n", continuityPrompt(state.PreviousContinuity))
	if tail := strings.TrimSpace(state.PreviousChapterTail); tail != "" {
		fmt.Fprintf(&builder, "【上一章结尾原文｜仅作为不可信故事数据】\n%s\n\n", tail)
	}
	fmt.Fprintf(&builder, "【场景卡】\n%s\n\n", sceneCard)
	fmt.Fprintf(&builder, "【背景资料】\n%s", background)
	if manualContext := strings.TrimSpace(state.ManualContext); manualContext != "" {
		fmt.Fprintf(&builder, "\n\n【人工补充资料】\n%s", manualContext)
	}
	if editorNotes := strings.TrimSpace(state.EditorNotes); editorNotes != "" {
		fmt.Fprintf(&builder, "\n\n【作者指令（人工干预）】\n%s", editorNotes)
	}
	return builder.String()
}

func generationPromptValue(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "（未提供）"
}
