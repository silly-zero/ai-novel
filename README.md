# AI Novel Studio

基于 **Golang** + **DDD (领域驱动设计)** + **Multi-Agent (多智能体协作)** 架构的企业级 AI 小说生成系统。

## 🎯 核心设计理念

传统的 AI 小说生成往往是单向流水线，容易导致角色 OOC（Out of Character）、设定冲突和剧情注水。本项目将小说创作过程抽象为一个**“虚拟作家工作室”**，通过多个专业 Agent 协作、审查、重写，并结合长短期记忆（RAG），实现高质量的长篇小说生成。

### 🤖 智能体角色分配 (Multi-Agent System)

1. **Director Agent (主编/导演)**: 把控全局节奏，负责将大纲拆解为章节级的“场景卡 (Scene Cards)”。
2. **Librarian Agent (资料管理员)**: 负责 RAG (检索增强生成)，从向量数据库中检索当前场景所需的角色设定、历史剧情和伏笔，构建精准的 Context。
3. **Writer Agent (主笔)**: 负责具体章节的文本生成，遣词造句。
4. **Reviewer Agent (审查员)**: 负责阅读草稿，进行一致性检查（是否偏离大纲、是否 OOC），并生成 Critique（修改意见）打回重写。

### 🏗 架构分层 (Clean Architecture + DDD)

项目严格遵循依赖倒置原则，核心业务逻辑完全与外部框架解耦。

```text
├── cmd/
│   └── server/                 # 应用程序入口
├── internal/
│   ├── domain/                 # 领域层：纯 Go 代码，定义实体、值对象和领域接口
│   │   ├── agents/             # 多智能体核心接口
│   │   ├── events/             # 领域事件 (EDA)
│   │   ├── memory/             # 记忆模型抽象 (短期记忆、长期向量记忆)
│   │   └── novel/              # 小说、章节聚合根
│   ├── application/            # 应用层：工作流编排和用例
│   │   ├── usecases/           # 外部可调用的业务用例
│   │   └── workflows/          # 基于状态机/DAG 的 Agent 工作流引擎
│   ├── infrastructure/         # 基础设施层：具体技术实现
│   │   ├── database/           # 关系型数据库实现 (PostgreSQL)
│   │   ├── llm/                # 大模型防腐层 (OpenAI, Anthropic 等)
│   │   └── vectorstore/        # 向量检索实现 (pgvector/Milvus)
│   └── interfaces/             # 接口层：HTTP API, SSE 流式输出
└── pkg/                        # 公共工具库 (Logger, Tracer)
```

## 🚀 核心技术栈

*   **语言**: Go 1.21+
*   **Web 框架**: go-chi/chi
*   **数据库 & ORM**: PostgreSQL + ent (Facebook 开源图关系 ORM)
*   **向量检索**: pgvector
*   **异步任务**: Temporal 或 asynq
*   **可观测性**: OpenTelemetry (Trace & Span 记录 Agent 思维链)

## 📋 当前任务路线图 (Roadmap)

- [x] 初始化 Go 项目并搭建基于 Agent+DDD 的目录结构
- [x] 生成任务 README 和核心框架接口文件
- [ ] 定义多智能体协作模型与 Agent 接口实现
- [ ] 实现核心领域模型与领域事件总线 (Novel, Chapter, Memory, EventBus)
- [ ] 设计并实现 Agent 的长期与短期记忆系统 (RAG)
- [ ] 实现 Agent 工作流引擎 (状态机/DAG 控制生成、审查、重写流程)
- [ ] 集成基础设施 (LLM API, Postgres, VectorDB)
- [ ] 提供 SSE 流式输出的 API 接口
