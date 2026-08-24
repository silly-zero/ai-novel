package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/chapterderivedtask"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/internal/domain/agents"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	_ "github.com/lib/pq"
)

func TestDelayedNewChapterCreationPostgres(t *testing.T) {
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
	novelRow, _ := createStaleTestNovel(t, ctx, client, "delayed-create", 1)
	store := &entGenerationChapterStore{client: client}
	target, err := store.Prepare(ctx, novelRow.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID != 0 || !target.isNew {
		t.Fatalf("target = %#v", target)
	}
	if exists, err := client.Chapter.Query().Where(chapter.HasNovelWith(novel.ID(novelRow.ID)), chapter.OrderEQ(2)).Exist(ctx); err != nil || exists {
		t.Fatalf("placeholder exists=%v error=%v", exists, err)
	}
	state := &agents.GenerationState{
		GenerationID: "delayed-generation",
		ChapterID:    "",
		Draft:        validGeneratedContent(),
		IsApproved:   true,
		Continuity:   agents.ContinuityPacket{LastBeat: "结尾", NextAction: "下一步"},
	}
	state.Draft += "结尾下一步"
	chapterID, err := store.Save(ctx, target, state)
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.Chapter.Get(ctx, chapterID)
	if err != nil {
		t.Fatal(err)
	}
	if created.Order != 2 || created.Content != state.Draft || created.DerivedStatus != string(domain.DerivedStatusPending) || created.DerivedGenerationID != state.GenerationID {
		t.Fatalf("created chapter = %#v", created)
	}
	tasks, err := client.ChapterDerivedTask.Query().Where(chapterderivedtask.ChapterID(chapterID)).Count(ctx)
	if err != nil || tasks != len(domain.DerivedHandlerKeys) {
		t.Fatalf("derived tasks=%d error=%v", tasks, err)
	}
}

func createStaleTestNovel(
	t *testing.T,
	ctx context.Context,
	client *ent.Client,
	prefix string,
	orders ...int,
) (*ent.Novel, map[int]*ent.Chapter) {
	t.Helper()
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := client.Chapter.Delete().Where(chapter.HasNovelWith(novel.ID(novelRow.ID))).Exec(cleanupCtx); err != nil {
			t.Errorf("cleanup stale chapters: %v", err)
			return
		}
		if err := client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx); err != nil {
			t.Errorf("cleanup stale novel: %v", err)
		}
	})
	chapters := make(map[int]*ent.Chapter, len(orders))
	for _, order := range orders {
		row, err := client.Chapter.Create().SetNovel(novelRow).
			SetTitle(fmt.Sprintf("第%d章", order)).SetContent("旧正文").SetWordCount(3).
			SetOrder(order).SetStatus(string(domain.StatusDraft)).
			SetLastBeat("旧").SetNextAction("旧").Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		chapters[order] = row
	}
	return novelRow, chapters
}

func TestChapterRewriteMarksFollowingChaptersStale(t *testing.T) {
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
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("stale-test-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := client.Chapter.Delete().Where(chapter.HasNovelWith(novel.ID(novelRow.ID))).Exec(cleanupCtx); err != nil {
			t.Errorf("cleanup stale chapters: %v", err)
			return
		}
		if err := client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx); err != nil {
			t.Errorf("cleanup stale novel: %v", err)
		}
	})
	chapters := make([]*ent.Chapter, 0, 3)
	for order := 1; order <= 3; order++ {
		row, err := client.Chapter.Create().SetNovel(novelRow).
			SetTitle(fmt.Sprintf("第%d章", order)).SetContent("旧正文").SetWordCount(3).
			SetOrder(order).SetStatus(string(domain.StatusDraft)).
			SetLastBeat("旧").SetNextAction("旧").Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		chapters = append(chapters, row)
	}
	newContent := "新正文"
	if _, err := updateChapterWithIntegrity(ctx, client, chapters[0].ID, UpdateChapterRequest{Content: &newContent}); err != nil {
		t.Fatal(err)
	}
	rows, err := client.Chapter.Query().Where(chapter.HasNovelWith(novel.ID(novelRow.ID))).Order(chapter.ByOrder()).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != string(domain.StatusStale) || rows[1].Status != string(domain.StatusStale) || rows[2].Status != string(domain.StatusStale) {
		t.Fatalf("statuses after rewrite = %q, %q, %q", rows[0].Status, rows[1].Status, rows[2].Status)
	}
	if rows[0].LastBeat != "" || len(rows[0].OpenLoops) != 0 || rows[0].NextAction != "" ||
		rows[0].DerivedStatus != string(domain.DerivedStatusFailed) || rows[0].DerivedGenerationID != "" {
		t.Fatalf("rewritten chapter retained derived state: %#v", rows[0])
	}
	requestedDraft := string(domain.StatusDraft)
	if _, err := updateChapterWithIntegrity(ctx, client, chapters[1].ID, UpdateChapterRequest{Status: &requestedDraft}); !errors.Is(err, errGenerationEarlierChapterStale) {
		t.Fatalf("stale status bypass error = %v", err)
	}
	secondStillStale, err := client.Chapter.Get(ctx, chapters[1].ID)
	if err != nil || secondStillStale.Status != string(domain.StatusStale) {
		t.Fatalf("stale status bypass changed chapter: %#v, error=%v", secondStillStale, err)
	}

	newTitle := "重命名第二章"
	if _, err := updateChapterWithIntegrity(ctx, client, chapters[1].ID, UpdateChapterRequest{Title: &newTitle}); err != nil {
		t.Fatal(err)
	}
	third, err := client.Chapter.Get(ctx, chapters[2].ID)
	if err != nil || third.Status != string(domain.StatusStale) {
		t.Fatalf("title-only update changed stale chain: %#v, error=%v", third, err)
	}
}

