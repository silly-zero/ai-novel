package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/chapterderivedtask"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

const maxDerivedTaskErrorRunes = 1000

type DerivedTaskRepository struct {
	client *ent.Client
}

func NewDerivedTaskRepository(client *ent.Client) *DerivedTaskRepository {
	return &DerivedTaskRepository{client: client}
}

func InitializeDerivedTasks(
	ctx context.Context,
	client *ent.Client,
	chapterID int,
	generationID string,
	status domain.DerivedTaskStatus,
) error {
	if chapterID <= 0 || strings.TrimSpace(generationID) == "" {
		return fmt.Errorf("invalid derived task target")
	}
	for _, key := range domain.DerivedHandlerKeys {
		if err := client.ChapterDerivedTask.Create().
			SetChapterID(chapterID).
			SetGenerationID(generationID).
			SetHandlerKey(chapterderivedtask.HandlerKey(key)).
			SetStatus(chapterderivedtask.Status(status)).
			OnConflictColumns(
				chapterderivedtask.FieldChapterID,
				chapterderivedtask.FieldGenerationID,
				chapterderivedtask.FieldHandlerKey,
			).
			Ignore().
			Exec(ctx); err != nil {
			return fmt.Errorf("initialize derived task %s: %w", key, err)
		}
	}
	return nil
}

func (r *DerivedTaskRepository) Initialize(
	ctx context.Context,
	chapterID int,
	generationID string,
	status domain.DerivedTaskStatus,
) error {
	return InitializeDerivedTasks(ctx, r.client, chapterID, generationID, status)
}

func (r *DerivedTaskRepository) Claim(
	ctx context.Context,
	chapterID int,
	generationID, handlerKey, leaseToken string,
	now, leaseUntil time.Time,
) (*domain.DerivedTask, error) {
	if chapterID <= 0 || generationID == "" || handlerKey == "" || leaseToken == "" || !leaseUntil.After(now) {
		return nil, fmt.Errorf("invalid derived task claim")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	client := tx.Client()
	if _, err := client.Chapter.Query().Where(
		chapter.ID(chapterID),
		chapter.DerivedGenerationID(generationID),
		func(selector *sql.Selector) { selector.ForUpdate() },
	).Only(ctx); err != nil {
		return nil, err
	}
	task, err := client.ChapterDerivedTask.Query().Where(
		chapterderivedtask.ChapterID(chapterID),
		chapterderivedtask.GenerationID(generationID),
		chapterderivedtask.HandlerKeyEQ(chapterderivedtask.HandlerKey(handlerKey)),
		func(selector *sql.Selector) { selector.ForUpdate() },
	).Only(ctx)
	if err != nil {
		return nil, err
	}
	claimable := task.Status == chapterderivedtask.StatusPending || task.Status == chapterderivedtask.StatusFailed ||
		(task.Status == chapterderivedtask.StatusRunning && task.LeaseUntil != nil && !task.LeaseUntil.After(now))
	if !claimable {
		return nil, nil
	}
	task, err = client.ChapterDerivedTask.UpdateOneID(task.ID).
		SetStatus(chapterderivedtask.StatusRunning).
		SetLeaseToken(leaseToken).
		SetLeaseUntil(leaseUntil).
		SetLastError("").
		AddAttempts(1).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	result := taskFromEnt(task)
	return &result, nil
}

func (r *DerivedTaskRepository) Complete(
	ctx context.Context,
	taskID, chapterID int,
	generationID, leaseToken string,
	now time.Time,
	success bool,
	lastError string,
) error {
	status := chapterderivedtask.StatusFailed
	if success {
		status = chapterderivedtask.StatusReady
		lastError = ""
	}
	lastError = boundedDerivedTaskError(lastError)
	_, err := r.client.ChapterDerivedTask.UpdateOneID(taskID).Where(
		chapterderivedtask.ChapterID(chapterID),
		chapterderivedtask.GenerationID(generationID),
		chapterderivedtask.HasChapterWith(chapter.DerivedGenerationID(generationID)),
		chapterderivedtask.StatusEQ(chapterderivedtask.StatusRunning),
		chapterderivedtask.LeaseToken(leaseToken),
		chapterderivedtask.LeaseUntilGT(now),
	).SetStatus(status).SetLeaseToken("").ClearLeaseUntil().SetLastError(lastError).Save(ctx)
	if ent.IsNotFound(err) {
		return fmt.Errorf("derived task lease changed")
	}
	return err
}

func (r *DerivedTaskRepository) Reconcile(
	ctx context.Context,
	chapterID int,
	generationID string,
) (domain.DerivedStatus, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	client := tx.Client()
	if _, err := client.Chapter.Query().Where(
		chapter.ID(chapterID),
		chapter.DerivedGenerationID(generationID),
		func(selector *sql.Selector) { selector.ForUpdate() },
	).Only(ctx); err != nil {
		return "", err
	}
	rows, err := client.ChapterDerivedTask.Query().Where(
		chapterderivedtask.ChapterID(chapterID),
		chapterderivedtask.GenerationID(generationID),
		func(selector *sql.Selector) { selector.ForUpdate() },
	).All(ctx)
	if err != nil {
		return "", err
	}
	status := domain.DerivedStatusPending
	if len(rows) == len(domain.DerivedHandlerKeys) {
		allReady := true
		failed := false
		for _, task := range rows {
			allReady = allReady && task.Status == chapterderivedtask.StatusReady
			failed = failed || task.Status == chapterderivedtask.StatusFailed
		}
		switch {
		case allReady:
			status = domain.DerivedStatusReady
		case failed:
			status = domain.DerivedStatusFailed
		}
	}
	if err := client.Chapter.UpdateOneID(chapterID).SetDerivedStatus(string(status)).Exec(ctx); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	committed = true
	return status, nil
}

func (r *DerivedTaskRepository) List(
	ctx context.Context,
	chapterID int,
	generationID string,
) ([]domain.DerivedTask, error) {
	rows, err := r.client.ChapterDerivedTask.Query().Where(
		chapterderivedtask.ChapterID(chapterID),
		chapterderivedtask.GenerationID(generationID),
	).Order(chapterderivedtask.ByHandlerKey()).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domain.DerivedTask, len(rows))
	for index, row := range rows {
		result[index] = taskFromEnt(row)
	}
	return result, nil
}

func taskFromEnt(task *ent.ChapterDerivedTask) domain.DerivedTask {
	return domain.DerivedTask{
		ID: task.ID, ChapterID: task.ChapterID, GenerationID: task.GenerationID,
		HandlerKey: string(task.HandlerKey), Status: domain.DerivedTaskStatus(task.Status),
		Attempts: task.Attempts, LeaseToken: task.LeaseToken, LeaseUntil: task.LeaseUntil,
		LastError: task.LastError,
	}
}

func boundedDerivedTaskError(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxDerivedTaskErrorRunes {
		return string(runes[:maxDerivedTaskErrorRunes])
	}
	return value
}
