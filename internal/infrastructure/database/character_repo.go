package database

import (
	"context"
	"fmt"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/character"
	"github.com/ai-novel/studio/ent/relationship"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

type CharacterRepository struct {
	client *ent.Client
}

func NewCharacterRepository(client *ent.Client) *CharacterRepository {
	return &CharacterRepository{client: client}
}

func (r *CharacterRepository) SaveCharacter(ctx context.Context, c *domain.Character) error {
	if c.ID == "" {
		res, err := r.client.Character.Create().
			SetNovelID(c.NovelID).
			SetName(c.Name).
			SetGender(c.Gender).
			SetAge(c.Age).
			SetAppearance(c.Appearance).
			SetPersonality(c.Personality).
			SetBackground(c.Background).
			SetCurrentStatus(c.CurrentStatus).
			Save(ctx)
		if err != nil {
			return err
		}
		c.ID = fmt.Sprintf("%d", res.ID)
		return nil
	}

	// 转换 ID 并更新
	var id int
	fmt.Sscanf(c.ID, "%d", &id)
	return r.client.Character.UpdateOneID(id).
		SetGender(c.Gender).
		SetAge(c.Age).
		SetAppearance(c.Appearance).
		SetPersonality(c.Personality).
		SetBackground(c.Background).
		SetCurrentStatus(c.CurrentStatus).
		Exec(ctx)
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
