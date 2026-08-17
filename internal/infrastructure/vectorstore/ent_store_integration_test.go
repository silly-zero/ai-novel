package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/memoryentry"
	"github.com/ai-novel/studio/internal/domain/memory"
	_ "github.com/lib/pq"
)

func TestEntVectorStoreSearchFiltersChapterBoundaryBeforeLimit(t *testing.T) {
	dsn := os.Getenv("AI_NOVEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AI_NOVEL_TEST_POSTGRES_DSN is not set")
	}
	client, err := ent.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	novelID := fmt.Sprintf("vector-boundary-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = client.MemoryEntry.Delete().Where(memoryentry.NovelID(novelID)).Exec(context.Background())
	})

	store := NewEntVectorStore(client)
	entries := []*memory.MemoryEntry{
		{NovelID: novelID, Content: "eligible-decimal", Metadata: map[string]any{"chapter_index": 2}, Embedding: []float32{1, 0}},
		{NovelID: novelID, Content: "eligible-exponent", Metadata: map[string]any{"chapter_index": 3}, Embedding: []float32{1, 0}},
		{NovelID: novelID, Content: "missing", Metadata: map[string]any{}, Embedding: []float32{1, 0}},
		{NovelID: novelID, Content: "string", Metadata: map[string]any{"chapter_index": "2"}, Embedding: []float32{1, 0}},
		{NovelID: novelID, Content: "object", Metadata: map[string]any{"chapter_index": map[string]any{"value": 2}}, Embedding: []float32{1, 0}},
		{NovelID: novelID, Content: "fraction", Metadata: map[string]any{"chapter_index": 2.5}, Embedding: []float32{1, 0}},
		{NovelID: novelID, Content: "too-large", Metadata: map[string]any{"chapter_index": uint64(1 << 53)}, Embedding: []float32{1, 0}},
		{NovelID: novelID, Content: "current", Metadata: map[string]any{"chapter_index": 4}, Embedding: []float32{1, 0}},
		{NovelID: novelID, Content: "future", Metadata: map[string]any{"chapter_index": 5}, Embedding: []float32{1, 0}},
	}
	if err := store.Add(ctx, entries); err != nil {
		t.Fatal(err)
	}
	if err := client.MemoryEntry.Update().
		Where(memoryentry.NovelID(novelID), memoryentry.Content("eligible-decimal")).
		SetMetadata(map[string]any{"chapter_index": json.RawMessage("2.0")}).
		Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.MemoryEntry.Update().
		Where(memoryentry.NovelID(novelID), memoryentry.Content("eligible-exponent")).
		SetMetadata(map[string]any{"chapter_index": json.RawMessage("3e0")}).
		Exec(ctx); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search(ctx, novelID, []float32{1, 0}, memory.SearchOptions{
		CandidateLimit:     2,
		ResultLimit:        2,
		MinSimilarity:      0,
		BeforeChapterIndex: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string]struct{}, len(results))
	for _, result := range results {
		contents[result.Entry.Content] = struct{}{}
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want both valid numeric representations", results)
	}
	for _, content := range []string{"eligible-decimal", "eligible-exponent"} {
		if _, exists := contents[content]; !exists {
			t.Fatalf("results = %#v, missing %q", results, content)
		}
	}
}
