package novel

import (
	"context"
	"time"
)

// WorldSetting 世界观设定领域模型
type WorldSetting struct {
	ID          string
	NovelID     string
	Category    string // 如：地理、武学等级、势力、宝物
	Name        string
	Description string
	Metadata    map[string]interface{}
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// WorldRepository 世界观持久化接口
type WorldRepository interface {
	SaveSetting(ctx context.Context, s *WorldSetting) error
	FindByName(ctx context.Context, novelID, name string) (*WorldSetting, error)
	ListByCategory(ctx context.Context, novelID, category string) ([]*WorldSetting, error)
	ListAll(ctx context.Context, novelID string) ([]*WorldSetting, error)
}
