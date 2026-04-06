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

### 🧠 记忆系统 (RAG 原理)

小说创作是一项长程任务，我们通过以下链路实现“长记性”：
1. **Embedding**: 利用 OpenAI `text-embedding-3` 模型将文本转化为高维向量。
2. **Vector Store**: 目前采用内存向量库（Memory Vector Store）进行余弦相似度计算，支持毫秒级检索。
3. **Retrieval**: 在每一章写作前，自动检索最相关的 3-5 条历史记忆注入 Context。

## 🛠 技术栈

- **语言**: Go 1.18+
- **Agent 框架**: [Eino](https://github.com/cloudwego/eino) (字节跳动开源)
- **配置管理**: Viper
- **LLM 组件**: Eino-ext (支持 OpenAI, DeepSeek, Claude)
- **数据库 (规划中)**: PostgreSQL + pgvector (向量存储), ent (ORM)

## 📦 快速开始

1. **配置**:
   复制 `configs/config.yaml` 并在其中填入你的 API Key：
   ```yaml
   llm:
     openai:
       api_key: "your-api-key"
       base_url: "https://api.deepseek.com" # 支持 DeepSeek 或 OpenAI
       model: "deepseek-chat"
       embedding_model: "text-embedding-3-small"
   ```

2. **运行**:
   ```bash
   go run cmd/server/main.go
   ```

## 📋 任务路线图 (Roadmap)

- [x] 基于 DDD 的目录结构初始化
- [x] 集成 Eino 编排 4 大 Agent 协作流
- [x] 实现 LLM 基础设施适配器 (Chat & Embedding)
- [x] 实现 RAG 检索模型与内存向量库
- [ ] **Next: 实现 Ingestion 链路（自动提取剧情摘要并存入记忆库）**
- [ ] **Next: 引入领域事件 (EventBus) 实现 Agent 异步解耦**
- [ ] 实现 PostgreSQL 数据库持久化
- [ ] 实现基于 SSE 的流式 API 接口

---
*本项目由 Trae IDE 辅助开发，旨在探索 Golang 在 AI Agent 领域的最佳实践。*
