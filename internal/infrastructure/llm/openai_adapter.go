package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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
	APIKey      string
	BaseURL     string
	Model       string
	MaxTokens   int
	Temperature *float32
	Timeout     time.Duration
}

func NewOpenAIAdapter(ctx context.Context, config ChatConfig) (*OpenAIAdapter, error) {
	maxTokens := config.MaxTokens
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:      config.APIKey,
		BaseURL:     config.BaseURL,
		Model:       config.Model,
		MaxTokens:   &maxTokens,
		Temperature: config.Temperature,
		Timeout:     config.Timeout,
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
func (a *OpenAIAdapter) Generate(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
) (string, error) {
	return a.generate(ctx, systemPrompt, userPrompt)
}

func (a *OpenAIAdapter) GenerateJSONObject(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
) (string, error) {
	return a.generate(
		ctx,
		systemPrompt,
		userPrompt,
		openai.WithExtraFields(map[string]any{
			"response_format": map[string]any{
				"type": "json_object",
			},
		}),
	)
}

func (a *OpenAIAdapter) generate(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	opts ...model.Option,
) (string, error) {
	resp, err := withRetry(ctx, a.retryPolicy, func() (*schema.Message, error) {
		resp, err := a.chatModel.Generate(
			ctx,
			messagesFor(systemPrompt, userPrompt),
			opts...,
		)
		if err := normalizeProviderError("chat generate", ctx, err); err != nil {
			return nil, err
		}
		if resp == nil || strings.TrimSpace(resp.Content) == "" {
			return nil, &ModelResponseError{}
		}
		return resp, nil
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

type streamAttemptResult struct {
	contentDelivered bool
}

// StreamGenerate 实现领域层的 agents.LLMService 接口，支持流式输出
func (a *OpenAIAdapter) StreamGenerate(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	onChunk func(string) error,
) error {
	if onChunk == nil {
		return fmt.Errorf("openai stream callback is nil")
	}

	_, err := withRetryIf(
		ctx,
		a.retryPolicy,
		func() (streamAttemptResult, error) {
			return a.streamGenerateAttempt(ctx, systemPrompt, userPrompt, onChunk)
		},
		func(result streamAttemptResult, _ error) bool {
			return !result.contentDelivered
		},
	)
	return err
}

func (a *OpenAIAdapter) streamGenerateAttempt(
	ctx context.Context,
	systemPrompt string,
	userPrompt string,
	onChunk func(string) error,
) (streamAttemptResult, error) {
	var result streamAttemptResult
	sr, err := a.chatModel.Stream(ctx, messagesFor(systemPrompt, userPrompt))
	if err != nil {
		return result, normalizeProviderError("chat stream start", ctx, err)
	}
	if sr == nil {
		return result, &ModelResponseError{}
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

	var pendingContent strings.Builder
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		msg, recvErr := sr.Recv()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		if msg != nil && msg.Content != "" {
			content := msg.Content
			if !result.contentDelivered {
				pendingContent.WriteString(content)
				if strings.TrimSpace(pendingContent.String()) == "" {
					content = ""
				} else {
					content = pendingContent.String()
					pendingContent.Reset()
					result.contentDelivered = true
				}
			}
			if content != "" {
				if err := onChunk(content); err != nil {
					return result, err
				}
			}
		}
		if errors.Is(recvErr, io.EOF) {
			if !result.contentDelivered {
				return result, &ModelResponseError{}
			}
			return result, nil
		}
		if recvErr != nil {
			return result, normalizeProviderError("chat stream receive", ctx, recvErr)
		}
	}
}
