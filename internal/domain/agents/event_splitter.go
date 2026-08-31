package agents

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	minEventChapterRunes    = 2500
	targetEventChapterRunes = 3300
	maxEventChapterRunes    = 4000
)

func SplitEventDraft(draft string, chapterCount int) ([]string, error) {
	if chapterCount < 2 || chapterCount > 3 {
		return nil, fmt.Errorf("event chapter count must be between 2 and 3")
	}
	text := strings.TrimSpace(draft)
	if text == "" {
		return nil, fmt.Errorf("event draft is empty")
	}
	runes := []rune(text)
	if len(runes) < minEventChapterRunes*chapterCount {
		return nil, fmt.Errorf("event draft is too short for %d chapters", chapterCount)
	}
	if len(runes) > maxEventChapterRunes*chapterCount {
		return nil, fmt.Errorf("event draft is too long for %d chapters", chapterCount)
	}

	boundaries := eventDraftBoundaries(runes)
	chapters := make([]string, 0, chapterCount)
	start := 0
	for remaining := chapterCount; remaining > 1; remaining-- {
		minEnd := start + minEventChapterRunes
		maxEnd := len(runes) - minEventChapterRunes*(remaining-1)
		if maxEnd-start > maxEventChapterRunes {
			maxEnd = start + maxEventChapterRunes
		}
		if minEnd > maxEnd {
			return nil, fmt.Errorf("event draft cannot satisfy chapter length bounds")
		}
		target := start + targetEventChapterRunes
		if target > maxEnd {
			target = maxEnd
		}
		end := nearestEventBoundary(boundaries, minEnd, maxEnd, target)
		if end == 0 {
			return nil, fmt.Errorf("event draft has no natural chapter boundary")
		}
		chapter := strings.TrimSpace(string(runes[start:end]))
		if chapter == "" {
			return nil, fmt.Errorf("event draft produced an empty chapter")
		}
		chapters = append(chapters, chapter)
		start = end
	}
	last := strings.TrimSpace(string(runes[start:]))
	lastRunes := len([]rune(last))
	if lastRunes < minEventChapterRunes || lastRunes > maxEventChapterRunes {
		return nil, fmt.Errorf("event draft final chapter length is invalid")
	}
	chapters = append(chapters, last)
	return chapters, nil
}

func eventDraftBoundaries(runes []rune) []int {
	boundaries := make([]int, 0, len(runes)/100)
	for index, current := range runes {
		end := index + 1
		if current == '\n' && index > 0 && runes[index-1] == '\n' {
			boundaries = append(boundaries, end)
			continue
		}
		if strings.ContainsRune("。！？!?；;…", current) {
			for end < len(runes) && unicode.IsSpace(runes[end]) {
				end++
			}
			boundaries = append(boundaries, end)
		}
	}
	return boundaries
}

func nearestEventBoundary(boundaries []int, minEnd, maxEnd, target int) int {
	best := 0
	bestDistance := int(^uint(0) >> 1)
	for _, boundary := range boundaries {
		if boundary < minEnd || boundary > maxEnd {
			continue
		}
		distance := boundary - target
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance || distance == bestDistance && boundary > best {
			best = boundary
			bestDistance = distance
		}
	}
	return best
}
