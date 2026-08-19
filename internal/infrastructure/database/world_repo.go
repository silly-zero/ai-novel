package database

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"entgo.io/ent/dialect/sql"
	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/ent/worldsetting"
	"github.com/ai-novel/studio/ent/worldstateversion"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

type WorldRepository struct {
	client *ent.Client
}

func NewWorldRepository(client *ent.Client) *WorldRepository {
	return &WorldRepository{client: client}
}

func (r *WorldRepository) FindByName(ctx context.Context, novelID, name string) (*domain.WorldSetting, error) {
	row, err := r.client.WorldSetting.Query().
		Where(worldsetting.NovelID(novelID), worldsetting.Name(name)).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return r.toDomain(row), nil
}

func (r *WorldRepository) ListByCategory(ctx context.Context, novelID, category string) ([]*domain.WorldSetting, error) {
	rows, err := r.client.WorldSetting.Query().
		Where(worldsetting.NovelID(novelID), worldsetting.Category(category)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	res := make([]*domain.WorldSetting, len(rows))
	for i, row := range rows {
		res[i] = r.toDomain(row)
	}
	return res, nil
}

func (r *WorldRepository) ListAll(ctx context.Context, novelID string) ([]*domain.WorldSetting, error) {
	rows, err := r.client.WorldSetting.Query().
		Where(worldsetting.NovelID(novelID)).
		Order(worldsetting.ByName(), worldsetting.ByID()).
		All(ctx)
	if err != nil {
		return nil, err
	}
	res := make([]*domain.WorldSetting, len(rows))
	for i, row := range rows {
		res[i] = r.toDomain(row)
	}
	return res, nil
}

func (r *WorldRepository) ListWorldSettingsBeforeChapter(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) ([]*domain.WorldSetting, error) {
	if chapterIndex <= 0 {
		return nil, fmt.Errorf("chapter index must be positive")
	}
	settings, err := r.ListAll(ctx, novelID)
	if err != nil {
		return nil, err
	}
	for _, setting := range settings {
		setting.CurrentState = ""
	}
	if len(settings) == 0 {
		return settings, nil
	}
	rows, err := r.client.WorldStateVersion.Query().
		Where(
			worldstateversion.ChapterIndexLT(chapterIndex),
			worldstateversion.Valid(true),
			worldstateversion.HasWorldSettingWith(worldsetting.NovelID(novelID)),
		).
		Where(func(selector *sql.Selector) {
			newer := sql.Table(worldstateversion.Table).As("newer_world_state")
			selector.Where(sql.NotExists(
				sql.Select(newer.C(worldstateversion.FieldID)).
					From(newer).
					Where(sql.And(
						sql.EQ(newer.C(worldstateversion.FieldValid), true),
						sql.ColumnsEQ(newer.C(worldstateversion.WorldSettingColumn), selector.C(worldstateversion.WorldSettingColumn)),
						sql.LT(newer.C(worldstateversion.FieldChapterIndex), chapterIndex),
						sql.Or(
							sql.ColumnsGT(newer.C(worldstateversion.FieldChapterIndex), selector.C(worldstateversion.FieldChapterIndex)),
							sql.And(
								sql.ColumnsEQ(newer.C(worldstateversion.FieldChapterIndex), selector.C(worldstateversion.FieldChapterIndex)),
								sql.ColumnsGT(newer.C(worldstateversion.FieldID), selector.C(worldstateversion.FieldID)),
							),
						),
					)),
			))
		}).
		WithWorldSetting(func(query *ent.WorldSettingQuery) {
			query.Where(worldsetting.NovelID(novelID))
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}
	stateByID := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Edges.WorldSetting != nil {
			stateByID[fmt.Sprintf("%d", row.Edges.WorldSetting.ID)] = row.CurrentState
		}
	}
	var versionedEntityIDs []int
	if err := r.client.WorldStateVersion.Query().
		Where(worldstateversion.HasWorldSettingWith(worldsetting.NovelID(novelID))).
		GroupBy(worldstateversion.FieldWorldSettingID).
		Scan(ctx, &versionedEntityIDs); err != nil {
		return nil, err
	}
	versionedIDs := make(map[string]struct{}, len(versionedEntityIDs))
	for _, id := range versionedEntityIDs {
		versionedIDs[fmt.Sprintf("%d", id)] = struct{}{}
	}
	versionedRows, err := r.client.WorldSetting.Query().
		Where(worldsetting.NovelID(novelID), worldsetting.StateVersioned(true)).
		Select(worldsetting.FieldID).
		Ints(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range versionedRows {
		versionedIDs[fmt.Sprintf("%d", id)] = struct{}{}
	}
	projected := settings[:0]
	for _, setting := range settings {
		state, hasPriorState := stateByID[setting.ID]
		if _, hasHistory := versionedIDs[setting.ID]; hasHistory && !hasPriorState {
			continue
		}
		setting.CurrentState = state
		projected = append(projected, setting)
	}
	return projected, nil
}

func (r *WorldRepository) ReplaceChapterWorldSettings(
	ctx context.Context,
	ref domain.ChapterStateRef,
	settings []*domain.WorldSetting,
) ([]*domain.WorldSetting, error) {
	ref.Normalize()
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	novelID, err := strconv.Atoi(ref.NovelID)
	if err != nil || novelID <= 0 {
		return nil, fmt.Errorf("invalid novel id %q", ref.NovelID)
	}
	chapterID, err := strconv.Atoi(ref.ChapterID)
	if err != nil || chapterID <= 0 {
		return nil, fmt.Errorf("invalid chapter id %q", ref.ChapterID)
	}
	seen := make(map[string]struct{}, len(settings))
	for index, setting := range settings {
		if setting == nil {
			return nil, fmt.Errorf("world setting %d is nil", index)
		}
		name := strings.TrimSpace(setting.Name)
		if name == "" || strings.TrimSpace(setting.CurrentState) == "" {
			return nil, fmt.Errorf("world setting %d requires name and current state", index)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate world setting name %q", name)
		}
		seen[name] = struct{}{}
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("start world state transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	txClient := tx.Client()
	if _, err := txClient.Novel.Query().Where(novel.ID(novelID), func(selector *sql.Selector) {
		selector.ForUpdate()
	}).Only(ctx); err != nil {
		return nil, fmt.Errorf("lock novel for world states: %w", err)
	}
	if _, err := txClient.Chapter.Query().Where(
		chapter.ID(chapterID),
		chapter.Order(ref.ChapterIndex),
		chapter.HasNovelWith(novel.ID(novelID)),
		func(selector *sql.Selector) { selector.ForUpdate() },
	).Only(ctx); err != nil {
		return nil, fmt.Errorf("validate chapter for world states: %w", err)
	}
	existingVersions, err := txClient.WorldStateVersion.Query().
		Where(
			worldstateversion.ChapterID(chapterID),
			worldstateversion.HasWorldSettingWith(worldsetting.NovelID(ref.NovelID)),
		).
		WithWorldSetting().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list chapter world states: %w", err)
	}
	affectedIDs := make(map[int]struct{}, len(existingVersions)+len(settings))
	for _, version := range existingVersions {
		if version.Edges.WorldSetting != nil {
			affectedIDs[version.Edges.WorldSetting.ID] = struct{}{}
		}
	}
	if _, err := txClient.WorldStateVersion.Delete().Where(
		worldstateversion.ChapterID(chapterID),
		worldstateversion.HasWorldSettingWith(worldsetting.NovelID(ref.NovelID)),
	).Exec(ctx); err != nil {
		return nil, fmt.Errorf("delete chapter world states: %w", err)
	}

	result := make([]*domain.WorldSetting, 0, len(settings))
	for _, input := range settings {
		row, queryErr := txClient.WorldSetting.Query().Where(
			worldsetting.NovelID(ref.NovelID),
			worldsetting.Name(strings.TrimSpace(input.Name)),
		).Only(ctx)
		if ent.IsNotFound(queryErr) {
			row, queryErr = txClient.WorldSetting.Create().
				SetNovelID(ref.NovelID).
				SetName(strings.TrimSpace(input.Name)).
				SetCategory(strings.TrimSpace(input.Category)).
				SetDescription(strings.TrimSpace(input.Description)).
				SetCurrentState("").
				SetStateVersioned(true).
				SetMetadata(input.Metadata).
				Save(ctx)
		} else if queryErr == nil {
			update := txClient.WorldSetting.UpdateOneID(row.ID).SetStateVersioned(true)
			if strings.TrimSpace(row.Category) == "" {
				update.SetCategory(strings.TrimSpace(input.Category))
			}
			if strings.TrimSpace(row.Description) == "" {
				update.SetDescription(strings.TrimSpace(input.Description))
			}
			if len(row.Metadata) == 0 && len(input.Metadata) > 0 {
				update.SetMetadata(input.Metadata)
			}
			row, queryErr = update.Save(ctx)
		}
		if queryErr != nil {
			return nil, fmt.Errorf("upsert world setting %q: %w", input.Name, queryErr)
		}
		affectedIDs[row.ID] = struct{}{}
		if _, err := txClient.WorldStateVersion.Create().
			SetWorldSettingID(row.ID).
			SetChapterID(chapterID).
			SetChapterIndex(ref.ChapterIndex).
			SetGenerationID(ref.GenerationID).
			SetCurrentState(strings.TrimSpace(input.CurrentState)).
			Save(ctx); err != nil {
			return nil, fmt.Errorf("save world state %q: %w", input.Name, err)
		}
		canonical := r.toDomain(row)
		canonical.CurrentState = strings.TrimSpace(input.CurrentState)
		result = append(result, canonical)
	}

	for id := range affectedIDs {
		latest, latestErr := txClient.WorldStateVersion.Query().
			Where(worldstateversion.WorldSettingID(id), worldstateversion.Valid(true)).
			Order(
				worldstateversion.ByChapterIndex(sql.OrderDesc()),
				worldstateversion.ByID(sql.OrderDesc()),
			).
			First(ctx)
		state := ""
		if latestErr == nil {
			state = latest.CurrentState
		} else if !ent.IsNotFound(latestErr) {
			return nil, fmt.Errorf("load latest world state %d: %w", id, latestErr)
		}
		if err := txClient.WorldSetting.UpdateOneID(id).SetCurrentState(state).Exec(ctx); err != nil {
			return nil, fmt.Errorf("refresh world state %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit world states: %w", err)
	}
	committed = true
	return result, nil
}

func (r *WorldRepository) toDomain(row *ent.WorldSetting) *domain.WorldSetting {
	return &domain.WorldSetting{
		ID:           fmt.Sprintf("%d", row.ID),
		NovelID:      row.NovelID,
		Category:     row.Category,
		Name:         row.Name,
		Description:  row.Description,
		CurrentState: row.CurrentState,
		Metadata:     row.Metadata,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}
