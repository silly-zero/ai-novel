package vectorstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"

	"github.com/ai-novel/studio/internal/domain/memory"
)

// MemoryVectorStore 是一个基于内存的简单向量存储实现，用于开发和演示
type MemoryVectorStore struct {
	mu      sync.RWMutex
	entries []*memory.MemoryEntry
}

func NewMemoryVectorStore() *MemoryVectorStore {
	return &MemoryVectorStore{entries: make([]*memory.MemoryEntry, 0)}
}

func (s *MemoryVectorStore) Add(ctx context.Context, entries []*memory.MemoryEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEntries(entries); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range entries {
		replaced := false
		if entry.DedupeKey != "" {
			for index, existing := range s.entries {
				if existing != nil && existing.DedupeKey == entry.DedupeKey {
					s.entries[index] = entry
					replaced = true
					break
				}
			}
		}
		if !replaced {
			s.entries = append(s.entries, entry)
		}
	}
	return nil
}

func (s *MemoryVectorStore) Search(
	ctx context.Context,
	novelID string,
	queryVector []float32,
	options memory.SearchOptions,
) ([]memory.SearchResult, error) {
	if err := validateSearch(queryVector, options); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	candidates := make([]*memory.MemoryEntry, 0, options.CandidateLimit)
	for index := len(s.entries) - 1; index >= 0 && len(candidates) < options.CandidateLimit; index-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := s.entries[index]
		if entry != nil && entry.NovelID == novelID && memoryEntryWithinChapterBoundary(entry, options) {
			candidates = append(candidates, entry)
		}
	}
	return rankCandidates(ctx, queryVector, candidates, options)
}

func memoryEntryWithinChapterBoundary(entry *memory.MemoryEntry, options memory.SearchOptions) bool {
	if options.BeforeChapterIndex <= 0 {
		return true
	}
	if entry == nil || entry.Metadata == nil {
		return false
	}
	status, ok := entry.Metadata["chapter_status"].(string)
	if !ok || (status != "Draft" && status != "Published") {
		return false
	}
	chapterIndex, ok := memoryChapterIndex(entry.Metadata["chapter_index"])
	return ok && chapterIndex < options.BeforeChapterIndex
}

func memoryChapterIndex(value any) (int, bool) {
	maxInt := int(^uint(0) >> 1)
	const maxExactJSONInteger int64 = 1<<53 - 1
	maxChapterIndex := int64(maxInt)
	if maxChapterIndex > maxExactJSONInteger {
		maxChapterIndex = maxExactJSONInteger
	}
	switch value := value.(type) {
	case int:
		return value, value > 0 && int64(value) <= maxChapterIndex
	case int8:
		return int(value), value > 0
	case int16:
		return int(value), value > 0
	case int32:
		return int(value), value > 0 && int64(value) <= maxChapterIndex
	case int64:
		return int(value), value > 0 && value <= maxChapterIndex
	case uint:
		return int(value), value > 0 && uint64(value) <= uint64(maxChapterIndex)
	case uint8:
		return int(value), value > 0
	case uint16:
		return int(value), value > 0
	case uint32:
		return int(value), value > 0 && uint64(value) <= uint64(maxChapterIndex)
	case uint64:
		return int(value), value > 0 && value <= uint64(maxChapterIndex)
	case float32:
		index := int(value)
		return index, value > 0 && float64(value) <= float64(maxChapterIndex) && float32(index) == value
	case float64:
		index := int(value)
		return index, value > 0 && value <= float64(maxChapterIndex) && float64(index) == value
	case json.Number:
		if _, err := json.Marshal(value); err != nil {
			return 0, false
		}
		parsed, ok := new(big.Rat).SetString(value.String())
		if !ok || !parsed.IsInt() || parsed.Sign() <= 0 || !parsed.Num().IsInt64() {
			return 0, false
		}
		index := parsed.Num().Int64()
		return int(index), index <= maxChapterIndex
	default:
		return 0, false
	}
}

func validateEntries(entries []*memory.MemoryEntry) error {
	if len(entries) == 0 {
		return errors.New("memory entries must not be empty")
	}
	dimension := 0
	for index, entry := range entries {
		if entry == nil {
			return fmt.Errorf("memory entry %d is nil", index)
		}
		if err := validateVector(entry.Embedding); err != nil {
			return fmt.Errorf("memory entry %d embedding: %w", index, err)
		}
		if dimension == 0 {
			dimension = len(entry.Embedding)
		} else if len(entry.Embedding) != dimension {
			return fmt.Errorf("memory entry %d embedding dimension %d does not match batch dimension %d", index, len(entry.Embedding), dimension)
		}
	}
	return nil
}

func validateSearch(queryVector []float32, options memory.SearchOptions) error {
	if err := validateVector(queryVector); err != nil {
		return fmt.Errorf("query vector: %w", err)
	}
	if options.CandidateLimit <= 0 {
		return errors.New("candidate limit must be greater than zero")
	}
	if options.ResultLimit <= 0 {
		return errors.New("result limit must be greater than zero")
	}
	if options.CandidateLimit < options.ResultLimit {
		return errors.New("candidate limit must be greater than or equal to result limit")
	}
	if math.IsNaN(float64(options.MinSimilarity)) || math.IsInf(float64(options.MinSimilarity), 0) ||
		options.MinSimilarity < 0 || options.MinSimilarity > 1 {
		return errors.New("minimum similarity must be between zero and one")
	}
	return nil
}

func validateVector(vector []float32) error {
	if len(vector) == 0 {
		return errors.New("vector must not be empty")
	}
	var norm float64
	for _, value := range vector {
		converted := float64(value)
		if math.IsNaN(converted) || math.IsInf(converted, 0) {
			return errors.New("vector must contain only finite values")
		}
		norm += converted * converted
	}
	if norm == 0 {
		return errors.New("vector norm must be greater than zero")
	}
	return nil
}

func rankCandidates(
	ctx context.Context,
	queryVector []float32,
	candidates []*memory.MemoryEntry,
	options memory.SearchOptions,
) ([]memory.SearchResult, error) {
	results := make([]memory.SearchResult, 0, min(len(candidates), options.ResultLimit))
	for _, entry := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry == nil || len(entry.Embedding) != len(queryVector) || validateVector(entry.Embedding) != nil {
			continue
		}
		score := cosineSimilarity(queryVector, entry.Embedding)
		if math.IsNaN(float64(score)) || math.IsInf(float64(score), 0) || score < options.MinSimilarity {
			continue
		}
		results = append(results, memory.SearchResult{Entry: entry, Score: score})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Entry.ID < results[j].Entry.ID
	})
	if len(results) > options.ResultLimit {
		results = results[:options.ResultLimit]
	}
	return results, nil
}

func cosineSimilarity(v1, v2 []float32) float32 {
	var dotProduct, norm1, norm2 float64
	for i := range v1 {
		dotProduct += float64(v1[i]) * float64(v2[i])
		norm1 += float64(v1[i]) * float64(v1[i])
		norm2 += float64(v2[i]) * float64(v2[i])
	}
	return float32(dotProduct / (math.Sqrt(norm1) * math.Sqrt(norm2)))
}
