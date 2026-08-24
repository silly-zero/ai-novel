package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var postgresEnvironmentKeys = []string{
	"DATABASE_POSTGRES_HOST",
	"DATABASE_POSTGRES_PORT",
	"DATABASE_POSTGRES_USER",
	"DATABASE_POSTGRES_PASSWORD",
	"DATABASE_POSTGRES_DBNAME",
	"DATABASE_POSTGRES_SSLMODE",
	"DATABASE_POSTGRES_ENABLE_FOREIGN_KEYS",
}

func loadPostgresEnvironmentConfig(t *testing.T, values map[string]string) (*Config, error) {
	t.Helper()
	unsetTestEnvironment(t)
	for _, key := range postgresEnvironmentKeys {
		value, exists := os.LookupEnv(key)
		_ = os.Unsetenv(key)
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(dir)
}

func TestLoadConfigPostgresEnvironmentOverridesYAML(t *testing.T) {
	cfg, err := loadPostgresEnvironmentConfig(t, map[string]string{
		"DATABASE_POSTGRES_HOST":                "db.example",
		"DATABASE_POSTGRES_PORT":                "6543",
		"DATABASE_POSTGRES_USER":                "runtime-user",
		"DATABASE_POSTGRES_PASSWORD":            "p@ss:word",
		"DATABASE_POSTGRES_DBNAME":              "runtime_db",
		"DATABASE_POSTGRES_SSLMODE":             "require",
		"DATABASE_POSTGRES_ENABLE_FOREIGN_KEYS": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	pg := cfg.Database.Postgres
	if pg.Host != "db.example" || pg.Port != 6543 || pg.User != "runtime-user" || pg.Password != "p@ss:word" || pg.DBName != "runtime_db" || pg.SSLMode != "require" || !pg.EnableForeignKeys {
		t.Fatalf("postgres config = %#v", pg)
	}
}

func TestLoadConfigAllowsEmptyPostgresPasswordOverride(t *testing.T) {
	cfg, err := loadPostgresEnvironmentConfig(t, map[string]string{"DATABASE_POSTGRES_PASSWORD": ""})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Postgres.Password != "" {
		t.Fatalf("password was not cleared")
	}
}

func TestLoadConfigRejectsInvalidPostgresEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{name: "port zero", key: "DATABASE_POSTGRES_PORT", value: "0", wantErr: "database.postgres.port"},
		{name: "port high", key: "DATABASE_POSTGRES_PORT", value: "65536", wantErr: "database.postgres.port"},
		{name: "port invalid", key: "DATABASE_POSTGRES_PORT", value: "invalid", wantErr: "database.postgres.port"},
		{name: "host empty", key: "DATABASE_POSTGRES_HOST", value: "", wantErr: "database.postgres.host"},
		{name: "user empty", key: "DATABASE_POSTGRES_USER", value: "", wantErr: "database.postgres.user"},
		{name: "dbname empty", key: "DATABASE_POSTGRES_DBNAME", value: "", wantErr: "database.postgres.dbname"},
		{name: "ssl empty", key: "DATABASE_POSTGRES_SSLMODE", value: "", wantErr: "database.postgres.sslmode"},
		{name: "ssl invalid", key: "DATABASE_POSTGRES_SSLMODE", value: "bogus", wantErr: "database.postgres.sslmode"},
		{name: "foreign keys invalid", key: "DATABASE_POSTGRES_ENABLE_FOREIGN_KEYS", value: "maybe", wantErr: "database.postgres.enable_foreign_keys"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadPostgresEnvironmentConfig(t, map[string]string{test.key: test.value})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %s", err, test.wantErr)
			}
		})
	}
}
