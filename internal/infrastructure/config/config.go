package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	App      AppConfig      `mapstructure:"app"`
	Database DatabaseConfig `mapstructure:"database"`
	LLM      LLMConfig      `mapstructure:"llm"`
}

type AppConfig struct {
	Env string `mapstructure:"env"`
}

type DatabaseConfig struct {
	Postgres PostgresConfig `mapstructure:"postgres"`
}

type PostgresConfig struct {
	Host              string `mapstructure:"host"`
	Port              int    `mapstructure:"port"`
	User              string `mapstructure:"user"`
	Password          string `mapstructure:"password"`
	DBName            string `mapstructure:"dbname"`
	SSLMode           string `mapstructure:"sslmode"`
	EnableForeignKeys bool   `mapstructure:"enable_foreign_keys"`
}

type LLMConfig struct {
	Chat      ChatConfig      `mapstructure:"chat"`
	Embedding EmbeddingConfig `mapstructure:"embedding"`
}

type ChatConfig struct {
	APIKey    string        `mapstructure:"api_key"`
	BaseURL   string        `mapstructure:"base_url"`
	Model     string        `mapstructure:"model"`
	MaxTokens int           `mapstructure:"max_tokens"`
	Timeout   time.Duration `mapstructure:"-"`
}

type EmbeddingConfig struct {
	APIKey  string        `mapstructure:"api_key"`
	BaseURL string        `mapstructure:"base_url"`
	Model   string        `mapstructure:"model"`
	Timeout time.Duration `mapstructure:"-"`
}

func LoadConfig(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(configPath)
	v.AddConfigPath(".")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	for key, env := range map[string]string{
		"llm.chat.api_key":       "LLM_CHAT_API_KEY",
		"llm.chat.base_url":      "LLM_CHAT_BASE_URL",
		"llm.chat.model":         "LLM_CHAT_MODEL",
		"llm.chat.max_tokens":    "LLM_CHAT_MAX_TOKENS",
		"llm.chat.timeout":       "LLM_CHAT_TIMEOUT",
		"llm.embedding.api_key":  "LLM_EMBEDDING_API_KEY",
		"llm.embedding.base_url": "LLM_EMBEDDING_BASE_URL",
		"llm.embedding.model":    "LLM_EMBEDDING_MODEL",
		"llm.embedding.timeout":  "LLM_EMBEDDING_TIMEOUT",
	} {
		if err := v.BindEnv(key, env); err != nil {
			return nil, fmt.Errorf("bind %s: %w", key, err)
		}
	}

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	maxTokens, err := parsePositiveInt(v, "llm.chat.max_tokens")
	if err != nil {
		return nil, err
	}
	chatTimeout, err := parseDuration(v, "llm.chat.timeout")
	if err != nil {
		return nil, err
	}
	embeddingTimeout, err := parseDuration(v, "llm.embedding.timeout")
	if err != nil {
		return nil, err
	}
	cfg.LLM.Chat.MaxTokens = maxTokens
	cfg.LLM.Chat.Timeout = chatTimeout
	cfg.LLM.Embedding.Timeout = embeddingTimeout

	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parsePositiveInt(v *viper.Viper, key string) (int, error) {
	value := v.Get(key)
	var parsed int64

	switch value := value.(type) {
	case string:
		raw := strings.TrimSpace(value)
		if raw == "" {
			return 0, fmt.Errorf("%s is required", key)
		}
		var err error
		parsed, err = strconv.ParseInt(raw, 10, 0)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
	case int:
		parsed = int64(value)
	case int8:
		parsed = int64(value)
	case int16:
		parsed = int64(value)
	case int32:
		parsed = int64(value)
	case int64:
		parsed = value
	case uint:
		if uint64(value) > uint64(^uint(0)>>1) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		parsed = int64(value)
	case uint8:
		parsed = int64(value)
	case uint16:
		parsed = int64(value)
	case uint32:
		parsed = int64(value)
	case uint64:
		if value > uint64(^uint(0)>>1) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		parsed = int64(value)
	case nil:
		return 0, fmt.Errorf("%s is required", key)
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}

	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", key)
	}
	return int(parsed), nil
}

func parseDuration(v *viper.Viper, key string) (time.Duration, error) {
	raw := strings.TrimSpace(v.GetString(key))
	if raw == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration with a unit: %w", key, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", key)
	}
	return duration, nil
}

func validate(cfg *Config) error {
	cfg.LLM.Chat.APIKey = strings.TrimSpace(cfg.LLM.Chat.APIKey)
	cfg.LLM.Chat.BaseURL = strings.TrimSpace(cfg.LLM.Chat.BaseURL)
	cfg.LLM.Chat.Model = strings.TrimSpace(cfg.LLM.Chat.Model)
	cfg.LLM.Embedding.APIKey = strings.TrimSpace(cfg.LLM.Embedding.APIKey)
	cfg.LLM.Embedding.BaseURL = strings.TrimSpace(cfg.LLM.Embedding.BaseURL)
	cfg.LLM.Embedding.Model = strings.TrimSpace(cfg.LLM.Embedding.Model)

	for _, field := range []struct {
		path  string
		value string
	}{
		{path: "llm.chat.api_key", value: cfg.LLM.Chat.APIKey},
		{path: "llm.chat.base_url", value: cfg.LLM.Chat.BaseURL},
		{path: "llm.chat.model", value: cfg.LLM.Chat.Model},
		{path: "llm.embedding.api_key", value: cfg.LLM.Embedding.APIKey},
		{path: "llm.embedding.base_url", value: cfg.LLM.Embedding.BaseURL},
		{path: "llm.embedding.model", value: cfg.LLM.Embedding.Model},
	} {
		if field.value == "" {
			return fmt.Errorf("%s is required", field.path)
		}
	}
	if isPlaceholderAPIKey(cfg.LLM.Chat.APIKey) {
		return fmt.Errorf("llm.chat.api_key is not configured")
	}
	if isPlaceholderAPIKey(cfg.LLM.Embedding.APIKey) {
		return fmt.Errorf("llm.embedding.api_key is not configured")
	}
	if cfg.LLM.Chat.MaxTokens <= 0 {
		return fmt.Errorf("llm.chat.max_tokens must be greater than zero")
	}
	return nil
}

func isPlaceholderAPIKey(value string) bool {
	switch strings.ToLower(value) {
	case "你的key", "your-api-key":
		return true
	default:
		return false
	}
}
