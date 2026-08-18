package database

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"entgo.io/ent/dialect/sql"
	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/character"
	"github.com/ai-novel/studio/ent/characterstateversion"
	"github.com/ai-novel/studio/ent/novel"
	"github.com/ai-novel/studio/ent/relationship"
	"github.com/ai-novel/studio/ent/relationshipstateversion"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

type CharacterRepository struct {
	client *ent.Client
}

func NewCharacterRepository(client *ent.Client) *CharacterRepository {
	return &CharacterRepository{client: client}
}

func (r *CharacterRepository) FindByName(ctx context.Context, novelID, name string) (*domain.Character, error) {
	row, err := r.client.Character.Query().
		Where(character.NovelID(novelID), character.Name(name)).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return r.toDomain(row), nil
}

func (r *CharacterRepository) GetCharacter(ctx context.Context, idStr string) (*domain.Character, error) {
	var id int
	fmt.Sscanf(idStr, "%d", &id)
	row, err := r.client.Character.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.toDomain(row), nil
}

func (r *CharacterRepository) ListCharacters(ctx context.Context, novelID string) ([]*domain.Character, error) {
	rows, err := r.client.Character.Query().
		Where(character.NovelID(novelID)).
		Order(character.ByName(), character.ByID()).
		All(ctx)
	if err != nil {
		return nil, err
	}
	res := make([]*domain.Character, len(rows))
	for i, row := range rows {
		res[i] = r.toDomain(row)
	}
	return res, nil
}

