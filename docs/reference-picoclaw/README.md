# PicoClaw 参考架构索引

这组文档记录对参考项目 [`../picoclaw`](../picoclaw) 的首次代码考察，用于 Hephaestus 后续的功能借鉴与迁移设计，而不是 PicoClaw 的替代官方文档。

考察基线：commit `49183d7e`（2026-07-23）。项目是以 Go 实现的轻量个人 AI Agent：Cobra CLI 启动 Gateway，Gateway 装配消息总线、Agent Loop、渠道和后台服务；Agent Loop 在每个会话内串行地推进「上下文 → LLM → 工具 → 最终回复」回合。

## 阅读顺序

1. [系统鸟瞰](01-system-overview.md)：边界、启动链与运行时数据流。
2. [代码地图](02-code-map.md)：目录和关键文件的快速定位。
3. [迁移指南](03-migration-notes.md)：建议的迁移切片、依赖边界和风险。

## 结论速览

- **核心内核**：`pkg/agent`，尤其是 `AgentLoop`、`TurnCoordinator` 与 `Pipeline`；其内部职责已经按文件拆分，适合作为阅读和抽取的主轴。
- **扩展模型**：渠道通过注册表与空白导入注册；模型通过 `LLMProvider`、工厂和 fallback 链接入；工具通过每个 Agent 的 registry 注册；MCP、Skills、Hooks 为第二层扩展点。
- **运行时契约**：`pkg/bus` 中的标准化入/出站消息和 `pkg/events` 的运行时事件，是各子系统之间最值得优先保留或重建的边界。
- **迁移策略**：优先迁移可独立验证的纯领域能力（provider、tool schema、session/routing），再接入 Agent 回合内核，最后才接渠道、WebUI、硬件和运维包装；不要直接复制 `gateway` 的整体装配代码。

## 参考项目入口

| 目的 | 位置 |
| --- | --- |
| CLI 根命令 | `cmd/picoclaw/main.go` |
| Gateway 运行时装配 | `pkg/gateway/gateway.go` |
| Agent 主循环 | `pkg/agent/agent.go` |
| Agent 初始化及共享工具 | `pkg/agent/agent_init.go` |
| 回合编排 | `pkg/agent/turn_coord.go`, `pkg/agent/pipeline_*.go` |
| 配置根模型 | `pkg/config/config.go` |
| 示例配置 | `config/config.example.json` |
| 官方架构说明 | `docs/architecture/` |

