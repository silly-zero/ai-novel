package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
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
	Env                      string        `mapstructure:"env"`
	ListenAddr               string        `mapstructure:"listen_addr"`
	CorsOrigins              []string      `mapstructure:"-"`
	MaxConcurrentGenerations int           `mapstructure:"-"`
	ReadHeaderTimeout        time.Duration `mapstructure:"-"`
	ReadTimeout              time.Duration `mapstructure:"-"`
	WriteTimeout             time.Duration `mapstructure:"-"`
	IdleTimeout              time.Duration `mapstructure:"-"`
	GenerationTimeout        time.Duration `mapstructure:"-"`
	StartupTimeout           time.Duration `mapstructure:"-"`
	ShutdownTimeout          time.Duration `mapstructure:"-"`
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
	for key, value := range map[string]any{
		"app.listen_addr":                "127.0.0.1:8081",
		"app.cors_origins":               "http://localhost:5173,http://127.0.0.1:5173",
		"app.max_concurrent_generations": 2,
		"app.read_header_timeout":        "5s",
		"app.read_timeout":               "15s",
		"app.write_timeout":              "30s",
		"app.idle_timeout":               "60s",
		"app.generation_timeout":         "30m",
		"app.startup_timeout":            "15s",
		"app.shutdown_timeout":           "15s",
	} {
		v.SetDefault(key, value)
	}

	for key, env := range map[string]string{
		"app.listen_addr":                "APP_LISTEN_ADDR",
		"app.cors_origins":               "APP_CORS_ORIGINS",
		"app.max_concurrent_generations": "APP_MAX_CONCURRENT_GENERATIONS",
		"app.read_header_timeout":        "APP_READ_HEADER_TIMEOUT",
		"app.read_timeout":               "APP_READ_TIMEOUT",
		"app.write_timeout":              "APP_WRITE_TIMEOUT",
		"app.idle_timeout":               "APP_IDLE_TIMEOUT",
		"app.generation_timeout":         "APP_GENERATION_TIMEOUT",
		"app.startup_timeout":            "APP_STARTUP_TIMEOUT",
		"app.shutdown_timeout":           "APP_SHUTDOWN_TIMEOUT",
	} {
		if err := v.BindEnv(key, env); err != nil {
			return nil, fmt.Errorf("bind %s: %w", key, err)
		}
	}

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
	maxConcurrent, err := parsePositiveInt(v, "app.max_concurrent_generations")
	if err != nil {
		return nil, err
	}
	appDurations := make(map[string]time.Duration)
	for _, key := range []string{
		"app.read_header_timeout",
		"app.read_timeout",
		"app.write_timeout",
		"app.idle_timeout",
		"app.generation_timeout",
		"app.startup_timeout",
		"app.shutdown_timeout",
	} {
		duration, durationErr := parseDuration(v, key)
		if durationErr != nil {
			return nil, durationErr
		}
		appDurations[key] = duration
	}
	origins, err := parseCorsOrigins(v.GetString("app.cors_origins"))
	if err != nil {
		return nil, err
	}

	cfg.App.ListenAddr = strings.TrimSpace(v.GetString("app.listen_addr"))
	cfg.App.CorsOrigins = origins
	cfg.App.MaxConcurrentGenerations = maxConcurrent
	cfg.App.ReadHeaderTimeout = appDurations["app.read_header_timeout"]
	cfg.App.ReadTimeout = appDurations["app.read_timeout"]
	cfg.App.WriteTimeout = appDurations["app.write_timeout"]
	cfg.App.IdleTimeout = appDurations["app.idle_timeout"]
	cfg.App.GenerationTimeout = appDurations["app.generation_timeout"]
	cfg.App.StartupTimeout = appDurations["app.startup_timeout"]
	cfg.App.ShutdownTimeout = appDurations["app.shutdown_timeout"]
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

func parseCorsOrigins(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" ||
			parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
			parsed.RawQuery != "" || parsed.Fragment != "" ||
			!isLoopbackHost(parsed.Hostname()) {
			return nil, errors.New("app.cors_origins must contain local HTTP origins without paths")
		}
		if port := parsed.Port(); port != "" {
			parsedPort, portErr := strconv.Atoi(port)
			if portErr != nil || parsedPort < 1 || parsedPort > 65535 {
				return nil, errors.New("app.cors_origins must contain local HTTP origins with valid ports")
			}
		}
		if _, ok := seen[origin]; ok {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	if len(origins) == 0 {
		return nil, errors.New("app.cors_origins is required")
	}
	return origins, nil
}

func validateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !isLoopbackHost(host) {
		return errors.New("app.listen_addr must use a loopback host and explicit port")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return errors.New("app.listen_addr must use a loopback host and valid port")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	cfg.App.ListenAddr = strings.TrimSpace(cfg.App.ListenAddr)
	if err := validateListenAddr(cfg.App.ListenAddr); err != nil {
		return err
	}
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
