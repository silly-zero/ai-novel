package agents

import (
	"context"
)

// AgentRole 定义智能体的角色枚举
type AgentRole string

const (
	RoleDirector  AgentRole = "Director"  // 导演/主编
	RoleWriter    AgentRole = "Writer"    // 主笔
	RoleReviewer  AgentRole = "Reviewer"  // 审查员
	RoleLibrarian AgentRole = "Librarian" // 资料管理员 (RAG)
)

// GenerationState 承载一次小说生成任务中的上下文状态
type GenerationState struct {
	NovelID      string
	ChapterID    string
	Outline      string   // 当前剧情大纲
	SceneCard    string   // 导演拆解出的场景卡
	Context      string   // 图书管理员检索出的背景资料 (角色设定、前情提要)
	Draft        string   // 主笔生成的草稿
	Critique     string   // 审查员的修改意见
	RetryCount   int      // 重试次数
	IsApproved   bool     // 是否通过审查
}

// Agent 是所有智能体的顶级抽象接口
// 采用类似 Actor-Critic 和 State Graph 的思想，Agent 接收当前状态并返回新状态
type Agent interface {
	// Role 返回当前 Agent 的角色
	Role() AgentRole

	// Run 执行 Agent 的核心逻辑，接收当前状态并返回更新后的状态
	Run(ctx context.Context, state *GenerationState) (*GenerationState, error)
}
