package database

import (
	"context"
	"fmt"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/worldsetting"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

type WorldRepository struct {
	client *ent.Client
}

func NewWorldRepository(client *ent.Client) *WorldRepository {
	return &WorldRepository{client: client}
}

func (r *WorldRepository) SaveSetting(ctx context.Context, s *domain.WorldSetting) error {
	if s.ID == "" {
		res, err := r.client.WorldSetting.Create().
			SetNovelID(s.NovelID).
			SetCategory(s.Category).
			SetName(s.Name).
			SetDescription(s.Description).
			SetMetadata(s.Metadata).
			Save(ctx)
		if err != nil {
			return err
		}
		s.ID = fmt.Sprintf("%d", res.ID)
		return nil
	}

	var id int
	fmt.Sscanf(s.ID, "%d", &id)
	return r.client.WorldSetting.UpdateOneID(id).
		SetCategory(s.Category).
		SetDescription(s.Description).
		SetMetadata(s.Metadata).
		Exec(ctx)
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

func (r *WorldRepository) toDomain(row *ent.WorldSetting) *domain.WorldSetting {
	return &domain.WorldSetting{
		ID:          fmt.Sprintf("%d", row.ID),
		NovelID:     row.NovelID,
		Category:    row.Category,
		Name:        row.Name,
		Description: row.Description,
		Metadata:    row.Metadata,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}
