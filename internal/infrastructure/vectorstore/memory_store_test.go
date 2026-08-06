package vectorstore

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/ai-novel/studio/internal/domain/memory"
)

func TestMemoryVectorStoreSearchAppliesLatestCandidateWindowAndLimits(t *testing.T) {
	store := NewMemoryVectorStore()
	entries := []*memory.MemoryEntry{
		{ID: "old-best", NovelID: "novel-1", Content: "old", Embedding: []float32{1, 0}},
		{ID: "other", NovelID: "novel-2", Content: "other", Embedding: []float32{1, 0}},
		{ID: "new-low", NovelID: "novel-1", Content: "low", Embedding: []float32{0.6, 0.8}},
		{ID: "new-best", NovelID: "novel-1", Content: "best", Embedding: []float32{0.8, 0.6}},
	}
	if err := store.Add(context.Background(), entries); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(context.Background(), "novel-1", []float32{1, 0}, memory.SearchOptions{
		CandidateLimit: 2,
		ResultLimit:    1,
		MinSimilarity:  0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Entry.ID != "new-best" {
		t.Fatalf("results = %#v, want only new-best", results)
	}
}

func TestMemoryVectorStoreSearchFiltersLowSimilarityAndMalformedStoredVectors(t *testing.T) {
	store := NewMemoryVectorStore()
	store.entries = []*memory.MemoryEntry{
		{ID: "low", NovelID: "novel", Embedding: []float32{0, 1}},
		{ID: "wrong-dimension", NovelID: "novel", Embedding: []float32{1}},
		{ID: "zero", NovelID: "novel", Embedding: []float32{0, 0}},
		{ID: "nan", NovelID: "novel", Embedding: []float32{float32(math.NaN()), 1}},
		{ID: "valid", NovelID: "novel", Embedding: []float32{1, 0}},
	}

	results, err := store.Search(context.Background(), "novel", []float32{1, 0}, memory.SearchOptions{
		CandidateLimit: 5,
		ResultLimit:    5,
		MinSimilarity:  0.75,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Entry.ID != "valid" {
		t.Fatalf("results = %#v, want only valid", results)
	}
}

func TestMemoryVectorStoreSearchUsesDeterministicTieOrder(t *testing.T) {
	store := NewMemoryVectorStore()
	if err := store.Add(context.Background(), []*memory.MemoryEntry{
		{ID: "b", NovelID: "novel", Embedding: []float32{1, 0}},
		{ID: "a", NovelID: "novel", Embedding: []float32{1, 0}},
		{ID: "c", NovelID: "novel", Embedding: []float32{1, 0}},
	}); err != nil {
		t.Fatal(err)
	}
	options := memory.SearchOptions{CandidateLimit: 3, ResultLimit: 3, MinSimilarity: 0}

	for attempt := 0; attempt < 5; attempt++ {
		results, err := store.Search(context.Background(), "novel", []float32{1, 0}, options)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 3 || results[0].Entry.ID != "a" || results[1].Entry.ID != "b" || results[2].Entry.ID != "c" {
			t.Fatalf("attempt %d results = %#v", attempt, results)
		}
	}
}

func TestMemoryVectorStoreRejectsInvalidSearch(t *testing.T) {
	validOptions := memory.SearchOptions{CandidateLimit: 2, ResultLimit: 1, MinSimilarity: 0.5}
	tests := []struct {
		name    string
		vector  []float32
		options memory.SearchOptions
	}{
		{name: "empty query", options: validOptions},
		{name: "zero query", vector: []float32{0, 0}, options: validOptions},
		{name: "nan query", vector: []float32{float32(math.NaN()), 1}, options: validOptions},
		{name: "infinite query", vector: []float32{float32(math.Inf(1)), 1}, options: validOptions},
		{name: "zero candidates", vector: []float32{1}, options: memory.SearchOptions{ResultLimit: 1}},
		{name: "zero results", vector: []float32{1}, options: memory.SearchOptions{CandidateLimit: 1}},
		{name: "results exceed candidates", vector: []float32{1}, options: memory.SearchOptions{CandidateLimit: 1, ResultLimit: 2}},
		{name: "invalid threshold", vector: []float32{1}, options: memory.SearchOptions{CandidateLimit: 1, ResultLimit: 1, MinSimilarity: 1.1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewMemoryVectorStore().Search(context.Background(), "novel", test.vector, test.options); err == nil {
				t.Fatal("Search returned nil error")
			}
		})
	}
}

func TestMemoryVectorStoreAddRejectsInvalidBatchAtomically(t *testing.T) {
	store := NewMemoryVectorStore()
	if err := store.Add(context.Background(), []*memory.MemoryEntry{
		{ID: "existing", NovelID: "novel", Embedding: []float32{1, 0}},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		entries []*memory.MemoryEntry
	}{
		{name: "empty", entries: nil},
		{name: "nil entry", entries: []*memory.MemoryEntry{nil}},
		{name: "zero vector", entries: []*memory.MemoryEntry{{Embedding: []float32{0, 0}}}},
		{name: "invalid vector", entries: []*memory.MemoryEntry{{Embedding: []float32{float32(math.Inf(1))}}}},
		{name: "mixed dimensions", entries: []*memory.MemoryEntry{{Embedding: []float32{1}}, {Embedding: []float32{1, 0}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.Add(context.Background(), test.entries); err == nil {
				t.Fatal("Add returned nil error")
			}
			if len(store.entries) != 1 || store.entries[0].ID != "existing" {
				t.Fatalf("entries changed after rejected batch: %#v", store.entries)
			}
		})
	}
}

func TestMemoryVectorStoreHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemoryVectorStore()
	options := memory.SearchOptions{CandidateLimit: 1, ResultLimit: 1, MinSimilarity: 0}
	if _, err := store.Search(ctx, "novel", []float32{1}, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search error = %v, want context.Canceled", err)
	}
	if err := store.Add(ctx, []*memory.MemoryEntry{{Embedding: []float32{1}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Add error = %v, want context.Canceled", err)
	}
}
