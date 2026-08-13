# PicoClaw 数据迁移审计

审计日期：2026-08-13。源数据位于仓库 `tmp/`，目标是 Hephaestus 当前的 PostgreSQL
运行时模型和 `HEPHAESTUS_PROJECTS_ROOT` 文件目录。本报告只做结构与可迁移性审计，尚未向数据库
或项目目录写入数据。

## 结论

聊天记录和项目文件已有明确落点，可以先迁移；长期记忆、日记、通用产出和演化记录在
Hephaestus 中还不是一等数据，直接复制只能保存文件，不能提供检索、引用、溯源、列表或管理能力。

建议分两阶段执行：

1. 先实现可重复、可预演的导入器，将旧聊天导入为归档 Session，将文件复制到一个专用 Project，
   同时生成清单、校验和及旧 ID 到新 ID 的映射。
2. 再补 Memory、Artifact 和 Diary 能力，并对第一阶段保存的文件建立索引；不要为了等待完整平台能力
   而继续把唯一副本留在 `tmp/`。

## 源数据盘点

| 数据域 | 文件数 | 大小（字节） | 主要格式 | 说明 |
| --- | ---: | ---: | --- | --- |
| `sessions` | 177 | 3,996,019 | JSONL、JSON、`.migrated` | 87 个主 JSONL、87 个元数据文件、3 个历史迁移副本 |
| `memory` | 99 | 325,850 | Markdown | 日记式记忆、会话摘要、心跳研究和专题记录混合 |
| `jason_diary` | 4 | 765 | Markdown | 3 篇日记和 1 个 README |
| `outputs` | 1 | 5,575 | Markdown | 独立产出文件 |
| `projects` | 16 | 222,556 | Markdown、HTML、Python 等 | 3 个项目目录，另有 `.DS_Store` |
| `generated` | 99 | 34,149,710 | HTML、JSON、代码、图片、日志 | 研究结果和网页抓取缓存混合 |
| `state` | 13 | 1,245,083 | JSON、JSONL | 含 770 条 evolution task record |
| `cron` | 1 | 32 | JSON | schema version 1，当前 jobs 为空 |
| `life` | 2 | 2,502 | 文本 | 旧程序特有的生活状态数据 |
| `scripts` | 11 | 24,934 | Shell、Python | 运维和辅助脚本 |
| `skills` | 32 | 3,654,861 | Markdown、Python、二进制等 | 技能源码、依赖或工具混合 |

整个 `tmp/` 还包含 `.venv`、`.git`、缓存、第三方许可证和解释器文件，不属于用户业务数据，
不能按目录整体复制。

### 聊天记录质量

- 87 个主 `.jsonl` 全部通过逐行 JSON 校验，共 1,837 条消息。
- 时间范围为 2026-06-23T17:37:03+08:00 至 2026-08-13T17:29:22+08:00。
- 角色分布：343 条 `user`、867 条 `assistant`、627 条 `tool`。
- 528 条 assistant 消息包含 `tool_calls`，627 条 tool 消息包含 `tool_call_id`。
- 558 条消息包含非空 `reasoning_content`。
- 文件分类为 84 个生成式 session key、2 个旧命名会话、1 个 `heartbeat` 会话。
- 每个主 JSONL 都有 `.meta.json`；元数据含 `key`、`summary`、`aliases`、`scope`、
  `count`、`created_at`、`updated_at` 和 `skip`。87 个 `skip` 均为 0。

## 目标映射

