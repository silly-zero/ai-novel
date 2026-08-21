package novel

import (
	"context"
	"time"
)

const (
	DerivedHandlerMemory    = "memory"
	DerivedHandlerCharacter = "character"
	DerivedHandlerWorld     = "world"
)

var DerivedHandlerKeys = []string{
	DerivedHandlerMemory,
	DerivedHandlerCharacter,
	DerivedHandlerWorld,
}

type DerivedTaskStatus string

const (
	DerivedTaskPending DerivedTaskStatus = "Pending"
	DerivedTaskRunning DerivedTaskStatus = "Running"
	DerivedTaskReady   DerivedTaskStatus = "Ready"
	DerivedTaskFailed  DerivedTaskStatus = "Failed"
)

type DerivedTask struct {
	ID           int
	ChapterID    int
	GenerationID string
	HandlerKey   string
	Status       DerivedTaskStatus
	Attempts     int
	LeaseToken   string
	LeaseUntil   *time.Time
	LastError    string
}

type DerivedTaskRepository interface {
	Initialize(ctx context.Context, chapterID int, generationID string, initialStatus DerivedTaskStatus) error
	Claim(ctx context.Context, chapterID int, generationID, handlerKey, leaseToken string, now, leaseUntil time.Time) (*DerivedTask, error)
	Complete(ctx context.Context, taskID, chapterID int, generationID, leaseToken string, now time.Time, success bool, lastError string) error
	Reconcile(ctx context.Context, chapterID int, generationID string) (DerivedStatus, error)
	List(ctx context.Context, chapterID int, generationID string) ([]DerivedTask, error)
}
