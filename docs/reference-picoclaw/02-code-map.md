# PicoClaw 代码地图

以下索引按“后续迁移时该去哪里找”组织，路径均相对于参考仓库 `/Users/cyvadra/github/picoclaw`。

## 顶层

| 目录/文件 | 作用 |
| --- | --- |
| `cmd/picoclaw/` | 主 CLI；`internal/*` 是各 Cobra 子命令的薄适配层。 |
| `pkg/` | 核心库和运行时实现。 |
| `config/config.example.json` | 完整配置能力的最好入口。 |
| `workspace/` | 首次启动时拷贝的 Agent 指令模板。 |
| `web/` | Launcher/Web UI 相关构建和资源。 |
| `docker/`, `build/`, `.goreleaser.yaml`, `Makefile` | 容器、交叉编译、发布与开发脚本。 |
| `docs/architecture/` | 关键机制说明（session、routing、events、steering、subturn、hooks）。 |
| `cmd/membench/` | Seahorse 记忆检索评测工具，不属于生产启动路径。 |

## `pkg` 子系统

| 包 | 核心职责 | 优先入口 |
| --- | --- | --- |
| `agent` | Agent 定义、消息调度、回合循环、上下文、hook、子回合、出站回复 | `agent.go`, `agent_init.go`, `turn_coord.go`, `pipeline_*.go` |
| `bus` | 进出站消息总线、背压、流式输出委托、消息上下文 | `bus.go`, `message.go` |
| `channels` | 渠道接口、注册表、管理器与 Telegram/Discord/飞书/企微/Slack/IRC/MQTT 等实现 | `interfaces.go`, `registry.go`, `manager*.go` |
| `providers` | LLM 通用数据模型、工厂、限速、冷却和 fallback；各协议 adapter | `types.go`, `factory*.go`, `fallback.go` |
| `tools` | 文件、shell、web、消息、定时、子 agent、硬件等工具及注册/校验 | `registry.go`, `toolloop.go`, `fs/`, `integration/` |
| `config` | JSON 配置模型、默认值、兼容迁移、路径和版本解析 | `config.go`, `gateway.go` |
| `session` | session scope、历史持久化、别名兼容与迁移 | 包内 store/scope 相关文件 |
| `routing` | 入站消息到目标 agent/session/model 的路由决策 | `routing` 包与 `agent/dispatch_request.go` |
| `memory`, `seahorse` | 普通记忆与可选全文检索式上下文（SQLite FTS） | `seahorse` engine/retrieval 相关文件 |
| `mcp`, `skills` | MCP server/tool 的发现与运行；技能加载/安装 | 各包 public facade |
| `events` | 运行时事件 envelope、bus、订阅和记录 | `events` 包 |
| `cron`, `heartbeat`, `health` | 后台计划任务、主动心跳、liveness/readiness/reload | 各包 service/server 文件 |
| `media`, `audio` | 媒体存储；ASR/TTS provider 适配 | `media`, `audio/asr`, `audio/tts` |
| `state`, `evolution` | 状态记录和 agent 自演化桥接 | `state`, `evolution`, `agent/evolution_bridge.go` |
| `auth`, `credential`, `identity` | OAuth/凭据加密与身份支撑 | 各包入口 |
| `devices`, `isolation`, `netbind`, `pid` | 硬件事件、隔离执行、网络监听、单实例保护 | 适合按产品需求选择性移植 |

## Agent 回合内部文件

| 文件群 | 职责 |
| --- | --- |
| `agent.go`, `agent_message.go`, `agent_outbound.go` | 消费 bus、会话并发控制、入站预处理和回复发布。 |
| `registry.go`, `instance.go`, `definition.go`, `discovery.go` | 多 agent 配置、定义文件与实例构建。 |
| `turn_coord.go`, `turn_state.go`, `turn_context.go` | 回合状态机及循环协调。 |
| `pipeline_setup.go` | 历史/输入/候选模型与 turn 初始化。 |
| `pipeline_llm.go` | 模型调用、重试/回退及 before/after LLM hooks。 |
| `pipeline_execute.go` | 工具执行、审批/拦截、媒体发送、steering 处理。 |
| `pipeline_finalize.go` | session 保存、压缩、终态与事件。 |
| `context*.go` | token 预算、history、cache、Seahorse 上下文实现。 |
| `prompt*.go` | system prompt、workspace 文档和动态 contributor。 |
| `hooks*.go`, `hook_*.go` | 外部 hook 进程及 JSON-RPC 生命周期协议。 |
| `subturn.go`, `steering.go` | 子任务执行及回合中途追加用户消息。 |

## 重要装配关系

`gateway.Run` 以 blank import 载入渠道实现 → `channels.Manager` 启动已启用渠道 → 渠道发布 `bus.InboundMessage` → `AgentLoop.Run` 路由与执行回合 → agent 注册的 tool/provider/MCP/skill 被 pipeline 调用 → `bus.OutboundMessage` 由渠道 manager 送回。

阅读代码时可从上述链路向两侧扩展；避免先从具体渠道或 Web UI 入手，它们对核心迁移的解释力较低。