func (r *CharacterRepository) ListCharactersBeforeChapter(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) ([]*domain.Character, error) {
	if chapterIndex <= 0 {
		return nil, fmt.Errorf("chapter index must be positive")
	}
	characters, err := r.ListCharacters(ctx, novelID)
	if err != nil {
		return nil, err
	}
	for _, character := range characters {
		character.CurrentStatus = ""
	}
	if len(characters) == 0 {
		return characters, nil
	}
	rows, err := r.client.CharacterStateVersion.Query().
		Where(
			characterstateversion.ChapterIndexLT(chapterIndex),
			characterstateversion.HasCharacterWith(character.NovelID(novelID)),
		).
		Where(func(selector *sql.Selector) {
			newer := sql.Table(characterstateversion.Table).As("newer_character_state")
			selector.Where(sql.NotExists(
				sql.Select(newer.C(characterstateversion.FieldID)).
					From(newer).
					Where(sql.And(
						sql.ColumnsEQ(newer.C(characterstateversion.CharacterColumn), selector.C(characterstateversion.CharacterColumn)),
						sql.LT(newer.C(characterstateversion.FieldChapterIndex), chapterIndex),
						sql.Or(
							sql.ColumnsGT(newer.C(characterstateversion.FieldChapterIndex), selector.C(characterstateversion.FieldChapterIndex)),
							sql.And(
								sql.ColumnsEQ(newer.C(characterstateversion.FieldChapterIndex), selector.C(characterstateversion.FieldChapterIndex)),
								sql.ColumnsGT(newer.C(characterstateversion.FieldID), selector.C(characterstateversion.FieldID)),
							),
						),
					)),
			))
		}).
		WithCharacter(func(query *ent.CharacterQuery) {
			query.Where(character.NovelID(novelID))
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}
	statusByID := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Edges.Character != nil {
			statusByID[fmt.Sprintf("%d", row.Edges.Character.ID)] = row.CurrentStatus
		}
	}
	var versionedEntityIDs []int
	if err := r.client.CharacterStateVersion.Query().
		Where(characterstateversion.HasCharacterWith(character.NovelID(novelID))).
		GroupBy(characterstateversion.FieldCharacterID).
		Scan(ctx, &versionedEntityIDs); err != nil {
		return nil, err
	}
	versionedIDs := make(map[string]struct{}, len(versionedEntityIDs))
	for _, id := range versionedEntityIDs {
		versionedIDs[fmt.Sprintf("%d", id)] = struct{}{}
	}
	versionedRows, err := r.client.Character.Query().
		Where(character.NovelID(novelID), character.StateVersioned(true)).
		Select(character.FieldID).
		Ints(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range versionedRows {
		versionedIDs[fmt.Sprintf("%d", id)] = struct{}{}
	}
	projected := characters[:0]
	for _, character := range characters {
		status, hasPriorState := statusByID[character.ID]
		if _, hasHistory := versionedIDs[character.ID]; hasHistory && !hasPriorState {
			continue
		}
		character.CurrentStatus = status
		projected = append(projected, character)
	}
	return projected, nil
}

func (r *CharacterRepository) ReplaceChapterCharacters(
	ctx context.Context,
	ref domain.ChapterStateRef,
	characters []*domain.Character,
) ([]*domain.Character, error) {
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
	seen := make(map[string]struct{}, len(characters))
	for index, character := range characters {
		if character == nil {
			return nil, fmt.Errorf("character %d is nil", index)
		}
		name := strings.TrimSpace(character.Name)
		if name == "" || strings.TrimSpace(character.CurrentStatus) == "" {
			return nil, fmt.Errorf("character %d requires name and current status", index)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate character name %q", name)
		}
		seen[name] = struct{}{}
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("start character state transaction: %w", err)
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
		return nil, fmt.Errorf("lock novel for character states: %w", err)
	}
	if _, err := txClient.Chapter.Query().Where(
		chapter.ID(chapterID),
		chapter.Order(ref.ChapterIndex),
		chapter.HasNovelWith(novel.ID(novelID)),
		func(selector *sql.Selector) { selector.ForUpdate() },
	).Only(ctx); err != nil {
		return nil, fmt.Errorf("validate chapter for character states: %w", err)
	}
	existingVersions, err := txClient.CharacterStateVersion.Query().
		Where(
			characterstateversion.ChapterID(chapterID),
			characterstateversion.HasCharacterWith(character.NovelID(ref.NovelID)),
		).
		WithCharacter().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list chapter character states: %w", err)
	}
	affectedIDs := make(map[int]struct{}, len(existingVersions)+len(characters))
	for _, version := range existingVersions {
		if version.Edges.Character != nil {
			affectedIDs[version.Edges.Character.ID] = struct{}{}
		}
	}
	if _, err := txClient.CharacterStateVersion.Delete().Where(
		characterstateversion.ChapterID(chapterID),
		characterstateversion.HasCharacterWith(character.NovelID(ref.NovelID)),
	).Exec(ctx); err != nil {
		return nil, fmt.Errorf("delete chapter character states: %w", err)
	}

	result := make([]*domain.Character, 0, len(characters))
	for _, input := range characters {
		row, queryErr := txClient.Character.Query().Where(
			character.NovelID(ref.NovelID),
			character.Name(strings.TrimSpace(input.Name)),
		).Only(ctx)
		if ent.IsNotFound(queryErr) {
			row, queryErr = txClient.Character.Create().
				SetNovelID(ref.NovelID).
				SetName(strings.TrimSpace(input.Name)).
				SetGender(strings.TrimSpace(input.Gender)).
				SetAge(input.Age).
				SetAppearance(strings.TrimSpace(input.Appearance)).
				SetPersonality(strings.TrimSpace(input.Personality)).
				SetBackground(strings.TrimSpace(input.Background)).
				SetCurrentStatus("").
				SetStateVersioned(true).
				Save(ctx)
		} else if queryErr == nil {
			update := txClient.Character.UpdateOneID(row.ID).SetStateVersioned(true)
			if strings.TrimSpace(row.Gender) == "" {
				update.SetGender(strings.TrimSpace(input.Gender))
			}
			if row.Age == 0 {
				update.SetAge(input.Age)
			}
			if strings.TrimSpace(row.Appearance) == "" {
				update.SetAppearance(strings.TrimSpace(input.Appearance))
			}
			if strings.TrimSpace(row.Personality) == "" {
				update.SetPersonality(strings.TrimSpace(input.Personality))
			}
			if strings.TrimSpace(row.Background) == "" {
				update.SetBackground(strings.TrimSpace(input.Background))
			}
			row, queryErr = update.Save(ctx)
		}
		if queryErr != nil {
			return nil, fmt.Errorf("upsert character %q: %w", input.Name, queryErr)
		}
		affectedIDs[row.ID] = struct{}{}
		if _, err := txClient.CharacterStateVersion.Create().
			SetCharacterID(row.ID).
			SetChapterID(chapterID).
			SetChapterIndex(ref.ChapterIndex).
			SetGenerationID(ref.GenerationID).
			SetCurrentStatus(strings.TrimSpace(input.CurrentStatus)).
			Save(ctx); err != nil {
			return nil, fmt.Errorf("save character state %q: %w", input.Name, err)
		}
		canonical := r.toDomain(row)
		canonical.CurrentStatus = strings.TrimSpace(input.CurrentStatus)
		result = append(result, canonical)
	}

	for id := range affectedIDs {
		latest, latestErr := txClient.CharacterStateVersion.Query().
			Where(characterstateversion.CharacterID(id)).
			Order(
				characterstateversion.ByChapterIndex(sql.OrderDesc()),
				characterstateversion.ByID(sql.OrderDesc()),
			).
			First(ctx)
		status := ""
		if latestErr == nil {
			status = latest.CurrentStatus
		} else if !ent.IsNotFound(latestErr) {
			return nil, fmt.Errorf("load latest character state %d: %w", id, latestErr)
		}
		if err := txClient.Character.UpdateOneID(id).SetCurrentStatus(status).Exec(ctx); err != nil {
			return nil, fmt.Errorf("refresh character state %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit character states: %w", err)
	}
	committed = true
	return result, nil
}

func (r *CharacterRepository) SaveRelationship(ctx context.Context, rel *domain.Relationship) error {
	if rel == nil {
		return nil
	}

	if rel.SourceCharacter == nil || rel.TargetCharacter == nil {
		return fmt.Errorf("relationship requires source and target character")
	}

	sourceID, err := r.resolveCharacterID(ctx, rel.NovelID, rel.SourceCharacter)
	if err != nil {
		return err
	}

	targetID, err := r.resolveCharacterID(ctx, rel.NovelID, rel.TargetCharacter)
	if err != nil {
		return err
	}

	existing, err := r.client.Relationship.Query().
		Where(
			relationship.NovelID(rel.NovelID),
			relationship.RelationType(rel.RelationType),
			relationship.HasCharacterWith(character.ID(sourceID)),
			relationship.HasTargetCharacterWith(character.ID(targetID)),
		).
		Only(ctx)

	if err == nil && existing != nil {
		rel.ID = fmt.Sprintf("%d", existing.ID)
		return r.client.Relationship.UpdateOneID(existing.ID).
			SetDescription(rel.Description).
			Exec(ctx)
	}
	if !ent.IsNotFound(err) {
		return fmt.Errorf("query existing relationship: %w", err)
	}

	created, err := r.client.Relationship.Create().
		SetNovelID(rel.NovelID).
		SetRelationType(rel.RelationType).
		SetDescription(rel.Description).
		SetCharacterID(sourceID).
		SetTargetCharacterID(targetID).
		Save(ctx)
	if err != nil {
		return err
	}

	rel.ID = fmt.Sprintf("%d", created.ID)
	return nil
}

type relationshipKey struct {
	sourceID     int
	targetID     int
	relationType string
}

type relationshipSnapshot struct {
	key         relationshipKey
	description string
	active      bool
	operation   domain.RelationshipOperation
}

func (r *CharacterRepository) ReplaceChapterRelationships(
	ctx context.Context,
	ref domain.ChapterStateRef,
	changes []domain.RelationshipChange,
) ([]*domain.Relationship, error) {
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
	normalized := make([]domain.RelationshipChange, len(changes))
	seen := make(map[relationshipKey]struct{}, len(changes))
	for index, change := range changes {
		change.Normalize()
		if err := change.Validate(); err != nil {
			return nil, fmt.Errorf("relationship change %d: %w", index, err)
		}
		sourceID, err := strconv.Atoi(strings.TrimSpace(change.SourceCharacter.ID))
		if err != nil || sourceID <= 0 {
			return nil, fmt.Errorf("relationship change %d has invalid source id", index)
		}
		targetID, err := strconv.Atoi(strings.TrimSpace(change.TargetCharacter.ID))
		if err != nil || targetID <= 0 {
			return nil, fmt.Errorf("relationship change %d has invalid target id", index)
		}
		key := relationshipKey{sourceID: sourceID, targetID: targetID, relationType: change.RelationType}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate relationship change %d", index)
		}
		seen[key] = struct{}{}
		normalized[index] = change
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("start relationship state transaction: %w", err)
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
		return nil, fmt.Errorf("lock novel for relationship states: %w", err)
	}
	if _, err := txClient.Chapter.Query().Where(
		chapter.ID(chapterID),
		chapter.Order(ref.ChapterIndex),
		chapter.HasNovelWith(novel.ID(novelID)),
		func(selector *sql.Selector) { selector.ForUpdate() },
	).Only(ctx); err != nil {
		return nil, fmt.Errorf("validate chapter for relationship states: %w", err)
	}

	characters, err := txClient.Character.Query().Where(character.NovelID(ref.NovelID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list relationship characters: %w", err)
	}
	characterByID := make(map[int]*ent.Character, len(characters))
	for _, row := range characters {
		characterByID[row.ID] = row
	}
	for index, change := range normalized {
		sourceID, _ := strconv.Atoi(strings.TrimSpace(change.SourceCharacter.ID))
		targetID, _ := strconv.Atoi(strings.TrimSpace(change.TargetCharacter.ID))
		if characterByID[sourceID] == nil || characterByID[targetID] == nil {
			return nil, fmt.Errorf("relationship change %d references character outside novel", index)
		}
	}

	snapshots := make(map[relationshipKey]relationshipSnapshot, len(normalized))
	oldRows, err := txClient.RelationshipStateVersion.Query().Where(
		relationshipstateversion.ChapterID(chapterID),
	).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list chapter relationship states: %w", err)
	}
	affected := make(map[relationshipKey]struct{}, len(oldRows)+len(normalized))
	for _, row := range oldRows {
		affected[relationshipKey{sourceID: row.SourceCharacterID, targetID: row.TargetCharacterID, relationType: row.RelationType}] = struct{}{}
	}
	if _, err := txClient.RelationshipStateVersion.Delete().Where(
		relationshipstateversion.ChapterID(chapterID),
	).Exec(ctx); err != nil {
		return nil, fmt.Errorf("delete chapter relationship states: %w", err)
	}
	for _, change := range normalized {
		sourceID, _ := strconv.Atoi(strings.TrimSpace(change.SourceCharacter.ID))
		targetID, _ := strconv.Atoi(strings.TrimSpace(change.TargetCharacter.ID))
		key := relationshipKey{sourceID: sourceID, targetID: targetID, relationType: change.RelationType}
		affected[key] = struct{}{}
		snapshots[key] = relationshipSnapshot{
			key: key, description: change.Description,
			active:    change.Operation == domain.RelationshipOperationUpsert,
			operation: change.Operation,
		}
	}
	for key, snapshot := range snapshots {
		if _, err := txClient.RelationshipStateVersion.Create().
			SetChapterID(chapterID).
			SetSourceCharacterID(key.sourceID).
			SetTargetCharacterID(key.targetID).
			SetChapterIndex(ref.ChapterIndex).
			SetGenerationID(ref.GenerationID).
			SetRelationType(key.relationType).
			SetDescription(snapshot.description).
			SetActive(snapshot.active).
			SetOperation(relationshipstateversion.Operation(snapshot.operation)).
			Save(ctx); err != nil {
			return nil, fmt.Errorf("save relationship state: %w", err)
		}
		affected[key] = struct{}{}
	}
	for key := range affected {
		if err := rebuildRelationshipCache(ctx, txClient, ref.NovelID, key); err != nil {
			return nil, err
		}
	}
	visibleRows, err := latestRelationshipVersionsBeforeChapter(ctx, txClient, ref.NovelID, ref.ChapterIndex+1)
	if err != nil {
		return nil, fmt.Errorf("load relationship state result: %w", err)
	}
	result := make([]*domain.Relationship, 0, len(visibleRows))
	for _, row := range visibleRows {
		if !row.Active {
			continue
		}
		source := characterByID[row.SourceCharacterID]
		target := characterByID[row.TargetCharacterID]
		if source == nil || target == nil {
			return nil, fmt.Errorf("relationship result references unknown character")
		}
		result = append(result, &domain.Relationship{
			ID: fmt.Sprintf("%d", row.ID), NovelID: ref.NovelID,
			SourceCharacter: r.toDomain(source), TargetCharacter: r.toDomain(target),
			RelationType: row.RelationType, Description: row.Description,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit relationship states: %w", err)
	}
	committed = true
	return result, nil
}

func latestRelationshipVersionsBeforeChapter(
	ctx context.Context,
	client *ent.Client,
	novelID string,
	chapterIndex int,
) ([]*ent.RelationshipStateVersion, error) {
	return client.RelationshipStateVersion.Query().
		Where(
			relationshipstateversion.ChapterIndexLT(chapterIndex),
			relationshipstateversion.HasSourceCharacterWith(character.NovelID(novelID)),
			relationshipstateversion.HasTargetCharacterWith(character.NovelID(novelID)),
		).
		Where(func(selector *sql.Selector) {
			newer := sql.Table(relationshipstateversion.Table).As("newer_relationship_state")
			selector.Where(sql.NotExists(
				sql.Select(newer.C(relationshipstateversion.FieldID)).From(newer).Where(sql.And(
					sql.ColumnsEQ(newer.C(relationshipstateversion.FieldSourceCharacterID), selector.C(relationshipstateversion.FieldSourceCharacterID)),
					sql.ColumnsEQ(newer.C(relationshipstateversion.FieldTargetCharacterID), selector.C(relationshipstateversion.FieldTargetCharacterID)),
					sql.ColumnsEQ(newer.C(relationshipstateversion.FieldRelationType), selector.C(relationshipstateversion.FieldRelationType)),
					sql.LT(newer.C(relationshipstateversion.FieldChapterIndex), chapterIndex),
					sql.Or(
						sql.ColumnsGT(newer.C(relationshipstateversion.FieldChapterIndex), selector.C(relationshipstateversion.FieldChapterIndex)),
						sql.And(
							sql.ColumnsEQ(newer.C(relationshipstateversion.FieldChapterIndex), selector.C(relationshipstateversion.FieldChapterIndex)),
							sql.ColumnsGT(newer.C(relationshipstateversion.FieldID), selector.C(relationshipstateversion.FieldID)),
						),
					),
				)),
			))
		}).
		Order(
			relationshipstateversion.BySourceCharacterID(),
			relationshipstateversion.ByTargetCharacterID(),
			relationshipstateversion.ByRelationType(),
			relationshipstateversion.ByID(),
		).
		All(ctx)
}

func RebuildRelationshipCache(
	ctx context.Context,
	client *ent.Client,
	novelID string,
	sourceID int,
	targetID int,
	relationType string,
) error {
	return rebuildRelationshipCache(ctx, client, novelID, relationshipKey{
		sourceID: sourceID, targetID: targetID, relationType: relationType,
	})
}

func rebuildRelationshipCache(ctx context.Context, client *ent.Client, novelID string, key relationshipKey) error {
	latest, err := client.RelationshipStateVersion.Query().Where(
		relationshipstateversion.SourceCharacterID(key.sourceID),
		relationshipstateversion.TargetCharacterID(key.targetID),
		relationshipstateversion.RelationType(key.relationType),
	).Order(
		relationshipstateversion.ByChapterIndex(sql.OrderDesc()),
		relationshipstateversion.ByID(sql.OrderDesc()),
	).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("load latest relationship state: %w", err)
	}
	if _, err := client.Relationship.Delete().Where(
		relationship.NovelID(novelID),
		relationship.RelationType(key.relationType),
		relationship.StateVersioned(true),
		relationship.HasCharacterWith(character.ID(key.sourceID)),
		relationship.HasTargetCharacterWith(character.ID(key.targetID)),
	).Exec(ctx); err != nil {
		return fmt.Errorf("clear relationship cache: %w", err)
	}
	if ent.IsNotFound(err) || !latest.Active {
		return nil
	}
	if _, err := client.Relationship.Create().
		SetNovelID(novelID).
		SetRelationType(key.relationType).
		SetDescription(latest.Description).
		SetStateVersioned(true).
		SetCharacterID(key.sourceID).
		SetTargetCharacterID(key.targetID).
		Save(ctx); err != nil {
		return fmt.Errorf("rebuild relationship cache: %w", err)
	}
	return nil
}

func (r *CharacterRepository) ListRelationshipsBeforeChapter(
	ctx context.Context,
	novelID string,
	chapterIndex int,
) ([]*domain.Relationship, error) {
	if strings.TrimSpace(novelID) == "" || chapterIndex <= 0 {
		return nil, fmt.Errorf("novel id and positive chapter index are required")
	}
	rows, err := r.client.RelationshipStateVersion.Query().
		Where(
			relationshipstateversion.ChapterIndexLT(chapterIndex),
			relationshipstateversion.HasSourceCharacterWith(character.NovelID(novelID)),
			relationshipstateversion.HasTargetCharacterWith(character.NovelID(novelID)),
		).
		Where(func(selector *sql.Selector) {
			newer := sql.Table(relationshipstateversion.Table).As("newer_relationship_state")
			selector.Where(sql.NotExists(
				sql.Select(newer.C(relationshipstateversion.FieldID)).
					From(newer).
					Where(sql.And(
						sql.ColumnsEQ(newer.C(relationshipstateversion.FieldSourceCharacterID), selector.C(relationshipstateversion.FieldSourceCharacterID)),
						sql.ColumnsEQ(newer.C(relationshipstateversion.FieldTargetCharacterID), selector.C(relationshipstateversion.FieldTargetCharacterID)),
						sql.ColumnsEQ(newer.C(relationshipstateversion.FieldRelationType), selector.C(relationshipstateversion.FieldRelationType)),
						sql.LT(newer.C(relationshipstateversion.FieldChapterIndex), chapterIndex),
						sql.Or(
							sql.ColumnsGT(newer.C(relationshipstateversion.FieldChapterIndex), selector.C(relationshipstateversion.FieldChapterIndex)),
							sql.And(
								sql.ColumnsEQ(newer.C(relationshipstateversion.FieldChapterIndex), selector.C(relationshipstateversion.FieldChapterIndex)),
								sql.ColumnsGT(newer.C(relationshipstateversion.FieldID), selector.C(relationshipstateversion.FieldID)),
							),
						),
					)),
			))
		}).
		WithSourceCharacter().
		WithTargetCharacter().
		Order(
			relationshipstateversion.BySourceCharacterID(),
			relationshipstateversion.ByTargetCharacterID(),
			relationshipstateversion.ByRelationType(),
			relationshipstateversion.ByID(),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*domain.Relationship, 0, len(rows))
	for _, row := range rows {
		if !row.Active || row.Edges.SourceCharacter == nil || row.Edges.TargetCharacter == nil {
			continue
		}
		result = append(result, &domain.Relationship{
			ID:              fmt.Sprintf("%d", row.ID),
			NovelID:         novelID,
			SourceCharacter: r.toDomain(row.Edges.SourceCharacter),
			TargetCharacter: r.toDomain(row.Edges.TargetCharacter),
			RelationType:    row.RelationType,
			Description:     row.Description,
		})
	}
	return result, nil
}

func (r *CharacterRepository) ListRelationships(ctx context.Context, novelID string) ([]*domain.Relationship, error) {
	rows, err := r.client.Relationship.Query().
		Where(relationship.NovelID(novelID)).
		WithCharacter().
		WithTargetCharacter().
		All(ctx)
	if err != nil {
		return nil, err
	}

	res := make([]*domain.Relationship, 0, len(rows))
	for _, row := range rows {
		var source *domain.Character
		if row.Edges.Character != nil {
			source = r.toDomain(row.Edges.Character)
		}

		var target *domain.Character
		if row.Edges.TargetCharacter != nil {
			target = r.toDomain(row.Edges.TargetCharacter)
		}

		res = append(res, &domain.Relationship{
			ID:              fmt.Sprintf("%d", row.ID),
			NovelID:         row.NovelID,
			SourceCharacter: source,
			TargetCharacter: target,
			RelationType:    row.RelationType,
			Description:     row.Description,
		})
	}

	return res, nil
}

func (r *CharacterRepository) toDomain(row *ent.Character) *domain.Character {
	return &domain.Character{
		ID:            fmt.Sprintf("%d", row.ID),
		NovelID:       row.NovelID,
		Name:          row.Name,
		Gender:        row.Gender,
		Age:           row.Age,
		Appearance:    row.Appearance,
		Personality:   row.Personality,
		Background:    row.Background,
		CurrentStatus: row.CurrentStatus,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

func (r *CharacterRepository) resolveCharacterID(ctx context.Context, novelID string, c *domain.Character) (int, error) {
	if c.ID != "" {
		var id int
		_, err := fmt.Sscanf(c.ID, "%d", &id)
		if err == nil && id > 0 {
			return id, nil
		}
	}

	if c.Name != "" {
		row, err := r.client.Character.Query().
			Where(character.NovelID(novelID), character.Name(c.Name)).
			Only(ctx)
		if err == nil && row != nil {
			c.ID = fmt.Sprintf("%d", row.ID)
			return row.ID, nil
		}
	}

	return 0, fmt.Errorf("failed to resolve character id for name=%s", c.Name)
}
