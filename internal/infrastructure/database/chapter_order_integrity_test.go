package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	_ "github.com/lib/pq"
)

func TestFindDuplicateChapterOrdersPostgres(t *testing.T) {
	dsn := os.Getenv("AI_NOVEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AI_NOVEL_TEST_POSTGRES_DSN is not set")
	}
	client, err := ent.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	firstNovel, err := client.Novel.Create().SetTitle(fmt.Sprintf("duplicate-a-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondNovel, err := client.Novel.Create().SetTitle(fmt.Sprintf("duplicate-b-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]*ent.Chapter, 0, 3)
	for _, item := range []struct {
		novelID int
		order   int
	}{
		{firstNovel.ID, 2},
		{firstNovel.ID, 2},
		{secondNovel.ID, 2},
	} {
		row, createErr := client.Chapter.Create().SetNovelID(item.novelID).SetTitle("章节").SetContent("正文").SetWordCount(2).SetOrder(item.order).Save(ctx)
		if createErr != nil {
			t.Fatal(createErr)
		}
		rows = append(rows, row)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.Chapter.Delete().Where(chapter.IDIn(rows[0].ID, rows[1].ID, rows[2].ID)).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(firstNovel.ID).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(secondNovel.ID).Exec(cleanupCtx)
	})
	groups, err := FindDuplicateChapterOrders(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].NovelID != firstNovel.ID || groups[0].Order != 2 || len(groups[0].ChapterIDs) != 2 || groups[0].ChapterIDs[0] != rows[0].ID || groups[0].ChapterIDs[1] != rows[1].ID {
		t.Fatalf("duplicate groups = %#v", groups)
	}
}
