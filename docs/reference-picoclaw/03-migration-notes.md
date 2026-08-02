# PicoClaw → Hephaestus 迁移笔记

## 建议的迁移顺序

| 阶段 | 迁移目标 | 依赖 | 验证结果 |
| --- | --- | --- | --- |
| 0 | 定义 Hephaestus 的领域契约：Message、Session、Model、Tool、RuntimeEvent | 无 | 能以 mock 跑通单回合。 |
| 1 | `providers` 通用类型、schema 转换、fallback/限速 | Config、HTTP/credential 抽象 | 多 provider 的 tool-call 回归测试。 |
| 2 | `tools` 的 registry、验证与安全路径策略 | Message/Session | 文件、shell、web 等工具可独测。 |
| 3 | `session`、`routing`、最小 `bus` | Phase 0 契约 | 会话隔离、顺序与路由测试。 |
| 4 | `agent` 回合内核（TurnCoordinator/Pipeline） | 前三阶段 | LLM → tool loop → final 响应的端到端测试。 |
| 5 | Channels/MCP/Skills/Hooks | bus、agent、tools | 每类扩展至少一个 adapter 冒烟测试。 |
| 6 | Gateway、cron、heartbeat、health、Web/launcher、硬件 | 完整内核 | 本地运行、重载和优雅退出。 |

## 应优先复用的概念，而非文件

- **规范化消息与上下文**：入站/出站消息必须显式包含 channel、chat、session/agent scope、reply target、媒体等事实；这是后续跨渠道稳定性的基础。
- **每 session 单回合 + steering**：它比简单“全局串行”更具扩展性，也避免同一上下文竞争写入。
- **Pipeline 的四段式边界**：Setup / CallLLM / ExecuteTools / Finalize 很适合变成 Hephaestus 的可测试 orchestration API。
- **Provider fallback 策略**：模型选择、冷却、限速、错误分类不要散落在渠道或 prompt 代码里。
- **运行时事件**：事件总线是监控、日志、UI 状态与 agent 自演化的统一观察面。

## 不建议原样复制的部分

| 范围 | 原因 | 建议 |
| --- | --- | --- |
| `pkg/gateway/gateway.go` | 装配多种可选服务，环境耦合大。 | 以自身部署模型重写 composition root，只复用服务接口。 |
| 所有渠道的 blank import | 编译期绑定会随产品需要膨胀二进制和依赖。 | 明确插件加载策略，先保留需要的 1–2 个渠道。 |
| `pkg/agent` 整包直接搬运 | 高内聚、引用大量配置与 side effects。 | 先抽接口和测试夹具，再以 pipeline 纵向切片迁移。 |
| 硬件/移动平台兼容逻辑 | 对桌面/服务端产品通常不是核心，维护成本高。 | 延后到明确有设备需求时。 |
| JSON 配置的历史兼容层 | 解决的是 PicoClaw 已有用户的版本迁移。 | 仅设计 Hephaestus 自己的 schema 演进策略。 |

## 许可证与归属提醒

参考仓库根 `LICENSE` 标注 MIT，`cmd/picoclaw/main.go` 也标注其受到 nanobot 启发/基于其实现。若直接复制受版权保护的代码而非仅借鉴设计，应在 Hephaestus 代码库保留原始 MIT 许可证文本、版权声明和必要 attribution；还应逐个检查引入的第三方依赖及其许可证。此处是工程提醒，不构成法律意见。

## 首个可执行切片建议

建议先做“单渠道（或本地 CLI）+ 单模型 + filesystem/shell 两个工具 + JSONL session”的垂直样本：

1. 用 Hephaestus 的 message/session/provider/tool 接口实现测试替身；
2. 将 PicoClaw 的 pipeline 行为拆成可替换的最小实现；
3. 固化三类测试：普通回答、一次工具调用、同 session 第二条消息；
4. 再引入 fallback、steering、MCP、长期记忆。

这样可先验证最关键的“回合一致性”而非被渠道和部署细节淹没。

