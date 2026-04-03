package llm

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// OpenAIAdapter 将 Eino 的 ChatModel 适配为领域层的 LLMService
type OpenAIAdapter struct {
	chatModel model.ChatModel
}

// NewOpenAIAdapter 构造函数，支持自定义 APIKey, BaseURL 和 Model
func NewOpenAIAdapter(ctx context.Context, apiKey, baseURL, modelName string) (*OpenAIAdapter, error) {
	// 1. 初始化 Eino OpenAI 组件
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   modelName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init eino openai component: %w", err)
	}

	return &OpenAIAdapter{
		chatModel: cm,
	}, nil
}

// Generate 实现领域层的 agents.LLMService 接口
func (a *OpenAIAdapter) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	// 2. 将提示词转换为 Eino 的 schema.Message
	messages := []*schema.Message{
		schema.SystemMessage(systemPrompt),
		schema.UserMessage(userPrompt),
	}

	// 3. 调用 Eino 的 Generate 方法
	resp, err := a.chatModel.Generate(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("openai generate error: %w", err)
	}

	// 4. 返回生成的文本内容
	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("openai returned empty response")
	}

	return resp.Content, nil
}
