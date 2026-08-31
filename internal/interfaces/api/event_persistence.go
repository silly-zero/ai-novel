package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/internal/domain/agents"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	databaseinfra "github.com/ai-novel/studio/internal/infrastructure/database"
)

func (s *entGenerationChapterStore) SaveEvent(
	ctx context.Context,
	targets []*generationChapterTarget,
	generationID string,
	contents []string,
) ([]int, error) {
	if len(targets) < 2 || len(targets) > 3 || len(contents) != len(targets) {
		return nil, errors.New("invalid event generation batch")
	}
	novelID := targets[0].NovelID
	for index, target := range targets {
		if target == nil || target.NovelID != novelID || target.Order != targets[0].Order+index {
			return nil, errors.New("invalid event generation target")
		}
		if agents.HasBlockingGeneratedContentIssues(contents[index]) {
			return nil, errors.New("event chapter content failed validation")
		}
	}
	if novelID <= 0 || targets[0].NovelUpdatedAt.IsZero() || strings.TrimSpace(generationID) == "" {
		return nil, errGenerationChapterChanged
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	txClient := tx.Client()
	if err := lockGenerationNovel(ctx, txClient, novelID); err != nil {
		if ent.IsNotFound(err) {
			return nil, errGenerationChapterChanged
		}
		return nil, err
	}
	updatedAt, err := generationNovelUpdatedAt(ctx, txClient, novelID)
	if err != nil || validateGenerationNovelSource(targets[0].NovelUpdatedAt, updatedAt) != nil {
		return nil, errGenerationChapterChanged
	}

	ids := make([]int, len(targets))
	existingIDs := make([]int, 0, len(targets))
	for index, target := range targets {
		if target.ID == 0 {
			if err := requireAvailableChapterOrder(ctx, novelID, target.Order, 0, func(ctx context.Context, novelID, order int) (*ent.Chapter, error) {
				return lookupGenerationChapter(ctx, txClient, novelID, order)
			}); err != nil {
				return nil, err
			}
			row, createErr := txClient.Chapter.Create().
				SetNovelID(novelID).
				SetTitle(chapterTitle(target.Order)).
				SetContent(contents[index]).
				SetWordCount(wordCountOf(contents[index])).
				SetOrder(target.Order).
				SetStatus(string(domain.StatusDraft)).
				SetDerivedStatus(string(domain.DerivedStatusPending)).
				SetDerivedGenerationID(generationID).
				SetLastBeat("").
				SetOpenLoops([]string{}).
				SetNextAction("").
				Save(ctx)
			if createErr != nil {
				return nil, createErr
			}
			ids[index] = row.ID
		} else {
			row, updateErr := txClient.Chapter.UpdateOneID(target.ID).
				Where(chapter.UpdatedAtEQ(target.UpdatedAt)).
				SetContent(contents[index]).
				SetWordCount(wordCountOf(contents[index])).
				SetStatus(string(domain.StatusDraft)).
				SetDerivedStatus(string(domain.DerivedStatusPending)).
				SetDerivedGenerationID(generationID).
				SetLastBeat("").
				SetOpenLoops([]string{}).
				SetNextAction("").
				Save(ctx)
			if ent.IsNotFound(updateErr) {
				return nil, errGenerationChapterChanged
			}
			if updateErr != nil {
				return nil, updateErr
			}
			ids[index] = row.ID
			existingIDs = append(existingIDs, row.ID)
		}
		if err := databaseinfra.InitializeDerivedTasks(ctx, txClient, ids[index], generationID, domain.DerivedTaskPending); err != nil {
			return nil, err
		}
	}
	if err := invalidateChapterDerivedData(ctx, txClient, novelID, existingIDs); err != nil {
		return nil, err
	}
	if err := markFollowingChaptersStale(ctx, txClient, novelID, targets[len(targets)-1].Order, ids[len(ids)-1]); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit event chapters: %w", err)
	}
	committed = true
	return ids, nil
}

var _ eventGenerationChapterStore = (*entGenerationChapterStore)(nil)
var _ = novel.FieldID
