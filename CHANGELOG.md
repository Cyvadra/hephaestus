# Changelog

本文件记录值得使用者注意的变更，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

v0.3.3 及更早版本的完整记录见 git tag 与提交历史。

## [Unreleased]

首个面向外部开发者的公开版本，主要是发布前的整理。

### Added

- `GET /healthz` 健康检查端点：公开、不访问数据库，返回运行状态与构建版本。
- `hephaestus --version` 输出构建版本；`make build-server` 通过 `git describe` 注入。
- `HEPHAESTUS_COOKIE_SECURE`：在反向代理终止 HTTPS 的部署中，为会话 Cookie 强制加上 `Secure` 属性。
- 会话与消息的历史检索（`/api/v1/search/sessions`、`/api/v1/search/messages`），Postgres 下由 pg_trgm GIN 索引支持。
- 对话运行的转向（steering）控制，可在生成过程中追加指令。
- 编辑 Identity / Impression 时可生成并参考 AI 回复。
- GitHub Actions CI：Go 构建、vet、race 测试、golangci-lint、govulncheck、Swagger 新鲜度检查，Postgres 集成测试，以及前端 lint / 类型检查 / 测试 / 构建。
- `CONTRIBUTING.md`、`SECURITY.md`、`THIRD_PARTY_NOTICES.md` 与本变更日志。
- `make lint` 与 `make check`，配套 `.golangci.yml` 固定 lint 规则集。
- `POST /sessions` 接受 `auto_approve`：可在会话创建时即开启「全部允许」，无需创建后再改一次。该策略与 `/interact auto-approve` 一样只属于当前服务进程。

### Changed

- API 默认以 Gin release 模式运行；`GIN_MODE` 仍可覆盖。
- `.env.example` 默认使用 SQLite，并移除与特定主机相关的取值。
- PM2 配置不再内置代理地址：仅在设置 `HEPHAESTUS_PROXY_URL` 时注入代理环境变量。
- Swagger 文档重新生成，补上缺失的 `/configurations/complete` 与鉴权声明。
- `HEPHAESTUS_POSTGRES_DSN` 标记为废弃别名，使用时会打印提示；请改用 `HEPHAESTUS_DATABASE_URL`。
- **`/workflow-runs/:id/stream` 的事件序号改为从 0 开始**（此前从 1 开始），与 `/chat-runs/:id/stream`、`/configurations/complete` 及前端的校验保持一致。依赖序号从 1 连续递增的外部消费方需相应调整。
- **新建会话的思考强度与「联网」不再沿用上一个会话的取值**：选定 Concierge 后，思考强度取该 Identity 的默认值；「联网」按 Concierge 分别记住上次的选择（未选过时，跟随该 Concierge 是否默认启用 `web` 工具组），并在 Concierge 无法提供 web 工具时置灰。
- 思考强度标签支持单击开关：处于「适度 / 深度 / 极度」任一时单击即关闭思考（回到「即答」），处于「即答」时单击开启到默认的「深度」；「适度」与「极度」仍由悬停菜单选择（英文界面档位名同步更正为 Max / High / Low / Direct）。
- **授权改为两态开关**：高亮代表「全部允许」，未高亮代表「每次询问」，单击即切换；原先的「超时拒绝」后端并不支持，已连同会话内的 Project 偏好记忆一并删除。草稿阶段的「全部允许」随会话创建一并下发；生成进行中也可切换（`/interact` 命令在会话消息入口处理，不受进行中的运行影响）。

### Fixed

- **首次连接空 PostgreSQL 数据库时无法启动**：全文检索索引在创建 `sessions` 表之前建立，报错 `relation "sessions" does not exist`。已有数据库不受影响，因此此前未被发现。
- **通过界面或 API 修改配置后，重启可能被静态模板静默覆盖**：写回记录时未更新 `updated_at`，而模板同步正是依据「文件是否比记录更新」来决定是否覆盖。
- 工具调用密集的运行在生成快照时耗时可达数十分钟，并使会话长时间处于 `running`：快照构建改为合并流式片段后一次序列化，复杂度从平方级降为线性。
- 流式响应在收到 finish reason 后未及时结束。
- 持久化工具审计结果时未剥离 NUL 字节。
- 子 Agent 运行失败时 `finish` 的错误被丢弃，失败无从追溯。
- **Concierge 提供 `web` 工具组时「联网」却无法开启**：可用性此前要求 Concierge 为会话默认勾选该工具组，导致像 `pure` 这样「提供但不默认启用」的 Concierge 永远置灰。现在只要 Concierge 提供该工具组就可切换，且开启时会一并激活会话的 `web` 工具组，使联网真正生效；「联网」的显示也改为反映实际生效状态（工具组已存在且未被 `EnableWebSearch` 关闭）。
- 配置列表过长时无法滚动。

### Removed

- 未被引用的 `pkg/baidu` OCR 包，以及 `HEPHAESTUS_BAIDU_OCR_*`、`HEPHAESTUS_UPLOAD_OCR_IMAGE_MAX_BYTES` 三个已废弃变量（设置后会被忽略）。
- 仓库内的 PicoClaw 调研笔记与草稿文档，内容合并至 `docs/release/Hephaestus.md`。
