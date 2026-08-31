package agents

import (
	"strings"
	"testing"
)

func TestSplitEventDraftUsesNaturalBoundaries(t *testing.T) {
	paragraphs := []string{
		strings.Repeat("第一阶段推进。", 400),
		strings.Repeat("第二阶段升级。", 400),
		strings.Repeat("第三阶段转折。", 400),
	}
	chapters, err := SplitEventDraft(strings.Join(paragraphs, "\n\n"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 3 {
		t.Fatalf("chapters = %d", len(chapters))
	}
	for index, chapter := range chapters {
		runes := len([]rune(chapter))
		if runes < minEventChapterRunes || runes > maxEventChapterRunes {
			t.Fatalf("chapter %d runes = %d", index+1, runes)
		}
		if !strings.HasSuffix(chapter, "。") {
			t.Fatalf("chapter %d did not end naturally", index+1)
		}
	}
}

func TestSplitEventDraftRejectsInvalidShape(t *testing.T) {
	for _, test := range []struct {
		name  string
		draft string
		count int
	}{
		{name: "count", draft: strings.Repeat("正文。", 2000), count: 1},
		{name: "empty", draft: " ", count: 2},
		{name: "short", draft: strings.Repeat("正文。", 100), count: 2},
		{name: "long", draft: strings.Repeat("正文。", 3000), count: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := SplitEventDraft(test.draft, test.count); err == nil {
				t.Fatal("invalid event draft was accepted")
			}
		})
	}
}
