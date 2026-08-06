package vectorstore

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql"
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEntries(entries); err != nil {
		return err
	}
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

func (s *EntVectorStore) Search(
	ctx context.Context,
	novelID string,
	queryVector []float32,
	options memory.SearchOptions,
) ([]memory.SearchResult, error) {
	if err := validateSearch(queryVector, options); err != nil {
		return nil, err
	}
	rows, err := s.client.MemoryEntry.Query().
		Where(memoryentry.NovelID(novelID)).
		Order(
			memoryentry.ByCreatedAt(sql.OrderDesc()),
			memoryentry.ByID(sql.OrderDesc()),
		).
		Limit(options.CandidateLimit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query memory entries: %w", err)
	}

	candidates := make([]*memory.MemoryEntry, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, &memory.MemoryEntry{
			ID:        fmt.Sprintf("%d", row.ID),
			NovelID:   row.NovelID,
			Content:   row.Content,
			Metadata:  row.Metadata,
			Embedding: row.Embedding,
		})
	}
	return rankCandidates(ctx, queryVector, candidates, options)
}
