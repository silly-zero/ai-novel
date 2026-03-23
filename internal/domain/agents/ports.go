package agents

import (
	"context"
)

// LLMService 定义了 Agent 依赖的语言模型服务接口 (依赖倒置，实现在 infrastructure)
type LLMService interface {
	Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}
