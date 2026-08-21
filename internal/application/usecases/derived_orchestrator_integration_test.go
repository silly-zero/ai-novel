package usecases

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapterderivedtask"
	"github.com/ai-novel/studio/internal/domain/events"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	"github.com/ai-novel/studio/internal/infrastructure/database"
	_ "github.com/lib/pq"
)

func TestDerivedOrchestratorRetriesOnlyFailedHandlerPostgres(t *testing.T) {
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
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("orchestrator-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chapterRow, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("正文").SetWordCount(2).SetOrder(1).SetDerivedStatus(string(domain.DerivedStatusPending)).SetDerivedGenerationID("g").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.ChapterDerivedTask.Delete().Where(chapterderivedtask.ChapterID(chapterRow.ID)).Exec(cleanupCtx)
		_ = client.Chapter.DeleteOneID(chapterRow.ID).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx)
	})
	repo := database.NewDerivedTaskRepository(client)
	orchestrator := NewDerivedOrchestrator(repo, time.Minute)
	calls := make(map[string]int)
	characterFails := true
	for _, key := range domain.DerivedHandlerKeys {
		key := key
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error {
			calls[key]++
			if key == domain.DerivedHandlerCharacter && characterFails {
				return errors.New("character failed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	event := events.ChapterGeneratedEvent{GenerationID: "g", NovelID: fmt.Sprintf("%d", novelRow.ID), ChapterID: fmt.Sprintf("%d", chapterRow.ID), ChapterIndex: 1, Content: "正文"}
	if err := orchestrator.RunCurrent(ctx, event); err == nil {
		t.Fatal("first run succeeded")
	}
	tasks, err := repo.List(ctx, chapterRow.ID, "g")
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]domain.DerivedTaskStatus)
	for _, task := range tasks {
		statuses[task.HandlerKey] = task.Status
	}
	if statuses[domain.DerivedHandlerMemory] != domain.DerivedTaskReady || statuses[domain.DerivedHandlerWorld] != domain.DerivedTaskReady || statuses[domain.DerivedHandlerCharacter] != domain.DerivedTaskFailed {
		t.Fatalf("statuses=%#v", statuses)
	}
	characterFails = false
	if err := orchestrator.RetryCurrent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if calls[domain.DerivedHandlerMemory] != 1 || calls[domain.DerivedHandlerWorld] != 1 || calls[domain.DerivedHandlerCharacter] != 2 {
		t.Fatalf("calls=%#v", calls)
	}
	chapterRow, err = client.Chapter.Get(ctx, chapterRow.ID)
	if err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusReady) {
		t.Fatalf("chapter=%#v error=%v", chapterRow, err)
	}
}
