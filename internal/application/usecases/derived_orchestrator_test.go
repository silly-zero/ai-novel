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
	tasks               map[string]novel.DerivedTask
	claims              int
	claimTimes          map[string]time.Time
	leaseEnds           map[string]time.Time
	initializeErr       error
	reconcileContextErr error
	reconcileDeadline   time.Time
	completeContextErrs []error
	completeDeadlines   []time.Time
	completions         []bool
	lastErrors          []string
	completeErr         error
	completeBlocks      bool
	completeStarted     chan struct{}
	cancelOnComplete    context.CancelFunc
	reconcileErr        error
	status              novel.DerivedStatus
}

func (r *orchestratorRepoFake) Initialize(context.Context, int, string, novel.DerivedTaskStatus) error {
	if r.initializeErr != nil {
		return r.initializeErr
	}
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
func (r *orchestratorRepoFake) Complete(ctx context.Context, taskID, _ int, _, _ string, _ time.Time, success bool, lastError string) error {
	r.completeContextErrs = append(r.completeContextErrs, ctx.Err())
	deadline, _ := ctx.Deadline()
	r.completeDeadlines = append(r.completeDeadlines, deadline)
	r.completions = append(r.completions, success)
	r.lastErrors = append(r.lastErrors, lastError)
	if r.completeStarted != nil {
		select {
		case r.completeStarted <- struct{}{}:
		default:
		}
	}
	if r.cancelOnComplete != nil {
		r.cancelOnComplete()
	}
	if r.completeBlocks {
		<-ctx.Done()
		return ctx.Err()
	}
	if r.completeErr != nil {
		return r.completeErr
	}
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
func (r *orchestratorRepoFake) Reconcile(ctx context.Context, _ int, _ string) (novel.DerivedStatus, error) {
	r.reconcileContextErr = ctx.Err()
	r.reconcileDeadline, _ = ctx.Deadline()
	return r.status, r.reconcileErr
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
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{HandlerTimeout: time.Minute, SettlementTimeout: time.Second})
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

func TestDerivedOrchestratorPreservesCancellationCauseFromInitialize(t *testing.T) {
	cause := errors.New("shutdown requested")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	repo := &orchestratorRepoFake{initializeErr: context.Canceled}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{})
	for _, key := range novel.DerivedHandlerKeys {
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	err := orchestrator.RunCurrent(ctx, events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("error = %v", err)
	}
}

func TestDerivedOrchestratorRejectsInvalidChapterID(t *testing.T) {
	repo := &orchestratorRepoFake{}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{HandlerTimeout: time.Minute, SettlementTimeout: time.Second})
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
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{HandlerTimeout: time.Minute, SettlementTimeout: time.Second})
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
	handlerTimeout := 100 * time.Millisecond
	settlementTimeout := 25 * time.Millisecond
	repo := &orchestratorRepoFake{status: novel.DerivedStatusReady}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{HandlerTimeout: handlerTimeout, SettlementTimeout: settlementTimeout})
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
		got := repo.leaseEnds[key].Sub(repo.claimTimes[key])
		want := handlerTimeout + settlementTimeout
		if got < want-10*time.Millisecond || got > want+10*time.Millisecond {
			t.Fatalf("lease %s = %s, want about %s", key, got, want)
		}
	}
}

