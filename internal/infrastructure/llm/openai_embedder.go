package llm

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/embedding/openai"
	"github.com/cloudwego/eino/components/embedding"
)

// OpenAIEmbedder 将 Eino 的 Embedding 组件适配为领域层的 memory.Embedder
type OpenAIEmbedder struct {
	embedder embedding.Embedder
}

// NewOpenAIEmbedder 构造函数
func NewOpenAIEmbedder(ctx context.Context, apiKey, baseURL, modelName string) (*OpenAIEmbedder, error) {
	// 1. 初始化 Eino OpenAI Embedding 组件
	emb, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   modelName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init eino openai embedding component: %w", err)
	}

	return &OpenAIEmbedder{
		embedder: emb,
	}, nil
}

// EmbedText 实现 memory.Embedder 接口
func (e *OpenAIEmbedder) EmbedText(ctx context.Context, text string) ([]float32, error) {
	// 2. 调用 Eino 的 EmbedStrings 方法 (注意：Eino 返回的是 [][]float64)
	vectors, err := e.embedder.EmbedStrings(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("openai embed text error: %w", err)
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
	vectors, err := e.embedder.EmbedStrings(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("openai embed batch error: %w", err)
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
