package vectorstore

import (
	"context"
	"math"
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
	return &MemoryVectorStore{
		entries: make([]*memory.MemoryEntry, 0),
	}
}

// Add 实现 memory.VectorStore 接口
func (s *MemoryVectorStore) Add(ctx context.Context, entries []*memory.MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entries...)
	return nil
}

// Search 实现 memory.VectorStore 接口 (使用余弦相似度)
func (s *MemoryVectorStore) Search(ctx context.Context, novelID string, queryVector []float32, limit int) ([]*memory.MemoryEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type scoreResult struct {
		entry *memory.MemoryEntry
		score float32
	}

	results := make([]scoreResult, 0)

	for _, entry := range s.entries {
		// 只在同一个小说范围内搜索
		if entry.NovelID != novelID {
			continue
		}

		score := cosineSimilarity(queryVector, entry.Embedding)
		results = append(results, scoreResult{entry: entry, score: score})
	}

	// 按相似度得分从高到低排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	// 返回 Top-K 结果
	final := make([]*memory.MemoryEntry, 0)
	for i := 0; i < len(results) && i < limit; i++ {
		final = append(final, results[i].entry)
	}

	return final, nil
}

// cosineSimilarity 计算余弦相似度
func cosineSimilarity(v1, v2 []float32) float32 {
	if len(v1) != len(v2) {
		return 0
	}
	var dotProduct, norm1, norm2 float64
	for i := range v1 {
		dotProduct += float64(v1[i]) * float64(v2[i])
		norm1 += float64(v1[i]) * float64(v1[i])
		norm2 += float64(v2[i]) * float64(v2[i])
	}
	if norm1 == 0 || norm2 == 0 {
		return 0
	}
	return float32(dotProduct / (math.Sqrt(norm1) * math.Sqrt(norm2)))
}
