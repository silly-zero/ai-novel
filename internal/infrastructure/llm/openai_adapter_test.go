package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type adapterTestChatModel struct {
	stream *schema.StreamReader[*schema.Message]
	err    error
}

func (f *adapterTestChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, nil
}

func (f *adapterTestChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return f.stream, f.err
}

func (f *adapterTestChatModel) BindTools([]*schema.ToolInfo) error {
	return nil
}

func TestOpenAIAdapterStreamGenerateCompletesOnEOF(t *testing.T) {
	reader := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("第一段", nil),
		schema.AssistantMessage("第二段", nil),
	})
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	var content string
	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		content += chunk
		return nil
	})
	if err != nil {
		t.Fatalf("StreamGenerate() error = %v", err)
	}
	if content != "第一段第二段" {
		t.Fatalf("content = %q, want %q", content, "第一段第二段")
	}
}

func TestOpenAIAdapterStreamGenerateReturnsReceiveError(t *testing.T) {
	receiveErr := errors.New("provider stream failed")
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		writer.Send(schema.AssistantMessage("部分正文", nil), nil)
		writer.Send(nil, receiveErr)
		writer.Close()
	}()
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(string) error { return nil })
	if !errors.Is(err, receiveErr) {
		t.Fatalf("StreamGenerate() error = %v, want wrapped %v", err, receiveErr)
	}
}

func TestOpenAIAdapterStreamGenerateReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader, writer := schema.Pipe[*schema.Message](0)
	go func() {
		writer.Send(nil, errors.New("provider canceled"))
		writer.Close()
	}()
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	err := adapter.StreamGenerate(ctx, "system", "user", func(string) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamGenerate() error = %v, want context canceled", err)
	}
}

func TestOpenAIAdapterStreamGenerateReturnsContextCancellationOnEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("部分正文", nil),
	})
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	err := adapter.StreamGenerate(ctx, "system", "user", func(string) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamGenerate() error = %v, want context canceled", err)
	}
}

func TestOpenAIAdapterStreamGenerateReturnsCallbackError(t *testing.T) {
	callbackErr := errors.New("consumer stopped")
	reader := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("第一段", nil),
		schema.AssistantMessage("第二段", nil),
	})
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	calls := 0
	err := adapter.StreamGenerate(context.Background(), "system", "user", func(string) error {
		calls++
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("StreamGenerate() error = %v, want callback error", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
}

func TestOpenAIAdapterStreamGenerateReturnsStartError(t *testing.T) {
	startErr := errors.New("provider unavailable")
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{err: startErr}}

	err := adapter.StreamGenerate(context.Background(), "system", "user", func(string) error { return nil })
	if !errors.Is(err, startErr) {
		t.Fatalf("StreamGenerate() error = %v, want wrapped %v", err, startErr)
	}
}

func TestOpenAIAdapterStreamGenerateRejectsNilCallback(t *testing.T) {
	adapter := &OpenAIAdapter{}

	err := adapter.StreamGenerate(context.Background(), "system", "user", nil)
	if err == nil {
		t.Fatal("StreamGenerate() error = nil, want nil callback error")
	}
}

func TestOpenAIAdapterStreamGenerateCancelsBlockedReceive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := schema.Pipe[*schema.Message](0)
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	result := make(chan error, 1)
	go func() {
		result <- adapter.StreamGenerate(ctx, "system", "user", func(string) error { return nil })
	}()

	cancel()
	go func() {
		writer.Send(nil, context.Canceled)
		writer.Close()
	}()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamGenerate() error = %v, want context canceled", err)
	}
}

func TestOpenAIAdapterStreamGenerateProcessesContentBeforeReceiveError(t *testing.T) {
	receiveErr := errors.New("provider stream failed")
	reader, writer := schema.Pipe[*schema.Message](1)
	writer.Send(schema.AssistantMessage("最后一段", nil), receiveErr)
	writer.Close()
	adapter := &OpenAIAdapter{chatModel: &adapterTestChatModel{stream: reader}}

	var content string
	err := adapter.StreamGenerate(context.Background(), "system", "user", func(chunk string) error {
		content += chunk
		return nil
	})
	if !errors.Is(err, receiveErr) {
		t.Fatalf("StreamGenerate() error = %v, want wrapped %v", err, receiveErr)
	}
	if content != "最后一段" {
		t.Fatalf("content = %q, want %q", content, "最后一段")
	}
}
