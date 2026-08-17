package vectorstore

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/ai-novel/studio/internal/domain/memory"
)

func TestChapterBoundaryPredicateBuildsSafePostgresExpression(t *testing.T) {
	query, args := sql.Dialect(dialect.Postgres).
		Select("*").
		From(sql.Table("memory_entries")).
		Where(chapterBoundaryPredicate("metadata", 4)).
		Query()
	for _, fragment := range []string{
		"CASE WHEN jsonb_typeof(metadata->'chapter_index') = 'number'",
		"::numeric ELSE NULL END",
		"BETWEEN 1 AND 9007199254740991",
		"= trunc(",
		"< $1",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, query)
		}
	}
	if len(args) != 1 || args[0] != 4 {
		t.Fatalf("args = %#v, want [4]", args)
	}
}

func TestMemoryVectorStoreSearchFiltersChapterBoundaryBeforeCandidateLimit(t *testing.T) {
	store := NewMemoryVectorStore()
	entries := []*memory.MemoryEntry{
		{ID: "eligible", NovelID: "novel", Metadata: map[string]any{"chapter_index": 2}, Embedding: []float32{1, 0}},
		{ID: "missing", NovelID: "novel", Metadata: map[string]any{}, Embedding: []float32{1, 0}},
		{ID: "invalid", NovelID: "novel", Metadata: map[string]any{"chapter_index": "3"}, Embedding: []float32{1, 0}},
		{ID: "current", NovelID: "novel", Metadata: map[string]any{"chapter_index": 4}, Embedding: []float32{1, 0}},
		{ID: "future", NovelID: "novel", Metadata: map[string]any{"chapter_index": float64(5)}, Embedding: []float32{1, 0}},
	}
	if err := store.Add(context.Background(), entries); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(context.Background(), "novel", []float32{1, 0}, memory.SearchOptions{
		CandidateLimit:     1,
		ResultLimit:        1,
		MinSimilarity:      0,
		BeforeChapterIndex: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Entry.ID != "eligible" {
		t.Fatalf("results = %#v, want eligible prior chapter", results)
	}
}

func TestMemoryVectorStoreSearchExcludesAllMemoriesBeforeFirstChapter(t *testing.T) {
	store := NewMemoryVectorStore()
	if err := store.Add(context.Background(), []*memory.MemoryEntry{
		{ID: "chapter-1", NovelID: "novel", Metadata: map[string]any{"chapter_index": 1}, Embedding: []float32{1, 0}},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(context.Background(), "novel", []float32{1, 0}, memory.SearchOptions{
		CandidateLimit:     1,
		ResultLimit:        1,
		MinSimilarity:      0,
		BeforeChapterIndex: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %#v, want none before first chapter", results)
	}
}

func TestMemoryChapterIndexAcceptsSupportedIntegerRepresentations(t *testing.T) {
	for _, value := range []any{
		int(2),
		int64(2),
		uint64(2),
		float32(2),
		float64(2),
		json.Number("2"),
		json.Number("2.0"),
		json.Number("2e0"),
	} {
		if index, ok := memoryChapterIndex(value); !ok || index != 2 {
			t.Fatalf("memoryChapterIndex(%#v) = (%d, %v), want (2, true)", value, index, ok)
		}
	}
}

func TestMemoryChapterIndexRejectsOverflowAndInexactValues(t *testing.T) {
	values := []any{
		uint64(^uint64(0)),
		uint64(1 << 53),
		int64(1 << 53),
		float64(1 << 53),
		float64(1.5),
		float32(1.5),
		json.Number("1.5"),
		json.Number("2.0000000000000001"),
		json.Number("9007199254740990.5"),
		json.Number("9007199254740992"),
		json.Number("2/1"),
		json.Number("0x2"),
		json.Number("+2"),
		int64(-1),
		"2",
		nil,
	}
	if ^uint(0)>>63 == 1 {
		largeInt := int64(1 << 53)
		values = append(values, int(largeInt))
	}
	for _, value := range values {
		if index, ok := memoryChapterIndex(value); ok {
			t.Fatalf("memoryChapterIndex(%#v) = (%d, true), want invalid", value, index)
		}
	}
}

func TestMemoryVectorStoreSearchLeavesChapterFilteringDisabledByDefault(t *testing.T) {
	store := NewMemoryVectorStore()
	if err := store.Add(context.Background(), []*memory.MemoryEntry{
		{ID: "legacy", NovelID: "novel", Embedding: []float32{1, 0}},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(context.Background(), "novel", []float32{1, 0}, memory.SearchOptions{
		CandidateLimit: 1,
		ResultLimit:    1,
		MinSimilarity:  0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Entry.ID != "legacy" {
		t.Fatalf("results = %#v, want legacy entry in unbounded search", results)
	}
}

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
