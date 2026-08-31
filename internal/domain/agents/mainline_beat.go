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
	outlineBeatLinePattern            = regexp.MustCompile(`^第\s*([0-9]+)\s*章\s*[：:]\s*(.*)$`)
	generatedOutlineBeatLinePattern   = regexp.MustCompile(`^第\s*([0-9０-９]+)\s*章\s*[：:]\s*(.*)$`)
	generatedOutlineListPrefixPattern = regexp.MustCompile(`^(?:[-+*]\s+|[0-9０-９]+[.)、]\s*)`)
	generatedOutlineNumericMarker     = regexp.MustCompile(`第\s*[0-9０-９]+\s*章`)
	generatedOutlineChineseMarker     = regexp.MustCompile(`第\s*[零〇一二三四五六七八九十百两]+\s*章`)
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
	if _, isPhasePlan := outlinePlanFromText(fullOutline); isPhasePlan {
		return mainlineBeatSelection{}
	}
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
	_, issue := normalizeGeneratedOutlineSegment(fullOutline, start, end)
	return issue
}

func normalizeGeneratedOutlineSegment(
	fullOutline string,
	start int,
	end int,
) (string, string) {
	if start <= 0 || end <= 0 || start > end {
		return "", outlineIssueInvalidRange
	}

	normalized := strings.ReplaceAll(fullOutline, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimPrefix(normalized, string(rune(0xFEFF)))
	lines := strings.Split(normalized, "\n")
	canonicalLines := make([]string, 0, end-start+1)
	events := make(map[int]string)
	expectedIndex := start
	insideChapterBlock := false
	insideFence := false

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "```") {
			if insideChapterBlock && len(events) != end-start+1 {
				return "", outlineIssueMalformedLine
			}
			insideFence = !insideFence
			continue
		}
		if line == "" {
			continue
		}
		line = strings.TrimSpace(generatedOutlineListPrefixPattern.ReplaceAllString(line, ""))
		if strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") && len(line) >= 4 {
			line = strings.TrimSpace(line[2 : len(line)-2])
		}

		matches := generatedOutlineBeatLinePattern.FindStringSubmatch(line)
		if len(matches) != 3 {
			if generatedOutlineNumericMarker.MatchString(line) ||
				generatedOutlineChineseMarker.MatchString(line) ||
				(insideChapterBlock && len(events) != end-start+1) {
				return "", outlineIssueMalformedLine
			}
			continue
		}
		insideChapterBlock = true

		indexText, ok := normalizeOutlineDigits(matches[1])
		if !ok {
			return "", outlineIssueMalformedLine
		}
		index, err := strconv.Atoi(indexText)
		if err != nil || index <= 0 {
			return "", outlineIssueInvalidChapter
		}
		if index < start || index > end {
			return "", outlineIssueOutOfRange
		}
		if _, exists := events[index]; exists {
			return "", outlineIssueDuplicateChapter
		}
		if index != expectedIndex {
			return "", outlineIssueOutOfOrder
		}
		event := strings.TrimSpace(matches[2])
		if event == "" {
			return "", outlineIssueBlankEvent
		}
		if len([]rune(event)) > maxMainlineEventRunes {
			return "", outlineIssueOversizedEvent
		}
		events[index] = event
		canonicalLines = append(canonicalLines, fmt.Sprintf("第%d章：%s", index, event))
		expectedIndex++
	}
	if insideFence || len(events) != end-start+1 {
		return "", outlineIssueMissingChapter
	}
	return strings.Join(canonicalLines, "\n"), ""
}

func normalizeOutlineDigits(value string) (string, bool) {
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char >= '０' && char <= '９':
			builder.WriteRune('0' + (char - '０'))
		default:
			return "", false
		}
	}
	return builder.String(), builder.Len() > 0
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
	prompt := fmt.Sprintf("【主线推进节点】\n- 当前章节：第%d章\n- 本章必须达到的主线推进节点：%s", beat.ChapterIndex, currentEvent)
	nextEvent := strings.TrimSpace(beat.NextEvent)
	if nextEvent != "" && len([]rune(nextEvent)) <= maxMainlineEventRunes {
		prompt += fmt.Sprintf("\n- 下一章预定推进节点（本章只可自然铺垫，不得提前达成决定性结果）：%s", nextEvent)
	}
	return prompt + "\n"
}
