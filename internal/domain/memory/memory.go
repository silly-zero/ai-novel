package memory

import "context"

// MemoryEntry 代表一条记忆记录
type MemoryEntry struct {
	ID        string
	NovelID   string
	Content   string // 记忆内容 (如：林平之发现了辟邪剑谱)
	Tags      []string
	Embedding []float32 // 向量化表示，用于相似度检索
}

// ShortTermMemory 短期记忆接口 (通常是最近 N 章的原文)
type ShortTermMemory interface {
	Add(ctx context.Context, novelID string, content string) error
	GetRecent(ctx context.Context, novelID string, limit int) ([]string, error)
}

// LongTermMemory 长期记忆接口 (基于向量数据库的 RAG)
type LongTermMemory interface {
	// Store 将新的设定或剧情提要存入向量库
	Store(ctx context.Context, entry *MemoryEntry) error

	// Retrieve 根据查询字符串 (如当前场景描述) 检索最相关的 N 条记忆
	Retrieve(ctx context.Context, novelID string, query string, topK int) ([]MemoryEntry, error)
}
