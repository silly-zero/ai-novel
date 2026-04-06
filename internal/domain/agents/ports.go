package agents

import (
	"context"
)

// LLMService 定义了 Agent 依赖的语言模型服务接口 (依赖倒置，实现在 infrastructure)
type LLMService interface {
	Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error)
	// StreamGenerate 流式生成文本
	StreamGenerate(ctx context.Context, systemPrompt, userPrompt string) (<-chan string, error)
}
