package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/character"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/ent/relationship"
	"github.com/ai-novel/studio/ent/relationshipstateversion"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	_ "github.com/lib/pq"
)

func TestRelationshipRepositoryVersionsRelationshipsByChapter(t *testing.T) {
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
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("relationship-test-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	novelID := fmt.Sprintf("%d", novelRow.ID)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		steps := []func() error{
			func() error {
				_, err := client.RelationshipStateVersion.Delete().Where(relationshipstateversion.HasSourceCharacterWith(character.NovelID(novelID))).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.Relationship.Delete().Where(relationship.NovelID(novelID)).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.Character.Delete().Where(character.NovelID(novelID)).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.Chapter.Delete().Where(chapter.HasNovelWith(novel.ID(novelRow.ID))).Exec(cleanupCtx)
				return err
			},
			func() error { return client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx) },
		}
		for _, step := range steps {
			if err := step(); err != nil {
				t.Errorf("cleanup relationship integration data: %v", err)
				return
			}
		}
	})
	chapterIDs := make(map[int]int)
	for _, order := range []int{1, 2, 3, 5} {
		row, err := client.Chapter.Create().SetNovel(novelRow).SetTitle(fmt.Sprintf("第%d章", order)).SetContent("正文").SetWordCount(2).SetOrder(order).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		chapterIDs[order] = row.ID
	}
	linRow, err := client.Character.Create().SetNovelID(novelID).SetName("林云").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	suRow, err := client.Character.Create().SetNovelID(novelID).SetName("苏青").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	zhaoRow, err := client.Character.Create().SetNovelID(novelID).SetName("赵峥").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lin := &domain.Character{ID: fmt.Sprintf("%d", linRow.ID), NovelID: novelID, Name: "林云"}
	su := &domain.Character{ID: fmt.Sprintf("%d", suRow.ID), NovelID: novelID, Name: "苏青"}
	zhao := &domain.Character{ID: fmt.Sprintf("%d", zhaoRow.ID), NovelID: novelID, Name: "赵峥"}
	repo := NewCharacterRepository(client)
	ref := func(order int, generation string) domain.ChapterStateRef {
		return domain.ChapterStateRef{NovelID: novelID, ChapterID: fmt.Sprintf("%d", chapterIDs[order]), ChapterIndex: order, GenerationID: generation}
	}

	otherNovel, err := client.Novel.Create().SetTitle(fmt.Sprintf("relationship-other-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherNovelID := fmt.Sprintf("%d", otherNovel.ID)
	otherChapter, err := client.Chapter.Create().SetNovel(otherNovel).SetTitle("其他章节").SetContent("正文").SetWordCount(2).SetOrder(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherCharacter, err := client.Character.Create().SetNovelID(otherNovelID).SetName("他人角色").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RelationshipStateVersion.Create().
		SetChapterID(otherChapter.ID).
		SetSourceCharacterID(otherCharacter.ID).
		SetTargetCharacterID(otherCharacter.ID).
		SetChapterIndex(1).
		SetGenerationID("other").
		SetRelationType("自省").
		SetDescription("其他小说关系").
		SetActive(true).
		SetOperation(relationshipstateversion.OperationUpsert).
		Save(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		steps := []func() error{
			func() error {
				_, err := client.RelationshipStateVersion.Delete().Where(
					relationshipstateversion.HasSourceCharacterWith(character.NovelID(otherNovelID)),
				).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.Character.Delete().Where(character.NovelID(otherNovelID)).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.Chapter.Delete().Where(chapter.HasNovelWith(novel.ID(otherNovel.ID))).Exec(cleanupCtx)
				return err
			},
			func() error { return client.Novel.DeleteOneID(otherNovel.ID).Exec(cleanupCtx) },
		}
		for _, step := range steps {
			if err := step(); err != nil {
				t.Errorf("cleanup other relationship integration data: %v", err)
				return
			}
		}
	})

	if _, err := repo.ReplaceChapterRelationships(ctx, ref(1, "g1"), []domain.RelationshipChange{{
		SourceCharacter: lin, TargetCharacter: su, RelationType: "盟友", Description: "共同调查", Operation: domain.RelationshipOperationUpsert,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReplaceChapterRelationships(ctx, ref(2, "g2"), nil); err != nil {
		t.Fatal(err)
	}
	before3, err := repo.ListRelationshipsBeforeChapter(ctx, novelID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(before3) != 1 || before3[0].RelationType != "盟友" || before3[0].Description != "共同调查" {
		t.Fatalf("before chapter 3 = %#v", before3)
	}
	if strings.Contains(fmt.Sprintf("%#v", before3), "其他小说关系") {
		t.Fatalf("cross-novel relationship leaked: %#v", before3)
	}

	if _, err := repo.ReplaceChapterRelationships(ctx, ref(3, "g3"), []domain.RelationshipChange{
		{SourceCharacter: lin, TargetCharacter: su, RelationType: "盟友", Operation: domain.RelationshipOperationRemove},
		{SourceCharacter: lin, TargetCharacter: su, RelationType: "敌人", Description: "本章决裂", Operation: domain.RelationshipOperationUpsert},
	}); err != nil {
		t.Fatal(err)
	}
	before3Again, err := repo.ListRelationshipsBeforeChapter(ctx, novelID, 3)
	if err != nil || len(before3Again) != 1 || before3Again[0].RelationType != "盟友" {
		t.Fatalf("same/future relationship leaked: %#v, error=%v", before3Again, err)
	}
	before5, err := repo.ListRelationshipsBeforeChapter(ctx, novelID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(before5) != 1 || before5[0].RelationType != "敌人" || before5[0].Description != "本章决裂" {
		t.Fatalf("before chapter 5 = %#v", before5)
	}
	if _, err := repo.ReplaceChapterRelationships(ctx, ref(5, "g5"), []domain.RelationshipChange{{
		SourceCharacter: lin, TargetCharacter: su, RelationType: "敌人", Description: "第五章继续敌对", Operation: domain.RelationshipOperationUpsert,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ReplaceChapterRelationships(ctx, ref(3, "rewrite-3"), nil); err != nil {
		t.Fatal(err)
	}
	rewritten, err := repo.ListRelationshipsBeforeChapter(ctx, novelID, 5)
	if err != nil || len(rewritten) != 1 || rewritten[0].RelationType != "盟友" {
		t.Fatalf("rewritten relationships before chapter 5 = %#v, error=%v", rewritten, err)
	}
	latestAfterRewrite, err := repo.ListRelationshipsBeforeChapter(ctx, novelID, 6)
	if err != nil {
		t.Fatal(err)
	}
	latestByType := make(map[string]string, len(latestAfterRewrite))
	for _, relationship := range latestAfterRewrite {
		latestByType[relationship.RelationType] = relationship.Description
	}
	if len(latestByType) != 2 || latestByType["盟友"] != "共同调查" || latestByType["敌人"] != "第五章继续敌对" {
		t.Fatalf("later relationship after early rewrite = %#v", latestAfterRewrite)
	}
	chapter2Versions, err := client.RelationshipStateVersion.Query().Where(
		relationshipstateversion.ChapterID(chapterIDs[2]),
	).Count(ctx)
	if err != nil || chapter2Versions != 0 {
		t.Fatalf("empty chapter wrote inherited copies: count=%d error=%v", chapter2Versions, err)
	}
	cached, err := repo.ListRelationships(ctx, novelID)
	if err != nil {
		t.Fatal(err)
	}
	cacheByType := make(map[string]string, len(cached))
	for _, relationship := range cached {
		cacheByType[relationship.RelationType] = relationship.Description
	}
	if len(cacheByType) != 2 || cacheByType["盟友"] != "共同调查" || cacheByType["敌人"] != "第五章继续敌对" {
		t.Fatalf("relationship cache after early rewrite = %#v", cached)
	}

	if _, err := repo.ReplaceChapterRelationships(ctx, ref(3, "rollback-base"), []domain.RelationshipChange{{
		SourceCharacter: lin, TargetCharacter: su, RelationType: "盟友", Description: "第三章原事件", Operation: domain.RelationshipOperationUpsert,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReplaceChapterRelationships(ctx, ref(3, "rollback-fail"), []domain.RelationshipChange{
		{SourceCharacter: lin, TargetCharacter: su, RelationType: "盟友", Description: "不应提交", Operation: domain.RelationshipOperationUpsert},
		{SourceCharacter: lin, TargetCharacter: su, RelationType: "敌人", Description: "\xff", Operation: domain.RelationshipOperationUpsert},
	}); err == nil {
		t.Fatal("invalid relationship batch was committed")
	}
	chapter3Rows, err := client.RelationshipStateVersion.Query().Where(
		relationshipstateversion.ChapterID(chapterIDs[3]),
	).All(ctx)
	if err != nil || len(chapter3Rows) != 1 || chapter3Rows[0].Description != "第三章原事件" {
		t.Fatalf("rollback chapter rows = %#v, error=%v", chapter3Rows, err)
	}
	cached, err = repo.ListRelationships(ctx, novelID)
	if err != nil {
		t.Fatal(err)
	}
	cacheByType = make(map[string]string, len(cached))
	for _, relationship := range cached {
		cacheByType[relationship.RelationType] = relationship.Description
	}
	if len(cacheByType) != 2 || cacheByType["盟友"] != "第三章原事件" || cacheByType["敌人"] != "第五章继续敌对" {
		t.Fatalf("rollback cache = %#v", cached)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := repo.ReplaceChapterRelationships(ctx, ref(2, "concurrent-a"), []domain.RelationshipChange{{
			SourceCharacter: lin, TargetCharacter: zhao, RelationType: "盟友", Description: "并发A", Operation: domain.RelationshipOperationUpsert,
		}})
		errs <- err
	}()
	go func() {
		<-start
		_, err := repo.ReplaceChapterRelationships(ctx, ref(2, "concurrent-b"), []domain.RelationshipChange{{
			SourceCharacter: su, TargetCharacter: zhao, RelationType: "盟友", Description: "并发B", Operation: domain.RelationshipOperationUpsert,
		}})
		errs <- err
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	concurrentRows, err := client.RelationshipStateVersion.Query().Where(
		relationshipstateversion.ChapterID(chapterIDs[2]),
	).All(ctx)
	if err != nil || len(concurrentRows) != 1 {
		t.Fatalf("concurrent chapter relationships = %#v, error=%v", concurrentRows, err)
	}
}
