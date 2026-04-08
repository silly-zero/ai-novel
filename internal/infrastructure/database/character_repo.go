package database

import (
	"context"
	"fmt"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/character"
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
	// 暂略具体实现
	return nil
}

func (r *CharacterRepository) ListRelationships(ctx context.Context, novelID string) ([]*domain.Relationship, error) {
	// 暂略具体实现
	return nil, nil
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
