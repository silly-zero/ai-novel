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
func TestWriterRunInjectsChapterContractOnRewrite(t *testing.T) {
	llm := &writerTestLLM{chunks: []string{"新正文"}}
	state := &GenerationState{
		SceneCard:       "场景",
		Context:         "背景",
		ChapterContract: validChapterContract(),
		Draft:           "旧正文",
		Critique:        "补齐血书情节",
	}

	if _, err := NewWriterAgent(llm).Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"主角确认密门与身世有关",
		"主角进入密门",
		"主角发现旧王朝血书",
		"揭晓最终反派身份",
		"主角决定追踪血书指向的地下祭坛",
		"旧正文",
		"补齐血书情节",
	} {
		if !strings.Contains(llm.userPrompt, value) {
			t.Fatalf("writer prompt missing %q: %s", value, llm.userPrompt)
		}
	}
	for _, rule := range []string{"完成全部 MustHappen", "不得执行 MustNotHappen", "章尾达到 EndState"} {
		if !strings.Contains(llm.systemPrompt, rule) {
			t.Fatalf("writer system prompt missing %q: %s", rule, llm.systemPrompt)
		}
	}
}

func TestWriterRunInjectsMainlineBeat(t *testing.T) {
	llm := &writerTestLLM{chunks: []string{"新正文"}}
	state := &GenerationState{
		SceneCard: "场景",
		Context:   "背景",
		MainlineBeat: MainlineEventBeat{
			ChapterIndex: 4,
			CurrentEvent: "主角找到血书",
			NextEvent:    "主角前往地下祭坛",
		},
	}

	if _, err := NewWriterAgent(llm).Run(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"第4章", "主角找到血书", "主角前往地下祭坛"} {
		if !strings.Contains(llm.userPrompt, value) {
			t.Fatalf("writer prompt missing %q: %s", value, llm.userPrompt)
		}
	}
	for _, rule := range []string{"实际呈现本章事件", "不得在本章提前完成"} {
		if !strings.Contains(llm.systemPrompt, rule) {
			t.Fatalf("writer system prompt missing %q: %s", rule, llm.systemPrompt)
		}
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
		ContractAssessment: ChapterContractAssessment{
			Goal: ContractRequirementAssessment{
				Satisfied: true,
				Evidence:  "旧草稿依据",
			},
		},
		IsApproved: true,
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
	if got.ContractAssessment.Goal != (ContractRequirementAssessment{}) ||
		len(got.ContractAssessment.MustHappen) != 0 ||
		len(got.ContractAssessment.MustNotHappen) != 0 ||
		got.ContractAssessment.EndState != (ContractRequirementAssessment{}) {
		t.Fatalf("ContractAssessment = %#v, want empty", got.ContractAssessment)
	}
	if got.IsApproved {
		t.Fatal("IsApproved = true, want false for unreviewed draft")
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
		ContractAssessment: ChapterContractAssessment{
			Goal: ContractRequirementAssessment{
				Satisfied: true,
				Evidence:  "旧草稿依据",
			},
		},
		IsApproved: true,
	}
	originalAssessment := state.ContractAssessment

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
	if got.ContractAssessment.Goal != originalAssessment.Goal || !got.IsApproved {
		t.Fatalf("review state changed on stream failure: assessment = %#v, approved = %v", got.ContractAssessment, got.IsApproved)
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
