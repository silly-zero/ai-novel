# AI Novel Studio (Eino Edition)

基于 **Golang** + **DDD (领域驱动设计)** + **Eino (Multi-Agent 框架)** 的企业级 AI 小说生成系统。

## 🎯 项目愿景

通过构建一个“虚拟作家工作室”，解决传统 AI 生成小说存在的“吃设定”、剧情不连贯、角色 OOC 等核心痛点。利用多智能体协作（Multi-Agent）与长短期记忆（RAG）技术，产出逻辑严密、行文优美、字数过百万的长篇小说。

## 🚀 核心架构设计

项目采用 **Clean Architecture** 分层，确保业务逻辑与具体技术实现（如 LLM 提供商、数据库）完全解耦。

### 🤖 智能体工作室 (Multi-Agent Workflows)

依托 **[CloudWeGo/Eino](https://github.com/cloudwego/eino)** 框架，我们将小说创作流程建模为一个有向图 (State Graph)：

- **Director Agent (主编)**: 拆解大纲，规划场景，生成“场景卡”。
- **Librarian Agent (资料员)**: 执行 RAG 检索。利用向量数据库，从数万字的历史剧情中精准提取角色设定与伏笔。
- **Writer Agent (主笔)**: 负责具体章节撰写，根据场景卡与背景资料遣词造句。
- **Reviewer Agent (审查员)**: 负责质量把关。如果不合格，会生成修改意见并触发 `Writer` 重写，形成 Actor-Critic 闭环。

### 🧠 记忆系统与异步解耦

- **RAG 系统**: 
  - **Embedding**: 利用 OpenAI `text-embedding-3` 模型。
  - **Vector Store**: 采用内存向量库进行余弦相似度计算，支持高效检索。
- **EventBus (异步神经网络)**:
  - 采用 **领域事件 (Domain Events)** 机制，当章节生成成功时发布 `ChapterGeneratedEvent`。
  - 通过异步总线触发记忆存储（Ingestion）、日志记录等后续流程，确保主创作流程的极速响应。

## 📂 项目结构 (Project Structure)

```text
ai-novel/
├── cmd/
│   └── server/                # 应用程序入口
│       └── main.go            # 组装基础设施、Agent 与启动工作流
├── configs/
│   └── config.yaml            # 核心配置文件 (LLM, Embedding, 数据库等)
├── internal/
│   ├── application/           # 应用层：业务流程编排
│   │   ├── usecases/          # 业务用例
│   │   └── workflows/         # Eino 工作流引擎实现
│   ├── domain/                # 领域层：纯业务逻辑 (无外部依赖)
│   │   ├── agents/            # Agent 角色定义与行为接口
│   │   ├── events/            # 领域事件定义 (EventBus 契约)
│   │   ├── memory/            # 记忆模型与向量检索接口
│   │   └── novel/             # 小说、章节聚合根
│   ├── infrastructure/        # 基础设施层：技术选型具体实现
│   │   ├── config/            # Viper 配置加载器
│   │   ├── eventbus/          # 异步事件总线实现 (Go Channels)
│   │   ├── llm/               # LLM/Embedding 适配器 (Eino-ext)
│   │   └── vectorstore/       # 向量数据库实现 (Memory/Postgres)
│   └── interfaces/            # 接口层：外部通信
│       └── api/               # RESTful / SSE 接口
├── pkg/                       # 公共工具库 (Logger, Utils)
└── README.md
```

## 🛠 技术栈

- **语言**: Go 1.18+
- **Agent 框架**: [Eino](https://github.com/cloudwego/eino) (字节跳动开源)
- **配置管理**: Viper (YAML + 环境变量支持)
- **LLM 组件**: Eino-ext (OpenAI 协议兼容)
- **事件机制**: 进程内异步 EventBus (Channel-based)
- **数据库 (规划中)**: PostgreSQL + pgvector, ent (ORM)

## 📦 快速开始

1. **配置**:
   编辑 `configs/config.yaml` 填入 API 信息：
   ```yaml
   llm:
     openai:
       api_key: "your-api-key"
       base_url: "https://api.deepseek.com"
       model: "deepseek-chat"
       embedding_model: "text-embedding-3-small"
   ```

2. **运行**:
   ```bash
   go run cmd/server/main.go
   ```

## 📋 任务路线图 (Roadmap)

- [x] 基于 DDD 的标准目录结构搭建
- [x] 集成 Eino 编排 4 大 Agent 协作流 (State Graph)
- [x] 实现 LLM 基础设施适配器 (Chat & Embedding)
- [x] 实现 RAG 检索模型与内存向量库
- [x] **引入领域事件总线 (EventBus) 实现异步解耦**
- [ ] **Next: 实现 Ingestion 订阅者（自动提取剧情摘要并存入记忆库）**
- [ ] 实现 PostgreSQL 数据库持久化 (Novel/Chapter 存储)
- [ ] 实现基于 SSE 的流式 API 接口

---
*本项目由 Trae IDE 辅助开发，致力于打造 Golang 生态下最优雅的 AI Agent 应用范式。*
