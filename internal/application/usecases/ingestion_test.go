package usecases

import (
	"context"
	"testing"

	"github.com/ai-novel/studio/internal/domain/events"
	"github.com/ai-novel/studio/internal/domain/memory"
)

type ingestionLLMFake struct{}

func (ingestionLLMFake) Generate(context.Context, string, string) (string, error) {
	return "章节摘要", nil
}

func (ingestionLLMFake) StreamGenerate(
	context.Context,
	string,
	string,
	func(string) error,
) error {
	return nil
}

type ingestionEmbedderFake struct{}

func (ingestionEmbedderFake) EmbedText(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}

func (ingestionEmbedderFake) EmbedBatch(
	context.Context,
	[]string,
) ([][]float32, error) {
	return nil, nil
}

type ingestionVectorStoreFake struct {
	entries []*memory.MemoryEntry
}

func (s *ingestionVectorStoreFake) Add(
	_ context.Context,
	entries []*memory.MemoryEntry,
) error {
	s.entries = entries
	return nil
}

func (s *ingestionVectorStoreFake) Search(
	context.Context,
	string,
	[]float32,
	memory.SearchOptions,
) ([]memory.SearchResult, error) {
	return nil, nil
}

func TestHandleChapterGeneratedStoresGenerationMetadata(t *testing.T) {
	store := &ingestionVectorStoreFake{}
	useCase := NewIngestionUseCase(
		ingestionLLMFake{},
		ingestionEmbedderFake{},
		store,
	)

	err := useCase.HandleChapterGenerated(
		context.Background(),
		events.ChapterGeneratedEvent{
			GenerationID: "generation-1",
			NovelID:      "7",
			ChapterID:    "11",
			ChapterIndex: 4,
			Content:      "正文",
		},
	)
	if err != nil {
		t.Fatalf("HandleChapterGenerated returned error: %v", err)
	}
	if len(store.entries) != 1 {
		t.Fatalf("stored entries = %d, want 1", len(store.entries))
	}
	metadata := store.entries[0].Metadata
	if metadata["generation_id"] != "generation-1" ||
		metadata["chapter_id"] != "11" || metadata["chapter_index"] != 4 ||
		metadata["type"] != "plot_summary" {
		t.Fatalf("metadata = %#v", metadata)
	}
}
