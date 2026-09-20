# 贡献指南

## 环境

- Go 1.26 或更高（版本以 `go.mod` 为准）
- Node.js 22 或更高，以及 npm
- SQLite 即可满足本地开发；Postgres 仅集成测试需要

```sh
cp .env.example .env   # 至少填入认证信息与一个模型来源
go run ./cmd/hephaestus
cd frontend && npm install && npm run dev
```

### `.env` 是唯一配置来源

启动时 `loadEnvironment`（`cmd/hephaestus/main.go`）会先清空进程中所有 `HEPHAESTUS_*`
环境变量，再从当前工作目录的 `.env` 重新载入。因此 shell 中的 `export` **不会**覆盖
`.env`，例如下面这条命令仍然会使用 `.env` 里的地址与数据库：

```sh
HEPHAESTUS_LISTEN_ADDR=127.0.0.1:9017 go run ./cmd/hephaestus   # 无效
```

需要用不同配置再跑一个实例时（例如本地联调或复现问题），请换一个工作目录，
在其中放一份独立的 `.env`，并把 `HEPHAESTUS_CONFIG_DIR` 写成绝对路径：

```sh
mkdir -p /tmp/heph-scratch && cp .env /tmp/heph-scratch/.env
# 编辑 /tmp/heph-scratch/.env：换掉 DATABASE_URL、LISTEN_ADDR、PROJECTS_ROOT，
# 并把 CONFIG_DIR 改为绝对路径
go build -o /tmp/heph-scratch/hephaestus ./cmd/hephaestus
cd /tmp/heph-scratch && ./hephaestus
```

务必同时换掉 `HEPHAESTUS_DATABASE_URL`，否则第二个实例会连上正式库。

## 提交前检查

```sh
make check        # go vet + golangci-lint + go test -race
make swagger      # 改动过 API 注解时必须重新生成
```

前端：

```sh
cd frontend
npm run lint && npx tsc -b && npm test && npm run build
```

`make check` 需要 golangci-lint v1.64 或更高：

```sh
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
go install github.com/swaggo/swag/cmd/swag@v1.16.6
```

CI 会重新生成 Swagger 并比对 `docs/swagger`，未同步会导致失败。

## 测试

`make test` 默认在 SQLite 上运行全部测试。设置 `HEPHAESTUS_TEST_POSTGRES_DSN` 后，`make test-integration` 会在真实 Postgres 上运行 `TestIntegration_*`；`internal/store` 与 `internal/chatrun` 中依赖 Postgres 的测试也只在该变量存在时执行，否则整包跳过。

该变量必须指向一个**专用**数据库：测试会建表并直接写入，不应与 `HEPHAESTUS_DATABASE_URL` 相同。测试需要能从空库跑通，因此用到会话的用例必须自己创建 `projects` 与 `sessions` 记录（见 `internal/chatrun/service_test.go` 的 `seedSession`），不要依赖库中已有的数据。

涉及网络的测试默认跳过，设置 `HEPHAESTUS_RUN_LIVE_TESTS=1` 才会运行。

## 配置文件约定

`config/` 中的静态配置按文件名前缀与扩展名识别，文件名必须与文件内的 `name` 字段一致：

| 前缀 | 扩展名 | 种类 |
| --- | --- | --- |
| `identity-` | `.toml` | Identity |
| `impression-` | `.toml` | Impression |
| `toolgroup-` | `.yaml` | Tool Group |
| `concierge-` | `.yaml` | Concierge |
| `workflow-` | `.yaml` | Workflow |
| `job-` | `.yaml` | Job |
| `constant-` | `.toml` | 提示词常量 |

Workflow 名称至少 10 个字符且不含空格。新增配置种类只需在 `internal/registry/loader.go` 的 `loaders` 表中增加一项。

## 提交信息

沿用现有风格：`type(scope): summary`，正文说明「为什么」而不是「改了哪几行」。

```
fix(chatrun): coalesce tool-call fragments when building run snapshots
```

常用 type：`feat`、`fix`、`chore`、`docs`、`test`、`refactor`、`opt`。

## 其他

- 不要提交 `.env`、构建产物、数据库文件或日志。
- 涉及安全的问题请先阅读 `SECURITY.md`，不要公开提交漏洞细节。
