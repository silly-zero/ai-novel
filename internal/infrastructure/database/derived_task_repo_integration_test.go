package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapterderivedtask"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	_ "github.com/lib/pq"
)

func TestDerivedTaskRepositoryClaimCompleteAndReconcile(t *testing.T) {
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
	novelRow, err := client.Novel.Create().SetTitle(fmt.Sprintf("derived-task-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chapterRow, err := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("正文").SetWordCount(2).SetOrder(1).
		SetDerivedStatus(string(domain.DerivedStatusPending)).SetDerivedGenerationID("g1").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.ChapterDerivedTask.Delete().Where(chapterderivedtask.ChapterID(chapterRow.ID)).Exec(cleanupCtx)
		_ = client.Chapter.DeleteOneID(chapterRow.ID).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx)
	})
	repo := NewDerivedTaskRepository(client)
	if err := repo.Initialize(ctx, chapterRow.ID, "g1", domain.DerivedTaskPending); err != nil {
		t.Fatal(err)
	}
	tasks, err := repo.List(ctx, chapterRow.ID, "g1")
	if err != nil || len(tasks) != 3 {
		t.Fatalf("tasks=%#v error=%v", tasks, err)
	}
	now := time.Now()
	claimed, err := repo.Claim(ctx, chapterRow.ID, "g1", domain.DerivedHandlerMemory, "lease-1", now, now.Add(time.Minute))
	if err != nil || claimed == nil || claimed.Attempts != 1 || claimed.Status != domain.DerivedTaskRunning {
		t.Fatalf("claim=%#v error=%v", claimed, err)
	}
	if second, err := repo.Claim(ctx, chapterRow.ID, "g1", domain.DerivedHandlerMemory, "lease-2", now, now.Add(time.Minute)); err != nil || second != nil {
		t.Fatalf("second claim=%#v error=%v", second, err)
	}
	if err := repo.Complete(ctx, claimed.ID, chapterRow.ID, "g1", "wrong", now, true, ""); err == nil {
		t.Fatal("wrong lease completed task")
	}
	if _, err := client.Chapter.UpdateOneID(chapterRow.ID).SetDerivedGenerationID("g2").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, claimed.ID, chapterRow.ID, "g1", "lease-1", now, true, ""); err == nil {
		t.Fatal("old generation completed task")
	}
	if _, err := client.Chapter.UpdateOneID(chapterRow.ID).SetDerivedGenerationID("g1").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, claimed.ID, chapterRow.ID, "g1", "lease-1", now, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.Initialize(ctx, chapterRow.ID, "g1", domain.DerivedTaskPending); err != nil {
		t.Fatal(err)
	}
	memoryTask, err := repo.Claim(ctx, chapterRow.ID, "g1", domain.DerivedHandlerMemory, "must-not-reset", now, now.Add(time.Minute))
	if err != nil || memoryTask != nil {
		t.Fatalf("ready task reset by initialize: task=%#v error=%v", memoryTask, err)
	}
	for _, key := range []string{domain.DerivedHandlerCharacter, domain.DerivedHandlerWorld} {
		task, err := repo.Claim(ctx, chapterRow.ID, "g1", key, "lease-"+key, now, now.Add(time.Minute))
		if err != nil || task == nil {
			t.Fatalf("claim %s=%#v error=%v", key, task, err)
		}
		success := key == domain.DerivedHandlerCharacter
		if err := repo.Complete(ctx, task.ID, chapterRow.ID, "g1", "lease-"+key, now, success, "world failed"); err != nil {
			t.Fatal(err)
		}
	}
	status, err := repo.Reconcile(ctx, chapterRow.ID, "g1")
	if err != nil || status != domain.DerivedStatusFailed {
		t.Fatalf("status=%s error=%v", status, err)
	}
	chapterRow, _ = client.Chapter.Get(ctx, chapterRow.ID)
	if chapterRow.DerivedStatus != string(domain.DerivedStatusFailed) {
		t.Fatalf("chapter=%#v", chapterRow)
	}
}

func TestDerivedTaskRepositoryReclaimsExpiredRunningOnly(t *testing.T) {
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
	novelRow, _ := client.Novel.Create().SetTitle(fmt.Sprintf("lease-%d", time.Now().UnixNano())).Save(ctx)
	chapterRow, _ := client.Chapter.Create().SetNovel(novelRow).SetTitle("第一章").SetContent("正文").SetWordCount(2).SetOrder(1).SetDerivedStatus(string(domain.DerivedStatusPending)).SetDerivedGenerationID("g").Save(ctx)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.ChapterDerivedTask.Delete().Where(chapterderivedtask.ChapterID(chapterRow.ID)).Exec(cleanupCtx)
		_ = client.Chapter.DeleteOneID(chapterRow.ID).Exec(cleanupCtx)
		_ = client.Novel.DeleteOneID(novelRow.ID).Exec(cleanupCtx)
	})
	repo := NewDerivedTaskRepository(client)
	if err := repo.Initialize(ctx, chapterRow.ID, "g", domain.DerivedTaskPending); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	task, err := repo.Claim(ctx, chapterRow.ID, "g", domain.DerivedHandlerMemory, "first", now, now.Add(time.Minute))
	if err != nil || task == nil {
		t.Fatalf("claim=%#v error=%v", task, err)
	}
	if task, err := repo.Claim(ctx, chapterRow.ID, "g", domain.DerivedHandlerMemory, "early", now.Add(time.Second), now.Add(time.Minute)); err != nil || task != nil {
		t.Fatalf("early reclaim=%#v error=%v", task, err)
	}
	if err := repo.Complete(ctx, task.ID, chapterRow.ID, "g1", "first", now.Add(2*time.Minute), true, ""); err == nil {
		t.Fatal("expired lease completed task")
	}
	if task, err := repo.Claim(ctx, chapterRow.ID, "g", domain.DerivedHandlerMemory, "expired", now.Add(2*time.Minute), now.Add(3*time.Minute)); err != nil || task == nil || task.Attempts != 2 {
		t.Fatalf("expired reclaim=%#v error=%v", task, err)
	}
}