| PicoClaw 数据 | Hephaestus 目标 | 可迁移性 | 转换规则 |
| --- | --- | --- | --- |
| session JSONL | `Session` + 线性 `ChatMessage` 链 | 高 | 保留 role、content、时间、reasoning、tool calls 和 tool call ID；最后一条消息设为 active leaf |
| session meta | Session 标题、摘要及导入清单 | 中 | `summary` 可写 Session.Summary；原 key、scope、aliases 没有正式字段，先保存在导入清单 |
| heartbeat session | 专用归档 Session 或运行记录 | 中 | 默认不进入普通聊天列表；需由导入参数显式包含 |
| `.migrated` session | 不导入 | 高 | 属于历史副本，否则会重复聊天 |
| `memory/*.md` | 专用 Project 下的 `memory/` 文件 | 仅保全 | 当前没有 Memory 模型或检索器，复制后 agent 不会自动召回 |
| `jason_diary/*.md` | 专用 Project 下的 `diary/` 文件 | 仅保全 | 保留原日期文件名和 mtime；未来建立 Diary 索引 |
| `outputs/*` | 专用 Project 下的 `artifacts/outputs/` | 仅保全 | 建立来源、哈希、MIME 和原路径清单 |
| `generated/*` | 专用 Project 下的 `artifacts/generated/` | 有条件 | 排除 cookie、日志缓存和重复网页快照，或先隔离后人工确认 |
| `projects/*` | 独立 Hephaestus Project | 高 | 规范化项目名；保留目录层级；为每个项目补 AGENTS.md |
| evolution records | 暂存为冷归档 | 低 | 与现有 WorkflowRun/JobRun 语义不等价，不应伪装成平台运行记录 |
| cron jobs | 无需迁移 | 高 | 当前 jobs 为空；仅保存源文件供审计 |
| skills | 人工评审后转为 registry/plugin/tool | 低 | Hephaestus 没有兼容的文件式 skill loader，不能直接启用 |
| `AGENT.md`、`SOUL.md`、`USER.md` 等 | Identity、Impression、Concierge 或 Project 文档 | 中 | 需要人工拆分语义，不能覆盖现有默认配置 |

建议创建一个 `picoclaw-archive` Project 承载未领域化的文件，并为 `tmp/projects` 下的真实项目分别创建
Project。旧聊天默认绑定 `picoclaw-archive`，除非 session meta 或人工映射能可靠确定所属项目。

## 平台结构缺口

### P0：迁移前必须补齐

1. **无数据导入入口。** 当前只有运行时创建 Session/Message 的服务，没有离线导入命令、预演、断点续传、
   幂等键、旧 ID 映射或导入报告。
2. **无导入来源与溯源字段。** Session、ChatMessage 和附件都不能记录 `source_system`、`source_id`、
   `source_path`、源校验和及导入批次。没有这些字段就无法可靠重跑或审计。
3. **删除 Session 不完整。** `session.Service.Delete` 删除 ChatMessage、Compression、PluginState 和
   ToolAudit，但没有显式删除 MessageAttachment；虽有消息外键级联意图，迁移前仍应以真实 Postgres schema
   验证约束存在，避免导入后产生孤儿附件。
4. **文件迁移缺少安全策略。** 源目录含 cookie 文件、`.venv`、`.git` 和缓存。导入器必须采用 allowlist、
   路径穿越检查、大小上限、敏感文件规则和哈希去重，不能使用递归全量复制。
5. **无备份与回滚协议。** 导入应在数据库备份之后运行；每批次必须可按 batch ID 回滚，文件写入需使用
   临时目录加原子 rename，避免数据库与磁盘半成功。

### P1：数据可用性缺口

1. **缺少长期记忆领域模型。** 没有 MemoryDocument/MemoryChunk、来源、时间范围、标签、重要度、失效状态、
   冲突关系或引用关系。
2. **缺少记忆检索。** 现有 `chat_history_search` 只搜索同一 Project 的 ChatMessage；不会搜索 Markdown
   记忆、日记、项目文档或产出，也没有全文索引、语义检索和结果引用。
3. **缺少产出物模型。** MessageAttachment 只是“某条 assistant 消息交付的项目相对路径”，不能表示与聊天
   无关的研究报告、网页快照、代码、数据集、版本、生成任务、派生关系或保留策略。
4. **缺少日记模型与视图。** 日记没有日期、作者、来源和条目级结构，也没有日历、时间线、搜索、编辑和导出 API/UI。
5. **Project 文件不可浏览管理。** 平台可让工具访问项目目录，但前端没有项目文件树、预览、元数据、搜索、
   批量下载或孤儿文件检查。
