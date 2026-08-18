package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	characterpredicate "github.com/ai-novel/studio/ent/character"
	"github.com/ai-novel/studio/ent/characterstateversion"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/ent/worldsetting"
	"github.com/ai-novel/studio/ent/worldstateversion"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	_ "github.com/lib/pq"
)

func TestStateRepositoriesProjectLatestStateBeforeChapter(t *testing.T) {
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
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("state-test-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	novelID := fmt.Sprintf("%d", novelRow.ID)
	novelIDs := []int{novelRow.ID}
	novelStringIDs := []string{novelID}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.CharacterStateVersion.Delete().Where(
			characterstateversion.HasCharacterWith(characterpredicate.NovelIDIn(novelStringIDs...)),
		).Exec(cleanupCtx)
		_, _ = client.WorldStateVersion.Delete().Where(
			worldstateversion.HasWorldSettingWith(worldsetting.NovelIDIn(novelStringIDs...)),
		).Exec(cleanupCtx)
		_, _ = client.Character.Delete().Where(characterpredicate.NovelIDIn(novelStringIDs...)).Exec(cleanupCtx)
		_, _ = client.WorldSetting.Delete().Where(worldsetting.NovelIDIn(novelStringIDs...)).Exec(cleanupCtx)
		_, _ = client.Chapter.Delete().Where(chapter.HasNovelWith(novel.IDIn(novelIDs...))).Exec(cleanupCtx)
		_, _ = client.Novel.Delete().Where(novel.IDIn(novelIDs...)).Exec(cleanupCtx)
	})
	otherNovelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("state-test-other-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherNovelID := fmt.Sprintf("%d", otherNovelRow.ID)
	novelIDs = append(novelIDs, otherNovelRow.ID)
	novelStringIDs = append(novelStringIDs, otherNovelID)
	chapterIDs := make(map[int]int)
	for _, order := range []int{1, 2, 3, 5, 7, 8, 9} {
		row, err := client.Chapter.Create().
			SetNovel(novelRow).
			SetTitle(fmt.Sprintf("第%d章", order)).
			SetContent("正文").
			SetWordCount(2).
			SetOrder(order).
			Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		chapterIDs[order] = row.ID
	}
	otherChapter, err := client.Chapter.Create().
		SetNovel(otherNovelRow).
		SetTitle("其他小说章节").
		SetContent("正文").
		SetWordCount(2).
		SetOrder(3).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	character, err := client.Character.Create().
		SetNovelID(novelID).
		SetName("林云").
		SetCurrentStatus("主表未来状态").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	world, err := client.WorldSetting.Create().
		SetNovelID(novelID).
		SetCategory("地理").
		SetName("青云山").
		SetDescription("宗门所在").
		SetCurrentState("主表未来状态").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []struct {
		chapterIndex int
		status       string
		state        string
	}{
		{chapterIndex: 1, status: "第一章角色状态", state: "第一章世界状态"},
		{chapterIndex: 3, status: "第三章角色状态", state: "第三章世界状态"},
		{chapterIndex: 5, status: "第五章角色状态", state: "第五章世界状态"},
		{chapterIndex: 7, status: "第七章角色状态", state: "第七章世界状态"},
	} {
		if _, err := client.CharacterStateVersion.Create().
			SetChapterID(chapterIDs[version.chapterIndex]).
			SetChapterIndex(version.chapterIndex).
			SetGenerationID("generation").
			SetCurrentStatus(version.status).
			SetCharacter(character).
			Save(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := client.WorldStateVersion.Create().
			SetChapterID(chapterIDs[version.chapterIndex]).
			SetChapterIndex(version.chapterIndex).
			SetGenerationID("generation").
			SetCurrentState(version.state).
			SetWorldSetting(world).
			Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	secondCharacter, err := client.Character.Create().
		SetNovelID(novelID).
		SetName("苏青").
		SetCurrentStatus("主表未来状态").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondWorld, err := client.WorldSetting.Create().
		SetNovelID(novelID).
		SetCategory("势力").
		SetName("青云门").
		SetDescription("宗门势力").
		SetCurrentState("主表未来状态").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CharacterStateVersion.Create().
		SetChapterID(chapterIDs[2]).
		SetChapterIndex(2).
		SetGenerationID("generation").
		SetCurrentStatus("第二章苏青状态").
		SetCharacter(secondCharacter).
		Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WorldStateVersion.Create().
		SetChapterID(chapterIDs[2]).
		SetChapterIndex(2).
		SetGenerationID("generation").
		SetCurrentState("第二章青云门状态").
		SetWorldSetting(secondWorld).
		Save(ctx); err != nil {
		t.Fatal(err)
	}
	noVersionCharacter, err := client.Character.Create().
		SetNovelID(novelID).
		SetName("赵峥").
		SetCurrentStatus("无历史的主表未来状态").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = noVersionCharacter
	noVersionWorld, err := client.WorldSetting.Create().
		SetNovelID(novelID).
		SetCategory("宝物").
		SetName("血书").
		SetDescription("重要道具").
		SetCurrentState("无历史的主表未来状态").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = noVersionWorld
	otherCharacter, err := client.Character.Create().SetNovelID(otherNovelID).SetName("他人角色").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CharacterStateVersion.Create().
		SetChapterID(otherChapter.ID).
		SetChapterIndex(4).
		SetGenerationID("other").
		SetCurrentStatus("他人小说状态").
		SetCharacter(otherCharacter).
		Save(ctx); err != nil {
		t.Fatal(err)
	}
	otherWorld, err := client.WorldSetting.Create().
		SetNovelID(otherNovelID).
		SetCategory("地理").
		SetName("他人地点").
		SetDescription("其他小说设定").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WorldStateVersion.Create().
		SetChapterID(otherChapter.ID).
		SetChapterIndex(4).
		SetGenerationID("other").
		SetCurrentState("他人小说世界状态").
		SetWorldSetting(otherWorld).
		Save(ctx); err != nil {
		t.Fatal(err)
	}

	characters, err := NewCharacterRepository(client).ListCharactersBeforeChapter(ctx, novelID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(characters) != 3 ||
		characters[0].Name != "林云" || characters[0].CurrentStatus != "第三章角色状态" ||
		characters[1].Name != "苏青" || characters[1].CurrentStatus != "第二章苏青状态" ||
		characters[2].Name != "赵峥" || characters[2].CurrentStatus != "" {
		t.Fatalf(
			"characters = [%s:%s, %s:%s, %s:%s]",
			characters[0].Name,
			characters[0].CurrentStatus,
			characters[1].Name,
			characters[1].CurrentStatus,
			characters[2].Name,
			characters[2].CurrentStatus,
		)
	}
	settings, err := NewWorldRepository(client).ListWorldSettingsBeforeChapter(ctx, novelID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != 3 ||
		settings[0].Name != "血书" || settings[0].CurrentState != "" ||
		settings[1].Name != "青云山" || settings[1].CurrentState != "第三章世界状态" ||
		settings[2].Name != "青云门" || settings[2].CurrentState != "第二章青云门状态" {
		t.Fatalf(
			"settings = [%s:%s, %s:%s, %s:%s]",
			settings[0].Name,
			settings[0].CurrentState,
			settings[1].Name,
			settings[1].CurrentState,
			settings[2].Name,
			settings[2].CurrentState,
		)
	}
	firstCharacters, err := NewCharacterRepository(client).ListCharactersBeforeChapter(ctx, novelID, 1)
	if err != nil {
		t.Fatal(err)
	}
	firstSettings, err := NewWorldRepository(client).ListWorldSettingsBeforeChapter(ctx, novelID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstCharacters) != 1 || firstCharacters[0].Name != "赵峥" || firstCharacters[0].CurrentStatus != "" ||
		len(firstSettings) != 1 || firstSettings[0].Name != "血书" || firstSettings[0].CurrentState != "" {
		t.Fatalf("first chapter leaked main table state: characters=%#v settings=%#v", firstCharacters, firstSettings)
	}
	if _, err := client.CharacterStateVersion.Create().
		SetChapterID(chapterIDs[3]).
		SetChapterIndex(4).
		SetGenerationID("duplicate").
		SetCurrentStatus("重复版本").
		SetCharacter(character).
		Save(ctx); err == nil {
		t.Fatal("duplicate character/chapter version was accepted")
	}
	if _, err := client.WorldStateVersion.Create().
		SetChapterID(chapterIDs[3]).
		SetChapterIndex(4).
		SetGenerationID("duplicate").
		SetCurrentState("重复版本").
		SetWorldSetting(world).
		Save(ctx); err == nil {
		t.Fatal("duplicate world/chapter version was accepted")
	}

	characterRepo := NewCharacterRepository(client)
	worldRepo := NewWorldRepository(client)
	if _, err := characterRepo.ReplaceChapterCharacters(ctx, domain.ChapterStateRef{
		NovelID:      novelID,
		ChapterID:    fmt.Sprintf("%d", otherChapter.ID),
		ChapterIndex: 3,
		GenerationID: "wrong-novel",
	}, nil); err == nil {
		t.Fatal("cross-novel chapter was accepted for character states")
	}
	if _, err := worldRepo.ReplaceChapterWorldSettings(ctx, domain.ChapterStateRef{
		NovelID:      novelID,
		ChapterID:    fmt.Sprintf("%d", chapterIDs[3]),
		ChapterIndex: 4,
		GenerationID: "wrong-order",
	}, nil); err == nil {
		t.Fatal("mismatched chapter order was accepted for world states")
	}

	chapter3 := domain.ChapterStateRef{
		NovelID:      "  " + novelID + "  ",
		ChapterID:    fmt.Sprintf("  %d  ", chapterIDs[3]),
		ChapterIndex: 3,
		GenerationID: "  rewrite-3  ",
	}
	canonicalCharacters, err := characterRepo.ReplaceChapterCharacters(ctx, chapter3, []*domain.Character{{
		Name:          "林云",
		Gender:        "男",
		CurrentStatus: "第三章重写状态",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(canonicalCharacters) != 1 || canonicalCharacters[0].ID == "" || canonicalCharacters[0].Gender != "男" || canonicalCharacters[0].CurrentStatus != "第三章重写状态" {
		t.Fatalf("canonical characters = %#v", canonicalCharacters)
	}
	canonicalWorld, err := worldRepo.ReplaceChapterWorldSettings(ctx, chapter3, []*domain.WorldSetting{{
		Name:         "青云山",
		Category:     "新分类不应覆盖",
		Description:  "新说明不应覆盖",
		CurrentState: "第三章重写状态",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(canonicalWorld) != 1 || canonicalWorld[0].ID == "" || canonicalWorld[0].Category != "地理" || canonicalWorld[0].Description != "宗门所在" || canonicalWorld[0].CurrentState != "第三章重写状态" {
		t.Fatalf("canonical world settings = %#v", canonicalWorld)
	}
	lin, err := characterRepo.FindByName(ctx, novelID, "林云")
	if err != nil {
		t.Fatal(err)
	}
	mountain, err := worldRepo.FindByName(ctx, novelID, "青云山")
	if err != nil {
		t.Fatal(err)
	}
	if lin.CurrentStatus != "第七章角色状态" || lin.Gender != "男" {
		t.Fatalf("old rewrite changed latest character cache: %#v", lin)
	}
	if mountain.CurrentState != "第七章世界状态" || mountain.Category != "地理" || mountain.Description != "宗门所在" {
		t.Fatalf("old rewrite changed latest world cache/static data: %#v", mountain)
	}
	characterVersions, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.CharacterID(character.ID),
		characterstateversion.ChapterID(chapterIDs[3]),
	).All(ctx)
	if err != nil || len(characterVersions) != 1 || characterVersions[0].CurrentStatus != "第三章重写状态" {
		t.Fatalf("character chapter replacement = %#v, error=%v", characterVersions, err)
	}
	worldVersions, err := client.WorldStateVersion.Query().Where(
		worldstateversion.WorldSettingID(world.ID),
		worldstateversion.ChapterID(chapterIDs[3]),
	).All(ctx)
	if err != nil || len(worldVersions) != 1 || worldVersions[0].CurrentState != "第三章重写状态" {
		t.Fatalf("world chapter replacement = %#v, error=%v", worldVersions, err)
	}

	chapter7 := domain.ChapterStateRef{
		NovelID:      novelID,
		ChapterID:    fmt.Sprintf("%d", chapterIDs[7]),
		ChapterIndex: 7,
		GenerationID: "rewrite-7",
	}
	if _, err := characterRepo.ReplaceChapterCharacters(ctx, chapter7, []*domain.Character{{
		Name:          "林云",
		Gender:        "女",
		CurrentStatus: "第七章重写状态",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := worldRepo.ReplaceChapterWorldSettings(ctx, chapter7, []*domain.WorldSetting{{
		Name:         "青云山",
		Category:     "新分类不应覆盖",
		Description:  "新说明不应覆盖",
		CurrentState: "第七章重写状态",
	}}); err != nil {
		t.Fatal(err)
	}
	lin, _ = characterRepo.FindByName(ctx, novelID, "林云")
	mountain, _ = worldRepo.FindByName(ctx, novelID, "青云山")
	if lin.CurrentStatus != "第七章重写状态" || lin.Gender != "男" || mountain.CurrentState != "第七章重写状态" {
		t.Fatalf("latest rewrite cache/static mismatch: character=%#v world=%#v", lin, mountain)
	}

	chapter2 := domain.ChapterStateRef{
		NovelID:      novelID,
		ChapterID:    fmt.Sprintf("%d", chapterIDs[2]),
		ChapterIndex: 2,
		GenerationID: "rewrite-2",
	}
	if _, err := characterRepo.ReplaceChapterCharacters(ctx, chapter2, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := worldRepo.ReplaceChapterWorldSettings(ctx, chapter2, nil); err != nil {
		t.Fatal(err)
	}
	su, _ := characterRepo.FindByName(ctx, novelID, "苏青")
	sect, _ := worldRepo.FindByName(ctx, novelID, "青云门")
	if su.CurrentStatus != "" || sect.CurrentState != "" {
		t.Fatalf("omitted chapter states remained: character=%#v world=%#v", su, sect)
	}

	chapter8 := domain.ChapterStateRef{
		NovelID:      novelID,
		ChapterID:    fmt.Sprintf("%d", chapterIDs[8]),
		ChapterIndex: 8,
		GenerationID: "original-8",
	}
	if _, err := characterRepo.ReplaceChapterCharacters(ctx, chapter8, []*domain.Character{{
		Name:          "林云",
		CurrentStatus: "第八章原状态",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := worldRepo.ReplaceChapterWorldSettings(ctx, chapter8, []*domain.WorldSetting{{
		Name:         "青云山",
		CurrentState: "第八章原状态",
		Metadata:     map[string]any{"source": "chapter-8"},
	}}); err != nil {
		t.Fatal(err)
	}
	mountain, _ = worldRepo.FindByName(ctx, novelID, "青云山")
	if mountain.Metadata["source"] != "chapter-8" {
		t.Fatalf("empty world metadata was not supplemented: %#v", mountain.Metadata)
	}
	chapter8.GenerationID = "rollback-8"
	if _, err := characterRepo.ReplaceChapterCharacters(ctx, chapter8, []*domain.Character{
		{Name: "林云", CurrentStatus: "不应提交"},
		{Name: "坏角色", CurrentStatus: "\xff"},
	}); err == nil {
		t.Fatal("invalid character batch was committed")
	}
	if _, err := worldRepo.ReplaceChapterWorldSettings(ctx, chapter8, []*domain.WorldSetting{
		{Name: "青云山", CurrentState: "不应提交"},
		{Name: "坏设定", CurrentState: "\xff"},
	}); err == nil {
		t.Fatal("invalid world batch was committed")
	}
	lin, _ = characterRepo.FindByName(ctx, novelID, "林云")
	mountain, _ = worldRepo.FindByName(ctx, novelID, "青云山")
	if lin.CurrentStatus != "第八章原状态" || mountain.CurrentState != "第八章原状态" {
		t.Fatalf("failed batch changed caches: character=%#v world=%#v", lin, mountain)
	}
	if _, err := characterRepo.FindByName(ctx, novelID, "坏角色"); !ent.IsNotFound(err) {
		t.Fatalf("failed character batch left entity: %v", err)
	}
	if _, err := worldRepo.FindByName(ctx, novelID, "坏设定"); !ent.IsNotFound(err) {
		t.Fatalf("failed world batch left entity: %v", err)
	}
	failedCharacterVersions, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.ChapterID(chapterIDs[8]),
		characterstateversion.HasCharacterWith(characterpredicate.NovelID(novelID)),
	).Count(ctx)
	if err != nil || failedCharacterVersions != 1 {
		t.Fatalf("chapter 8 character versions = %d, error=%v", failedCharacterVersions, err)
	}
	failedCharacterVersion, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.ChapterID(chapterIDs[8]),
		characterstateversion.CharacterID(character.ID),
	).Only(ctx)
	if err != nil || failedCharacterVersion.CurrentStatus != "第八章原状态" {
		t.Fatalf("chapter 8 character state = %#v, error=%v", failedCharacterVersion, err)
	}
	failedWorldVersions, err := client.WorldStateVersion.Query().Where(
		worldstateversion.ChapterID(chapterIDs[8]),
		worldstateversion.HasWorldSettingWith(worldsetting.NovelID(novelID)),
	).Count(ctx)
	if err != nil || failedWorldVersions != 1 {
		t.Fatalf("chapter 8 world versions = %d, error=%v", failedWorldVersions, err)
	}
	failedWorldVersion, err := client.WorldStateVersion.Query().Where(
		worldstateversion.ChapterID(chapterIDs[8]),
		worldstateversion.WorldSettingID(world.ID),
	).Only(ctx)
	if err != nil || failedWorldVersion.CurrentState != "第八章原状态" {
		t.Fatalf("chapter 8 world state = %#v, error=%v", failedWorldVersion, err)
	}
	chapter7Character, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.CharacterID(character.ID),
		characterstateversion.ChapterID(chapterIDs[7]),
	).Only(ctx)
	if err != nil || chapter7Character.CurrentStatus != "第七章重写状态" {
		t.Fatalf("chapter 7 character state = %#v, error=%v", chapter7Character, err)
	}
	chapter7World, err := client.WorldStateVersion.Query().Where(
		worldstateversion.WorldSettingID(world.ID),
		worldstateversion.ChapterID(chapterIDs[7]),
	).Only(ctx)
	if err != nil || chapter7World.CurrentState != "第七章重写状态" {
		t.Fatalf("chapter 7 world state = %#v, error=%v", chapter7World, err)
	}
	chapter9 := domain.ChapterStateRef{
		NovelID:      novelID,
		ChapterID:    fmt.Sprintf("%d", chapterIDs[9]),
		ChapterIndex: 9,
		GenerationID: "concurrent-9",
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := characterRepo.ReplaceChapterCharacters(ctx, chapter9, []*domain.Character{{Name: "林云", CurrentStatus: "并发状态A"}})
		errs <- err
	}()
	go func() {
		<-start
		_, err := characterRepo.ReplaceChapterCharacters(ctx, chapter9, []*domain.Character{{Name: "苏青", CurrentStatus: "并发状态B"}})
		errs <- err
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	concurrentVersions, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.ChapterID(chapterIDs[9]),
		characterstateversion.HasCharacterWith(characterpredicate.NovelID(novelID)),
	).All(ctx)
	if err != nil || len(concurrentVersions) != 1 {
		t.Fatalf("concurrent replacement left %#v, error=%v", concurrentVersions, err)
	}
}
