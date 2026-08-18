package api

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/character"
	"github.com/ai-novel/studio/ent/characterstateversion"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/ent/worldsetting"
	"github.com/ai-novel/studio/ent/worldstateversion"
	_ "github.com/lib/pq"
)

func TestDeleteChapterWithIntegrityCascadesStateVersionsAndRefreshesCaches(t *testing.T) {
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
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("delete-state-test-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	novelID := fmt.Sprintf("%d", novelRow.ID)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		steps := []func() error{
			func() error {
				_, err := client.CharacterStateVersion.Delete().Where(
					characterstateversion.HasCharacterWith(character.NovelID(novelID)),
				).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.WorldStateVersion.Delete().Where(
					worldstateversion.HasWorldSettingWith(worldsetting.NovelID(novelID)),
				).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.Character.Delete().Where(character.NovelID(novelID)).Exec(cleanupCtx)
				return err
			},
			func() error {
				_, err := client.WorldSetting.Delete().Where(worldsetting.NovelID(novelID)).Exec(cleanupCtx)
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
				t.Errorf("cleanup state version integration data: %v", err)
				return
			}
		}
	})
	chapter1, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("正文").SetWordCount(2).SetOrder(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chapter2, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第二章").SetContent("正文").SetWordCount(2).SetOrder(3).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	character, err := client.Character.Create().SetNovelID(novelID).SetName("林云").SetCurrentStatus("第二章状态").SetStateVersioned(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	world, err := client.WorldSetting.Create().SetNovelID(novelID).SetCategory("地理").SetName("青云山").SetDescription("宗门").SetCurrentState("第二章状态").SetStateVersioned(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []struct {
		chapterID int
		index     int
		state     string
	}{
		{chapterID: chapter1.ID, index: 1, state: "第一章状态"},
		{chapterID: chapter2.ID, index: 3, state: "第二章状态"},
	} {
		if _, err := client.CharacterStateVersion.Create().SetCharacterID(character.ID).SetChapterID(version.chapterID).SetChapterIndex(version.index).SetGenerationID("generation").SetCurrentStatus(version.state).Save(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := client.WorldStateVersion.Create().SetWorldSettingID(world.ID).SetChapterID(version.chapterID).SetChapterIndex(version.index).SetGenerationID("generation").SetCurrentState(version.state).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}

	newOrder := 2
	if _, err := updateChapterWithIntegrity(ctx, client, chapter2.ID, UpdateChapterRequest{Order: &newOrder}); err != nil {
		t.Fatal(err)
	}
	characterVersion, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.ChapterID(chapter2.ID),
	).Only(ctx)
	if err != nil || characterVersion.ChapterIndex != newOrder {
		t.Fatalf("moved character version = %#v, error=%v", characterVersion, err)
	}
	worldVersion, err := client.WorldStateVersion.Query().Where(
		worldstateversion.ChapterID(chapter2.ID),
	).Only(ctx)
	if err != nil || worldVersion.ChapterIndex != newOrder {
		t.Fatalf("moved world version = %#v, error=%v", worldVersion, err)
	}

	if err := deleteChapterWithIntegrity(ctx, client, chapter2.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := client.CharacterStateVersion.Query().Where(characterstateversion.ChapterID(chapter2.ID)).Count(ctx); err != nil || count != 0 {
		t.Fatalf("character versions after delete = %d, error=%v", count, err)
	}
	if count, err := client.WorldStateVersion.Query().Where(worldstateversion.ChapterID(chapter2.ID)).Count(ctx); err != nil || count != 0 {
		t.Fatalf("world versions after delete = %d, error=%v", count, err)
	}
	character, err = client.Character.Get(ctx, character.ID)
	if err != nil {
		t.Fatal(err)
	}
	world, err = client.WorldSetting.Get(ctx, world.ID)
	if err != nil {
		t.Fatal(err)
	}
	if character.CurrentStatus != "第一章状态" || world.CurrentState != "第一章状态" {
		t.Fatalf("caches after delete: character=%q world=%q", character.CurrentStatus, world.CurrentState)
	}
	if !character.StateVersioned || !world.StateVersioned {
		t.Fatalf("versioned markers after delete: character=%v world=%v", character.StateVersioned, world.StateVersioned)
	}
	if err := deleteChapterWithIntegrity(ctx, client, chapter1.ID); err != nil {
		t.Fatal(err)
	}
	character, _ = client.Character.Get(ctx, character.ID)
	world, _ = client.WorldSetting.Get(ctx, world.ID)
	if character.CurrentStatus != "" || world.CurrentState != "" || !character.StateVersioned || !world.StateVersioned {
		t.Fatalf("last version delete: character=%#v world=%#v", character, world)
	}
}
