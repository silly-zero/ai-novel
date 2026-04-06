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
	messages := []*schema.Message{
		schema.SystemMessage(systemPrompt),
		schema.UserMessage(userPrompt),
	}

	resp, err := a.chatModel.Generate(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("openai generate error: %w", err)
	}

	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("openai returned empty response")
	}

	return resp.Content, nil
}

// StreamGenerate 实现领域层的 agents.LLMService 接口，支持流式输出
func (a *OpenAIAdapter) StreamGenerate(ctx context.Context, systemPrompt, userPrompt string) (<-chan string, error) {
	messages := []*schema.Message{
		schema.SystemMessage(systemPrompt),
		schema.UserMessage(userPrompt),
	}

	// 调用 Eino 的 Stream 方法
	sr, err := a.chatModel.Stream(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("openai stream error: %w", err)
	}

	out := make(chan string)
	go func() {
		defer close(out)
		defer sr.Close()
		for {
			msg, err := sr.Recv()
			if err != nil {
				// 结束或出错
				return
			}
			out <- msg.Content
		}
	}()

	return out, nil
}
