package memory

import "context"

// Embedder 定义了将文本转换为向量的能力接口
type Embedder interface {
	// EmbedText 将一段文本转换为高维向量 ([]float32)
	EmbedText(ctx context.Context, text string) ([]float32, error)

	// EmbedBatch 批量转换，提高效率
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

// MemoryEntry 代表一条存入向量库的记忆记录
type MemoryEntry struct {
	ID        string
	DedupeKey string
	NovelID   string
	Content   string
	Metadata  map[string]any
	Embedding []float32
}

type SearchOptions struct {
	CandidateLimit     int
	ResultLimit        int
	MinSimilarity      float32
	BeforeChapterIndex int
}

type SearchResult struct {
	Entry *MemoryEntry
	Score float32
}

// VectorStore 定义了向量数据库的存取接口 (Repository 模式)
type VectorStore interface {
	// Add 将记忆存入库中
	Add(ctx context.Context, entries []*MemoryEntry) error

	// Search 根据查询向量返回达到最低相关度的有序记忆
	Search(ctx context.Context, novelID string, queryVector []float32, options SearchOptions) ([]SearchResult, error)
}
