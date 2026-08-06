package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfig = `
database:
  postgres:
    host: localhost
    port: 5432
    user: postgres
    password: test
    dbname: ai_novel
    sslmode: disable
llm:
  chat:
    api_key: chat-test-key
    base_url: https://chat.example/v1
    model: chat-model
    max_tokens: 2048
    timeout: 5m
  embedding:
    api_key: embedding-test-key
    base_url: https://embedding.example/v1
    model: embedding-model
    timeout: 30s
`

var environmentKeys = []string{
	"APP_LISTEN_ADDR",
	"APP_CORS_ORIGINS",
	"APP_MAX_CONCURRENT_GENERATIONS",
	"APP_READ_HEADER_TIMEOUT",
	"APP_READ_TIMEOUT",
	"APP_WRITE_TIMEOUT",
	"APP_IDLE_TIMEOUT",
	"APP_GENERATION_TIMEOUT",
	"APP_STARTUP_TIMEOUT",
	"APP_SHUTDOWN_TIMEOUT",
	"LLM_CHAT_API_KEY",
	"LLM_CHAT_BASE_URL",
	"LLM_CHAT_MODEL",
	"LLM_CHAT_MAX_TOKENS",
	"LLM_CHAT_TIMEOUT",
	"LLM_EMBEDDING_API_KEY",
	"LLM_EMBEDDING_BASE_URL",
	"LLM_EMBEDDING_MODEL",
	"LLM_EMBEDDING_TIMEOUT",
	"RAG_MIN_SIMILARITY",
	"RAG_CANDIDATE_LIMIT",
	"RAG_RESULT_LIMIT",
	"RAG_MAX_QUERIES",
	"RAG_MAX_CONTEXT_MEMORIES",
}

func unsetTestEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range environmentKeys {
		value, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func loadTestConfig(t *testing.T, content string) (*Config, error) {
	t.Helper()
	unsetTestEnvironment(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(dir)
}

func TestLoadConfigReadsSplitModelConfiguration(t *testing.T) {
	cfg, err := loadTestConfig(t, validConfig)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.App.ListenAddr != "127.0.0.1:8081" ||
		cfg.App.MaxConcurrentGenerations != 2 ||
		cfg.App.GenerationTimeout != 30*time.Minute ||
		len(cfg.App.CorsOrigins) != 2 {
		t.Fatalf("app defaults = %#v", cfg.App)
	}
	if cfg.LLM.Chat.APIKey != "chat-test-key" ||
		cfg.LLM.Chat.BaseURL != "https://chat.example/v1" ||
		cfg.LLM.Chat.Model != "chat-model" || cfg.LLM.Chat.MaxTokens != 2048 ||
		cfg.LLM.Chat.Timeout != 5*time.Minute {
		t.Fatalf("chat config = %#v", cfg.LLM.Chat)
	}
	if cfg.LLM.Embedding.APIKey != "embedding-test-key" ||
		cfg.LLM.Embedding.BaseURL != "https://embedding.example/v1" ||
		cfg.LLM.Embedding.Model != "embedding-model" ||
		cfg.LLM.Embedding.Timeout != 30*time.Second {
		t.Fatalf("embedding config = %#v", cfg.LLM.Embedding)
	}
	if cfg.RAG.MinSimilarity != 0.55 || cfg.RAG.CandidateLimit != 100 ||
		cfg.RAG.ResultLimit != 4 || cfg.RAG.MaxQueries != 4 ||
		cfg.RAG.MaxContextMemories != 8 {
		t.Fatalf("rag defaults = %#v", cfg.RAG)
	}
}

func TestLoadConfigReadsEnvironmentWithoutConfigFile(t *testing.T) {
	unsetTestEnvironment(t)
	values := map[string]string{
		"LLM_CHAT_API_KEY":       "env-chat-key",
		"LLM_CHAT_BASE_URL":      "https://env-chat.example/v1",
		"LLM_CHAT_MODEL":         "env-chat-model",
		"LLM_CHAT_MAX_TOKENS":    "4096",
		"LLM_CHAT_TIMEOUT":       "45s",
		"LLM_EMBEDDING_API_KEY":  "env-embedding-key",
		"LLM_EMBEDDING_BASE_URL": "https://env-embedding.example/v1",
		"LLM_EMBEDDING_MODEL":    "env-embedding-model",
		"LLM_EMBEDDING_TIMEOUT":  "20s",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	dir := t.TempDir()

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.LLM.Chat.APIKey != "env-chat-key" || cfg.LLM.Chat.MaxTokens != 4096 ||
		cfg.LLM.Chat.Timeout != 45*time.Second {
		t.Fatalf("chat env config = %#v", cfg.LLM.Chat)
	}
	if cfg.LLM.Embedding.APIKey != "env-embedding-key" ||
		cfg.LLM.Embedding.Timeout != 20*time.Second {
		t.Fatalf("embedding env config = %#v", cfg.LLM.Embedding)
	}
}

func TestLoadConfigEnvironmentOverridesYAML(t *testing.T) {
	unsetTestEnvironment(t)
	t.Setenv("LLM_CHAT_MODEL", "env-chat-model")
	t.Setenv("LLM_CHAT_MAX_TOKENS", "4096")
	t.Setenv("LLM_EMBEDDING_BASE_URL", "https://env-embedding.example/v1")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.LLM.Chat.Model != "env-chat-model" || cfg.LLM.Chat.MaxTokens != 4096 {
		t.Fatalf("chat env overrides = %#v", cfg.LLM.Chat)
	}
	if cfg.LLM.Embedding.BaseURL != "https://env-embedding.example/v1" {
		t.Fatalf("embedding env overrides = %#v", cfg.LLM.Embedding)
	}
}

func TestLoadConfigRejectsInvalidModelConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{name: "chat api key", old: "api_key: chat-test-key", new: "api_key: ''", wantErr: "llm.chat.api_key"},
		{name: "chat placeholder", old: "api_key: chat-test-key", new: "api_key: your-api-key", wantErr: "llm.chat.api_key"},
		{name: "chat base url", old: "base_url: https://chat.example/v1", new: "base_url: ''", wantErr: "llm.chat.base_url"},
		{name: "chat model", old: "model: chat-model", new: "model: ''", wantErr: "llm.chat.model"},
		{name: "max tokens", old: "max_tokens: 2048", new: "max_tokens: 0", wantErr: "llm.chat.max_tokens"},
		{name: "max tokens boolean", old: "max_tokens: 2048", new: "max_tokens: true", wantErr: "llm.chat.max_tokens"},
		{name: "max tokens float", old: "max_tokens: 2048", new: "max_tokens: 1.9", wantErr: "llm.chat.max_tokens"},
		{name: "max tokens text", old: "max_tokens: 2048", new: "max_tokens: invalid", wantErr: "llm.chat.max_tokens"},
		{name: "chat timeout missing", old: "timeout: 5m", new: "timeout: ''", wantErr: "llm.chat.timeout"},
		{name: "chat timeout unit", old: "timeout: 5m", new: "timeout: 300", wantErr: "llm.chat.timeout"},
		{name: "chat timeout negative", old: "timeout: 5m", new: "timeout: -1s", wantErr: "llm.chat.timeout"},
		{name: "embedding api key", old: "api_key: embedding-test-key", new: "api_key: ''", wantErr: "llm.embedding.api_key"},
		{name: "embedding base url", old: "base_url: https://embedding.example/v1", new: "base_url: ''", wantErr: "llm.embedding.base_url"},
		{name: "embedding model", old: "model: embedding-model", new: "model: ''", wantErr: "llm.embedding.model"},
		{name: "embedding timeout", old: "timeout: 30s", new: "timeout: invalid", wantErr: "llm.embedding.timeout"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadTestConfig(t, strings.Replace(validConfig, test.old, test.new, 1))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want field %s", err, test.wantErr)
			}
			if strings.Contains(err.Error(), "chat-test-key") ||
				strings.Contains(err.Error(), "embedding-test-key") {
				t.Fatalf("error exposed API key: %v", err)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidAppConfiguration(t *testing.T) {
	tests := []struct {
		key     string
		value   string
		wantErr string
	}{
		{key: "APP_LISTEN_ADDR", value: " ", wantErr: "app.listen_addr"},
		{key: "APP_LISTEN_ADDR", value: ":8081", wantErr: "app.listen_addr"},
		{key: "APP_LISTEN_ADDR", value: "0.0.0.0:8081", wantErr: "app.listen_addr"},
		{key: "APP_LISTEN_ADDR", value: "192.168.1.10:8081", wantErr: "app.listen_addr"},
		{key: "APP_LISTEN_ADDR", value: "127.0.0.1:0", wantErr: "app.listen_addr"},
		{key: "APP_CORS_ORIGINS", value: "*", wantErr: "app.cors_origins"},
		{key: "APP_CORS_ORIGINS", value: "localhost:5173", wantErr: "app.cors_origins"},
		{key: "APP_CORS_ORIGINS", value: "http://", wantErr: "app.cors_origins"},
		{key: "APP_CORS_ORIGINS", value: "http://192.168.1.10:5173", wantErr: "app.cors_origins"},
		{key: "APP_CORS_ORIGINS", value: "http://localhost:5173/path", wantErr: "app.cors_origins"},
		{key: "APP_CORS_ORIGINS", value: "http://localhost:5173?x=1", wantErr: "app.cors_origins"},
		{key: "APP_CORS_ORIGINS", value: "http://user@localhost:5173", wantErr: "app.cors_origins"},
		{key: "APP_MAX_CONCURRENT_GENERATIONS", value: "0", wantErr: "app.max_concurrent_generations"},
		{key: "APP_READ_TIMEOUT", value: "15", wantErr: "app.read_timeout"},
		{key: "APP_GENERATION_TIMEOUT", value: "-1s", wantErr: "app.generation_timeout"},
	}
	for _, test := range tests {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			unsetTestEnvironment(t)
			t.Setenv(test.key, test.value)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(validConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(dir)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want field %s", err, test.wantErr)
			}
		})
	}
}
func TestLoadConfigReadsRAGConfiguration(t *testing.T) {
	cfg, err := loadTestConfig(t, validConfig+`
rag:
  min_similarity: 0.6
  candidate_limit: 20
  result_limit: 2
  max_queries: 3
  max_context_memories: 4
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RAG.MinSimilarity != 0.6 || cfg.RAG.CandidateLimit != 20 ||
		cfg.RAG.ResultLimit != 2 || cfg.RAG.MaxQueries != 3 ||
		cfg.RAG.MaxContextMemories != 4 {
		t.Fatalf("rag yaml config = %#v", cfg.RAG)
	}
}

func TestLoadConfigRAGEnvironmentOverridesYAML(t *testing.T) {
	unsetTestEnvironment(t)
	t.Setenv("RAG_MIN_SIMILARITY", "0.7")
	t.Setenv("RAG_CANDIDATE_LIMIT", "25")
	t.Setenv("RAG_RESULT_LIMIT", "3")
	t.Setenv("RAG_MAX_QUERIES", "2")
	t.Setenv("RAG_MAX_CONTEXT_MEMORIES", "5")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(validConfig+`
rag:
  min_similarity: 0.6
  candidate_limit: 20
  result_limit: 2
  max_queries: 3
  max_context_memories: 4
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RAG.MinSimilarity != 0.7 || cfg.RAG.CandidateLimit != 25 ||
		cfg.RAG.ResultLimit != 3 || cfg.RAG.MaxQueries != 2 ||
		cfg.RAG.MaxContextMemories != 5 {
		t.Fatalf("rag config = %#v", cfg.RAG)
	}
}

func TestLoadConfigRejectsInvalidRAGConfiguration(t *testing.T) {
	tests := []struct {
		key     string
		value   string
		wantErr string
	}{
		{key: "RAG_MIN_SIMILARITY", value: " ", wantErr: "rag.min_similarity"},
		{key: "RAG_MIN_SIMILARITY", value: "true", wantErr: "rag.min_similarity"},
		{key: "RAG_MIN_SIMILARITY", value: "NaN", wantErr: "rag.min_similarity"},
		{key: "RAG_MIN_SIMILARITY", value: "+Inf", wantErr: "rag.min_similarity"},
		{key: "RAG_MIN_SIMILARITY", value: "-0.1", wantErr: "rag.min_similarity"},
		{key: "RAG_MIN_SIMILARITY", value: "1.1", wantErr: "rag.min_similarity"},
		{key: "RAG_CANDIDATE_LIMIT", value: "0", wantErr: "rag.candidate_limit"},
		{key: "RAG_RESULT_LIMIT", value: "-1", wantErr: "rag.result_limit"},
		{key: "RAG_MAX_QUERIES", value: "1.5", wantErr: "rag.max_queries"},
		{key: "RAG_MAX_CONTEXT_MEMORIES", value: "true", wantErr: "rag.max_context_memories"},
		{key: "RAG_CANDIDATE_LIMIT", value: "2", wantErr: "rag.candidate_limit"},
	}
	for _, test := range tests {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			unsetTestEnvironment(t)
			t.Setenv(test.key, test.value)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(validConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(dir)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want field %s", err, test.wantErr)
			}
		})
	}
}

func TestLoadConfigRejectsOldOpenAIShape(t *testing.T) {
	_, err := loadTestConfig(t, `
llm:
  openai:
    api_key: test-key
    base_url: https://example.com/v1
    model: old-model
    embedding_model: old-embedding
    max_tokens: 2048
`)
	if err == nil || !strings.Contains(err.Error(), "llm.chat.max_tokens") {
		t.Fatalf("error = %v, want missing new chat config", err)
	}
}
