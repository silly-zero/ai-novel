package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// OpenAIAdapter 将 Eino 的 ChatModel 适配为领域层的 LLMService
type OpenAIAdapter struct {
	chatModel   model.ChatModel
	retryPolicy retryPolicy
}

type ChatConfig struct {
	APIKey    string
	BaseURL   string
	Model     string
	MaxTokens int
	Timeout   time.Duration
}

func NewOpenAIAdapter(ctx context.Context, config ChatConfig) (*OpenAIAdapter, error) {
	maxTokens := config.MaxTokens
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:    config.APIKey,
		BaseURL:   config.BaseURL,
		Model:     config.Model,
		MaxTokens: &maxTokens,
		Timeout:   config.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init eino openai component: %w", err)
	}

	return &OpenAIAdapter{
		chatModel:   cm,
		retryPolicy: defaultRetryPolicy(),
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
	resp, err := withRetry(ctx, a.retryPolicy, func() (*schema.Message, error) {
		resp, err := a.chatModel.Generate(ctx, messagesFor(systemPrompt, userPrompt))
		return resp, normalizeProviderError("chat generate", ctx, err)
	})
	if err != nil {
		return "", err
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
