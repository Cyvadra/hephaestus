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

### Changed

- API 默认以 Gin release 模式运行；`GIN_MODE` 仍可覆盖。
- `.env.example` 默认使用 SQLite，并移除与特定主机相关的取值。
- PM2 配置不再内置代理地址：仅在设置 `HEPHAESTUS_PROXY_URL` 时注入代理环境变量。
- Swagger 文档重新生成，补上缺失的 `/configurations/complete` 与鉴权声明。
- `HEPHAESTUS_POSTGRES_DSN` 标记为废弃别名，使用时会打印提示；请改用 `HEPHAESTUS_DATABASE_URL`。

### Fixed

- 工具调用密集的运行在生成快照时耗时可达数十分钟，并使会话长时间处于 `running`：快照构建改为合并流式片段后一次序列化，复杂度从平方级降为线性。
- 流式响应在收到 finish reason 后未及时结束。
- 持久化工具审计结果时未剥离 NUL 字节。
- 子 Agent 运行失败时 `finish` 的错误被丢弃，失败无从追溯。
- 配置列表过长时无法滚动。

### Removed

- 未被引用的 `pkg/baidu` OCR 包，以及 `HEPHAESTUS_BAIDU_OCR_*`、`HEPHAESTUS_UPLOAD_OCR_IMAGE_MAX_BYTES` 三个已废弃变量（设置后会被忽略）。
- 仓库内的 PicoClaw 调研笔记与草稿文档，内容合并至 `docs/release/Hephaestus.md`。
