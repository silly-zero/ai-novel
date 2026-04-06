package vectorstore

import (
	"context"
	"fmt"
	"sort"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/memoryentry"
	"github.com/ai-novel/studio/internal/domain/memory"
)

// EntVectorStore 实现了 memory.VectorStore 接口，使用 PostgreSQL 存储
type EntVectorStore struct {
	client *ent.Client
}

func NewEntVectorStore(client *ent.Client) *EntVectorStore {
	return &EntVectorStore{client: client}
}

func (s *EntVectorStore) Add(ctx context.Context, entries []*memory.MemoryEntry) error {
	bulk := make([]*ent.MemoryEntryCreate, len(entries))
	for i, entry := range entries {
		bulk[i] = s.client.MemoryEntry.Create().
			SetNovelID(entry.NovelID).
			SetContent(entry.Content).
			SetMetadata(entry.Metadata).
			SetEmbedding(entry.Embedding)
	}
	_, err := s.client.MemoryEntry.CreateBulk(bulk...).Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to save memory entries to ent: %w", err)
	}
	return nil
}

type entScoreResult struct {
	entry *memory.MemoryEntry
	score float32
}

func (s *EntVectorStore) Search(ctx context.Context, novelID string, queryVector []float32, limit int) ([]*memory.MemoryEntry, error) {
	rows, err := s.client.MemoryEntry.Query().
		Where(memoryentry.NovelID(novelID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query memory entries: %w", err)
	}

	results := make([]entScoreResult, 0)
	for _, row := range rows {
		entry := &memory.MemoryEntry{
			ID:        fmt.Sprintf("%d", row.ID),
			NovelID:   row.NovelID,
			Content:   row.Content,
			Metadata:  row.Metadata,
			Embedding: row.Embedding,
		}
		score := CosineSimilarity(queryVector, entry.Embedding)
		results = append(results, entScoreResult{entry: entry, score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	final := make([]*memory.MemoryEntry, 0)
	for i := 0; i < len(results) && i < limit; i++ {
		final = append(final, results[i].entry)
	}

	return final, nil
}
