package database

import (
	"context"
	"fmt"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/chapter"
	"github.com/ai-novel/studio/ent/novel"
	domain "github.com/ai-novel/studio/internal/domain/novel"
)

// Repository 实现了 domain.Repository 接口
type Repository struct {
	client *ent.Client
}

func NewRepository(client *ent.Client) *Repository {
	return &Repository{client: client}
}

func (r *Repository) SaveNovel(ctx context.Context, n *domain.Novel) error {
	// 将领域模型转换为 ent 模型并保存
	_, err := r.client.Novel.
		Create().
		SetTitle(n.Title).
		SetDescription(n.Description).
		SetStatus(string(n.Status)).
		SetTags(n.Tags).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to save novel: %w", err)
	}
	return nil
}

func (r *Repository) GetNovel(ctx context.Context, id int) (*domain.Novel, error) {
	n, err := r.client.Novel.
		Query().
		Where(novel.ID(id)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get novel: %w", err)
	}

	return &domain.Novel{
		ID:          fmt.Sprintf("%d", n.ID),
		Title:       n.Title,
		Description: n.Description,
		Status:      domain.Status(n.Status),
		Tags:        n.Tags,
		CreatedAt:   n.CreatedAt,
		UpdatedAt:   n.UpdatedAt,
	}, nil
}

func (r *Repository) SaveChapter(ctx context.Context, c *domain.Chapter) error {
	_, err := r.client.Chapter.
		Create().
		SetTitle(c.Title).
		SetContent(c.Content).
		SetWordCount(c.WordCount).
		SetOrder(c.Order).
		SetStatus(string(c.Status)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to save chapter: %w", err)
	}
	return nil
}

func (r *Repository) GetChapter(ctx context.Context, id int) (*domain.Chapter, error) {
	c, err := r.client.Chapter.
		Query().
		Where(chapter.ID(id)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get chapter: %w", err)
	}

	return &domain.Chapter{
		ID:        fmt.Sprintf("%d", c.ID),
		Title:     c.Title,
		Content:   c.Content,
		WordCount: c.WordCount,
		Order:     c.Order,
		Status:    domain.Status(c.Status),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}, nil
}
