# AI Novel Studio (Eino Edition)

基于 **Golang** + **DDD (领域驱动设计)** + **Eino (Multi-Agent 框架)** 的企业级 AI 小说生成系统。

## 🎯 项目愿景

通过构建一个“虚拟作家工作室”，解决传统 AI 生成小说存在的“吃设定”、剧情不连贯、角色 OOC 等核心痛点。利用多智能体协作（Multi-Agent）与长短期记忆（RAG）技术，产出逻辑严密、行文优美、字数过百万的长篇小说。

## 🚀 核心架构设计

项目采用 **Clean Architecture** 分层，确保业务逻辑与具体技术实现（如 LLM 提供商、数据库）完全解耦。

### 🤖 智能体工作室 (Multi-Agent Workflows)

依托 **[CloudWeGo/Eino](https://github.com/cloudwego/eino)** 框架，我们将小说创作流程建模为一个有向图 (State Graph)：

- **Architect Agent (架构师)**: 接收一句话 Idea，自动构思并规划整部小说的全书大纲（前 10 章概括）。
- **Character Agent (人设师)**: **(新)** 自动从剧情中提取并维护角色卡（姓名、外貌、性格、地位），确保人物设定不崩坏。
- **Plot Agent (编剧)**: 根据全书大纲和当前章节序号，自动生成详细的本章剧情大纲。
- **Director Agent (主编)**: 拆解大纲，规划场景，生成“场景卡”。
- **Librarian Agent (资料员)**: 执行 **智能 RAG 检索**。利用 LLM 制定检索计划，结合结构化角色档案检索与向量数据库检索，为写作提供精准的上下文。
- **Writer Agent (主笔)**: 负责具体章节撰写，根据场景卡与背景资料遣词造句。支持 **Token 级流式输出**。
- **Reviewer Agent (审查员)**: 负责质量把关。如果不合格，会生成修改意见并触发 `Writer` 重写，形成 Actor-Critic 闭环。

### 🧠 记忆系统与异步解耦

- **RAG 系统**: 
  - **Embedding**: 利用 OpenAI `text-embedding-3` 模型。
  - **Vector Store**: 采用 PostgreSQL + 内存余弦相似度检索，确保数据持久化且检索高效。
- **EventBus (异步神经网络)**:
  - 采用 **领域事件 (Domain Events)** 机制。
  - 监听 `token.generated` 实现 API 端的流式推送 (SSE)。
  - 监听 `chapter.generated` 触发记忆存储（Ingestion）、日志记录等异步流程。

## 📂 项目结构 (Project Structure)

```text
ai-novel/
├── cmd/
│   └── server/                # 应用程序入口
├── configs/
│   └── config.yaml            # 核心配置文件 (LLM, Embedding, 数据库等)
├── ent/                       # Ent ORM 生成代码 (数据库 Schema)
├── internal/
│   ├── application/           # 应用层：业务流程编排
│   │   ├── usecases/          # 业务用例 (如 Ingestion 记忆注入)
│   │   └── workflows/         # Eino 工作流引擎实现
│   ├── domain/                # 领域层：纯业务逻辑 (无外部依赖)
│   │   ├── agents/            # Agent 角色定义与行为接口
│   │   ├── events/            # 领域事件定义 (EventBus 契约)
│   │   ├── memory/            # 记忆模型与向量检索接口
│   │   └── novel/             # 小说、章节聚合根
│   ├── infrastructure/        # 基础设施层：技术选型具体实现
│   │   ├── config/            # Viper 配置加载器
│   │   ├── database/          # PostgreSQL + Ent 实现
│   │   ├── eventbus/          # 异步事件总线实现 (Go Channels)
│   │   ├── llm/               # LLM/Embedding 适配器 (Eino-ext)
│   │   └── vectorstore/       # 向量数据库实现 (EntStore)
│   └── interfaces/            # 接口层：外部通信
│       └── api/               # RESTful / SSE 流式接口实现
├── pkg/                       # 公共工具库
└── README.md
```

## 🛠 技术栈

- **语言**: Go 1.18+
- **Agent 框架**: [Eino](https://github.com/cloudwego/eino) (字节跳动开源)
- **ORM**: [Ent](https://entgo.io/) (Facebook 开源)
- **配置管理**: Viper (YAML + 环境变量支持)
- **LLM 组件**: Eino-ext (OpenAI 协议兼容)
- **事件机制**: 进程内异步 EventBus (Channel-based)
- **数据库**: PostgreSQL (支持数据持久化)

## 📦 快速开始

1. **配置**:
   编辑 `configs/config.yaml` 填入 API 和数据库信息：
   ```yaml
   database:
     postgres:
       host: "localhost"
       password: "your-password"
       dbname: "ai_novel"
   llm:
     openai:
       api_key: "your-api-key"
       base_url: "https://api.deepseek.com"
       model: "deepseek-chat"
   ```

2. **运行**:
   ```bash
   go run cmd/server/main.go
   ```

3. **体验流式 API**:
   ```bash
   curl -N "http://localhost:8080/api/v1/novel/generate?novel_id=test-001&outline=写一个主角在深山发现古老遗迹的故事"
   ```

## 📋 任务路线图 (Roadmap)

- [x] 基于 DDD 的标准目录结构搭建
- [x] 集成 Eino 编排 4 大 Agent 协作流 (State Graph)
- [x] 实现 LLM 基础设施适配器 (Chat & Embedding)
- [x] 实现 RAG 检索模型与内存向量库
- [x] 引入领域事件总线 (EventBus) 实现异步解耦
- [x] 实现 Ingestion 订阅者（自动提取剧情摘要并存入记忆库）
- [x] 实现 PostgreSQL + Ent 数据库持久化
- [x] 实现基于 SSE 的流式 API 接口
- [x] 实现 Plot Agent (自动生成章节剧情大纲)
- [x] 实现 Architect Agent (从 Idea 扩展为全书大纲路线图)
- [x] 实现 Character Agent (自动生成并维护角色卡与关系网)
- [x] **优化 Librarian 检索算法 (支持智能检索计划与结构化档案提取)**
- [ ] **Next: 实现 World Agent (维护地理、武学、势力等世界观设定)**
- [ ] **Next: 实现 Graph RAG (基于知识图谱的角色关系深度检索)**


