package usecases

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapterderivedtask"
	"github.com/ai-novel/studio/internal/domain/events"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	"github.com/ai-novel/studio/internal/infrastructure/database"
	_ "github.com/lib/pq"
)

func TestDerivedOrchestratorSettlesDeadlineAndRetriesImmediatelyPostgres(t *testing.T) {
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
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("orchestrator-deadline-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chapterRow, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("正文").SetWordCount(2).SetOrder(1).SetDerivedStatus(string(domain.DerivedStatusPending)).SetDerivedGenerationID("deadline-g").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chapterID := chapterRow.ID
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.ChapterDerivedTask.Delete().Where(chapterderivedtask.ChapterID(chapterID)).Exec(cleanupCtx)
		_ = client.Chapter.DeleteOneID(chapterID).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx)
	})
	repo := database.NewDerivedTaskRepository(client)
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{
		HandlerTimeout:    time.Second,
		SettlementTimeout: time.Second,
	})
	memoryTimesOut := true
	calls := make(map[string]int)
	for _, key := range domain.DerivedHandlerKeys {
		key := key
		if err := orchestrator.Register(key, func(ctx context.Context, _ events.ChapterGeneratedEvent) error {
			calls[key]++
			if key == domain.DerivedHandlerMemory && memoryTimesOut {
				<-ctx.Done()
				return nil
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	event := events.ChapterGeneratedEvent{
		GenerationID: "deadline-g",
		NovelID:      fmt.Sprintf("%d", novelRow.ID),
		ChapterID:    fmt.Sprintf("%d", chapterRow.ID),
		ChapterIndex: 1,
		Content:      "正文",
	}
	if err := orchestrator.RunCurrent(ctx, event); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first run error = %v", err)
	}
	tasks, err := repo.List(ctx, chapterID, event.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	var memoryTask domain.DerivedTask
	for _, task := range tasks {
		if task.HandlerKey == domain.DerivedHandlerMemory {
			memoryTask = task
			break
		}
	}
	if memoryTask.Status != domain.DerivedTaskFailed || memoryTask.LeaseToken != "" || memoryTask.LeaseUntil != nil || !strings.Contains(memoryTask.LastError, context.DeadlineExceeded.Error()) {
		t.Fatalf("memory task = %#v", memoryTask)
	}
	chapterRow, err = client.Chapter.Get(ctx, chapterID)
	if err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusFailed) {
		t.Fatalf("chapter=%#v error=%v", chapterRow, err)
	}
	memoryTimesOut = false
	if err := orchestrator.RetryCurrent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if calls[domain.DerivedHandlerMemory] != 2 || calls[domain.DerivedHandlerCharacter] != 1 || calls[domain.DerivedHandlerWorld] != 1 {
		t.Fatalf("calls=%#v", calls)
	}
	chapterRow, err = client.Chapter.Get(ctx, chapterID)
	if err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusReady) {
		t.Fatalf("chapter=%#v error=%v", chapterRow, err)
	}
}

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
	chapterID := chapterRow.ID
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.ChapterDerivedTask.Delete().Where(chapterderivedtask.ChapterID(chapterID)).Exec(cleanupCtx)
		_ = client.Chapter.DeleteOneID(chapterID).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx)
	})
	repo := database.NewDerivedTaskRepository(client)
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{HandlerTimeout: time.Minute, SettlementTimeout: time.Second})
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
	tasks, err := repo.List(ctx, chapterID, "g")
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
	chapterRow, err = client.Chapter.Get(ctx, chapterID)
	if err != nil || chapterRow.DerivedStatus != string(domain.DerivedStatusReady) {
		t.Fatalf("chapter=%#v error=%v", chapterRow, err)
	}
}
