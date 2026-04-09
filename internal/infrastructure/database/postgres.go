package database

import (
	"context"
	"fmt"
	"log"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/migrate"
	_ "github.com/lib/pq"
)

// PostgresConfig 临时定义以解决编译问题
type PostgresConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
	EnableForeignKeys bool
}

// Client 包装了 ent.Client
type Client struct {
	*ent.Client
}

// NewClient 初始化 PostgreSQL 客户端并执行自动迁移
func NewClient(ctx context.Context, cfg *PostgresConfig) (*Client, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)

	client, err := ent.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed opening connection to postgres: %w", err)
	}

	// 运行自动迁移
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(cfg.EnableForeignKeys)); err != nil {
		return nil, fmt.Errorf("failed creating schema resources: %w", err)
	}

	log.Println("✅ 数据库连接成功并完成自动迁移")
	return &Client{client}, nil
}
