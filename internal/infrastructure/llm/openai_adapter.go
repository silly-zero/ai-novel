package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

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

func messagesFor(systemPrompt, userPrompt string) []*schema.Message {
	return []*schema.Message{
		schema.SystemMessage(systemPrompt),
		schema.UserMessage(userPrompt),
	}
}

// Generate 实现领域层的 agents.LLMService 接口
func (a *OpenAIAdapter) Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	resp, err := a.chatModel.Generate(ctx, messagesFor(systemPrompt, userPrompt))
	if err != nil {
		return "", fmt.Errorf("openai generate error: %w", err)
	}

	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("openai returned empty response")
	}

	return resp.Content, nil
}

// StreamGenerate 实现领域层的 agents.LLMService 接口，支持流式输出
func (a *OpenAIAdapter) StreamGenerate(ctx context.Context, systemPrompt, userPrompt string, onChunk func(string) error) error {
	if onChunk == nil {
		return fmt.Errorf("openai stream callback is nil")
	}

	sr, err := a.chatModel.Stream(ctx, messagesFor(systemPrompt, userPrompt))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("openai stream error: %w", err)
	}

	var closeOnce sync.Once
	closeStream := func() {
		closeOnce.Do(sr.Close)
	}
	streamDone := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Go(func() {
		select {
		case <-ctx.Done():
			closeStream()
		case <-streamDone:
		}
	})
	defer func() {
		close(streamDone)
		closeStream()
		watcher.Wait()
	}()

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		msg, recvErr := sr.Recv()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if msg != nil && msg.Content != "" {
			if err := onChunk(msg.Content); err != nil {
				return err
			}
		}
		if errors.Is(recvErr, io.EOF) {
			return nil
		}
		if recvErr != nil {
			return fmt.Errorf("openai stream receive error: %w", recvErr)
		}
	}
}
