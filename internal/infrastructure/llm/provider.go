package llm

import "context"

// Provider 是基础设施层提供给所有 Agent 调用的统一大模型网关 (ACL防腐层)
type Provider interface {
	// GenerateText 流式生成文本 (SSE 支持)
	GenerateText(ctx context.Context, req *CompletionRequest) (<-chan string, error)

	// GenerateStructured 结构化输出 (如 JSON 模式提取角色设定)
	GenerateStructured(ctx context.Context, req *CompletionRequest, target interface{}) error
}

// CompletionRequest 封装了与具体 LLM 提供商解耦的请求结构
type CompletionRequest struct {
	Model       string
	Temperature float32
	MaxTokens   int
	System      string
	Messages    []Message
}

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content string
}
