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
	RoleCharacter AgentRole = "Character" // 角色管理 (维护人物档案)
)

// GenerationStreamEventType 标识实时生成事件类型
type GenerationStreamEventType string

const (
	GenerationStreamEventToken GenerationStreamEventType = "token"
	GenerationStreamEventRetry GenerationStreamEventType = "retry"
)

// GenerationStreamEvent 承载一次生成请求中的有序实时输出
type GenerationStreamEvent struct {
	Type       GenerationStreamEventType
	Token      string
	RetryCount int
	Critique   string
}

// GenerationStreamSink 同步投递一次生成请求中的实时输出
type GenerationStreamSink func(context.Context, GenerationStreamEvent) error

// ContinuityPacket carries the structured handoff between adjacent chapters.
type ContinuityPacket struct {
	LastBeat   string   `json:"last_beat"`
	OpenLoops  []string `json:"open_loops"`
	NextAction string   `json:"next_action"`
}

func (p ContinuityPacket) IsEmpty() bool {
	return p.LastBeat == "" && len(p.OpenLoops) == 0 && p.NextAction == ""
}

type ChapterContract struct {
	Goal          string   `json:"chapter_goal"`
	MustHappen    []string `json:"must_happen"`
	MustNotHappen []string `json:"must_not_happen"`
	EndState      string   `json:"end_state"`
}

type ContractRequirementAssessment struct {
	Satisfied bool   `json:"satisfied"`
	Evidence  string `json:"evidence"`
}

type ChapterContractAssessment struct {
	Goal          ContractRequirementAssessment   `json:"goal"`
	MustHappen    []ContractRequirementAssessment `json:"must_happen"`
	MustNotHappen []ContractRequirementAssessment `json:"must_not_happen"`
	EndState      ContractRequirementAssessment   `json:"end_state"`
}

type ContinuityAssessment struct {
	ChapterHead *ContractRequirementAssessment `json:"chapter_head"`
	ChapterTail ContractRequirementAssessment  `json:"chapter_tail"`
}

type CanonConstraint struct {
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Statement string `json:"statement"`
}

type CanonConsistencyAssessment struct {
	ConstraintIndex int    `json:"constraint_index"`
	Satisfied       bool   `json:"satisfied"`
	Evidence        string `json:"evidence"`
}

type MainlineAssessment struct {
	CurrentEvent ContractRequirementAssessment  `json:"current_event"`
	NextEvent    *ContractRequirementAssessment `json:"next_event"`
}

func (a MainlineAssessment) IsEmpty() bool {
	return a.CurrentEvent == (ContractRequirementAssessment{}) && a.NextEvent == nil
}

func (c ChapterContract) IsEmpty() bool {
	return c.Goal == "" && len(c.MustHappen) == 0 && len(c.MustNotHappen) == 0 && c.EndState == ""
}

type MainlineEventBeat struct {
	ChapterIndex int
	CurrentEvent string
	NextEvent    string
}

// GenerationState 承载一次小说生成任务中的上下文状态
type GenerationState struct {
	GenerationID         string
	StreamSink           GenerationStreamSink
	NovelID              string
	ChapterID            string
	ChapterIndex         int                          // 当前章节序号
	Idea                 string                       // 初始想法 (一句话 Idea)
	FullOutline          string                       // 全书大纲 (由 Architect Agent 生成)
	ExistingOutline      string                       // 已有全书大纲（续写时参考）
	OutlineStart         int                          // 生成大纲的起始章
	OutlineEnd           int                          // 生成大纲的结束章
	Outline              string                       // 当前章节剧情大纲 (由 Plot Agent 生成)
	ChapterContract      ChapterContract              // 当前章节必须遵守的结构化剧情契约
	MainlineBeat         MainlineEventBeat            // 从全书逐章大纲确定性选出的当前/下一主线节拍
	MainlineAssessment   MainlineAssessment           // 最终草稿的主线事件节拍评估
	ContractAssessment   ChapterContractAssessment    // 最终草稿对章节契约的瞬时评估
	ContinuityAssessment ContinuityAssessment         // 最终草稿的章首承接与章尾接力评估
	CanonConstraints     []CanonConstraint            // 本次上下文准备冻结的角色/世界账本约束
	CanonAssessment      []CanonConsistencyAssessment // 最终草稿对账本约束的瞬时评估
	SceneCard            string                       // 导演拆解出的场景卡
	EditorNotes          string                       // 人工干预：作者/编辑给出的指令或限制
	ManualContext        string                       // 人工补充的资料片段（优先注入到 Context）
	Context              string                       // 图书管理员检索出的背景资料 (角色设定、前情提要)
	PreviousContinuity   ContinuityPacket             // 上一章的结构化接力状态
	Draft                string                       // 主笔生成的草稿
	Critique             string                       // 审查员的修改意见
	Continuity           ContinuityPacket             // 当前草稿对应的结构化接力状态
	RetryCount           int                          // 重试次数
	IsApproved           bool                         // 是否通过审查
}

// Agent 是所有智能体的顶级抽象接口
// 采用类似 Actor-Critic 和 State Graph 的思想，Agent 接收当前状态并返回新状态
type Agent interface {
	// Role 返回当前 Agent 的角色
	Role() AgentRole

	// Run 执行 Agent 的核心逻辑，接收当前状态并返回更新后的状态
	Run(ctx context.Context, state *GenerationState) (*GenerationState, error)
}
