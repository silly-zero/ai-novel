package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/character"
	"github.com/ai-novel/studio/ent/characterstateversion"
	"github.com/ai-novel/studio/ent/worldsetting"
	"github.com/ai-novel/studio/ent/worldstateversion"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	_ "github.com/lib/pq"
)

func TestReplaceChapterStatesRebuildsStaticDataWithoutValidHistory(t *testing.T) {
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
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("static-rebuild-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	novelID := fmt.Sprintf("%d", novelRow.ID)
	chapterRow, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("正文").SetWordCount(2).SetOrder(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	characterRow, err := client.Character.Create().SetNovelID(novelID).SetName("林云").SetGender("男").SetBackground("旧背景").SetStateVersioned(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worldRow, err := client.WorldSetting.Create().SetNovelID(novelID).SetName("青云山").SetCategory("地理").SetDescription("旧说明").SetStateVersioned(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CharacterStateVersion.Create().SetCharacterID(characterRow.ID).SetChapterID(chapterRow.ID).SetChapterIndex(1).SetGenerationID("old").SetCurrentStatus("旧状态").SetValid(false).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WorldStateVersion.Create().SetWorldSettingID(worldRow.ID).SetChapterID(chapterRow.ID).SetChapterIndex(1).SetGenerationID("old").SetCurrentState("旧状态").SetValid(false).Save(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.CharacterStateVersion.Delete().Where(characterstateversion.CharacterID(characterRow.ID)).Exec(cleanupCtx)
		_, _ = client.WorldStateVersion.Delete().Where(worldstateversion.WorldSettingID(worldRow.ID)).Exec(cleanupCtx)
		_, _ = client.Character.Delete().Where(character.NovelID(novelID)).Exec(cleanupCtx)
		_, _ = client.WorldSetting.Delete().Where(worldsetting.NovelID(novelID)).Exec(cleanupCtx)
		_ = client.Chapter.DeleteOneID(chapterRow.ID).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx)
	})
	ref := domain.ChapterStateRef{NovelID: novelID, ChapterID: fmt.Sprintf("%d", chapterRow.ID), ChapterIndex: 1, GenerationID: "new"}
	if _, err := NewCharacterRepository(client).ReplaceChapterCharacters(ctx, ref, []*domain.Character{{Name: "林云", Gender: "女", Background: "新背景", CurrentStatus: "新状态"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorldRepository(client).ReplaceChapterWorldSettings(ctx, ref, []*domain.WorldSetting{{Name: "青云山", Category: "势力", Description: "新说明", CurrentState: "新状态"}}); err != nil {
		t.Fatal(err)
	}
	characterRow, _ = client.Character.Get(ctx, characterRow.ID)
	worldRow, _ = client.WorldSetting.Get(ctx, worldRow.ID)
	if characterRow.Gender != "女" || characterRow.Background != "新背景" || worldRow.Category != "势力" || worldRow.Description != "新说明" {
		t.Fatalf("static data not rebuilt: character=%#v world=%#v", characterRow, worldRow)
	}
}
