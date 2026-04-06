package novel

import (
	"context"
	"time"
)

// Novel 是小说聚合根 (Aggregate Root)
type Novel struct {
	ID          string
	Title       string
	Description string
	Status      Status
	Tags        []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Chapter 是章节实体 (Entity)
type Chapter struct {
	ID        string
	NovelID   string
	Title     string
	Content   string
	WordCount int
	Order     int
	Status    Status
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Status 状态值对象 (Value Object)
type Status string

const (
	StatusDraft      Status = "Draft"      // 草稿
	StatusGenerating Status = "Generating" // 生成中
	StatusReviewing  Status = "Reviewing"  // 审查中
	StatusPublished  Status = "Published"  // 已发布
)

// Repository 定义了小说的持久化接口，具体实现在 infrastructure 层
type Repository interface {
	SaveNovel(ctx context.Context, n *Novel) error
	GetNovel(ctx context.Context, id int) (*Novel, error)
	SaveChapter(ctx context.Context, c *Chapter) error
	GetChapter(ctx context.Context, id int) (*Chapter, error)
}
