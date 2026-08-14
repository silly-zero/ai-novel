package agents

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const maxMainlineEventRunes = 500

const (
	mainlineBeatIssueInvalidChapterIndex = "invalid_chapter_index"
	mainlineBeatIssueMissingCurrent      = "current_chapter_missing"
	mainlineBeatIssueDuplicateCurrent    = "current_chapter_duplicate"
	mainlineBeatIssueBlankCurrent        = "current_event_blank"
	mainlineBeatIssueOversizedCurrent    = "current_event_oversized"

	outlineIssueInvalidRange     = "invalid_outline_range"
	outlineIssueMalformedLine    = "generated_outline_malformed_line"
	outlineIssueInvalidChapter   = "generated_outline_invalid_chapter"
	outlineIssueOutOfRange       = "generated_outline_chapter_out_of_range"
	outlineIssueOutOfOrder       = "generated_outline_chapter_out_of_order"
	outlineIssueDuplicateChapter = "generated_outline_duplicate_chapter"
	outlineIssueBlankEvent       = "generated_outline_blank_event"
	outlineIssueOversizedEvent   = "generated_outline_oversized_event"
	outlineIssueMissingChapter   = "generated_outline_missing_chapter"
	outlineIssueRangeOverlap     = "existing_outline_range_overlap"
)

var (
	outlineBeatLinePattern          = regexp.MustCompile(`^第\s*([0-9]+)\s*章\s*[：:]\s*(.*)$`)
	generatedOutlineBeatLinePattern = regexp.MustCompile(`^第([0-9]+)章：(.*)$`)
)

type outlineChapterEntry struct {
	event string
}

type parsedMainlineOutline struct {
	hasStructuredLines bool
	chapters           map[int][]outlineChapterEntry
}

type mainlineBeatSelection struct {
	Beat                 MainlineEventBeat
	HasStructuredOutline bool
	IssueCode            string
}

func parseMainlineOutline(fullOutline string) parsedMainlineOutline {
	parsed := parsedMainlineOutline{
		chapters: make(map[int][]outlineChapterEntry),
	}
	for _, line := range strings.Split(fullOutline, "\n") {
		matches := outlineBeatLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(matches) != 3 {
			continue
		}
		parsed.hasStructuredLines = true
		index, err := strconv.Atoi(matches[1])
		if err != nil || index <= 0 {
			continue
		}
		parsed.chapters[index] = append(parsed.chapters[index], outlineChapterEntry{
			event: strings.TrimSpace(matches[2]),
		})
	}
	return parsed
}

func inspectMainlineEventBeat(fullOutline string, chapterIndex int) mainlineBeatSelection {
	parsed := parseMainlineOutline(fullOutline)
	selection := mainlineBeatSelection{
		HasStructuredOutline: parsed.hasStructuredLines,
	}
	if !parsed.hasStructuredLines {
		return selection
	}
	if chapterIndex <= 0 {
		selection.IssueCode = mainlineBeatIssueInvalidChapterIndex
		return selection
	}

	entries := parsed.chapters[chapterIndex]
	switch {
	case len(entries) == 0:
		selection.IssueCode = mainlineBeatIssueMissingCurrent
		return selection
	case len(entries) > 1:
		selection.IssueCode = mainlineBeatIssueDuplicateCurrent
		return selection
	case entries[0].event == "":
		selection.IssueCode = mainlineBeatIssueBlankCurrent
		return selection
	case len([]rune(entries[0].event)) > maxMainlineEventRunes:
		selection.IssueCode = mainlineBeatIssueOversizedCurrent
		return selection
	}

	selection.Beat = MainlineEventBeat{
		ChapterIndex: chapterIndex,
		CurrentEvent: entries[0].event,
	}
	nextEntries := parsed.chapters[chapterIndex+1]
	if len(nextEntries) == 1 &&
		nextEntries[0].event != "" &&
		len([]rune(nextEntries[0].event)) <= maxMainlineEventRunes {
		selection.Beat.NextEvent = nextEntries[0].event
	}
	return selection
}

func validateGeneratedOutlineSegment(fullOutline string, start, end int) string {
	if start <= 0 || end <= 0 || start > end {
		return outlineIssueInvalidRange
	}

	events := make(map[int]string)
	expectedIndex := start
	normalizedOutline := strings.ReplaceAll(fullOutline, "\r\n", "\n")
	if strings.Contains(normalizedOutline, "\r") {
		return outlineIssueMalformedLine
	}
	normalizedOutline = strings.TrimSuffix(normalizedOutline, "\n")
	for _, line := range strings.Split(normalizedOutline, "\n") {
		if strings.TrimSpace(line) != line || line == "" {
			return outlineIssueMalformedLine
		}
		matches := generatedOutlineBeatLinePattern.FindStringSubmatch(line)
		if len(matches) != 3 {
			return outlineIssueMalformedLine
		}
		index, err := strconv.Atoi(matches[1])
		if err != nil || index <= 0 {
			return outlineIssueInvalidChapter
		}
		if strconv.Itoa(index) != matches[1] {
			return outlineIssueMalformedLine
		}
		if index < start || index > end {
			return outlineIssueOutOfRange
		}
		if _, exists := events[index]; exists {
			return outlineIssueDuplicateChapter
		}
		if index != expectedIndex {
			return outlineIssueOutOfOrder
		}
		event := matches[2]
		if event == "" {
			return outlineIssueBlankEvent
		}
		if strings.TrimSpace(event) != event {
			return outlineIssueMalformedLine
		}
		if len([]rune(event)) > maxMainlineEventRunes {
			return outlineIssueOversizedEvent
		}
		events[index] = event
		expectedIndex++
	}
	if len(events) != end-start+1 {
		return outlineIssueMissingChapter
	}
	return ""
}

func validateOutlineRangeDoesNotOverlap(fullOutline string, start, end int) string {
	if start <= 0 || end <= 0 || start > end {
		return outlineIssueInvalidRange
	}
	parsed := parseMainlineOutline(fullOutline)
	for index := range parsed.chapters {
		if index >= start && index <= end {
			return outlineIssueRangeOverlap
		}
	}
	return ""
}

func selectMainlineEventBeat(fullOutline string, chapterIndex int) MainlineEventBeat {
	return inspectMainlineEventBeat(fullOutline, chapterIndex).Beat
}

func mainlineEventBeatIsValid(beat MainlineEventBeat) bool {
	return beat.ChapterIndex > 0 &&
		strings.TrimSpace(beat.CurrentEvent) != "" &&
		len([]rune(beat.CurrentEvent)) <= maxMainlineEventRunes
}

func mainlineBeatPrompt(beat MainlineEventBeat) string {
	if !mainlineEventBeatIsValid(beat) {
		return ""
	}

	currentEvent := strings.TrimSpace(beat.CurrentEvent)
	prompt := fmt.Sprintf("【主线事件节拍】\n- 当前章节：第%d章\n- 本章必须推动的主线事件：%s", beat.ChapterIndex, currentEvent)
	nextEvent := strings.TrimSpace(beat.NextEvent)
	if nextEvent != "" && len([]rune(nextEvent)) <= maxMainlineEventRunes {
		prompt += fmt.Sprintf("\n- 下一章预定事件（本章不得提前完成）：%s", nextEvent)
	}
	return prompt + "\n"
}
