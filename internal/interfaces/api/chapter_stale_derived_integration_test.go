package api

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/characterstateversion"
	"github.com/ai-novel/studio/ent/memoryentry"
	"github.com/ai-novel/studio/ent/relationshipstateversion"
	"github.com/ai-novel/studio/ent/worldstateversion"
	databaseinfra "github.com/ai-novel/studio/internal/infrastructure/database"
	_ "github.com/lib/pq"
)

func TestChapterRewriteInvalidatesDerivedData(t *testing.T) {
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
	novelRow, chapters := createStaleTestNovel(t, ctx, client, fmt.Sprintf("derived-%d", time.Now().UnixNano()), 1, 2)
	novelID := fmt.Sprintf("%d", novelRow.ID)
	character, err := client.Character.Create().SetNovelID(novelID).SetName("林云").SetStateVersioned(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	world, err := client.WorldSetting.Create().SetNovelID(novelID).SetCategory("地理").SetName("青云山").SetDescription("宗门").SetStateVersioned(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, chapterRow := range chapters {
		if _, err := client.CharacterStateVersion.Create().SetCharacterID(character.ID).SetChapterID(chapterRow.ID).SetChapterIndex(chapterRow.Order).SetGenerationID("g").SetCurrentStatus("状态").Save(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := client.WorldStateVersion.Create().SetWorldSettingID(world.ID).SetChapterID(chapterRow.ID).SetChapterIndex(chapterRow.Order).SetGenerationID("g").SetCurrentState("状态").Save(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := client.RelationshipStateVersion.Create().SetChapterID(chapterRow.ID).SetSourceCharacterID(character.ID).SetTargetCharacterID(character.ID).SetChapterIndex(chapterRow.Order).SetGenerationID("g").SetRelationType("自省").SetActive(true).SetOperation(relationshipstateversion.OperationUpsert).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.MemoryEntry.Create().SetNovelID(novelID).SetContent("第二章摘要").SetEmbedding([]float32{1}).SetMetadata(map[string]any{
		"chapter_id": fmt.Sprintf("%d", chapters[2].ID), "chapter_index": 2, "chapter_status": "Draft",
	}).Save(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.MemoryEntry.Delete().Where(memoryentry.NovelID(novelID)).Exec(cleanupCtx)
		_, _ = client.RelationshipStateVersion.Delete().Where(relationshipstateversion.SourceCharacterID(character.ID)).Exec(cleanupCtx)
		_, _ = client.CharacterStateVersion.Delete().Where(characterstateversion.CharacterID(character.ID)).Exec(cleanupCtx)
		_, _ = client.WorldStateVersion.Delete().Where(worldstateversion.WorldSettingID(world.ID)).Exec(cleanupCtx)
		_ = client.Character.DeleteOneID(character.ID).Exec(cleanupCtx)
		_ = client.WorldSetting.DeleteOneID(world.ID).Exec(cleanupCtx)
	})

	newContent := "新正文"
	if _, err := updateChapterWithIntegrity(ctx, client, chapters[1].ID, UpdateChapterRequest{Content: &newContent}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []struct {
		name  string
		count func() (int, error)
	}{
		{name: "character", count: func() (int, error) {
			return client.CharacterStateVersion.Query().Where(
				characterstateversion.CharacterID(character.ID), characterstateversion.Valid(true),
			).Count(ctx)
		}},
		{name: "world", count: func() (int, error) {
			return client.WorldStateVersion.Query().Where(
				worldstateversion.WorldSettingID(world.ID), worldstateversion.Valid(true),
			).Count(ctx)
		}},
		{name: "relationship", count: func() (int, error) {
			return client.RelationshipStateVersion.Query().Where(
				relationshipstateversion.SourceCharacterID(character.ID), relationshipstateversion.Valid(true),
			).Count(ctx)
		}},
	} {
		count, err := query.count()
		if err != nil || count != 0 {
			t.Fatalf("valid %s versions = %d, error=%v", query.name, count, err)
		}
	}
	memoryRow, err := client.MemoryEntry.Query().Where(memoryentry.NovelID(novelID)).Only(ctx)
	if err != nil || memoryRow.Metadata["chapter_status"] != "Stale" {
		t.Fatalf("memory metadata = %#v, error=%v", memoryRow.Metadata, err)
	}
	characters, err := databaseinfra.NewCharacterRepository(client).ListCharactersBeforeChapter(ctx, novelID, 3)
	if err != nil || len(characters) != 0 {
		t.Fatalf("characters after invalidation = %#v, error=%v", characters, err)
	}
	characterRow, err := client.Character.Get(ctx, character.ID)
	if err != nil || characterRow.CurrentStatus != "" {
		t.Fatalf("character cache after invalidation = %#v, error=%v", characterRow, err)
	}
	worlds, err := databaseinfra.NewWorldRepository(client).ListWorldSettingsBeforeChapter(ctx, novelID, 3)
	if err != nil || len(worlds) != 0 {
		t.Fatalf("worlds after invalidation = %#v, error=%v", worlds, err)
	}
	worldRow, err := client.WorldSetting.Get(ctx, world.ID)
	if err != nil || worldRow.CurrentState != "" {
		t.Fatalf("world cache after invalidation = %#v, error=%v", worldRow, err)
	}
	relationships, err := databaseinfra.NewCharacterRepository(client).ListRelationshipsBeforeChapter(ctx, novelID, 3)
	if err != nil || len(relationships) != 0 {
		t.Fatalf("relationships after invalidation = %#v, error=%v", relationships, err)
	}
}
