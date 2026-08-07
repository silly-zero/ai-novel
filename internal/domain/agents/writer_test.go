package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type writerTestLLM struct {
	chunks       []string
	streamErr    error
	afterStream  func()
	systemPrompt string
	userPrompt   string
}

func (f *writerTestLLM) Generate(context.Context, string, string) (string, error) {
	return "", nil
}

func (f *writerTestLLM) StreamGenerate(ctx context.Context, systemPrompt, userPrompt string, onChunk func(string) error) error {
	f.systemPrompt = systemPrompt
	f.userPrompt = userPrompt
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

func TestWriterRunDeliversTokensInOrder(t *testing.T) {
	var tokens []string
	writer := NewWriterAgent(&writerTestLLM{chunks: []string{"第一段", "第二段", "第三段"}})
	state := &GenerationState{
		SceneCard: "场景",
		Context:   "背景",
		StreamSink: func(_ context.Context, event GenerationStreamEvent) error {
			if event.Type != GenerationStreamEventToken {
				t.Fatalf("event type = %q, want %q", event.Type, GenerationStreamEventToken)
			}
			tokens = append(tokens, event.Token)
			return nil
		},
	}

	if _, err := writer.Run(context.Background(), state); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := strings.Join(tokens, ""); got != "第一段第二段第三段" {
		t.Fatalf("delivered tokens = %q, want complete ordered draft", got)
	}
}

func TestWriterRunInjectsPreviousContinuityOnRewrite(t *testing.T) {
	llm := &writerTestLLM{chunks: []string{"新正文"}}
	writer := NewWriterAgent(llm)
	state := &GenerationState{
		SceneCard: "场景",
		Context:   "背景",
		Draft:     "旧草稿",
		Critique:  "承接上一章",
		PreviousContinuity: ContinuityPacket{
			LastBeat:   "主角推开密门。",
			OpenLoops:  []string{"密门后是谁", "警报是否触发"},
			NextAction: "主角立即进入密门。",
		},
	}

	if _, err := writer.Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"主角推开密门。", "密门后是谁", "警报是否触发", "主角立即进入密门。", "承接上一章"} {
		if !strings.Contains(llm.userPrompt, value) {
			t.Fatalf("writer prompt missing %q: %s", value, llm.userPrompt)
		}
	}
	if !strings.Contains(llm.systemPrompt, "开头必须承接") {
		t.Fatalf("writer system prompt missing continuity rule: %s", llm.systemPrompt)
	}
}
func TestWriterRunReturnsSinkErrorWithoutReplacingDraft(t *testing.T) {
	sinkErr := errors.New("stream consumer stopped")
	writer := NewWriterAgent(&writerTestLLM{chunks: []string{"第一段", "第二段", "第三段"}})
	state := &GenerationState{
		SceneCard: "场景",
		Context:   "背景",
		Draft:     "旧草稿",
		Critique:  "修改意见",
		StreamSink: func(_ context.Context, event GenerationStreamEvent) error {
			if event.Token == "第二段" {
				return sinkErr
			}
			return nil
		},
	}

	got, err := writer.Run(context.Background(), state)
	if !errors.Is(err, sinkErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, sinkErr)
	}
	if got.Draft != "旧草稿" {
		t.Fatalf("Draft = %q, want original draft", got.Draft)
	}
	if got.Critique != "修改意见" {
		t.Fatalf("Critique = %q, want original critique", got.Critique)
	}
}

func TestWriterRunCompletesStream(t *testing.T) {
	llm := &writerTestLLM{chunks: []string{"第一段", "第二段"}}
	writer := NewWriterAgent(llm)
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
	writer := NewWriterAgent(llm)
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

	writer := NewWriterAgent(&writerTestLLM{chunks: []string{"不会生成"}})
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
	})
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
