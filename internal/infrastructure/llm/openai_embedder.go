package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino-ext/components/embedding/openai"
	"github.com/cloudwego/eino/components/embedding"
)

// OpenAIEmbedder 将 Eino 的 Embedding 组件适配为领域层的 memory.Embedder
type OpenAIEmbedder struct {
	embedder    embedding.Embedder
	retryPolicy retryPolicy
}

type EmbeddingConfig struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
}

func NewOpenAIEmbedder(
	ctx context.Context,
	config EmbeddingConfig,
) (*OpenAIEmbedder, error) {
	emb, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
		APIKey:  config.APIKey,
		BaseURL: config.BaseURL,
		Model:   config.Model,
		Timeout: config.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init eino openai embedding component: %w", err)
	}

	return &OpenAIEmbedder{
		embedder:    emb,
		retryPolicy: defaultRetryPolicy(),
	}, nil
}

// EmbedText 实现 memory.Embedder 接口
func (e *OpenAIEmbedder) EmbedText(ctx context.Context, text string) ([]float32, error) {
	// 2. 调用 Eino 的 EmbedStrings 方法 (注意：Eino 返回的是 [][]float64)
	vectors, err := withRetry(ctx, e.retryPolicy, func() ([][]float64, error) {
		vectors, err := e.embedder.EmbedStrings(ctx, []string{text})
		return vectors, normalizeProviderError("embedding text", ctx, err)
	})
	if err != nil {
		return nil, err
	}

	if len(vectors) == 0 {
		return nil, fmt.Errorf("openai returned empty vectors")
	}

	// 3. 转换为 []float32
	res := make([]float32, len(vectors[0]))
	for i, v := range vectors[0] {
		res[i] = float32(v)
	}

	return res, nil
}

// EmbedBatch 批量转换向量
func (e *OpenAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	vectors, err := withRetry(ctx, e.retryPolicy, func() ([][]float64, error) {
		vectors, err := e.embedder.EmbedStrings(ctx, texts)
		return vectors, normalizeProviderError("embedding batch", ctx, err)
	})
	if err != nil {
		return nil, err
	}

	res := make([][]float32, len(vectors))
	for i, vec := range vectors {
		res[i] = make([]float32, len(vec))
		for j, v := range vec {
			res[i][j] = float32(v)
		}
	}

	return res, nil
}
