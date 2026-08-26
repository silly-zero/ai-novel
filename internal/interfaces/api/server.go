package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/characterstateversion"
	"github.com/ai-novel/studio/ent/memoryentry"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/ent/predicate"
	"github.com/ai-novel/studio/ent/relationshipstateversion"
	"github.com/ai-novel/studio/ent/worldstateversion"
	"github.com/ai-novel/studio/internal/application/workflows"
	"github.com/ai-novel/studio/internal/domain/agents"
	domain "github.com/ai-novel/studio/internal/domain/novel"
	databaseinfra "github.com/ai-novel/studio/internal/infrastructure/database"
	llminfra "github.com/ai-novel/studio/internal/infrastructure/llm"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type generationEngine interface {
	PrepareContext(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
	RunChapterGeneration(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
	PublishChapterGenerated(context.Context, *agents.GenerationState) error
	ExtractContinuity(context.Context, *agents.GenerationState) (*agents.GenerationState, error)
}

type generationChapterTarget struct {
	ID                  int
	NovelID             int
	Title               string
	Content             string
	WordCount           int
	Order               int
	Status              string
	DerivedStatus       string
	DerivedGenerationID string
	LastBeat            string
	OpenLoops           []string
	NextAction          string
	PreviousContinuity  agents.ContinuityPacket
	UpdatedAt           time.Time
	NovelUpdatedAt      time.Time
	isNew               bool
}

type generationChapterStore interface {
	Prepare(context.Context, int, int, int) (*generationChapterTarget, error)
	Save(context.Context, *generationChapterTarget, *agents.GenerationState) (int, error)
}

type entGenerationChapterStore struct {
	client *ent.Client
}

var (
	errGenerationChapterChanged          = errors.New("chapter changed during generation")
	errGenerationPreviousChapterMissing  = errors.New("previous chapter is required before generation")
	errChapterOrderOccupied              = errors.New("chapter order is already occupied")
	errChapterHasSuccessor               = errors.New("chapter has a successor and cannot be moved or deleted")
	errGenerationEarlierChapterStale     = errors.New("an earlier chapter is stale and must be regenerated first")
	errGenerationPreviousDerivedNotReady = errors.New("previous chapter derived data is not ready")
)

type generationPreviousChapterMissingError struct {
	NovelID      int
	MissingOrder int
}

func (e *generationPreviousChapterMissingError) Error() string {
	return fmt.Sprintf(
		"previous chapter %d is required before generating the next chapter for novel %d",
		e.MissingOrder,
		e.NovelID,
	)
}

func (e *generationPreviousChapterMissingError) Unwrap() error {
	return errGenerationPreviousChapterMissing
}

type generationNovelLock func(
	context.Context,
	int,
) error

type generationChapterLookup func(
	context.Context,
	int,
	int,
) (*ent.Chapter, error)

type generationPreviousChapterLookup func(
	context.Context,
	int,
	int,
) (*ent.Chapter, error)

func (s *entGenerationChapterStore) Prepare(
	ctx context.Context,
	novelID int,
	chapterID int,
	chapterIndex int,
) (*generationChapterTarget, error) {
	if novelID <= 0 {
		return nil, errors.New("invalid novel id")
	}
	if chapterID <= 0 && chapterIndex <= 0 {
		return nil, errors.New("invalid chapter index")
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
		return nil, err
	}
	query := txClient.Chapter.Query()
	if chapterID > 0 {
		query = query.Where(
			chapter.ID(chapterID),
			chapter.HasNovelWith(novel.ID(novelID)),
		)
	} else {
		query = query.Where(
			chapter.OrderEQ(chapterIndex),
			chapter.HasNovelWith(novel.ID(novelID)),
		)
	}

	row, err := query.Only(ctx)
	if ent.IsNotFound(err) {
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		committed = true
		if chapterID > 0 {
			return nil, errors.New("chapter not found")
		}
		return s.prepareNewGenerationChapter(ctx, novelID, chapterIndex)
	}
	if err != nil {
		return nil, err
	}
	if err := requireEarliestStaleTarget(ctx, txClient, novelID, row.Order); err != nil {
		return nil, err
	}
	target := generationChapterTargetFromRow(row)
	target.NovelID = novelID
	target.NovelUpdatedAt, err = generationNovelUpdatedAt(ctx, txClient, novelID)
	if err != nil {
		return nil, err
	}
	packet, err := preparePreviousContinuity(
		ctx,
		novelID,
		target.Order,
		func(ctx context.Context, novelID, order int) (*ent.Chapter, error) {
			return lookupPreviousChapterForShare(ctx, txClient, novelID, order)
		},
	)
	if err != nil {
		return nil, err
	}
	target.PreviousContinuity = packet
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return target, nil
}

func (s *entGenerationChapterStore) prepareNewGenerationChapter(
	ctx context.Context,
	novelID int,
	chapterIndex int,
) (*generationChapterTarget, error) {
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
	row, err := prepareNewGenerationChapter(
		ctx,
		novelID,
		chapterIndex,
		func(ctx context.Context, novelID int) error {
			if err := lockGenerationNovel(ctx, txClient, novelID); err != nil {
				return err
			}
			return requireEarliestStaleTarget(ctx, txClient, novelID, chapterIndex)
		},
		func(ctx context.Context, novelID, order int) (*ent.Chapter, error) {
			return lookupGenerationChapter(ctx, txClient, novelID, order)
		},
		func(ctx context.Context, novelID, order int) (*ent.Chapter, error) {
			return lookupPreviousChapterForShare(ctx, txClient, novelID, order)
		},
	)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("generated chapter target is nil")
	}
	target := row
	target.NovelID = novelID
	target.NovelUpdatedAt, err = generationNovelUpdatedAt(ctx, txClient, novelID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return target, nil
}

func prepareNewGenerationChapter(
	ctx context.Context,
	novelID int,
	chapterIndex int,
	lockNovel generationNovelLock,
	lookupTarget generationChapterLookup,
	lookup generationPreviousChapterLookup,
) (*generationChapterTarget, error) {
	if err := lockNovel(ctx, novelID); err != nil {
		return nil, err
	}
	row, err := lookupTarget(ctx, novelID, chapterIndex)
	if err == nil {
		target := generationChapterTargetFromRow(row)
		packet, err := preparePreviousContinuity(ctx, novelID, target.Order, lookup)
		if err != nil {
			return nil, err
		}
		target.PreviousContinuity = packet
		return target, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	packet, err := preparePreviousContinuity(
		ctx,
		novelID,
		chapterIndex,
		lookup,
	)
	if err != nil {
		return nil, err
	}
	row = &ent.Chapter{
		Order:         chapterIndex,
		Status:        "Draft",
		DerivedStatus: string(domain.DerivedStatusReady),
		OpenLoops:     []string{},
	}
	target := generationChapterTargetFromRow(row)
	target.PreviousContinuity = packet
	target.isNew = true
	return target, nil
}

func preparePreviousContinuity(
	ctx context.Context,
	novelID int,
	targetOrder int,
	lookup generationPreviousChapterLookup,
) (agents.ContinuityPacket, error) {
	if targetOrder <= 1 {
		return agents.ContinuityPacket{}, nil
	}
	previousOrder := targetOrder - 1
	previous, err := lookup(ctx, novelID, previousOrder)
	if ent.IsNotFound(err) {
		return agents.ContinuityPacket{}, &generationPreviousChapterMissingError{
			NovelID:      novelID,
			MissingOrder: previousOrder,
		}
	}
	if err != nil {
		return agents.ContinuityPacket{}, err
	}
	if previous.Status == string(domain.StatusStale) {
		return agents.ContinuityPacket{}, fmt.Errorf("%w: chapter %d", errGenerationEarlierChapterStale, previousOrder)
	}
	if previous.DerivedStatus != string(domain.DerivedStatusReady) {
		return agents.ContinuityPacket{}, fmt.Errorf("%w: chapter %d", errGenerationPreviousDerivedNotReady, previousOrder)
	}
	packet := agents.ContinuityPacket{
		LastBeat:   strings.TrimSpace(previous.LastBeat),
		OpenLoops:  append([]string(nil), previous.OpenLoops...),
		NextAction: strings.TrimSpace(previous.NextAction),
	}
	if err := agents.ValidateContinuityPacket(&packet); err != nil {
		return agents.ContinuityPacket{}, fmt.Errorf("previous chapter continuity is invalid: %w", err)
	}
	return packet, nil
}

func requireEarliestStaleTarget(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	targetOrder int,
) error {
	if targetOrder <= 0 {
		return errors.New("invalid generation target order")
	}
	row, err := client.Chapter.Query().Where(
		chapter.StatusEQ(string(domain.StatusStale)),
		chapter.HasNovelWith(novel.ID(novelID)),
	).Order(chapter.ByOrder()).First(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Order != targetOrder {
		return fmt.Errorf("%w: chapter %d", errGenerationEarlierChapterStale, row.Order)
	}
	return nil
}

func lockGenerationNovel(
	ctx context.Context,
	client *ent.Client,
	novelID int,
) error {
	_, err := client.Novel.Query().Where(
		novel.ID(novelID),
		func(selector *sql.Selector) {
			selector.ForUpdate()
		},
	).Only(ctx)
	return err
}

func lookupGenerationChapter(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	order int,
) (*ent.Chapter, error) {
	return client.Chapter.Query().Where(
		chapter.OrderEQ(order),
		chapter.HasNovelWith(novel.ID(novelID)),
	).Only(ctx)
}

func lookupPreviousChapterForShare(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	order int,
) (*ent.Chapter, error) {
	return client.Chapter.Query().Where(
		chapter.OrderEQ(order),
		chapter.HasNovelWith(novel.ID(novelID)),
		func(selector *sql.Selector) {
			selector.ForShare()
		},
	).Only(ctx)
}

func lookupPreviousChapter(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	order int,
) (*ent.Chapter, error) {
	return lookupGenerationChapter(ctx, client, novelID, order)
}

func (s *entGenerationChapterStore) lookupPreviousChapter(
	ctx context.Context,
	novelID int,
	order int,
) (*ent.Chapter, error) {
	return lookupPreviousChapter(ctx, s.client, novelID, order)
}

func isChapterIntegrityConflict(err error) bool {
	return errors.Is(err, errGenerationPreviousChapterMissing) ||
		errors.Is(err, errGenerationPreviousDerivedNotReady) ||
		errors.Is(err, errGenerationEarlierChapterStale) ||
		errors.Is(err, errChapterOrderOccupied) ||
		errors.Is(err, errChapterHasSuccessor)
}

func chapterMutationHTTPStatus(err error) int {
	switch {
	case ent.IsNotFound(err):
		return http.StatusNotFound
	case isChapterIntegrityConflict(err):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func requireAvailableChapterOrder(
	ctx context.Context,
	novelID int,
	order int,
	currentChapterID int,
	lookup generationChapterLookup,
) error {
	row, err := lookup(ctx, novelID, order)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if currentChapterID > 0 && row.ID == currentChapterID {
		return nil
	}
	return errChapterOrderOccupied
}

func requirePreviousChapterOrder(
	ctx context.Context,
	novelID int,
	order int,
	currentChapterID int,
	lookup generationChapterLookup,
) error {
	if order <= 1 {
		return nil
	}
	previousOrder := order - 1
	row, err := lookup(ctx, novelID, previousOrder)
	if ent.IsNotFound(err) {
		return &generationPreviousChapterMissingError{
			NovelID:      novelID,
			MissingOrder: previousOrder,
		}
	}
	if err != nil {
		return err
	}
	if currentChapterID > 0 && row.ID == currentChapterID {
		return &generationPreviousChapterMissingError{
			NovelID:      novelID,
			MissingOrder: previousOrder,
		}
	}
	return nil
}

func chapterSuccessorResult(exists bool) error {
	if exists {
		return errChapterHasSuccessor
	}
	return nil
}

func requireNoChapterSuccessor(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	order int,
) error {
	exists, err := client.Chapter.Query().Where(
		chapter.OrderGT(order),
		chapter.HasNovelWith(novel.ID(novelID)),
	).Exist(ctx)
	if err != nil {
		return err
	}
	return chapterSuccessorResult(exists)
}

func createChapterWithIntegrity(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	requestedOrder int,
	title string,
	content string,
	status string,
) (*ent.Chapter, error) {
	tx, err := client.Tx(ctx)
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
		return nil, err
	}
	order := requestedOrder
	if order <= 0 {
		last, queryErr := txClient.Chapter.Query().Where(
			chapter.HasNovelWith(novel.ID(novelID)),
		).Order(ent.Desc(chapter.FieldOrder)).First(ctx)
		switch {
		case queryErr == nil:
			order = last.Order + 1
		case ent.IsNotFound(queryErr):
			order = 1
		default:
			return nil, queryErr
		}
	}
	lookup := func(ctx context.Context, novelID, order int) (*ent.Chapter, error) {
		return lookupGenerationChapter(ctx, txClient, novelID, order)
	}
	if err := requireAvailableChapterOrder(ctx, novelID, order, 0, lookup); err != nil {
		return nil, err
	}
	if err := requirePreviousChapterOrder(ctx, novelID, order, 0, lookup); err != nil {
		return nil, err
	}
	if title == "" {
		title = chapterTitle(order)
	}
	row, err := txClient.Chapter.Create().
		SetNovelID(novelID).
		SetTitle(title).
		SetContent(content).
		SetWordCount(wordCountOf(content)).
		SetOrder(order).
		SetStatus(status).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return row.Unwrap(), nil
}

func updateChapterMemoryMetadata(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	chapterID int,
	mutate func(map[string]any),
) error {
	rows, err := client.MemoryEntry.Query().Where(memoryentry.NovelID(strconv.Itoa(novelID))).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Metadata == nil || row.Metadata["chapter_id"] != strconv.Itoa(chapterID) {
			continue
		}
		metadata := make(map[string]any, len(row.Metadata)+2)
		for key, value := range row.Metadata {
			metadata[key] = value
		}
		mutate(metadata)
		if err := client.MemoryEntry.UpdateOneID(row.ID).SetMetadata(metadata).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func invalidateChapterDerivedData(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	chapterIDs []int,
) error {
	if len(chapterIDs) == 0 {
		return nil
	}
	characterVersions, err := client.CharacterStateVersion.Query().Where(
		characterstateversion.ChapterIDIn(chapterIDs...),
		characterstateversion.Valid(true),
	).All(ctx)
	if err != nil {
		return err
	}
	characterIDs := make(map[int]struct{}, len(characterVersions))
	for _, version := range characterVersions {
		characterIDs[version.CharacterID] = struct{}{}
	}
	worldVersions, err := client.WorldStateVersion.Query().Where(
		worldstateversion.ChapterIDIn(chapterIDs...),
		worldstateversion.Valid(true),
	).All(ctx)
	if err != nil {
		return err
	}
	worldIDs := make(map[int]struct{}, len(worldVersions))
	for _, version := range worldVersions {
		worldIDs[version.WorldSettingID] = struct{}{}
	}
	relationshipVersions, err := client.RelationshipStateVersion.Query().Where(
		relationshipstateversion.ChapterIDIn(chapterIDs...),
		relationshipstateversion.Valid(true),
	).All(ctx)
	if err != nil {
		return err
	}
	if _, err := client.CharacterStateVersion.Update().Where(
		characterstateversion.ChapterIDIn(chapterIDs...),
	).SetValid(false).Save(ctx); err != nil {
		return err
	}
	if _, err := client.WorldStateVersion.Update().Where(
		worldstateversion.ChapterIDIn(chapterIDs...),
	).SetValid(false).Save(ctx); err != nil {
		return err
	}
	if _, err := client.RelationshipStateVersion.Update().Where(
		relationshipstateversion.ChapterIDIn(chapterIDs...),
	).SetValid(false).Save(ctx); err != nil {
		return err
	}
	for id := range characterIDs {
		latest, latestErr := client.CharacterStateVersion.Query().Where(
			characterstateversion.CharacterID(id), characterstateversion.Valid(true),
		).Order(characterstateversion.ByChapterIndex(sql.OrderDesc()), characterstateversion.ByID(sql.OrderDesc())).First(ctx)
		status := ""
		if latestErr == nil {
			status = latest.CurrentStatus
		} else if !ent.IsNotFound(latestErr) {
			return latestErr
		}
		if err := client.Character.UpdateOneID(id).SetCurrentStatus(status).SetStateVersioned(true).Exec(ctx); err != nil {
			return err
		}
	}
	for id := range worldIDs {
		latest, latestErr := client.WorldStateVersion.Query().Where(
			worldstateversion.WorldSettingID(id), worldstateversion.Valid(true),
		).Order(worldstateversion.ByChapterIndex(sql.OrderDesc()), worldstateversion.ByID(sql.OrderDesc())).First(ctx)
		state := ""
		if latestErr == nil {
			state = latest.CurrentState
		} else if !ent.IsNotFound(latestErr) {
			return latestErr
		}
		if err := client.WorldSetting.UpdateOneID(id).SetCurrentState(state).SetStateVersioned(true).Exec(ctx); err != nil {
			return err
		}
	}
	for _, version := range relationshipVersions {
		if err := databaseinfra.RebuildRelationshipCache(
			ctx, client, strconv.Itoa(novelID),
			version.SourceCharacterID, version.TargetCharacterID, version.RelationType,
		); err != nil {
			return err
		}
	}
	for _, chapterID := range chapterIDs {
		if err := updateChapterMemoryMetadata(ctx, client, novelID, chapterID, func(metadata map[string]any) {
			metadata["chapter_status"] = "Stale"
		}); err != nil {
			return err
		}
	}
	return nil
}

func markFollowingChaptersStale(
	ctx context.Context,
	client *ent.Client,
	novelID int,
	chapterOrder int,
	excludeChapterID int,
) error {
	if novelID <= 0 || chapterOrder <= 0 {
		return errors.New("invalid chapter stale boundary")
	}
	staleRows, err := client.Chapter.Query().Where(
		chapter.IDNEQ(excludeChapterID),
		chapter.OrderGT(chapterOrder),
		chapter.HasNovelWith(novel.ID(novelID)),
	).Select(chapter.FieldID).Ints(ctx)
	if err != nil {
		return err
	}
	if _, err := client.Chapter.Update().Where(
		chapter.IDNEQ(excludeChapterID),
		chapter.OrderGT(chapterOrder),
		chapter.HasNovelWith(novel.ID(novelID)),
	).SetStatus(string(domain.StatusStale)).
		SetDerivedStatus(string(domain.DerivedStatusFailed)).
		SetDerivedGenerationID("").
		Save(ctx); err != nil {
		return err
	}
	if err := invalidateChapterDerivedData(ctx, client, novelID, staleRows); err != nil {
		return err
	}
	return nil
}

func updateChapterWithIntegrity(
	ctx context.Context,
	client *ent.Client,
	chapterID int,
	req UpdateChapterRequest,
) (*ent.Chapter, error) {
	tx, err := client.Tx(ctx)
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
	row, err := txClient.Chapter.Query().Where(chapter.ID(chapterID)).WithNovel().Only(ctx)
	if err != nil {
		return nil, err
	}
	novelRow, err := row.Edges.NovelOrErr()
	if err != nil {
		return nil, err
	}
	if err := lockGenerationNovel(ctx, txClient, novelRow.ID); err != nil {
		return nil, err
	}
	row, err = txClient.Chapter.Query().Where(
		chapter.ID(chapterID),
		chapter.HasNovelWith(novel.ID(novelRow.ID)),
	).Only(ctx)
	if err != nil {
		return nil, err
	}
	if req.Order != nil && *req.Order != row.Order {
		lookup := func(ctx context.Context, novelID, order int) (*ent.Chapter, error) {
			return lookupGenerationChapter(ctx, txClient, novelID, order)
		}
		if err := requireNoChapterSuccessor(ctx, txClient, novelRow.ID, row.Order); err != nil {
			return nil, err
		}
		if err := requireAvailableChapterOrder(ctx, novelRow.ID, *req.Order, chapterID, lookup); err != nil {
			return nil, err
		}
		if err := requirePreviousChapterOrder(ctx, novelRow.ID, *req.Order, chapterID, lookup); err != nil {
			return nil, err
		}
	}

	contentChanged := req.Content != nil && *req.Content != row.Content
	orderChanged := req.Order != nil && *req.Order != row.Order
	explicitStale := req.Status != nil && strings.TrimSpace(*req.Status) == string(domain.StatusStale) && row.Status != string(domain.StatusStale)
	targetInvalidated := contentChanged || orderChanged || explicitStale
	semanticChanged := targetInvalidated
	finalOrder := row.Order
	if orderChanged {
		semanticChanged = true
		finalOrder = *req.Order
	}
	staleBoundary := row.Order
	if finalOrder < staleBoundary {
		staleBoundary = finalOrder
	}
	update := txClient.Chapter.UpdateOneID(chapterID)
	if req.Title != nil {
		update.SetTitle(strings.TrimSpace(*req.Title))
	}
	if req.Order != nil {
		update.SetOrder(*req.Order)
		if orderChanged {
			update.SetStatus(string(domain.StatusStale)).
				SetLastBeat("").SetOpenLoops([]string{}).SetNextAction("")
		}
		if _, err := txClient.CharacterStateVersion.Update().
			Where(characterstateversion.ChapterID(chapterID)).
			SetChapterIndex(*req.Order).
			Save(ctx); err != nil {
			return nil, err
		}
		if _, err := txClient.WorldStateVersion.Update().
			Where(worldstateversion.ChapterID(chapterID)).
			SetChapterIndex(*req.Order).
			Save(ctx); err != nil {
			return nil, err
		}
		relationshipVersions, err := txClient.RelationshipStateVersion.Query().
			Where(relationshipstateversion.ChapterID(chapterID)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := txClient.RelationshipStateVersion.Update().
			Where(relationshipstateversion.ChapterID(chapterID)).
			SetChapterIndex(*req.Order).
			Save(ctx); err != nil {
			return nil, err
		}
		for _, version := range relationshipVersions {
			if err := databaseinfra.RebuildRelationshipCache(
				ctx, txClient, strconv.Itoa(novelRow.ID),
				version.SourceCharacterID, version.TargetCharacterID, version.RelationType,
			); err != nil {
				return nil, err
			}
		}
		if err := updateChapterMemoryMetadata(ctx, txClient, novelRow.ID, chapterID, func(metadata map[string]any) {
			metadata["chapter_index"] = *req.Order
		}); err != nil {
			return nil, err
		}
	}
	if req.Status != nil {
		requestedStatus := strings.TrimSpace(*req.Status)
		if row.Status == string(domain.StatusStale) && requestedStatus != string(domain.StatusStale) {
			return nil, errGenerationEarlierChapterStale
		}
		update.SetStatus(requestedStatus)
	}
	if req.Content != nil {
		update.SetContent(*req.Content)
		update.SetWordCount(wordCountOf(*req.Content))
		if contentChanged {
			update.SetStatus(string(domain.StatusStale)).
				SetLastBeat("").SetOpenLoops([]string{}).SetNextAction("")
		}
	}
	if targetInvalidated {
		update.SetStatus(string(domain.StatusStale)).
			SetDerivedStatus(string(domain.DerivedStatusFailed)).
			SetDerivedGenerationID("").
			SetLastBeat("").SetOpenLoops([]string{}).SetNextAction("")
	}
	row, err = update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if semanticChanged {
		if targetInvalidated {
			if err := invalidateChapterDerivedData(ctx, txClient, novelRow.ID, []int{chapterID}); err != nil {
				return nil, err
			}
		}
		if err := markFollowingChaptersStale(ctx, txClient, novelRow.ID, staleBoundary, chapterID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return row.Unwrap(), nil
}

func deleteChapterWithIntegrity(
	ctx context.Context,
	client *ent.Client,
	chapterID int,
) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	txClient := tx.Client()
	row, err := txClient.Chapter.Query().Where(chapter.ID(chapterID)).WithNovel().Only(ctx)
	if err != nil {
		return err
	}
	novelRow, err := row.Edges.NovelOrErr()
	if err != nil {
		return err
	}
	if err := lockGenerationNovel(ctx, txClient, novelRow.ID); err != nil {
		return err
	}
	row, err = txClient.Chapter.Query().Where(
		chapter.ID(chapterID),
		chapter.HasNovelWith(novel.ID(novelRow.ID)),
	).Only(ctx)
	if err != nil {
		return err
	}
	if err := requireNoChapterSuccessor(ctx, txClient, novelRow.ID, row.Order); err != nil {
		return err
	}
	characterVersions, err := txClient.CharacterStateVersion.Query().
		Where(characterstateversion.ChapterID(chapterID)).
		All(ctx)
	if err != nil {
		return err
	}
	characterIDs := make(map[int]struct{}, len(characterVersions))
	for _, version := range characterVersions {
		characterIDs[version.CharacterID] = struct{}{}
	}
	worldVersions, err := txClient.WorldStateVersion.Query().
		Where(worldstateversion.ChapterID(chapterID)).
		All(ctx)
	if err != nil {
		return err
	}
	worldIDs := make(map[int]struct{}, len(worldVersions))
	for _, version := range worldVersions {
		worldIDs[version.WorldSettingID] = struct{}{}
	}
	relationshipVersions, err := txClient.RelationshipStateVersion.Query().
		Where(relationshipstateversion.ChapterID(chapterID)).
		All(ctx)
	if err != nil {
		return err
	}
	type relationshipCacheKey struct {
		sourceID, targetID int
		relationType       string
	}
	relationshipKeys := make(map[relationshipCacheKey]struct{}, len(relationshipVersions))
	for _, version := range relationshipVersions {
		relationshipKeys[relationshipCacheKey{
			sourceID: version.SourceCharacterID, targetID: version.TargetCharacterID, relationType: version.RelationType,
		}] = struct{}{}
	}
	if _, err := txClient.CharacterStateVersion.Delete().Where(
		characterstateversion.ChapterID(chapterID),
	).Exec(ctx); err != nil {
		return err
	}
	if _, err := txClient.WorldStateVersion.Delete().Where(
		worldstateversion.ChapterID(chapterID),
	).Exec(ctx); err != nil {
		return err
	}
	if _, err := txClient.RelationshipStateVersion.Delete().Where(
		relationshipstateversion.ChapterID(chapterID),
	).Exec(ctx); err != nil {
		return err
	}
	if err := updateChapterMemoryMetadata(ctx, txClient, novelRow.ID, chapterID, func(metadata map[string]any) {
		metadata["chapter_status"] = "Stale"
	}); err != nil {
		return err
	}
	if err := txClient.Chapter.DeleteOneID(chapterID).Exec(ctx); err != nil {
		return err
	}
	for id := range characterIDs {
		latest, latestErr := txClient.CharacterStateVersion.Query().
			Where(characterstateversion.CharacterID(id), characterstateversion.Valid(true)).
			Order(characterstateversion.ByChapterIndex(sql.OrderDesc()), characterstateversion.ByID(sql.OrderDesc())).
			First(ctx)
		status := ""
		if latestErr == nil {
			status = latest.CurrentStatus
		} else if !ent.IsNotFound(latestErr) {
			return latestErr
		}
		if err := txClient.Character.UpdateOneID(id).
			SetCurrentStatus(status).
			SetStateVersioned(true).
			Exec(ctx); err != nil {
			return err
		}
	}
	for id := range worldIDs {
		latest, latestErr := txClient.WorldStateVersion.Query().
			Where(worldstateversion.WorldSettingID(id), worldstateversion.Valid(true)).
			Order(worldstateversion.ByChapterIndex(sql.OrderDesc()), worldstateversion.ByID(sql.OrderDesc())).
			First(ctx)
		state := ""
		if latestErr == nil {
			state = latest.CurrentState
		} else if !ent.IsNotFound(latestErr) {
			return latestErr
		}
		if err := txClient.WorldSetting.UpdateOneID(id).
			SetCurrentState(state).
			SetStateVersioned(true).
			Exec(ctx); err != nil {
			return err
		}
	}
	for key := range relationshipKeys {
		if err := databaseinfra.RebuildRelationshipCache(
			ctx,
			txClient,
			strconv.Itoa(novelRow.ID),
			key.sourceID,
			key.targetID,
			key.relationType,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *entGenerationChapterStore) Save(
	ctx context.Context,
	target *generationChapterTarget,
	state *agents.GenerationState,
) (int, error) {
	if err := validateGenerationChapterSave(target, state); err != nil {
		return 0, err
	}
	if target.NovelID <= 0 || target.NovelUpdatedAt.IsZero() {
		return 0, errGenerationChapterChanged
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	txClient := tx.Client()
	if err := lockGenerationNovel(ctx, txClient, target.NovelID); err != nil {
		if ent.IsNotFound(err) {
			return 0, errGenerationChapterChanged
		}
		return 0, err
	}
	novelUpdatedAt, err := generationNovelUpdatedAt(ctx, txClient, target.NovelID)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, errGenerationChapterChanged
		}
		return 0, err
	}
	if err := validateGenerationNovelSource(target.NovelUpdatedAt, novelUpdatedAt); err != nil {
		return 0, err
	}

	if target.isNew {
		if err := requireAvailableChapterOrder(ctx, target.NovelID, target.Order, 0, func(ctx context.Context, novelID, order int) (*ent.Chapter, error) {
			return lookupGenerationChapter(ctx, txClient, novelID, order)
		}); err != nil {
			return 0, err
		}
		if err := requireEarliestStaleTarget(ctx, txClient, target.NovelID, target.Order); err != nil {
			return 0, err
		}
		if target.Order > 1 {
			previous, previousErr := lookupPreviousChapter(ctx, txClient, target.NovelID, target.Order-1)
			if previousErr != nil {
				return 0, previousErr
			}
			if previous.Status == string(domain.StatusStale) || previous.DerivedStatus != string(domain.DerivedStatusReady) {
				return 0, errGenerationPreviousDerivedNotReady
			}
			currentPacket := agents.ContinuityPacket{
				LastBeat:   strings.TrimSpace(previous.LastBeat),
				OpenLoops:  append([]string(nil), previous.OpenLoops...),
				NextAction: strings.TrimSpace(previous.NextAction),
			}
			if !continuityPacketsEqual(target.PreviousContinuity, currentPacket) {
				return 0, errGenerationChapterChanged
			}
		}
		chapterTitleValue := target.Title
		if strings.TrimSpace(chapterTitleValue) == "" {
			chapterTitleValue = chapterTitle(target.Order)
		}
		row, createErr := txClient.Chapter.Create().
			SetNovelID(target.NovelID).
			SetTitle(chapterTitleValue).
			SetContent(state.Draft).
			SetWordCount(wordCountOf(state.Draft)).
			SetOrder(target.Order).
			SetStatus("Draft").
			SetDerivedStatus(string(domain.DerivedStatusPending)).
			SetDerivedGenerationID(state.GenerationID).
			SetLastBeat(state.Continuity.LastBeat).
			SetOpenLoops(state.Continuity.OpenLoops).
			SetNextAction(state.Continuity.NextAction).
			Save(ctx)
		if createErr != nil {
			return 0, createErr
		}
		if err := databaseinfra.InitializeDerivedTasks(ctx, txClient, row.ID, state.GenerationID, domain.DerivedTaskPending); err != nil {
			return 0, err
		}
		if err := markFollowingChaptersStale(ctx, txClient, target.NovelID, target.Order, row.ID); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		committed = true
		return row.ID, nil
	}

	_, err = txClient.Chapter.
		UpdateOneID(target.ID).
		Where(
			chapter.TitleEQ(target.Title),
			chapter.ContentEQ(target.Content),
			chapter.WordCountEQ(target.WordCount),
			chapter.OrderEQ(target.Order),
			chapter.StatusEQ(target.Status),
			chapter.DerivedStatusEQ(target.DerivedStatus),
			chapter.DerivedGenerationIDEQ(target.DerivedGenerationID),
			chapter.LastBeatEQ(target.LastBeat),
			predicate.Chapter(func(selector *sql.Selector) {
				openLoopsPredicate := sqljson.ValueEQ(chapter.FieldOpenLoops, target.OpenLoops)
				if len(target.OpenLoops) == 0 {
					openLoopsPredicate = sql.Or(
						sql.IsNull(chapter.FieldOpenLoops),
						openLoopsPredicate,
					)
				}
				selector.Where(openLoopsPredicate)
			}),
			chapter.NextActionEQ(target.NextAction),
			chapter.UpdatedAtEQ(target.UpdatedAt),
		).
		SetContent(state.Draft).
		SetWordCount(wordCountOf(state.Draft)).
		SetStatus("Draft").
		SetDerivedStatus(string(domain.DerivedStatusPending)).
		SetDerivedGenerationID(state.GenerationID).
		SetLastBeat(state.Continuity.LastBeat).
		SetOpenLoops(state.Continuity.OpenLoops).
		SetNextAction(state.Continuity.NextAction).
		Save(ctx)
	if ent.IsNotFound(err) {
		return 0, errGenerationChapterChanged
	}
	if err != nil {
		return 0, err
	}
	if err := databaseinfra.InitializeDerivedTasks(
		ctx,
		txClient,
		target.ID,
		state.GenerationID,
		domain.DerivedTaskPending,
	); err != nil {
		return 0, err
	}
	if err := invalidateChapterDerivedData(ctx, txClient, target.NovelID, []int{target.ID}); err != nil {
		return 0, err
	}
	if err := markFollowingChaptersStale(ctx, txClient, target.NovelID, target.Order, target.ID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return target.ID, nil
}

func (s *Server) finalizeDerivedAfterPublish(
	ctx context.Context,
	chapterID int,
	generationID string,
	publishErr error,
) error {
	if s.db == nil {
		return publishErr
	}
	row, err := s.db.Chapter.Get(ctx, chapterID)
	if err != nil {
		return errors.Join(publishErr, err)
	}
	if row.DerivedGenerationID != generationID {
		return errors.Join(publishErr, errGenerationChapterChanged)
	}
	if s.derivedTasks != nil {
		tasks, taskErr := s.derivedTasks.List(ctx, chapterID, generationID)
		if taskErr != nil {
			return errors.Join(publishErr, taskErr)
		}
		if len(tasks) > 0 {
			if _, reconcileErr := s.derivedTasks.Reconcile(ctx, chapterID, generationID); reconcileErr != nil {
				return errors.Join(publishErr, reconcileErr)
			}
			row, err = s.db.Chapter.Get(ctx, chapterID)
			if err != nil {
				return errors.Join(publishErr, err)
			}
			if row.DerivedGenerationID != generationID {
				return errors.Join(publishErr, errGenerationChapterChanged)
			}
			if publishErr != nil {
				return publishErr
			}
			if row.DerivedStatus != string(domain.DerivedStatusReady) {
				return fmt.Errorf("derived tasks incomplete: %s", row.DerivedStatus)
			}
			return nil
		}
	}
	if row.DerivedStatus != string(domain.DerivedStatusPending) {
		if row.DerivedStatus == string(domain.DerivedStatusReady) && publishErr == nil {
			return nil
		}
		return publishErr
	}
	status := domain.DerivedStatusReady
	if publishErr != nil {
		status = domain.DerivedStatusFailed
	}
	return errors.Join(publishErr, setChapterDerivedStatus(ctx, s.db, chapterID, generationID, status))
}

func (s *Server) setChapterDerivedStatus(
	ctx context.Context,
	chapterID int,
	generationID string,
	status domain.DerivedStatus,
) error {
	if s.db == nil {
		return nil
	}
	return setChapterDerivedStatus(ctx, s.db, chapterID, generationID, status)
}

func setChapterDerivedStatus(
	ctx context.Context,
	client *ent.Client,
	chapterID int,
	generationID string,
	status domain.DerivedStatus,
) error {
	if chapterID <= 0 || strings.TrimSpace(generationID) == "" {
		return errors.New("invalid derived status target")
	}
	_, err := client.Chapter.UpdateOneID(chapterID).Where(
		chapter.DerivedStatusEQ(string(domain.DerivedStatusPending)),
		chapter.DerivedGenerationIDEQ(generationID),
	).SetDerivedStatus(string(status)).Save(ctx)
	if ent.IsNotFound(err) {
		return errGenerationChapterChanged
	}
	return err
}

func continuityPacketsEqual(left, right agents.ContinuityPacket) bool {
	if left.LastBeat != right.LastBeat || left.NextAction != right.NextAction || len(left.OpenLoops) != len(right.OpenLoops) {
		return false
	}
	for index := range left.OpenLoops {
		if left.OpenLoops[index] != right.OpenLoops[index] {
			return false
		}
	}
	return true
}

func validateGenerationChapterSave(
	target *generationChapterTarget,
	state *agents.GenerationState,
) error {
	if target == nil {
		return errors.New("generation chapter target is nil")
	}
	if state == nil {
		return errors.New("generation state is nil")
	}
	if !state.IsApproved {
		return errors.New("generation state is not approved")
	}
	issues := agents.ValidateGeneratedContent(state.Draft)
	if len(issues) > 0 {
		codes := make([]string, 0, len(issues))
		for _, issue := range issues {
			codes = append(codes, issue.Code)
		}
		return fmt.Errorf(
			"generated chapter content failed validation: %s",
			strings.Join(codes, ", "),
		)
	}
	if err := agents.ValidateContinuityPacketAgainstDraft(&state.Continuity, state.Draft); err != nil {
		return fmt.Errorf("generated chapter continuity failed validation: %w", err)
	}
	return nil
}

func generationNovelSourceMatches(expected, actual time.Time) bool {
	return !expected.IsZero() && expected.Equal(actual)
}

func validateGenerationNovelSource(expected, actual time.Time) error {
	if !generationNovelSourceMatches(expected, actual) {
		return errGenerationChapterChanged
	}
	return nil
}

func generationNovelUpdatedAt(
	ctx context.Context,
	client *ent.Client,
	novelID int,
) (time.Time, error) {
	row, err := client.Novel.Query().Where(novel.ID(novelID)).Only(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return row.UpdatedAt, nil
}

func generationChapterTargetFromRow(row *ent.Chapter) *generationChapterTarget {
	return &generationChapterTarget{
		ID:                  row.ID,
		Title:               row.Title,
		Content:             row.Content,
		WordCount:           row.WordCount,
		Order:               row.Order,
		Status:              row.Status,
		DerivedStatus:       row.DerivedStatus,
		DerivedGenerationID: row.DerivedGenerationID,
		LastBeat:            row.LastBeat,
		OpenLoops:           append([]string(nil), row.OpenLoops...),
		NextAction:          row.NextAction,
		UpdatedAt:           row.UpdatedAt,
	}
}

type activeGeneration struct {
	generationID string
	ctx          context.Context
	cancel       context.CancelCauseFunc
	finished     bool
}

type generationGuard struct {
	mu       sync.Mutex
	active   map[int]activeGeneration
	mutating map[int]bool
}

func newGenerationGuard() *generationGuard {
	return &generationGuard{active: make(map[int]activeGeneration), mutating: make(map[int]bool)}
}

func (g *generationGuard) acquireMutation(novelID int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, active := g.active[novelID]; active || g.mutating[novelID] {
		return false
	}
	g.mutating[novelID] = true
	return true
}

func (g *generationGuard) releaseMutation(novelID int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.mutating, novelID)
}

func (g *generationGuard) acquire(
	novelID int,
	generationID string,
	ctx context.Context,
	cancel context.CancelCauseFunc,
) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.active[novelID]; exists || g.mutating[novelID] {
		return false
	}
	g.active[novelID] = activeGeneration{
		generationID: generationID,
		ctx:          ctx,
		cancel:       cancel,
	}
	return true
}

func (g *generationGuard) cancel(
	novelID int,
	generationID string,
	cause error,
) generationCancelResult {
	g.mu.Lock()
	defer g.mu.Unlock()

	active, exists := g.active[novelID]
	if !exists {
		return generationCancelNotFound
	}
	if active.generationID != generationID {
		return generationCancelConflict
	}
	if active.finished {
		return generationCancelConflict
	}
	active.cancel(cause)
	return generationCancelAccepted
}

func (g *generationGuard) finish(
	novelID int,
	generationID string,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	active, exists := g.active[novelID]
	if !exists || active.generationID != generationID {
		return nil
	}
	active.finished = true
	g.active[novelID] = active
	return context.Cause(active.ctx)
}

func (g *generationGuard) release(novelID int, generationID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	active, exists := g.active[novelID]
	if !exists || active.generationID != generationID {
		return
	}
	delete(g.active, novelID)
}

type generationCancelResult int

const (
	generationCancelAccepted generationCancelResult = iota
	generationCancelNotFound
	generationCancelConflict
)

var (
	errGenerationCancelled = errors.New("generation cancelled by user")
	errGenerationProtocol  = errors.New("invalid generation stream event")
)

type ServerConfig struct {
	ListenAddr               string
	CorsOrigins              []string
	MaxConcurrentGenerations int
	ReadHeaderTimeout        time.Duration
	ReadTimeout              time.Duration
	WriteTimeout             time.Duration
	IdleTimeout              time.Duration
	GenerationTimeout        time.Duration
}

func defaultServerConfig() ServerConfig {
	return ServerConfig{
		ListenAddr:               "127.0.0.1:8081",
		CorsOrigins:              []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		MaxConcurrentGenerations: 2,
		ReadHeaderTimeout:        5 * time.Second,
		ReadTimeout:              15 * time.Second,
		WriteTimeout:             30 * time.Second,
		IdleTimeout:              60 * time.Second,
		GenerationTimeout:        30 * time.Minute,
	}
}

type modelCapacity struct {
	mu      sync.Mutex
	closing bool
	slots   chan struct{}
	active  sync.WaitGroup
}

func newModelCapacity(limit int) *modelCapacity {
	return &modelCapacity{slots: make(chan struct{}, limit)}
}

func (c *modelCapacity) tryAcquire() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return false
	}
	select {
	case c.slots <- struct{}{}:
		c.active.Add(1)
		return true
	default:
		return false
	}
}

func (c *modelCapacity) release() {
	<-c.slots
	c.active.Done()
}

func (c *modelCapacity) closeAndWait(ctx context.Context) error {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	done := make(chan struct{})
	go func() {
		c.active.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Server struct {
	engine          generationEngine
	db              *ent.Client
	derivedTasks    domain.DerivedTaskRepository
	chapterStore    generationChapterStore
	router          *chi.Mux
	generationGuard *generationGuard
	config          ServerConfig
	corsOrigins     map[string]struct{}
	modelCapacity   *modelCapacity
	lifecycleCtx    context.Context
	cancelLifecycle context.CancelFunc
}

func NewServer(engine *workflows.WorkflowEngine, db *ent.Client, configs ...ServerConfig) *Server {
	var engineAdapter generationEngine
	if engine != nil {
		engineAdapter = engine
	}
	return newServerWithConfig(engineAdapter, db, firstServerConfig(configs))
}

func firstServerConfig(configs []ServerConfig) ServerConfig {
	if len(configs) == 0 {
		return defaultServerConfig()
	}
	return configs[0]
}

func newServer(engine generationEngine, db *ent.Client) *Server {
	return newServerWithConfig(engine, db, defaultServerConfig())
}

func newServerWithConfig(engine generationEngine, db *ent.Client, cfg ServerConfig) *Server {
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	origins := make(map[string]struct{}, len(cfg.CorsOrigins))
	for _, origin := range cfg.CorsOrigins {
		origins[origin] = struct{}{}
	}
	s := &Server{
		engine:          engine,
		db:              db,
		derivedTasks:    nil,
		router:          chi.NewRouter(),
		generationGuard: newGenerationGuard(),
		config:          cfg,
		corsOrigins:     origins,
		modelCapacity:   newModelCapacity(cfg.MaxConcurrentGenerations),
		lifecycleCtx:    lifecycleCtx,
		cancelLifecycle: cancelLifecycle,
	}
	if db != nil {
		s.chapterStore = &entGenerationChapterStore{client: db}
		s.derivedTasks = databaseinfra.NewDerivedTaskRepository(db)
	}

	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(s.corsMiddleware)
	s.router.Use(s.lifecycleMiddleware)
	s.router.Options("/*", s.HandleOptions)

	s.router.Get("/api/v1/novels", s.HandleListNovels)
	s.router.Post("/api/v1/novels", s.HandleCreateNovel)
	s.router.Get("/api/v1/novels/{id}", s.HandleGetNovel)
	s.router.Put("/api/v1/novels/{id}", s.HandleUpdateNovel)
	s.router.Get("/api/v1/novels/{id}/chapters", s.HandleListChapters)
	s.router.Post("/api/v1/novels/{id}/chapters", s.HandleCreateChapter)
	s.router.Get("/api/v1/chapters/{id}", s.HandleGetChapter)
	s.router.Put("/api/v1/chapters/{id}", s.HandleUpdateChapter)
	s.router.Delete("/api/v1/chapters/{id}", s.HandleDeleteChapter)
	s.router.Post("/api/v1/chapters/{id}/derived/retry", s.HandleRetryChapterDerived)
	s.router.Post("/api/v1/novel/generate", s.HandleGenerateChapter)
	s.router.Post("/api/v1/novels/{id}/generate/cancel", s.HandleCancelGeneration)
	s.router.Post("/api/v1/novel/preview-context", s.HandlePreviewContext)

	return s
}

type NovelSummary struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	Tags        []string  `json:"tags,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type NovelDetail struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Idea        string    `json:"idea,omitempty"`
	Outline     string    `json:"outline,omitempty"`
	Status      string    `json:"status"`
	Tags        []string  `json:"tags,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const maxDerivedAPIErrorRunes = 1000

var derivedSecretPattern = regexp.MustCompile(`(?i)["']?\b(?:generation_id|lease_token|task_id)\b["']?\s*[:=]\s*["']?[^"'\s,;}]+["']?`)

type ChapterItem struct {
	ID               string    `json:"id"`
	NovelID          string    `json:"novel_id"`
	Title            string    `json:"title"`
	Content          string    `json:"content"`
	WordCount        int       `json:"word_count"`
	Order            int       `json:"order"`
	Status           string    `json:"status"`
	DerivedStatus    string    `json:"derived_status"`
	DerivedRetryable bool      `json:"derived_retryable"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ChapterDetailItem struct {
	ChapterItem
	DerivedTasks []DerivedTaskItem `json:"derived_tasks"`
}

type ChapterDerivedSnapshot struct {
	ChapterID        string            `json:"chapter_id"`
	DerivedStatus    string            `json:"derived_status"`
	DerivedRetryable bool              `json:"derived_retryable"`
	DerivedTasks     []DerivedTaskItem `json:"derived_tasks"`
	Error            string            `json:"error"`
}

type DerivedTaskItem struct {
	HandlerKey string `json:"handler_key"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	LastError  string `json:"last_error,omitempty"`
}

func derivedTaskItems(tasks []domain.DerivedTask) []DerivedTaskItem {
	items := make([]DerivedTaskItem, len(tasks))
	for index, task := range tasks {
		items[index] = DerivedTaskItem{
			HandlerKey: task.HandlerKey,
			Status:     string(task.Status),
			Attempts:   task.Attempts,
			LastError:  boundedDerivedAPIError(task.LastError),
		}
	}
	return items
}

func chapterItemFromRow(row *ent.Chapter, novelID string) ChapterItem {
	return ChapterItem{
		ID:               strconv.Itoa(row.ID),
		NovelID:          novelID,
		Title:            row.Title,
		Content:          row.Content,
		WordCount:        row.WordCount,
		Order:            row.Order,
		Status:           row.Status,
		DerivedStatus:    row.DerivedStatus,
		DerivedRetryable: chapterDerivedRetryable(row),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func chapterDerivedRetryable(row *ent.Chapter) bool {
	if row == nil || row.Status == string(domain.StatusStale) || strings.TrimSpace(row.DerivedGenerationID) == "" {
		return false
	}
	return row.DerivedStatus == string(domain.DerivedStatusFailed) || row.DerivedStatus == string(domain.DerivedStatusPending)
}

func boundedDerivedAPIError(value string) string {
	value = strings.TrimSpace(derivedSecretPattern.ReplaceAllString(value, "[internal identifier redacted]"))
	runes := []rune(value)
	if len(runes) > maxDerivedAPIErrorRunes {
		return string(runes[:maxDerivedAPIErrorRunes])
	}
	return value
}

func writeBoundedDerivedHTTPError(w http.ResponseWriter, err error, status int) {
	message := "derived task operation failed"
	if err != nil {
		message = boundedDerivedAPIError(err.Error())
	}
	http.Error(w, message, status)
}

func writeChapterDerivedSnapshot(w http.ResponseWriter, status int, snapshot ChapterDerivedSnapshot) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(snapshot)
}

type CreateNovelRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type UpdateNovelRequest struct {
	Idea    *string `json:"idea,omitempty"`
	Outline *string `json:"outline,omitempty"`
}

type CreateChapterRequest struct {
	Title   string `json:"title,omitempty"`
	Content string `json:"content,omitempty"`
	Order   int    `json:"order,omitempty"`
	Status  string `json:"status,omitempty"`
}

type UpdateChapterRequest struct {
	Title   *string `json:"title,omitempty"`
	Content *string `json:"content,omitempty"`
	Order   *int    `json:"order,omitempty"`
	Status  *string `json:"status,omitempty"`
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if _, allowed := s.corsOrigins[origin]; allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
			} else {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
		} else if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			http.Error(w, "cross-site request not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) lifecycleMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancelCause(r.Context())
		stop := context.AfterFunc(s.lifecycleCtx, func() {
			cancel(context.Cause(s.lifecycleCtx))
		})
		defer func() {
			stop()
			cancel(context.Canceled)
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) HandleListNovels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	rows, err := s.db.Novel.
		Query().
		Order(ent.Desc(novel.FieldUpdatedAt), ent.Desc(novel.FieldCreatedAt)).
		All(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	items := make([]NovelSummary, 0, len(rows))
	for _, n := range rows {
		items = append(items, NovelSummary{
			ID:          fmt.Sprintf("%d", n.ID),
			Title:       n.Title,
			Description: n.Description,
			Status:      n.Status,
			Tags:        n.Tags,
			CreatedAt:   n.CreatedAt,
			UpdatedAt:   n.UpdatedAt,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (s *Server) HandleOptions(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) HandleCreateNovel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	var req CreateNovelRequest
	if err := decodeStrictJSONObjectWithLimit(w, r, &req, []string{"title", "description", "type", "tags"}, 1<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	description := strings.TrimSpace(req.Description)
	novelType := strings.TrimSpace(req.Type)
	tags := make([]string, 0, len(req.Tags)+1)
	if novelType != "" {
		tags = append(tags, novelType)
	}
	for _, t := range req.Tags {
		tt := strings.TrimSpace(t)
		if tt == "" {
			continue
		}
		if novelType != "" && tt == novelType {
			continue
		}
		tags = append(tags, tt)
	}

	row, err := s.db.Novel.
		Create().
		SetTitle(title).
		SetDescription(description).
		SetIdea("").
		SetOutline("").
		SetStatus("Draft").
		SetTags(tags).
		Save(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	item := NovelSummary{
		ID:          fmt.Sprintf("%d", row.ID),
		Title:       row.Title,
		Description: row.Description,
		Status:      row.Status,
		Tags:        row.Tags,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleGetNovel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, parseErr := parseIntParam(chi.URLParam(r, "id"))
	if parseErr != nil {
		http.Error(w, parseErr.Error(), http.StatusBadRequest)
		return
	}

	row, err := s.db.Novel.
		Query().
		Where(novel.ID(id)).
		WithChapters(func(q *ent.ChapterQuery) {
			q.Order(ent.Asc(chapter.FieldOrder), ent.Asc(chapter.FieldCreatedAt))
		}).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "novel not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	item := NovelDetail{
		ID:          fmt.Sprintf("%d", row.ID),
		Title:       row.Title,
		Description: row.Description,
		Idea:        row.Idea,
		Outline:     row.Outline,
		Status:      row.Status,
		Tags:        row.Tags,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	chapters := make([]ChapterItem, 0, len(row.Edges.Chapters))
	for _, c := range row.Edges.Chapters {
		chapters = append(chapters, chapterItemFromRow(c, item.ID))
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"item":     item,
		"chapters": chapters,
	})
}

func (s *Server) HandleUpdateNovel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, parseErr := parseIntParam(chi.URLParam(r, "id"))
	if parseErr != nil {
		http.Error(w, parseErr.Error(), http.StatusBadRequest)
		return
	}

	var req UpdateNovelRequest
	if err := decodeStrictJSONObjectWithLimit(w, r, &req, []string{"idea", "outline"}, 5<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	upd := s.db.Novel.UpdateOneID(id)
	if req.Idea != nil {
		upd.SetIdea(strings.TrimSpace(*req.Idea))
	}
	if req.Outline != nil {
		upd.SetOutline(strings.TrimSpace(*req.Outline))
	}

	row, saveErr := upd.Save(r.Context())
	if saveErr != nil {
		if ent.IsNotFound(saveErr) {
			http.Error(w, "novel not found", http.StatusNotFound)
			return
		}
		http.Error(w, saveErr.Error(), http.StatusInternalServerError)
		return
	}

	item := NovelDetail{
		ID:          fmt.Sprintf("%d", row.ID),
		Title:       row.Title,
		Description: row.Description,
		Idea:        row.Idea,
		Outline:     row.Outline,
		Status:      row.Status,
		Tags:        row.Tags,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleListChapters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	novelID, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := 50
	offset := 0
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("offset")); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n >= 0 {
			offset = n
		}
	}

	rows, err := s.db.Chapter.
		Query().
		Where(chapter.HasNovelWith(novel.ID(novelID))).
		Order(ent.Asc(chapter.FieldOrder), ent.Asc(chapter.FieldCreatedAt)).
		Limit(limit).
		Offset(offset).
		All(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	items := make([]ChapterItem, 0, len(rows))
	for _, c := range rows {
		items = append(items, chapterItemFromRow(c, strconv.Itoa(novelID)))
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (s *Server) HandleGetChapter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	row, err := s.db.Chapter.
		Query().
		Where(chapter.ID(id)).
		WithNovel().
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	novelID := ""
	if row.Edges.Novel != nil {
		novelID = strconv.Itoa(row.Edges.Novel.ID)
	}

	tasks := []domain.DerivedTask{}
	if generationID := strings.TrimSpace(row.DerivedGenerationID); generationID != "" && s.derivedTasks != nil {
		tasks, err = s.derivedTasks.List(r.Context(), row.ID, generationID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	item := ChapterDetailItem{
		ChapterItem:  chapterItemFromRow(row, novelID),
		DerivedTasks: derivedTaskItems(tasks),
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleCreateChapter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	novelID, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !s.generationGuard.acquireMutation(novelID) {
		http.Error(w, "该小说正在生成或修改，不能修改章节", http.StatusConflict)
		return
	}
	defer s.generationGuard.releaseMutation(novelID)

	var req CreateChapterRequest
	if err := decodeStrictJSONObjectWithLimit(w, r, &req, []string{"title", "content", "order", "status"}, 5<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	order := req.Order
	title := strings.TrimSpace(req.Title)
	content := req.Content
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "Draft"
	}

	row, err := createChapterWithIntegrity(
		r.Context(),
		s.db,
		novelID,
		order,
		title,
		content,
		status,
	)
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "novel not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), chapterMutationHTTPStatus(err))
		return
	}
	item := chapterItemFromRow(row, strconv.Itoa(novelID))

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleUpdateChapter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	chapterRow, queryErr := s.db.Chapter.Query().Where(chapter.ID(id)).WithNovel().Only(r.Context())
	if queryErr != nil {
		if ent.IsNotFound(queryErr) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		http.Error(w, queryErr.Error(), http.StatusInternalServerError)
		return
	}
	chapterNovel, queryErr := chapterRow.Edges.NovelOrErr()
	if queryErr != nil {
		http.Error(w, queryErr.Error(), http.StatusInternalServerError)
		return
	}
	if !s.generationGuard.acquireMutation(chapterNovel.ID) {
		http.Error(w, "该小说正在生成或修改，不能修改章节", http.StatusConflict)
		return
	}
	defer s.generationGuard.releaseMutation(chapterNovel.ID)

	var req UpdateChapterRequest
	if err := decodeStrictJSONObjectWithLimit(w, r, &req, []string{"title", "content", "order", "status"}, 10<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Order != nil && *req.Order <= 0 {
		http.Error(w, "order must be > 0", http.StatusBadRequest)
		return
	}
	row, saveErr := updateChapterWithIntegrity(r.Context(), s.db, id, req)
	if saveErr != nil {
		if ent.IsNotFound(saveErr) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		http.Error(w, saveErr.Error(), chapterMutationHTTPStatus(saveErr))
		return
	}

	novelID := ""
	n, queryErr := row.QueryNovel().Only(r.Context())
	if queryErr == nil && n != nil {
		novelID = fmt.Sprintf("%d", n.ID)
	}

	item := chapterItemFromRow(row, novelID)

	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func (s *Server) HandleRetryChapterDerived(w http.ResponseWriter, r *http.Request) {
	if s.db == nil || s.engine == nil {
		http.Error(w, "server not configured", http.StatusInternalServerError)
		return
	}
	chapterID, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	row, err := s.db.Chapter.Query().Where(chapter.ID(chapterID)).WithNovel().Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		writeBoundedDerivedHTTPError(w, err, http.StatusInternalServerError)
		return
	}
	novelRow, err := row.Edges.NovelOrErr()
	if err != nil {
		writeBoundedDerivedHTTPError(w, err, http.StatusInternalServerError)
		return
	}
	if !s.generationGuard.acquireMutation(novelRow.ID) {
		http.Error(w, "该小说正在生成或修改，不能重试派生处理", http.StatusConflict)
		return
	}
	defer s.generationGuard.releaseMutation(novelRow.ID)
	if !chapterDerivedRetryable(row) {
		http.Error(w, "chapter derived data is not retryable", http.StatusConflict)
		return
	}
	generationID := row.DerivedGenerationID
	if s.derivedTasks != nil {
		tasks, listErr := s.derivedTasks.List(r.Context(), row.ID, generationID)
		if listErr != nil {
			writeBoundedDerivedHTTPError(w, listErr, http.StatusInternalServerError)
			return
		}
		if len(tasks) != len(domain.DerivedHandlerKeys) {
			if err := s.derivedTasks.Initialize(r.Context(), row.ID, generationID, domain.DerivedTaskPending); err != nil {
				writeBoundedDerivedHTTPError(w, err, http.StatusInternalServerError)
				return
			}
		}
	}
	updated, err := s.db.Chapter.UpdateOneID(row.ID).Where(
		chapter.DerivedStatusIn(string(domain.DerivedStatusFailed), string(domain.DerivedStatusPending)),
		chapter.DerivedGenerationIDEQ(generationID),
	).SetDerivedStatus(string(domain.DerivedStatusPending)).Save(r.Context())
	if err != nil {
		writeBoundedDerivedHTTPError(w, err, chapterMutationHTTPStatus(err))
		return
	}
	state := &agents.GenerationState{
		GenerationID: generationID,
		NovelID:      strconv.Itoa(novelRow.ID),
		ChapterID:    strconv.Itoa(updated.ID),
		ChapterIndex: updated.Order,
		Draft:        updated.Content,
	}
	retryCtx, retryCancel := context.WithTimeout(s.lifecycleCtx, s.config.GenerationTimeout)
	defer retryCancel()
	publishErr := s.engine.PublishChapterGenerated(retryCtx, state)
	statusCtx, statusCancel := context.WithTimeout(s.lifecycleCtx, 10*time.Second)
	defer statusCancel()
	latest := updated
	chapterLoaded := false
	var tasks []domain.DerivedTask
	settlementErr := publishErr
	if refreshed, refreshErr := s.db.Chapter.Get(statusCtx, updated.ID); refreshErr != nil {
		settlementErr = errors.Join(settlementErr, refreshErr)
	} else {
		latest = refreshed
		chapterLoaded = true
	}
	if chapterLoaded && latest.DerivedGenerationID != generationID {
		settlementErr = errors.Join(settlementErr, errGenerationChapterChanged)
	}
	if latest.DerivedGenerationID == generationID && s.derivedTasks != nil {
		if _, reconcileErr := s.derivedTasks.Reconcile(statusCtx, latest.ID, generationID); reconcileErr != nil {
			settlementErr = errors.Join(settlementErr, reconcileErr)
		} else if refreshed, refreshErr := s.db.Chapter.Get(statusCtx, latest.ID); refreshErr != nil {
			settlementErr = errors.Join(settlementErr, refreshErr)
		} else {
			latest = refreshed
		}
		listed, listErr := s.derivedTasks.List(statusCtx, latest.ID, generationID)
		if listErr != nil {
			settlementErr = errors.Join(settlementErr, listErr)
		} else {
			tasks = listed
		}
	}
	if len(tasks) == 0 && latest.DerivedGenerationID == generationID && latest.DerivedStatus == string(domain.DerivedStatusPending) {
		status := domain.DerivedStatusReady
		if publishErr != nil {
			status = domain.DerivedStatusFailed
		}
		if statusErr := setChapterDerivedStatus(statusCtx, s.db, latest.ID, generationID, status); statusErr != nil {
			settlementErr = errors.Join(settlementErr, statusErr)
		} else if refreshed, refreshErr := s.db.Chapter.Get(statusCtx, latest.ID); refreshErr != nil {
			settlementErr = errors.Join(settlementErr, refreshErr)
		} else {
			latest = refreshed
		}
	}
	if latest.DerivedStatus != string(domain.DerivedStatusReady) && settlementErr == nil {
		settlementErr = fmt.Errorf("derived tasks incomplete: %s", latest.DerivedStatus)
	}
	snapshotError := ""
	if settlementErr != nil {
		snapshotError = settlementErr.Error()
	}
	snapshot := ChapterDerivedSnapshot{
		ChapterID:        strconv.Itoa(latest.ID),
		DerivedStatus:    latest.DerivedStatus,
		DerivedRetryable: chapterDerivedRetryable(latest),
		DerivedTasks:     derivedTaskItems(tasks),
		Error:            boundedDerivedAPIError(snapshotError),
	}
	if settlementErr != nil {
		writeChapterDerivedSnapshot(w, http.StatusInternalServerError, snapshot)
		return
	}
	writeChapterDerivedSnapshot(w, http.StatusOK, snapshot)
}

func (s *Server) HandleDeleteChapter(w http.ResponseWriter, r *http.Request) {

	if s.db == nil {
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	id, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	chapterRow, queryErr := s.db.Chapter.Query().Where(chapter.ID(id)).WithNovel().Only(r.Context())
	if queryErr != nil {
		if ent.IsNotFound(queryErr) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		http.Error(w, queryErr.Error(), http.StatusInternalServerError)
		return
	}
	chapterNovel, queryErr := chapterRow.Edges.NovelOrErr()
	if queryErr != nil {
		http.Error(w, queryErr.Error(), http.StatusInternalServerError)
		return
	}
	if !s.generationGuard.acquireMutation(chapterNovel.ID) {
		http.Error(w, "该小说正在生成或修改，不能删除章节", http.StatusConflict)
		return
	}
	defer s.generationGuard.releaseMutation(chapterNovel.ID)

	if err := deleteChapterWithIntegrity(r.Context(), s.db, id); err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "chapter not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), chapterMutationHTTPStatus(err))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.config.ListenAddr,
		Handler:           s.router,
		ReadHeaderTimeout: s.config.ReadHeaderTimeout,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       s.config.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

func (s *Server) Shutdown(ctx context.Context, httpServer *http.Server) error {
	s.cancelLifecycle()
	shutdownErr := httpServer.Shutdown(ctx)
	capacityErr := s.modelCapacity.closeAndWait(ctx)
	if shutdownErr != nil {
		return shutdownErr
	}
	return capacityErr
}

func parseIntParam(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty id")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid id: %q", v)
	}
	return n, nil
}

func wordCountOf(s string) int {
	return len([]rune(strings.TrimSpace(s)))
}

func chapterTitle(index int) string {
	if index <= 0 {
		return "未命名章节"
	}
	return fmt.Sprintf("第%d章", index)
}

type CancelGenerationRequest struct {
	GenerationID string `json:"generation_id"`
}

var errRequestBodyTooLarge = errors.New("request body too large")

func decodeStrictJSONObjectWithLimit(w http.ResponseWriter, r *http.Request, dst any, fields []string, maxBytes int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errRequestBodyTooLarge
		}
		return fmt.Errorf("invalid json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errRequestBodyTooLarge
		}
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return fmt.Errorf("invalid trailing json: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errors.New("request body must be a JSON object")
	}
	for _, field := range fields {
		if value, ok := object[field]; ok && string(bytes.TrimSpace(value)) == "null" {
			return fmt.Errorf("%s must not be null", field)
		}
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(dst); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

func decodeStrictJSONObject(w http.ResponseWriter, r *http.Request, dst any, fields []string) error {
	return decodeStrictJSONObjectWithLimit(w, r, dst, fields, 1<<20)
}

type GenerateChapterRequest struct {
	NovelID         *int   `json:"novel_id"`
	ChapterID       *int   `json:"chapter_id,omitempty"`
	Persist         *bool  `json:"persist,omitempty"`
	ChapterIndex    *int   `json:"chapter_index,omitempty"`
	Outline         string `json:"outline,omitempty"`
	Idea            string `json:"idea,omitempty"`
	ExistingOutline string `json:"existing_outline,omitempty"`
	OutlineStart    *int   `json:"outline_start,omitempty"`
	OutlineEnd      *int   `json:"outline_end,omitempty"`
	EditorNotes     string `json:"editor_notes,omitempty"`
	ManualContext   string `json:"manual_context,omitempty"`
}

func decodeGenerateChapterRequest(w http.ResponseWriter, r *http.Request) (GenerateChapterRequest, error) {
	var req GenerateChapterRequest
	err := decodeStrictJSONObject(w, r, &req, []string{"novel_id", "chapter_id", "persist", "chapter_index", "outline", "idea", "existing_outline", "outline_start", "outline_end", "editor_notes", "manual_context"})
	return req, err
}

func normalizeGenerateChapterRequest(req GenerateChapterRequest) (GenerateChapterRequest, error) {
	if req.NovelID == nil || *req.NovelID <= 0 {
		return req, errors.New("novel_id must be a positive integer")
	}
	if req.ChapterID != nil && *req.ChapterID <= 0 {
		return req, errors.New("chapter_id must be a positive integer")
	}
	if req.ChapterIndex != nil && *req.ChapterIndex <= 0 {
		return req, errors.New("chapter_index must be a positive integer")
	}
	if (req.OutlineStart == nil) != (req.OutlineEnd == nil) {
		return req, errors.New("outline_start and outline_end must be provided together")
	}
	if req.OutlineStart != nil && (*req.OutlineStart <= 0 || *req.OutlineEnd <= 0 || *req.OutlineStart > *req.OutlineEnd) {
		return req, errors.New("outline range is invalid")
	}
	if req.ChapterIndex == nil {
		value := 1
		req.ChapterIndex = &value
	}
	if req.Persist == nil {
		value := true
		req.Persist = &value
	}
	req.Outline = strings.TrimSpace(req.Outline)
	req.Idea = strings.TrimSpace(req.Idea)
	req.ExistingOutline = strings.TrimSpace(req.ExistingOutline)
	req.EditorNotes = strings.TrimSpace(req.EditorNotes)
	req.ManualContext = strings.TrimSpace(req.ManualContext)
	return req, nil
}

type generationStatus string

const (
	generationStatusSuccess   generationStatus = "success"
	generationStatusError     generationStatus = "error"
	generationStatusCancelled generationStatus = "cancelled"
)

type generationResult struct {
	GenerationID string           `json:"generation_id"`
	Status       generationStatus `json:"status"`
	Message      string           `json:"message,omitempty"`
	ErrorCode    string           `json:"error_code,omitempty"`
	ChapterID    string           `json:"chapter_id,omitempty"`
	Persisted    bool             `json:"persisted,omitempty"`
}

const generationSSEWriteTimeout = 15 * time.Second

type generationSSEWriter struct {
	writer       http.ResponseWriter
	controller   *http.ResponseController
	terminalSent bool
}

func newGenerationSSEWriter(
	writer http.ResponseWriter,
	controller *http.ResponseController,
) *generationSSEWriter {
	return &generationSSEWriter{writer: writer, controller: controller}
}

func (w *generationSSEWriter) send(event string, payload any) error {
	if err := w.controller.SetWriteDeadline(time.Now().Add(generationSSEWriteTimeout)); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(
		w.writer,
		"event: %s\ndata: %s\n\n",
		event,
		data,
	); err != nil {
		return err
	}
	if err := w.controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func (w *generationSSEWriter) terminal(result generationResult) error {
	if w.terminalSent {
		return errors.New("generation terminal already sent")
	}
	w.terminalSent = true
	return w.send("terminal", result)
}

var generationDiagnosticStages = map[string]bool{
	"admission":             true,
	"context_preparation":   true,
	"chapter_generation":    true,
	"continuity_extraction": true,
	"persistence":           true,
	"derived_processing":    true,
	"terminal_delivery":     true,
}

var generationDiagnosticStatuses = map[string]bool{
	"started":   true,
	"success":   true,
	"error":     true,
	"cancelled": true,
	"rejected":  true,
}

var generationDiagnosticErrorCodes = map[string]bool{
	"generation_cancelled":        true,
	"generation_timeout":          true,
	"generation_protocol_error":   true,
	"provider_busy":               true,
	"provider_error":              true,
	"context_preparation_failed":  true,
	"review_failed":               true,
	"chapter_changed":             true,
	"derived_processing_failed":   true,
	"generation_failed":           true,
	"terminal_delivery_failed":    true,
	"write_deadline_clear_failed": true,
}

func logGenerationDiagnostic(
	generationID string,
	stage string,
	status string,
	errorCode string,
	providerStatus int,
) {
	if !generationDiagnosticStages[stage] {
		stage = "admission"
	}
	if !generationDiagnosticStatuses[status] {
		status = "error"
	}
	if errorCode != "" && !generationDiagnosticErrorCodes[errorCode] {
		errorCode = "generation_failed"
	}
	if providerStatus < 100 || providerStatus > 599 {
		providerStatus = 0
	}

	fields := fmt.Sprintf(
		"[Generation] generation_id=%s stage=%s status=%s",
		sanitizeGenerationDiagnosticID(generationID),
		stage,
		status,
	)
	if errorCode != "" {
		fields += " error_code=" + errorCode
	}
	if providerStatus != 0 {
		fields += " provider_status=" + strconv.Itoa(providerStatus)
	}
	log.Print(fields)
}

func sanitizeGenerationDiagnosticID(generationID string) string {
	if generationID == "" || len(generationID) > 64 {
		return "invalid"
	}
	for _, char := range generationID {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' {
			return "invalid"
		}
	}
	return generationID
}

func generationProviderStatus(err error) int {
	var providerErr *llminfra.ProviderError
	if errors.As(err, &providerErr) && providerErr.StatusCode >= 100 && providerErr.StatusCode <= 599 {
		return providerErr.StatusCode
	}
	return 0
}

func publicGenerationError(err error) (string, string) {
	if errors.Is(err, errGenerationCancelled) || errors.Is(err, context.Canceled) {
		return "generation_cancelled", "生成已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "generation_timeout", "生成超时，请稍后重试"
	}
	if errors.Is(err, errGenerationProtocol) {
		return "generation_protocol_error", "生成连接协议异常"
	}
	if errors.Is(err, errGenerationChapterChanged) {
		return "chapter_changed", "章节在生成期间已被修改，未覆盖现有内容"
	}
	var providerErr *llminfra.ProviderError
	if errors.As(err, &providerErr) {
		if providerErr.Retryable {
			return "provider_busy", "模型服务繁忙，请稍后重试"
		}
		return "provider_error", "模型服务请求失败，请检查配置后重试"
	}
	message := strings.ToLower(err.Error())
	if strings.HasPrefix(message, "reviewer agent") || strings.Contains(message, "failed to generate acceptable chapter") {
		return "review_failed", "正文审查未通过，请重试"
	}
	if strings.HasPrefix(message, "context preparation failed") || strings.HasPrefix(message, "architect agent") || strings.HasPrefix(message, "librarian") {
		return "context_preparation_failed", "上下文准备失败，请重试"
	}
	return "generation_failed", "生成失败，请重试"
}

func classifyGenerationResult(
	generationID string,
	cause error,
	runErr error,
	finalState *agents.GenerationState,
) generationResult {
	if runErr == nil && finalState != nil &&
		!errors.Is(cause, errGenerationCancelled) &&
		!errors.Is(cause, errGenerationProtocol) &&
		!errors.Is(cause, context.DeadlineExceeded) {
		cause = nil
	}
	switch {
	case errors.Is(cause, errGenerationCancelled):
		return generationResult{GenerationID: generationID, Status: generationStatusCancelled, Message: "生成已取消", ErrorCode: "generation_cancelled"}
	case cause != nil:
		code, message := publicGenerationError(cause)
		return generationResult{GenerationID: generationID, Status: generationStatusError, Message: message, ErrorCode: code}
	case runErr != nil:
		code, message := publicGenerationError(runErr)
		return generationResult{GenerationID: generationID, Status: generationStatusError, Message: message, ErrorCode: code}
	case finalState == nil:
		return generationResult{GenerationID: generationID, Status: generationStatusError, Message: "生成失败，请重试", ErrorCode: "generation_failed"}
	default:
		return generationResult{
			GenerationID: generationID,
			Status:       generationStatusSuccess,
			Message:      "生成完成",
		}
	}
}

func (s *Server) HandleCancelGeneration(
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	novelID, err := parseIntParam(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req CancelGenerationRequest
	if err := decodeStrictJSONObjectWithLimit(w, r, &req, []string{"generation_id"}, 1<<20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.GenerationID = strings.TrimSpace(req.GenerationID)
	if req.GenerationID == "" {
		http.Error(w, "generation_id is required", http.StatusBadRequest)
		return
	}

	switch s.generationGuard.cancel(
		novelID,
		req.GenerationID,
		errGenerationCancelled,
	) {
	case generationCancelAccepted:
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"generation_id": req.GenerationID,
			"status":        "cancelling",
		})
	case generationCancelNotFound:
		http.Error(w, "no active generation for novel", http.StatusNotFound)
	case generationCancelConflict:
		http.Error(w, "generation_id does not match active generation", http.StatusConflict)
	}
}

func (s *Server) HandleGenerateChapter(w http.ResponseWriter, r *http.Request) {
	request, err := decodeGenerateChapterRequest(w, r)
	if err != nil {
		if errors.Is(err, errRequestBodyTooLarge) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	request, err = normalizeGenerateChapterRequest(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.engine == nil {
		http.Error(w, "engine not configured", http.StatusInternalServerError)
		return
	}
	novelIDInt := *request.NovelID
	novelID := strconv.Itoa(novelIDInt)
	chapterIndex := *request.ChapterIndex
	persist := *request.Persist
	chapterIDStr := ""
	if request.ChapterID != nil {
		chapterIDStr = strconv.Itoa(*request.ChapterID)
	}
	outline := request.Outline
	idea := request.Idea
	editorNotes := request.EditorNotes
	manualContext := request.ManualContext
	existingOutline := request.ExistingOutline
	outlineStart, outlineEnd := 0, 0
	if request.OutlineStart != nil {
		outlineStart, outlineEnd = *request.OutlineStart, *request.OutlineEnd
	}
	generationID, err := agents.NewGenerationID()
	if err != nil {
		http.Error(w, "failed to create generation id", http.StatusInternalServerError)
		return
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(r.Context(), s.config.GenerationTimeout)
	defer deadlineCancel()
	generationDeadline, _ := deadlineCtx.Deadline()
	generationCtx, cancelGeneration := context.WithCancelCause(deadlineCtx)
	defer cancelGeneration(context.Canceled)
	if !s.generationGuard.acquire(
		novelIDInt,
		generationID,
		generationCtx,
		cancelGeneration,
	) {
		http.Error(w, "该小说正在生成，请等待当前任务完成后再试", http.StatusConflict)
		return
	}
	leaseOwnedByHandler := true
	defer func() {
		if leaseOwnedByHandler {
			s.generationGuard.release(novelIDInt, generationID)
		}
	}()
	if !s.modelCapacity.tryAcquire() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "模型正在处理其他任务，请稍后再试", http.StatusTooManyRequests)
		return
	}
	capacityOwnedByHandler := true
	defer func() {
		if capacityOwnedByHandler {
			s.modelCapacity.release()
		}
	}()

	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		logGenerationDiagnostic(
			generationID,
			"admission",
			"error",
			"write_deadline_clear_failed",
			0,
		)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")

	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}
	sse := newGenerationSSEWriter(w, http.NewResponseController(w))

	ctx := generationCtx
	finishSync := func(runErr error, finalState *agents.GenerationState) {
		cause := s.generationGuard.finish(novelIDInt, generationID)
		result := classifyGenerationResult(
			generationID,
			cause,
			runErr,
			finalState,
		)
		s.generationGuard.release(novelIDInt, generationID)
		leaseOwnedByHandler = false
		s.modelCapacity.release()
		capacityOwnedByHandler = false
		if r.Context().Err() == nil {
			if terminalErr := sse.terminal(result); terminalErr != nil {
				logGenerationDiagnostic(generationID, "terminal_delivery", "error", "terminal_delivery_failed", 0)
			} else if result.Status == generationStatusSuccess {
				logGenerationDiagnostic(generationID, "terminal_delivery", "success", "", 0)
			}
		}
	}

	if err := sse.send("start", map[string]string{
		"generation_id": generationID,
		"message":       "生成已开始",
	}); err != nil {
		cancelGeneration(err)
		return
	}

	var initialNovelUpdatedAt time.Time
	if s.db != nil {
		loadCtx, loadCancel := context.WithTimeout(r.Context(), 5*time.Second)
		row, qErr := s.db.Novel.Query().Where(novel.ID(novelIDInt)).Only(loadCtx)
		loadCancel()
		if qErr != nil {
			http.Error(w, "failed to load novel", http.StatusInternalServerError)
			return
		}
		initialNovelUpdatedAt = row.UpdatedAt
		if idea == "" {
			idea = strings.TrimSpace(row.Idea)
		}
		if outline == "" {
			outline = strings.TrimSpace(row.Outline)
		}
		if existingOutline == "" {
			existingOutline = strings.TrimSpace(row.Outline)
		}
	}

	if outline == "" && idea == "" && existingOutline == "" {
		http.Error(w, "Missing outline and idea (no saved outline found)", http.StatusBadRequest)
		return
	}

	var chapterTarget *generationChapterTarget
	if persist {
		if s.chapterStore == nil {
			finishSync(errors.New("database not configured"), nil)
			return
		}
		chapterIDInt := 0
		if strings.TrimSpace(chapterIDStr) != "" {
			chapterIDInt, err = parseIntParam(chapterIDStr)
			if err != nil {
				finishSync(err, nil)
				return
			}
		}
		prepareCtx, prepareCancel := context.WithTimeout(ctx, 10*time.Second)
		chapterTarget, err = s.chapterStore.Prepare(
			prepareCtx,
			novelIDInt,
			chapterIDInt,
			chapterIndex,
		)
		prepareCancel()
		if err != nil {
			finishSync(err, nil)
			return
		}
		if !initialNovelUpdatedAt.IsZero() &&
			!chapterTarget.NovelUpdatedAt.Equal(initialNovelUpdatedAt) {
			finishSync(errGenerationChapterChanged, nil)
			return
		}
	}

	streamChan := make(chan agents.GenerationStreamEvent)
	streamSink := func(streamCtx context.Context, event agents.GenerationStreamEvent) error {
		if event.Type != agents.GenerationStreamEventToken &&
			event.Type != agents.GenerationStreamEventRetry {
			cancelGeneration(errGenerationProtocol)
			return errGenerationProtocol
		}
		select {
		case streamChan <- event:
			return nil
		case <-streamCtx.Done():
			return streamCtx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	chapterID := ""
	chapterIndexForGeneration := chapterIndex
	if chapterTarget != nil {
		if chapterTarget.ID > 0 {
			chapterID = strconv.Itoa(chapterTarget.ID)
		}
		chapterIndexForGeneration = chapterTarget.Order
	}
	state := &agents.GenerationState{
		GenerationID:    generationID,
		NovelID:         novelID,
		ChapterID:       chapterID,
		ChapterIndex:    chapterIndexForGeneration,
		Idea:            idea,
		FullOutline:     outline,
		EditorNotes:     editorNotes,
		ManualContext:   manualContext,
		ExistingOutline: existingOutline,
		OutlineStart:    outlineStart,
		OutlineEnd:      outlineEnd,
		PreviousContinuity: func() agents.ContinuityPacket {
			if chapterTarget == nil {
				return agents.ContinuityPacket{}
			}
			return chapterTarget.PreviousContinuity
		}(),
	}

	logGenerationDiagnostic(generationID, "context_preparation", "started", "", 0)
	prepared, prepErr := s.engine.PrepareContext(ctx, state)
	if prepErr != nil {
		contextErr := fmt.Errorf("context preparation failed: %w", prepErr)
		code, _ := publicGenerationError(contextErr)
		logGenerationDiagnostic(
			generationID,
			"context_preparation",
			"error",
			code,
			generationProviderStatus(contextErr),
		)
		finishSync(contextErr, nil)
		return
	}
	if prepared == nil {
		logGenerationDiagnostic(
			generationID,
			"context_preparation",
			"error",
			"context_preparation_failed",
			0,
		)
		finishSync(errors.New("context preparation failed: no state returned"), nil)
		return
	}
	prepared.GenerationID = generationID
	prepared.StreamSink = streamSink
	prepared.NovelID = novelID

	meta := map[string]interface{}{
		"chapter_index": prepared.ChapterIndex,
		"chapter_id": func() any {
			if chapterID == "" {
				return nil
			}
			return chapterID
		}(),
		"context_stats": map[string]int{
			"context_lines":    1 + strings.Count(prepared.Context, "\n"),
			"scene_card_lines": 1 + strings.Count(prepared.SceneCard, "\n"),
		},
	}
	if err := sse.send("context_meta", meta); err != nil {
		cancelGeneration(err)
		return
	}

	resultChan := make(chan generationResult, 1)
	leaseOwnedByHandler = false
	capacityOwnedByHandler = false
	go func() {
		finalState, runErr := s.engine.RunChapterGeneration(ctx, prepared)
		failedStage := "chapter_generation"
		if runErr == nil && persist && finalState != nil {
			if chapterTarget.ID > 0 {
				finalState.ChapterID = strconv.Itoa(chapterTarget.ID)
			}
			finalState.NovelID = novelID
			finalState.GenerationID = generationID
			failedStage = "continuity_extraction"
			finalState, runErr = s.engine.ExtractContinuity(ctx, finalState)
			if finalState != nil {
				finalState.GenerationID = generationID
			}
		}
		cause := s.generationGuard.finish(novelIDInt, generationID)
		result := classifyGenerationResult(generationID, cause, runErr, finalState)
		if result.Status == generationStatusError {
			logGenerationDiagnostic(generationID, failedStage, "error", result.ErrorCode, generationProviderStatus(runErr))
		} else if result.Status == generationStatusCancelled {
			logGenerationDiagnostic(generationID, failedStage, "cancelled", result.ErrorCode, generationProviderStatus(runErr))
		}

		if result.Status == generationStatusSuccess && persist {
			postprocessCtx, cancelPostprocess := context.WithDeadline(
				s.lifecycleCtx,
				generationDeadline,
			)
			defer cancelPostprocess()
			saveCtx, saveCancel := context.WithTimeout(postprocessCtx, 10*time.Second)
			chapterID, saveErr := s.chapterStore.Save(saveCtx, chapterTarget, finalState)
			saveCancel()
			if saveErr != nil {
				code, message := publicGenerationError(saveErr)
				logGenerationDiagnostic(generationID, "persistence", "error", code, generationProviderStatus(saveErr))
				result = generationResult{GenerationID: generationID, Status: generationStatusError, Message: message, ErrorCode: code}
			} else {
				finalState.ChapterID = strconv.Itoa(chapterID)
				result.ChapterID = strconv.Itoa(chapterID)
				result.Persisted = true
				publishErr := s.engine.PublishChapterGenerated(postprocessCtx, finalState)
				statusCtx, statusCancel := context.WithTimeout(s.lifecycleCtx, 10*time.Second)
				derivedErr := s.finalizeDerivedAfterPublish(
					statusCtx,
					chapterID,
					generationID,
					publishErr,
				)
				statusCancel()
				if derivedErr != nil {
					logGenerationDiagnostic(generationID, "derived_processing", "error", "derived_processing_failed", 0)
					result = generationResult{GenerationID: generationID, Status: generationStatusError, Message: "正文已保存，但派生处理失败", ErrorCode: "derived_processing_failed", ChapterID: strconv.Itoa(chapterID), Persisted: true}
				}
			}
		}

		s.generationGuard.release(novelIDInt, generationID)
		s.modelCapacity.release()
		resultChan <- result
	}()

	streamEvents := (<-chan agents.GenerationStreamEvent)(streamChan)
	protocolFailed := false
	for {
		select {
		case <-r.Context().Done():
			return
		case result := <-resultChan:
			if r.Context().Err() != nil {
				return
			}
			if protocolFailed {
				result = generationResult{GenerationID: generationID, Status: generationStatusError, Message: "生成连接协议异常", ErrorCode: "generation_protocol_error"}
			}
			if terminalErr := sse.terminal(result); terminalErr != nil {
				logGenerationDiagnostic(generationID, "terminal_delivery", "error", "terminal_delivery_failed", 0)
				cancelGeneration(terminalErr)
			} else if result.Status == generationStatusSuccess {
				logGenerationDiagnostic(generationID, "terminal_delivery", "success", "", 0)
			}
			return
		case streamEvent := <-streamEvents:
			var sendErr error
			switch streamEvent.Type {
			case agents.GenerationStreamEventRetry:
				sendErr = sse.send("retry", map[string]interface{}{
					"retry_count": streamEvent.RetryCount,
					"critique":    streamEvent.Critique,
				})
			case agents.GenerationStreamEventToken:
				sendErr = sse.send("token", map[string]string{
					"token": streamEvent.Token,
				})
			default:
				protocolFailed = true
				streamEvents = nil
				cancelGeneration(errGenerationProtocol)
				continue
			}
			if sendErr != nil {
				cancelGeneration(sendErr)
				return
			}
		}
	}
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

type PreviewContextRequest struct {
	NovelID         *int   `json:"novel_id"`
	ChapterIndex    *int   `json:"chapter_index,omitempty"`
	Outline         string `json:"outline,omitempty"`
	Idea            string `json:"idea,omitempty"`
	ExistingOutline string `json:"existing_outline,omitempty"`
	OutlineStart    *int   `json:"outline_start,omitempty"`
	OutlineEnd      *int   `json:"outline_end,omitempty"`
	EditorNotes     string `json:"editor_notes,omitempty"`
	ManualContext   string `json:"manual_context,omitempty"`
}

func decodePreviewContextRequest(w http.ResponseWriter, r *http.Request) (PreviewContextRequest, error) {
	var req PreviewContextRequest
	if err := decodeStrictJSONObject(w, r, &req, []string{"novel_id", "chapter_index", "outline", "idea", "existing_outline", "outline_start", "outline_end", "editor_notes", "manual_context"}); err != nil {
		return req, err
	}
	if req.NovelID == nil || *req.NovelID <= 0 {
		return req, errors.New("novel_id must be a positive integer")
	}
	if req.ChapterIndex != nil && *req.ChapterIndex <= 0 {
		return req, errors.New("chapter_index must be a positive integer")
	}
	if (req.OutlineStart == nil) != (req.OutlineEnd == nil) {
		return req, errors.New("outline_start and outline_end must be provided together")
	}
	if req.OutlineStart != nil && (*req.OutlineStart <= 0 || *req.OutlineEnd <= 0 || *req.OutlineStart > *req.OutlineEnd) {
		return req, errors.New("outline range is invalid")
	}
	if req.ChapterIndex == nil {
		value := 1
		req.ChapterIndex = &value
	}
	req.Outline = strings.TrimSpace(req.Outline)
	req.Idea = strings.TrimSpace(req.Idea)
	req.ExistingOutline = strings.TrimSpace(req.ExistingOutline)
	req.EditorNotes = strings.TrimSpace(req.EditorNotes)
	req.ManualContext = strings.TrimSpace(req.ManualContext)
	return req, nil
}

func (s *Server) HandlePreviewContext(w http.ResponseWriter, r *http.Request) {
	req, err := decodePreviewContextRequest(w, r)
	if err != nil {
		if errors.Is(err, errRequestBodyTooLarge) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	if s.engine == nil {
		http.Error(w, "engine not configured", http.StatusInternalServerError)
		return
	}
	novelID := strconv.Itoa(*req.NovelID)
	chapterIndex := *req.ChapterIndex
	outline := req.Outline
	idea := req.Idea
	editorNotes := req.EditorNotes
	manualContext := req.ManualContext
	existingOutline := req.ExistingOutline
	outlineStart, outlineEnd := 0, 0
	if req.OutlineStart != nil {
		outlineStart, outlineEnd = *req.OutlineStart, *req.OutlineEnd
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.config.GenerationTimeout)
	defer cancel()
	if s.db != nil && (idea == "" || outline == "" || existingOutline == "") {
		loadCtx, loadCancel := context.WithTimeout(ctx, 5*time.Second)
		row, qErr := s.db.Novel.Query().Where(novel.ID(*req.NovelID)).Only(loadCtx)
		loadCancel()
		if qErr != nil {
			http.Error(w, "failed to load novel", http.StatusInternalServerError)
			return
		}
		if idea == "" {
			idea = strings.TrimSpace(row.Idea)
		}
		if outline == "" {
			outline = strings.TrimSpace(row.Outline)
		}
		if existingOutline == "" {
			existingOutline = strings.TrimSpace(row.Outline)
		}
	}
	if outline == "" && idea == "" && existingOutline == "" {
		http.Error(w, "Missing outline and idea (no saved outline found)", http.StatusBadRequest)
		return
	}
	if !s.modelCapacity.tryAcquire() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "模型正在处理其他任务，请稍后再试", http.StatusTooManyRequests)
		return
	}
	defer s.modelCapacity.release()
	state := &agents.GenerationState{NovelID: novelID, ChapterIndex: chapterIndex, FullOutline: outline, Idea: idea, EditorNotes: editorNotes, ManualContext: manualContext, ExistingOutline: existingOutline, OutlineStart: outlineStart, OutlineEnd: outlineEnd}
	res, err := s.engine.PrepareContext(ctx, state)
	if err != nil || res == nil {
		http.Error(w, "preview context preparation failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"novel_id": res.NovelID, "chapter_index": res.ChapterIndex, "full_outline": res.FullOutline, "outline": res.Outline, "scene_card": res.SceneCard, "context": res.Context, "editor_notes": res.EditorNotes, "manual_context": res.ManualContext})
}
