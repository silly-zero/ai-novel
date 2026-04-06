package config

import (
	"fmt"
	"github.com/spf13/viper"
	"strings"
)

type Config struct {
	App AppConfig `mapstructure:"app"`
	LLM LLMConfig `mapstructure:"llm"`
}

type AppConfig struct {
	Name string `mapstructure:"name"`
	Env  string `mapstructure:"env"`
}

type LLMConfig struct {
	OpenAI OpenAIConfig `mapstructure:"openai"`
}

type OpenAIConfig struct {
	APIKey         string `mapstructure:"api_key"`
	BaseURL        string `mapstructure:"base_url"`
	Model          string `mapstructure:"model"`
	EmbeddingModel string `mapstructure:"embedding_model"`
}

func LoadConfig(configPath string) (*Config, error) {
	v := viper.New()

	// 1. 设置配置文件名和路径
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(configPath)
	v.AddConfigPath(".") // 也可以在当前目录找

	// 2. 支持环境变量覆盖 (例如：APP_ENV 覆盖 app.env)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 3. 读取配置文件
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// 4. 解析到结构体
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}
