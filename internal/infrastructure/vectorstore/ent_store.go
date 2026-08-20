package vectorstore

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql"
	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/ent/memoryentry"
	"github.com/ai-novel/studio/ent/predicate"
	"github.com/ai-novel/studio/internal/domain/memory"
)

// EntVectorStore 实现了 memory.VectorStore 接口，使用 PostgreSQL 存储
type EntVectorStore struct {
	client *ent.Client
}

func NewEntVectorStore(client *ent.Client) *EntVectorStore {
	return &EntVectorStore{client: client}
}

func chapterBoundaryPredicate(metadataColumn string, beforeChapterIndex int) *sql.Predicate {
	value := fmt.Sprintf(
		"CASE WHEN jsonb_typeof(%s->'chapter_index') = 'number' AND %s->>'chapter_status' IN ('Draft', 'Published') THEN (%s->>'chapter_index')::numeric ELSE NULL END",
		metadataColumn,
		metadataColumn,
		metadataColumn,
	)
	return sql.P(func(builder *sql.Builder) {
		builder.WriteString(fmt.Sprintf(
			"%s BETWEEN 1 AND 9007199254740991 AND %s = trunc(%s) AND %s < ",
			value,
			value,
			value,
			value,
		)).Arg(beforeChapterIndex)
	})
}

func (s *EntVectorStore) Add(ctx context.Context, entries []*memory.MemoryEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEntries(entries); err != nil {
		return err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("start memory transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	client := tx.Client()
	for _, entry := range entries {
		create := client.MemoryEntry.Create().
			SetNovelID(entry.NovelID).
			SetContent(entry.Content).
			SetMetadata(entry.Metadata).
			SetEmbedding(entry.Embedding)
		if entry.DedupeKey != "" {
			if err := create.SetDedupeKey(entry.DedupeKey).
				OnConflictColumns(memoryentry.FieldDedupeKey).
				UpdateNewValues().
				Exec(ctx); err != nil {
				return fmt.Errorf("upsert memory entry: %w", err)
			}
			continue
		}
		if _, err := create.Save(ctx); err != nil {
			return fmt.Errorf("create memory entry: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit memory entries: %w", err)
	}
	committed = true
	return nil
}

func (s *EntVectorStore) MarkChapterStale(ctx context.Context, novelID, chapterID string) error {
	rows, err := s.client.MemoryEntry.Query().Where(memoryentry.NovelID(novelID)).All(ctx)
	if err != nil {
		return fmt.Errorf("query chapter memories: %w", err)
	}
	for _, row := range rows {
		if row.Metadata == nil || row.Metadata["chapter_id"] != chapterID {
			continue
		}
		metadata := make(map[string]any, len(row.Metadata)+1)
		for key, value := range row.Metadata {
			metadata[key] = value
		}
		metadata["chapter_status"] = "Stale"
		if err := s.client.MemoryEntry.UpdateOneID(row.ID).SetMetadata(metadata).Exec(ctx); err != nil {
			return fmt.Errorf("mark chapter memory stale: %w", err)
		}
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
	query := s.client.MemoryEntry.Query().
		Where(memoryentry.NovelID(novelID)).
		Order(
			memoryentry.ByCreatedAt(sql.OrderDesc()),
			memoryentry.ByID(sql.OrderDesc()),
		)
	if options.BeforeChapterIndex > 0 {
		query.Where(predicate.MemoryEntry(func(selector *sql.Selector) {
			selector.Where(chapterBoundaryPredicate(
				selector.C(memoryentry.FieldMetadata),
				options.BeforeChapterIndex,
			))
		}))
	}
	rows, err := query.Limit(options.CandidateLimit).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query memory entries: %w", err)
	}

	candidates := make([]*memory.MemoryEntry, 0, min(len(rows), options.CandidateLimit))
	for _, row := range rows {
		entry := &memory.MemoryEntry{
			ID:        fmt.Sprintf("%d", row.ID),
			DedupeKey: row.DedupeKey,
			NovelID:   row.NovelID,
			Content:   row.Content,
			Metadata:  row.Metadata,
			Embedding: row.Embedding,
		}
		if !memoryEntryWithinChapterBoundary(entry, options) {
			continue
		}
		candidates = append(candidates, entry)
		if len(candidates) == options.CandidateLimit {
			break
		}
	}
	return rankCandidates(ctx, queryVector, candidates, options)
}