func TestDerivedOrchestratorSettlesDeadlineWithFreshContext(t *testing.T) {
	repo := &orchestratorRepoFake{status: novel.DerivedStatusFailed}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{
		HandlerTimeout:    20 * time.Millisecond,
		SettlementTimeout: 100 * time.Millisecond,
	})
	for _, key := range novel.DerivedHandlerKeys {
		key := key
		if err := orchestrator.Register(key, func(ctx context.Context, _ events.ChapterGeneratedEvent) error {
			if key != novel.DerivedHandlerMemory {
				return nil
			}
			<-ctx.Done()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	err := orchestrator.RunCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if len(repo.completions) != len(novel.DerivedHandlerKeys) || repo.completions[0] {
		t.Fatalf("completions = %#v", repo.completions)
	}
	if repo.completeContextErrs[0] != nil || repo.completeDeadlines[0].IsZero() {
		t.Fatalf("complete context error=%v deadline=%v", repo.completeContextErrs[0], repo.completeDeadlines[0])
	}
	if !strings.Contains(repo.lastErrors[0], context.DeadlineExceeded.Error()) {
		t.Fatalf("last error = %q", repo.lastErrors[0])
	}
	if repo.reconcileContextErr != nil || repo.reconcileDeadline.IsZero() {
		t.Fatalf("reconcile context error=%v deadline=%v", repo.reconcileContextErr, repo.reconcileDeadline)
	}
}

func TestDerivedOrchestratorStopsClaimingAfterCallerCancellation(t *testing.T) {
	repo := &orchestratorRepoFake{status: novel.DerivedStatusFailed}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{
		HandlerTimeout:    time.Second,
		SettlementTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, key := range novel.DerivedHandlerKeys {
		key := key
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error {
			if key == novel.DerivedHandlerMemory {
				cancel()
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	err := orchestrator.RunCurrent(ctx, events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if repo.claims != 1 {
		t.Fatalf("claims = %d, want 1", repo.claims)
	}
	if len(repo.completions) != 1 || repo.completions[0] {
		t.Fatalf("completions = %#v", repo.completions)
	}
	if repo.completeContextErrs[0] != nil || repo.reconcileContextErr != nil {
		t.Fatalf("settlement contexts: complete=%v reconcile=%v", repo.completeContextErrs[0], repo.reconcileContextErr)
	}
}

func TestDerivedOrchestratorReturnsCancellationFromFinalSettlement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repo := &orchestratorRepoFake{
		status:           novel.DerivedStatusReady,
		cancelOnComplete: cancel,
	}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{
		HandlerTimeout:    time.Second,
		SettlementTimeout: time.Second,
	})
	for _, key := range novel.DerivedHandlerKeys {
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	err := orchestrator.RunCurrent(ctx, events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if repo.completeContextErrs[0] != nil || repo.reconcileContextErr != nil {
		t.Fatalf("settlement contexts: complete=%v reconcile=%v", repo.completeContextErrs[0], repo.reconcileContextErr)
	}
}

func TestDerivedOrchestratorPreservesCancellationWithFinalHandlerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handlerErr := errors.New("handler stopped")
	repo := &orchestratorRepoFake{status: novel.DerivedStatusFailed}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{
		HandlerTimeout:    time.Second,
		SettlementTimeout: time.Second,
	})
	for _, key := range novel.DerivedHandlerKeys {
		key := key
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error {
			if key == novel.DerivedHandlerWorld {
				cancel()
				return handlerErr
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	err := orchestrator.RunCurrent(ctx, events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, handlerErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestDerivedOrchestratorReconcilesAfterCompleteFailure(t *testing.T) {
	completeErr := errors.New("complete failed")
	reconcileErr := errors.New("reconcile failed")
	repo := &orchestratorRepoFake{
		completeErr:  completeErr,
		reconcileErr: reconcileErr,
	}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{
		HandlerTimeout:    time.Second,
		SettlementTimeout: time.Second,
	})
	handlerErr := errors.New("handler failed")
	for _, key := range novel.DerivedHandlerKeys {
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error { return handlerErr }); err != nil {
			t.Fatal(err)
		}
	}
	err := orchestrator.RunCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	for _, want := range []error{handlerErr, completeErr, reconcileErr} {
		if !errors.Is(err, want) {
			t.Fatalf("error %v does not contain %v", err, want)
		}
	}
	if repo.reconcileContextErr != nil {
		t.Fatalf("reconcile context error = %v", repo.reconcileContextErr)
	}
}

func TestDerivedOrchestratorBoundsSettlement(t *testing.T) {
	repo := &orchestratorRepoFake{completeBlocks: true}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{
		HandlerTimeout:    time.Second,
		SettlementTimeout: 20 * time.Millisecond,
	})
	for _, key := range novel.DerivedHandlerKeys {
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	err := orchestrator.RunCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("settlement took %s", elapsed)
	}
}

func TestDerivedOrchestratorRecordsHandlerFailure(t *testing.T) {
	repo := &orchestratorRepoFake{status: novel.DerivedStatusFailed}
	orchestrator := NewDerivedOrchestrator(repo, DerivedOrchestratorConfig{HandlerTimeout: time.Minute, SettlementTimeout: time.Second})
	for _, key := range novel.DerivedHandlerKeys {
		if err := orchestrator.Register(key, func(context.Context, events.ChapterGeneratedEvent) error { return errors.New("failed") }); err != nil {
			t.Fatal(err)
		}
	}
	if err := orchestrator.RunCurrent(context.Background(), events.ChapterGeneratedEvent{ChapterID: "1", GenerationID: "g"}); err == nil {
		t.Fatal("failure was swallowed")
	}
}