6. **会话来源语义丢失。** PicoClaw 的 channel/account/chat/generation scope 无目标字段；导入后无法按 QQ、pico、
   heartbeat 或 generation 筛选。
7. **模型信息丢失。** 旧消息记录了 `model_name`，ChatMessage 没有对应字段，无法保留逐消息模型来源。

### P2：运营与演进缺口

1. 没有迁移管理页面：无法查看批次进度、冲突、跳过项、失败项和校验结果。
2. 没有统一的数据导出、备份、恢复和 schema version 策略。
3. evolution task record 与现有 WorkflowRun/JobRun 缺少语义桥接；平台不能展示旧自主任务历史。
4. 文件式 skills 与 Hephaestus 的 ToolGroup/Plugin/Workflow 不兼容，缺少评审、权限声明、沙箱和转换流程。
5. 没有数据生命周期策略：网页抓取缓存、日志、cookie、研究产出和长期知识目前无法按类别保留或清理。
6. 聊天搜索会在 Go 内重建 active path 并做内容匹配；导入 1,837 条消息尚可，但数据继续增长后需要数据库全文索引
   或独立检索层。

## 导入规则

### 必须保留

- 原文件保持只读，迁移从副本工作；每个文件记录 SHA-256、字节数和 mtime。
- 主 session JSONL 与 meta 一对一匹配；逐文件事务导入。
- JSONL 顺序转换为 ParentMessageID 链；时间戳按原时区解析并存为绝对时间。
- `tool_calls` 原样存入 JSONB，`tool_call_id` 原样保留；未知字段写入导入清单，不静默丢弃。
- 旧会话先标记 archived，标题优先取 meta summary 的安全截断或生成稳定占位标题。
- 文件复制后再校验目标 SHA-256；只有数据库记录和文件都成功才提交批次项。

### 默认排除或隔离

- `*.migrated`、`.git/`、`.venv/`、`__pycache__/`、`*.pyc`、`.DS_Store`。
- `cookies.txt`、私钥、token、secret、credential、浏览器 profile 等凭据候选。
- 重复网页快照、临时日志和明显抓取缓存；先进入 quarantine 清单，不直接暴露给 agent。
- `heartbeat.jsonl` 和 evolution records；除非显式使用 `--include-system-runs`。

## 验收标准

1. 预演报告的文件数、消息数、角色分布和总字节数与本报告一致，差异必须解释。
2. 87 个主 JSONL 均成功解析；默认聊天导入应排除 heartbeat，因此预计 86 个 Session、1,425 条消息。
3. 导入后每个 Session 的 active path 消息数等于源 JSONL 行数，首尾时间与源文件一致。
4. tool call 与 tool result 的 ID 可关联；无法关联的 99 条差额需要进入告警清单，而不是阻断整个会话。
5. 重跑同一批次不新增 Session、Message 或文件；更改源文件后以冲突报告停止，不自动覆盖。
6. 随机抽取至少 10 个会话，比对全文、角色、时间、reasoning 和 tool payload。
7. 搜索抽样关键词能找到迁入聊天；记忆和日记在 P1 检索能力完成前至少可从 Project 文件路径访问。
8. 敏感候选和默认排除文件均未进入 Hephaestus Projects Root 或数据库。

## 建议实施顺序

1. 增加 `MigrationBatch`、`MigrationItem` 和来源字段，定义幂等与回滚契约。
2. 实现 `hephaestus migrate picoclaw --source ./tmp --dry-run`，先支持 session/meta 和项目文件。
3. 用本报告数字做 fixture 级验收，再在数据库备份后执行正式导入。
4. 增加 Artifact 索引与项目文件 API/UI，使产出物可浏览和追溯。
5. 增加 MemoryDocument/Chunk、全文与语义检索、引用展示，再索引 memory/diary/project 文档。
6. 最后评审 skills、evolution 和 heartbeat，决定转换为平台配置、运行历史还是只保留冷归档。