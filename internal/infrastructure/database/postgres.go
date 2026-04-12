package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/migrate"
	"github.com/lib/pq"
)

// PostgresConfig 临时定义以解决编译问题
type PostgresConfig struct {
	Host              string
	Port              int
	User              string
	Password          string
	DBName            string
	SSLMode           string
	EnableForeignKeys bool
}

// Client 包装了 ent.Client
type Client struct {
	*ent.Client
}

func buildDSN(cfg *PostgresConfig, dbName string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:   "/" + dbName,
	}
	q := url.Values{}
	q.Set("sslmode", cfg.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

func ensureDatabaseExists(ctx context.Context, cfg *PostgresConfig) error {
	if cfg == nil {
		return fmt.Errorf("postgres config is nil")
	}
	if cfg.DBName == "" {
		return fmt.Errorf("postgres dbname is empty")
	}
	adminDSN := buildDSN(cfg, "postgres")
	db, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("open postgres admin connection: %w", err)
	}
	defer db.Close()

	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, cfg.DBName).Scan(&exists); err != nil {
		return fmt.Errorf("check database exists: %w", err)
	}
	if exists {
		return nil
	}

	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s`, pq.QuoteIdentifier(cfg.DBName)))
	if err != nil {
		return fmt.Errorf("create database %q: %w", cfg.DBName, err)
	}
	return nil
}

// NewClient 初始化 PostgreSQL 客户端并执行自动迁移
func NewClient(ctx context.Context, cfg *PostgresConfig) (*Client, error) {
	if err := ensureDatabaseExists(ctx, cfg); err != nil {
		return nil, err
	}
	dsn := buildDSN(cfg, cfg.DBName)

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
