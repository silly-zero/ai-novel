package database

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"entgo.io/ent/dialect/sql"
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
	res, err := r.client.Novel.
		Create().
		SetTitle(n.Title).
		SetDescription(n.Description).
		SetStatus(string(n.Status)).
		SetTags(n.Tags).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to save novel: %w", err)
	}
	if n != nil && n.ID == "" {
		n.ID = fmt.Sprintf("%d", res.ID)
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

var (
	errChapterOrderOccupied       = errors.New("chapter order is already occupied")
	errPreviousChapterUnavailable = errors.New("previous chapter is required")
)

func (r *Repository) SaveChapter(ctx context.Context, c *domain.Chapter) error {
	if c == nil {
		return errors.New("chapter is nil")
	}
	novelID, err := strconv.Atoi(c.NovelID)
	if err != nil || novelID <= 0 {
		return fmt.Errorf("invalid chapter novel id: %q", c.NovelID)
	}
	if c.Order <= 0 {
		return fmt.Errorf("invalid chapter order: %d", c.Order)
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("start chapter transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	txClient := tx.Client()
	if _, err := txClient.Novel.Query().Where(
		novel.ID(novelID),
		func(selector *sql.Selector) {
			selector.ForUpdate()
		},
	).Only(ctx); err != nil {
		return fmt.Errorf("lock chapter novel: %w", err)
	}
	if _, err := txClient.Chapter.Query().Where(
		chapter.OrderEQ(c.Order),
		chapter.HasNovelWith(novel.ID(novelID)),
	).Only(ctx); err == nil {
		return errChapterOrderOccupied
	} else if !ent.IsNotFound(err) {
		return fmt.Errorf("check chapter order: %w", err)
	}
	if c.Order > 1 {
		if _, err := txClient.Chapter.Query().Where(
			chapter.OrderEQ(c.Order-1),
			chapter.HasNovelWith(novel.ID(novelID)),
		).Only(ctx); ent.IsNotFound(err) {
			return fmt.Errorf("%w: order %d", errPreviousChapterUnavailable, c.Order-1)
		} else if err != nil {
			return fmt.Errorf("check previous chapter: %w", err)
		}
	}

	_, err = txClient.Chapter.
		Create().
		SetNovelID(novelID).
		SetTitle(c.Title).
		SetContent(c.Content).
		SetWordCount(c.WordCount).
		SetOrder(c.Order).
		SetStatus(string(c.Status)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to save chapter: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chapter transaction: %w", err)
	}
	committed = true
	return nil
}

func (r *Repository) GetChapter(ctx context.Context, id int) (*domain.Chapter, error) {
	c, err := r.client.Chapter.
		Query().
		Where(chapter.ID(id)).
		WithNovel().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get chapter: %w", err)
	}

	var novelID string
	if c.Edges.Novel != nil {
		novelID = fmt.Sprintf("%d", c.Edges.Novel.ID)
	}

	return &domain.Chapter{
		ID:        fmt.Sprintf("%d", c.ID),
		NovelID:   novelID,
		Title:     c.Title,
		Content:   c.Content,
		WordCount: c.WordCount,
		Order:     c.Order,
		Status:    domain.Status(c.Status),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}, nil
}
