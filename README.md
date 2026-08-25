# AI Novel Studio (Eino Edition)

基于 **Golang** + **DDD (领域驱动设计)** + **Eino (Multi-Agent 框架)** 的企业级 AI 小说生成系统。

## 🎯 项目愿景

通过构建一个“虚拟作家工作室”，解决传统 AI 生成小说存在的“吃设定”、剧情不连贯、角色 OOC 等核心痛点。利用多智能体协作（Multi-Agent）与长短期记忆（RAG）技术，产出逻辑严密、行文优美、字数过百万的长篇小说。

## 🚀 核心架构设计

项目采用 **Clean Architecture** 分层，确保业务逻辑与具体技术实现（如 LLM 提供商、数据库）完全解耦。

### 🤖 智能体工作室 (Multi-Agent Workflows)

依托 **[CloudWeGo/Eino](https://github.com/cloudwego/eino)** 框架，我们将小说创作流程建模为一个有向图 (State Graph)：

- **Architect Agent (架构师)**: 接收一句话 Idea，自动构思并规划整部小说的全书大纲（前 10 章概括）。
- **Character Agent (人设师)**: 自动从剧情中提取并维护角色卡（姓名、外貌、性格、地位），确保人物设定不崩坏。
- **World Agent (设定师)**: 自动维护地理、武学等级、势力关系、特殊道具等世界观设定，确保背景逻辑严密。
- **Plot Agent (编剧)**: 根据全书大纲和当前章节序号，自动生成详细的本章剧情大纲。
- **Director Agent (主编)**: 拆解大纲，规划场景，生成“场景卡”。
- **Librarian Agent (资料员)**: 执行 **智能 RAG 检索**。利用 LLM 制定检索计划，结合结构化角色档案检索与向量数据库检索，为写作提供精准的上下文。
- **Writer Agent (主笔)**: 负责具体章节撰写，根据场景卡与背景资料遣词造句。支持 **Token 级流式输出**。
- **Reviewer Agent (审查员)**: 负责质量把关。如果不合格，会生成修改意见并触发 `Writer` 重写，形成 Actor-Critic 闭环。

### 🧠 记忆系统与实时生成

- **RAG 系统**:
  - **Embedding**: 默认使用智谱 OpenAI-compatible API 的 `embedding-3` 模型。
  - **Vector Store**: 采用 PostgreSQL 持久化 JSON 向量，并在 Go 中执行有界的余弦相似度检索。
- **实时生成与 EventBus**:
  - Writer 的正文 Token 与 Retry 通过请求级同步 Sink 有序推送到 SSE，支持背压和取消。
  - 章节成功持久化后发布 `chapter.generated`；订阅处理器并行执行，`Publish` 等待全部处理器结束并聚合错误。

### 🔌 本地服务边界

项目面向单人本地使用，服务默认监听 `127.0.0.1:8081`，不提供认证或多租户功能。前端开发服务器默认监听 `http://localhost:5173`，并将 `/api` 请求代理到后端。

## 📂 项目结构 (Project Structure)

```text
ai-novel/
├── cmd/
│   └── server/                # 应用程序入口
├── configs/
│   └── config.yaml.example    # 非密钥配置模板；本地 config.yaml 不应提交
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
│   │   ├── eventbus/          # 进程内同步并行事件总线
│   │   ├── llm/               # LLM/Embedding 适配器 (Eino-ext)
│   │   └── vectorstore/       # 向量数据库实现 (EntStore)
│   └── interfaces/            # 接口层：外部通信
│       └── api/               # RESTful / SSE 流式接口实现
├── pkg/                       # 公共工具库
└── README.md
```

## 🛠 技术栈

- **语言**: Go 1.25.4（以 `go.mod` 为准）
- **Agent 框架**: [Eino](https://github.com/cloudwego/eino) (字节跳动开源)
- **ORM**: [Ent](https://entgo.io/) (Facebook 开源)
- **配置管理**: Viper (YAML + 环境变量支持)
- **LLM 组件**: Eino-ext OpenAI-compatible 适配器（默认使用智谱 API，可替换兼容服务的 endpoint、model 和凭据）
- **事件机制**: 进程内同步并行 EventBus
- **数据库**: PostgreSQL (支持数据持久化)

## 📦 快速开始

### 前置条件

- Go 1.25.4。
- PostgreSQL 已运行，并允许配置中的用户连接到 PostgreSQL 管理库；服务启动时会创建目标数据库并执行 Ent schema migration。
- 智谱或其他 OpenAI-compatible Chat/Embedding API 凭据。

1. **配置**:
   复制 tracked 的 `configs/config.yaml.example` 为本地 `configs/config.yaml`，再填写数据库和模型凭据。真实的 `configs/config.yaml` 已被忽略，不能提交；也可以只使用环境变量提供配置。

   Chat 与 Embedding 独立配置，当前默认模型分别为 `glm-4.5-air` 和 `embedding-3`：
   ```yaml
   database:
     postgres:
       host: "localhost"
       port: 5432
       user: "postgres"
       password: "your-password"
       dbname: "ai_novel"
       sslmode: "disable"
       enable_foreign_keys: false
   llm:
     chat:
       api_key: "your-api-key"
       base_url: "https://open.bigmodel.cn/api/paas/v4/"
       model: "glm-4.5-air"
       max_tokens: 2048
       timeout: "5m"
     embedding:
       api_key: "your-api-key"
       base_url: "https://open.bigmodel.cn/api/paas/v4/"
       model: "embedding-3"
       timeout: "1m"
   ```

   `app` 配置控制本地监听地址、CORS、生成并发数和超时；`rag` 配置控制相似度阈值、候选窗口、每次查询结果数、查询数和最终上下文上限。带单位的 duration 示例包括 `5m`、`1m` 和 `30m`。完整字段和默认值见 `configs/config.yaml.example`。

   上述 `database.postgres`、`app`、`llm` 和 `rag` 字段均可通过环境变量覆盖，或在无配置文件时直接提供。支持：
   - `DATABASE_POSTGRES_HOST`、`DATABASE_POSTGRES_PORT`、`DATABASE_POSTGRES_USER`、`DATABASE_POSTGRES_PASSWORD`、`DATABASE_POSTGRES_DBNAME`、`DATABASE_POSTGRES_SSLMODE`、`DATABASE_POSTGRES_ENABLE_FOREIGN_KEYS`
   - `APP_LISTEN_ADDR`、`APP_CORS_ORIGINS`、`APP_MAX_CONCURRENT_GENERATIONS`、`APP_READ_HEADER_TIMEOUT`、`APP_READ_TIMEOUT`、`APP_WRITE_TIMEOUT`、`APP_IDLE_TIMEOUT`、`APP_GENERATION_TIMEOUT`、`APP_STARTUP_TIMEOUT`、`APP_SHUTDOWN_TIMEOUT`
   - `LLM_CHAT_API_KEY`、`LLM_CHAT_BASE_URL`、`LLM_CHAT_MODEL`、`LLM_CHAT_MAX_TOKENS`、`LLM_CHAT_TIMEOUT`
   - `LLM_EMBEDDING_API_KEY`、`LLM_EMBEDDING_BASE_URL`、`LLM_EMBEDDING_MODEL`、`LLM_EMBEDDING_TIMEOUT`
   - `RAG_MIN_SIMILARITY`、`RAG_CANDIDATE_LIMIT`、`RAG_RESULT_LIMIT`、`RAG_MAX_QUERIES`、`RAG_MAX_CONTEXT_MEMORIES`

   数据库环境变量会覆盖 YAML；`DATABASE_POSTGRES_PASSWORD` 可以为空，但仅适用于本地已配置无需密码认证的 PostgreSQL。端口必须为 `1..65535`，`sslmode` 支持 `disable`、`allow`、`prefer`、`require`、`verify-ca`、`verify-full`。只读诊断命令的 `--dsn` / `AI_NOVEL_POSTGRES_DSN` 不参与服务启动配置，也不会触发建库或迁移。

   示例中的 API key 只是占位符；不要把真实凭据写入 README、示例配置或 Git。

   本地 `configs/config.yaml` 仅用于开发，建议设置为仅当前用户可读：`chmod 600 configs/config.yaml`。如果凭据曾经进入 Git 历史，应按已暴露处理并立即在对应 API/数据库侧撤销或轮换；从当前文件删除或加入 `.gitignore` 不能清除历史副本。前端 `VITE_*` 变量会编译进浏览器，只能放非敏感公开配置，后端密钥必须留在后端环境。

2. **运行**:
   ```bash
   go run ./cmd/server
   ```
   默认 API 地址为 `http://127.0.0.1:8081`，可通过 `app.listen_addr` 或 `APP_LISTEN_ADDR` 修改。

3. **体验流式 API**:

   `novel_id` 和 `chapter_index` 必须是 JSON 正整数；章节生成使用 POST JSON，响应仍为 SSE。`persist` 是 JSON boolean，省略时默认为 true；请求体最大 1 MiB，未知字段和尾随 JSON 会被拒绝。GET 生成接口已移除，预览接口仍保持 GET。
   ```bash
   # 基础用法：从大纲生成
   curl -N -X POST "http://127.0.0.1:8081/api/v1/novel/generate" \
     -H "Content-Type: application/json" \
     -H "Accept: text/event-stream" \
     --data-raw '{"novel_id":1,"outline":"写一个主角在深山发现古老遗迹的故事"}'

   # 人工干预/共创：注入作者指令与手工资料
   curl -N -X POST "http://127.0.0.1:8081/api/v1/novel/generate" \
     -H "Content-Type: application/json" \
     -H "Accept: text/event-stream" \
     --data-raw '{"novel_id":1,"idea":"主角能听懂动物语言","chapter_index":1,"editor_notes":"保持轻松幽默的语气，禁用第一人称","manual_context":"青阳镇位于群山脚下，镇北有一条小河"}'
   ```

   SSE 过程事件依次包括 `start`、`context_meta`、`token` 和可能出现的 `retry`；每个请求最后只发送一次 `terminal`，状态为 `success`、`error` 或 `cancelled`。

4. **预览上下文 JSON（不写入章节）**:
   ```bash
   # 仅生成“场景卡 + 背景资料 + 共创指令”，不进入写作/审查
   curl -X POST "http://127.0.0.1:8081/api/v1/novel/preview-context" \
     -H "Content-Type: application/json" \
     -H "Accept: application/json" \
     --data-raw '{"novel_id":1,"idea":"主角能听懂动物语言","chapter_index":1,"editor_notes":"保持轻松幽默，禁用第一人称","manual_context":"青阳镇位于群山脚下，镇北有一条小河"}'
   ```
   该接口返回 JSON 合成上下文，由前端创作工作台内嵌展示；项目没有独立预览页面，也不是文件下载接口。

## 📋 任务路线图 (Roadmap)

- [x] 基于 DDD 的标准目录结构搭建
- [x] 集成 Eino 编排 4 大 Agent 协作流 (State Graph)
- [x] 实现 LLM 基础设施适配器 (Chat & Embedding)
- [x] 实现 RAG 检索模型与内存向量库
- [x] 引入领域事件总线 (EventBus) 实现同步并行 fan-out
- [x] 实现 Ingestion 订阅者（自动提取剧情摘要并存入记忆库）
- [x] 实现 PostgreSQL + Ent 数据库持久化
- [x] 实现基于 SSE 的流式 API 接口
- [x] 实现 Plot Agent (自动生成章节剧情大纲)
- [x] 实现 Architect Agent (从 Idea 扩展为全书大纲路线图)
- [x] 实现 Character Agent (自动生成并维护角色卡与关系网)
- [x] 实现 World Agent (维护地理、武学、势力等世界观设定)
- [x] 优化 Librarian 检索算法 (支持智能检索计划与结构化档案提取)
- [x] 实现 Graph RAG (基于角色关系网 + 结构化设定库 + 向量记忆的混合检索)
- [x] 实现 Human-in-the-loop：新增 editor_notes 与 manual_context，支持“人工干预/共创”
