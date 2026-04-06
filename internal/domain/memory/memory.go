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
	NovelID   string
	Content   string    // 原始文本内容 (如：林动在青阳镇发现石符)
	Metadata  map[string]interface{} // 扩展信息 (如：出场人物、章节号)
	Embedding []float32 // 对应的向量
}

// VectorStore 定义了向量数据库的存取接口 (Repository 模式)
type VectorStore interface {
	// Add 将记忆存入库中
	Add(ctx context.Context, entries []*MemoryEntry) error
	
	// Search 根据查询向量，找到最相关的 N 条记录
	Search(ctx context.Context, novelID string, queryVector []float32, limit int) ([]*MemoryEntry, error)
}
