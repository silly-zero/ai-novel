package usecases

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ai-novel/studio/internal/domain/agents"
	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/ai-novel/studio/internal/domain/novel"
)

type DerivedHandler func(context.Context, events.ChapterGeneratedEvent) error

type DerivedOrchestrator struct {
	repo     novel.DerivedTaskRepository
	handlers map[string]DerivedHandler
	lease    time.Duration
}

func NewDerivedOrchestrator(repo novel.DerivedTaskRepository, lease time.Duration) *DerivedOrchestrator {
	if lease <= 0 {
		lease = time.Minute
	}
	return &DerivedOrchestrator{repo: repo, handlers: make(map[string]DerivedHandler), lease: lease}
}

func (o *DerivedOrchestrator) Register(key string, handler DerivedHandler) error {
	if !containsDerivedHandler(key) || handler == nil {
		return fmt.Errorf("invalid derived handler %q", key)
	}
	if _, exists := o.handlers[key]; exists {
		return fmt.Errorf("derived handler %q already registered", key)
	}
	o.handlers[key] = handler
	return nil
}

func (o *DerivedOrchestrator) RunCurrent(ctx context.Context, event events.ChapterGeneratedEvent) error {
	return o.run(ctx, event)
}

func (o *DerivedOrchestrator) RetryCurrent(ctx context.Context, event events.ChapterGeneratedEvent) error {
	return o.run(ctx, event)
}

func (o *DerivedOrchestrator) run(ctx context.Context, event events.ChapterGeneratedEvent) error {
	if o.repo == nil {
		return errors.New("derived task repository is not configured")
	}
	chapterID, err := derivedChapterID(event.ChapterID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(event.GenerationID) == "" {
		return errors.New("derived event generation is required")
	}
	if len(o.handlers) != len(novel.DerivedHandlerKeys) {
		return errors.New("derived handlers are incomplete")
	}
	if err := o.repo.Initialize(ctx, chapterID, event.GenerationID, novel.DerivedTaskPending); err != nil {
		return err
	}
	var errs []error
	for _, key := range novel.DerivedHandlerKeys {
		leaseToken, tokenErr := agents.NewGenerationID()
		if tokenErr != nil {
			errs = append(errs, fmt.Errorf("create lease token %s: %w", key, tokenErr))
			continue
		}
		now := time.Now()
		task, claimErr := o.repo.Claim(ctx, chapterID, event.GenerationID, key, leaseToken, now, now.Add(o.lease))
		if claimErr != nil {
			errs = append(errs, fmt.Errorf("claim %s: %w", key, claimErr))
			continue
		}
		if task == nil {
			continue
		}
		handlerErr := runDerivedHandler(ctx, o.handlers[key], event)
		completeErr := o.repo.Complete(ctx, task.ID, chapterID, event.GenerationID, task.LeaseToken, time.Now(), handlerErr == nil, errorString(handlerErr))
		if handlerErr != nil {
			errs = append(errs, fmt.Errorf("derived handler %s: %w", key, handlerErr))
		}
		if completeErr != nil {
			errs = append(errs, fmt.Errorf("complete %s: %w", key, completeErr))
		}
	}
	_, reconcileErr := o.repo.Reconcile(ctx, chapterID, event.GenerationID)
	if reconcileErr != nil {
		errs = append(errs, reconcileErr)
	}
	return errors.Join(errs...)
}

func containsDerivedHandler(key string) bool {
	for _, item := range novel.DerivedHandlerKeys {
		if item == key {
			return true
		}
	}
	return false
}

func derivedChapterID(value string) (int, error) {
	id, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid derived event chapter %q", value)
	}
	return id, nil
}

func runDerivedHandler(ctx context.Context, handler DerivedHandler, event events.ChapterGeneratedEvent) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return handler(ctx, event)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
