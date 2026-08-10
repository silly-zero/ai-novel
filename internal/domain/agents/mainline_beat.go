package agents

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const maxMainlineEventRunes = 500

var outlineBeatLinePattern = regexp.MustCompile(`^第\s*([0-9]+)\s*章\s*[：:]\s*(.*)$`)

func selectMainlineEventBeat(fullOutline string, chapterIndex int) MainlineEventBeat {
	if chapterIndex <= 0 {
		return MainlineEventBeat{}
	}

	events := make(map[int]string)
	duplicates := make(map[int]bool)
	for _, line := range strings.Split(fullOutline, "\n") {
		matches := outlineBeatLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(matches) != 3 {
			continue
		}
		index, err := strconv.Atoi(matches[1])
		if err != nil || index <= 0 {
			continue
		}
		event := strings.TrimSpace(matches[2])
		if _, exists := events[index]; exists || duplicates[index] {
			duplicates[index] = true
			continue
		}
		if event == "" || len([]rune(event)) > maxMainlineEventRunes {
			duplicates[index] = true
			continue
		}
		events[index] = event
	}

	currentEvent, exists := events[chapterIndex]
	if !exists || duplicates[chapterIndex] {
		return MainlineEventBeat{}
	}
	beat := MainlineEventBeat{
		ChapterIndex: chapterIndex,
		CurrentEvent: currentEvent,
	}
	if nextEvent, exists := events[chapterIndex+1]; exists && !duplicates[chapterIndex+1] {
		beat.NextEvent = nextEvent
	}
	return beat
}

func mainlineBeatPrompt(beat MainlineEventBeat) string {
	currentEvent := strings.TrimSpace(beat.CurrentEvent)
	if beat.ChapterIndex <= 0 || currentEvent == "" || len([]rune(currentEvent)) > maxMainlineEventRunes {
		return ""
	}

	prompt := fmt.Sprintf("【主线事件节拍】\n- 当前章节：第%d章\n- 本章必须推动的主线事件：%s", beat.ChapterIndex, currentEvent)
	nextEvent := strings.TrimSpace(beat.NextEvent)
	if nextEvent != "" && len([]rune(nextEvent)) <= maxMainlineEventRunes {
		prompt += fmt.Sprintf("\n- 下一章预定事件（本章不得提前完成）：%s", nextEvent)
	}
	return prompt + "\n"
}
