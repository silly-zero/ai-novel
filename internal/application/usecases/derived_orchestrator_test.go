package usecases

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/ai-novel/studio/internal/domain/novel"
)

type orchestratorRepoFake struct {
	tasks       map[string]novel.DerivedTask
	claims      int
	claimTimes  map[string]time.Time
	leaseEnds   map[string]time.Time
	completions []bool
	status      novel.DerivedStatus
}

func (r *orchestratorRepoFake) Initialize(context.Context, int, string, novel.DerivedTaskStatus) error {
	if r.tasks == nil {
		r.tasks = make(map[string]novel.DerivedTask)
	}
	for _, key := range novel.DerivedHandlerKeys {
		if _, ok := r.tasks[key]; !ok {
			r.tasks[key] = novel.DerivedTask{ID: len(r.tasks) + 1, ChapterID: 1, GenerationID: "g", HandlerKey: key, Status: novel.DerivedTaskPending}
		}
	}
	return nil
}
func (r *orchestratorRepoFake) Claim(_ context.Context, _ int, _ string, key, token string, now, leaseUntil time.Time) (*novel.DerivedTask, error) {
	task := r.tasks[key]
	if task.Status == novel.DerivedTaskReady {
		return nil, nil
	}
	if r.claimTimes == nil {
		r.claimTimes = make(map[string]time.Time)
		r.leaseEnds = make(map[string]time.Time)
	}
	r.claimTimes[key] = now
	r.leaseEnds[key] = leaseUntil
	task.Status = novel.DerivedTaskRunning
	task.LeaseToken = token
	r.tasks[key] = task
	r.claims++
	return &task, nil
}
func (r *orchestratorRepoFake) Complete(_ context.Context, taskID, _ int, _, _ string, _ time.Time, success bool, _ string) error {
	r.completions = append(r.completions, success)
	for key, task := range r.tasks {
		if task.ID == taskID {
			if success {
				task.Status = novel.DerivedTaskReady
			} else {
				task.Status = novel.DerivedTaskFailed
			}
			r.tasks[key] = task
		}
	}
	return nil
}
func (r *orchestratorRepoFake) Reconcile(context.Context, int, string) (novel.DerivedStatus, error) {
	return r.status, nil
}
func (r *orchestratorRepoFake) List(context.Context, int, string) ([]novel.DerivedTask, error) {
	return nil, nil
}

func TestDerivedOrchestratorRetriesOnlyUnreadyTasks(t *testing.T) {
	repo := &orchestratorRepoFake{tasks: map[string]novel.DerivedTask{
		novel.DerivedHandlerMemory:    {ID: 1, HandlerKey: novel.DerivedHandlerMemory, GenerationID: "g", Status: novel.DerivedTaskReady},
		novel.DerivedHandlerCharacter: {ID: 2, HandlerKey: novel.DerivedHandlerCharacter, GenerationID: "g", Status: novel.DerivedTaskFailed},
		novel.DerivedHandlerWorld:     {ID: 3, HandlerKey: novel.DerivedHandlerWorld, GenerationID: "g", Status: novel.DerivedTaskReady},
	}, status: novel.DerivedStatusReady}
	calls := make(map[string]int)
	orchestrator := NewDerivedOrchestrator(repo, time.Minute)
	for _, key := range novel.DerivedHandlerKeys {
		key := key
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error { calls[key]++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	err := orchestrator.RetryCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if calls[novel.DerivedHandlerMemory] != 0 || calls[novel.DerivedHandlerWorld] != 0 || calls[novel.DerivedHandlerCharacter] != 1 {
		t.Fatalf("calls=%#v", calls)
	}
}

func TestDerivedOrchestratorRejectsInvalidChapterID(t *testing.T) {
	repo := &orchestratorRepoFake{}
	orchestrator := NewDerivedOrchestrator(repo, time.Minute)
	for _, key := range novel.DerivedHandlerKeys {
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, chapterID := range []string{"", "0", "-1", "12x"} {
		err := orchestrator.RunCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: chapterID, GenerationID: "g"})
		if err == nil {
			t.Fatalf("chapter %q was accepted", chapterID)
		}
	}
	if repo.tasks != nil {
		t.Fatalf("invalid event initialized tasks: %#v", repo.tasks)
	}
}

func TestDerivedOrchestratorRecordsHandlerPanicAndContinues(t *testing.T) {
	repo := &orchestratorRepoFake{status: novel.DerivedStatusFailed}
	orchestrator := NewDerivedOrchestrator(repo, time.Minute)
	calls := make(map[string]int)
	for _, key := range novel.DerivedHandlerKeys {
		key := key
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error {
			calls[key]++
			if key == novel.DerivedHandlerCharacter {
				panic("broken handler")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	err := orchestrator.RunCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	if err == nil || !strings.Contains(err.Error(), "panic: broken handler") {
		t.Fatalf("error = %v", err)
	}
	for _, key := range novel.DerivedHandlerKeys {
		if calls[key] != 1 {
			t.Fatalf("calls[%s] = %d", key, calls[key])
		}
	}
	if task := repo.tasks[novel.DerivedHandlerCharacter]; task.Status != novel.DerivedTaskFailed {
		t.Fatalf("character task = %#v", task)
	}
}

func TestDerivedOrchestratorStartsEachLeaseAtClaimTime(t *testing.T) {
	lease := 100 * time.Millisecond
	repo := &orchestratorRepoFake{status: novel.DerivedStatusReady}
	orchestrator := NewDerivedOrchestrator(repo, lease)
	for _, key := range novel.DerivedHandlerKeys {
		key := key
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error {
			if key == novel.DerivedHandlerMemory {
				time.Sleep(20 * time.Millisecond)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := orchestrator.RunCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"}); err != nil {
		t.Fatal(err)
	}
	if !repo.claimTimes[novel.DerivedHandlerCharacter].After(repo.claimTimes[novel.DerivedHandlerMemory]) {
		t.Fatalf("claim times = %#v", repo.claimTimes)
	}
	for _, key := range novel.DerivedHandlerKeys {
		if got := repo.leaseEnds[key].Sub(repo.claimTimes[key]); got != lease {
			t.Fatalf("lease %s = %s, want %s", key, got, lease)
		}
	}
}

func TestDerivedOrchestratorRecordsHandlerFailure(t *testing.T) {
	repo := &orchestratorRepoFake{status: novel.DerivedStatusFailed}
	orchestrator := NewDerivedOrchestrator(repo, time.Minute)
	for _, key := range novel.DerivedHandlerKeys {
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error { return errors.New("failed") }); err != nil {
			t.Fatal(err)
		}
	}
	if err := orchestrator.RunCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"}); err == nil {
		t.Fatal("failure was swallowed")
	}
}
