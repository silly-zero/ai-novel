package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type writerTestLLM struct {
	chunks      []string
	streamErr   error
	afterStream func()
}

func (f *writerTestLLM) Generate(context.Context, string, string) (string, error) {
	return "", nil
}

func (f *writerTestLLM) StreamGenerate(ctx context.Context, _, _ string, onChunk func(string) error) error {
	for _, chunk := range f.chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := onChunk(chunk); err != nil {
			return err
		}
	}
	if f.afterStream != nil {
		f.afterStream()
	}
	return f.streamErr
}

func TestWriterRunCompletesStream(t *testing.T) {
	llm := &writerTestLLM{chunks: []string{"第一段", "第二段"}}
	writer := NewWriterAgent(llm, nil)
	state := &GenerationState{
		SceneCard: "场景",
		Context:   "背景",
		Draft:     "旧草稿",
		Critique:  "修改意见",
	}

	got, err := writer.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got.Draft != "第一段第二段" {
		t.Fatalf("Draft = %q, want %q", got.Draft, "第一段第二段")
	}
	if got.Critique != "" {
		t.Fatalf("Critique = %q, want empty", got.Critique)
	}
}

func TestWriterRunReturnsStreamErrorWithoutReplacingDraft(t *testing.T) {
	streamErr := errors.New("connection reset")
	llm := &writerTestLLM{
		chunks:    []string{"不完整正文"},
		streamErr: streamErr,
	}
	writer := NewWriterAgent(llm, nil)
	state := &GenerationState{
		SceneCard: "场景",
		Context:   "背景",
		Draft:     "旧草稿",
		Critique:  "修改意见",
	}

	got, err := writer.Run(context.Background(), state)
	if !errors.Is(err, streamErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, streamErr)
	}
	if !strings.Contains(err.Error(), "writer agent stream failed") {
		t.Fatalf("Run() error = %q, want stream failure context", err)
	}
	if got.Draft != "旧草稿" {
		t.Fatalf("Draft = %q, want original draft", got.Draft)
	}
	if got.Critique != "修改意见" {
		t.Fatalf("Critique = %q, want original critique", got.Critique)
	}
}

func TestWriterRunReturnsCanceledContextWithoutReplacingDraft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	writer := NewWriterAgent(&writerTestLLM{chunks: []string{"不会生成"}}, nil)
	state := &GenerationState{
		SceneCard: "场景",
		Context:   "背景",
		Draft:     "旧草稿",
	}

	got, err := writer.Run(ctx, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if !strings.Contains(err.Error(), "writer agent stream canceled") {
		t.Fatalf("Run() error = %q, want cancellation context", err)
	}
	if got.Draft != "旧草稿" {
		t.Fatalf("Draft = %q, want original draft", got.Draft)
	}
}

func TestWriterRunChecksContextAfterSuccessfulStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := NewWriterAgent(&writerTestLLM{
		chunks:      []string{"未提交正文"},
		afterStream: cancel,
	}, nil)
	state := &GenerationState{
		SceneCard: "场景",
		Context:   "背景",
		Draft:     "旧草稿",
		Critique:  "修改意见",
	}

	got, err := writer.Run(ctx, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
	if got.Draft != "旧草稿" {
		t.Fatalf("Draft = %q, want original draft", got.Draft)
	}
	if got.Critique != "修改意见" {
		t.Fatalf("Critique = %q, want original critique", got.Critique)
	}
}