func TestChapterTitleOnlyUpdateDoesNotMarkFollowingStale(t *testing.T) {
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
	_, chapters := createStaleTestNovel(t, ctx, client, "title-only", 1, 2)
	newTitle := "仅修改标题"
	if _, err := updateChapterWithIntegrity(ctx, client, chapters[1].ID, UpdateChapterRequest{Title: &newTitle}); err != nil {
		t.Fatal(err)
	}
	second, err := client.Chapter.Get(ctx, chapters[2].ID)
	if err != nil || second.Status != string(domain.StatusDraft) {
		t.Fatalf("title-only status = %#v, error=%v", second, err)
	}
}

func TestGenerationRewriteMarksFollowingStale(t *testing.T) {
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
	novelRow, chapters := createStaleTestNovel(t, ctx, client, "generation-rewrite", 1, 2)
	novelCurrent, err := client.Novel.Get(ctx, novelRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := generationChapterTargetFromRow(chapters[1])
	target.NovelID = novelRow.ID
	target.NovelUpdatedAt = novelCurrent.UpdatedAt
	state := &agents.GenerationState{
		NovelID: fmt.Sprintf("%d", novelRow.ID), ChapterID: fmt.Sprintf("%d", chapters[1].ID), ChapterIndex: 1,
		Draft: validGeneratedContent(), Continuity: agents.ContinuityPacket{LastBeat: "文", OpenLoops: []string{}, NextAction: "文"}, IsApproved: true,
	}
	if _, err := (&entGenerationChapterStore{client: client}).Save(ctx, target, state); err != nil {
		t.Fatal(err)
	}
	second, err := client.Chapter.Get(ctx, chapters[2].ID)
	if err != nil || second.Status != string(domain.StatusStale) {
		t.Fatalf("generation rewrite status = %#v, error=%v", second, err)
	}
}

func TestChapterOrderMoveCannotSkipAnyLaterChapter(t *testing.T) {
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
	_, chapters := createStaleTestNovel(t, ctx, client, "order-move", 1, 2, 4)
	newOrder := 5
	if _, err := updateChapterWithIntegrity(ctx, client, chapters[2].ID, UpdateChapterRequest{Order: &newOrder}); !errors.Is(err, errChapterHasSuccessor) {
		t.Fatalf("move error = %v", err)
	}
	fourth, err := client.Chapter.Get(ctx, chapters[4].ID)
	if err != nil || fourth.Status != string(domain.StatusDraft) || fourth.Order != 4 {
		t.Fatalf("rejected move changed later chapter: %#v, error=%v", fourth, err)
	}
	original, err := client.Chapter.Get(ctx, chapters[2].ID)
	if err != nil || original.Status != string(domain.StatusDraft) || original.Order != 2 {
		t.Fatalf("rejected move changed target: %#v, error=%v", original, err)
	}
}

func TestGenerationPrepareRequiresEarliestStaleChapter(t *testing.T) {
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
	novelRow, chapters := createStaleTestNovel(t, ctx, client, "stale-order", 1, 2, 3)
	if _, err := client.Chapter.Update().Where(chapter.IDIn(chapters[2].ID, chapters[3].ID)).SetStatus(string(domain.StatusStale)).Save(ctx); err != nil {
		t.Fatal(err)
	}
	store := &entGenerationChapterStore{client: client}
	if _, err := store.Prepare(ctx, novelRow.ID, chapters[3].ID, 3); !errors.Is(err, errGenerationEarlierChapterStale) {
		t.Fatalf("skip stale error = %v", err)
	}
	second, err := store.Prepare(ctx, novelRow.ID, chapters[2].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Order != 2 {
		t.Fatalf("prepared target = %#v", second)
	}
	if _, err := client.Chapter.UpdateOneID(chapters[2].ID).
		SetStatus(string(domain.StatusDraft)).SetLastBeat("新接力").SetOpenLoops([]string{}).SetNextAction("继续").Save(ctx); err != nil {
		t.Fatal(err)
	}
	third, err := store.Prepare(ctx, novelRow.ID, chapters[3].ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if third.PreviousContinuity.LastBeat != "新接力" || third.PreviousContinuity.NextAction != "继续" {
		t.Fatalf("third continuity = %#v", third.PreviousContinuity)
	}
}

func TestAllowedChapterMoveInvalidatesMovedChapter(t *testing.T) {
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
	_, chapters := createStaleTestNovel(t, ctx, client, "allowed-move", 1, 3)
	newOrder := 2
	requestedDraft := string(domain.StatusDraft)
	moved, err := updateChapterWithIntegrity(ctx, client, chapters[3].ID, UpdateChapterRequest{Order: &newOrder, Status: &requestedDraft})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Order != 2 || moved.Status != string(domain.StatusStale) || moved.LastBeat != "" || moved.NextAction != "" || len(moved.OpenLoops) != 0 {
		t.Fatalf("moved chapter = %#v", moved)
	}
}
