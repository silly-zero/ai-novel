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
	RolePlot      AgentRole = "Plot"      // 编剧 (从 Idea 生成大纲)
	RoleArchitect AgentRole = "Architect" // 架构师 (生成全书大纲)
)

// GenerationState 承载一次小说生成任务中的上下文状态
type GenerationState struct {
	NovelID      string
	ChapterID    string
	ChapterIndex int    // 当前章节序号
	Idea         string // 初始想法 (一句话 Idea)
	FullOutline  string // 全书大纲 (由 Architect Agent 生成)
	Outline      string // 当前章节剧情大纲 (由 Plot Agent 生成)
	SceneCard    string // 导演拆解出的场景卡
	Context      string // 图书管理员检索出的背景资料 (角色设定、前情提要)
	Draft        string // 主笔生成的草稿
	Critique     string // 审查员的修改意见
	RetryCount   int    // 重试次数
	IsApproved   bool   // 是否通过审查
}

// Agent 是所有智能体的顶级抽象接口
// 采用类似 Actor-Critic 和 State Graph 的思想，Agent 接收当前状态并返回新状态
type Agent interface {
	// Role 返回当前 Agent 的角色
	Role() AgentRole

	// Run 执行 Agent 的核心逻辑，接收当前状态并返回更新后的状态
	Run(ctx context.Context, state *GenerationState) (*GenerationState, error)
}
