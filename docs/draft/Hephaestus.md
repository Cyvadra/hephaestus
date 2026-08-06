## Hephaestus

> The advanced LLM <==> Human interaction framework



### Purpose

- Build a single-user, well-designed, rational, flexible, light-weight AI agent framework.
- For my own use, to support AI companion, customized and frequently-used workflows, agent identity switch, session history, and other highly personalized features. Thus we need a solid base. To allow user (me, a developer) implement these stuff easily, via config files or something.
- Extend LLM's potential, by making every input and every chained call (like presets / workflow), clean and clear. Eliminate ambiguity.



### 产品形态

运行在个人电脑上，包含后端的网页应用 （前端设计不纳入当前文档）



### Principle

- 遵循奥卡姆剃刀原则，用最简洁的方式满足业务要求



### 技术选型

- 事件驱动
- Use Gin as web framework, Swagger for API documentation, GORM for database.



### Model

#### Identity

- 意义：Agent 的核心身份
- 读写：toml
- 字段：
  - name, description
    - invisible to LLM
    - `name` is identical
  - preferred model
    - `deepseek-v4-flash` by default
  - reasoning_effort
    - "none","high","max"
    - "none" is an alias for `thinking: disabled` and no actual `reasoning_effort` field passed to model provider
  - max tokens
    - 当设置值大于 model provider 允许的 max tokens，允许静默裁切
  - temperature, top_p, etc.
    - model provider 支持则传递，不支持则忽略
    - 即，作为参数传递给 model provider interface，由模型实现层自行决策
  - system prompt
    - Cannot be empty. Use "You're a helpful assistant." by default.
  - injected messages
    - each {"role":"xxx", "content":"xxx"}

#### Impression

- 意义： messages to be appended to llm context (can be system, user, assistant message sequence; must append **AFTER** `identity` injected messages)
- Format: toml
- 字段：
  - name
    - unique
  - description
    - won't be passed in LLM context, only for maintenance
  - enabled
    - when "enabled" == false, silently ignore this impression, and do not inject to context
  - messages
    - list
    - each {"role":"xxx","content":"xxxxx"}
- `Impression` 并非 Skill，每个 Impression 是一串消息序列的集合，消息有先后顺序，且每条都会指定 role
- 设计上 `Impression` 即包含 assistant message. This is intended.
- 远期场景： Agent 自主提炼记忆 （当前不用实现，仅需兼容/预留）
  - 生成并保存 Impression (static toml file)
  - 提醒用户审核，通过后正式注入 （enabled ==> true） 

#### Tool Group

- Meaning: Registry, 工具集合，主要是为了便于运维和管理
- Format: yaml
- 字段：
  - name
    - unique
  - tools
    - a list of actual tool names
- 设计意义： client 仅可选择 tool group，不可直接指定 tool calling，以便管理
- tool group settings are stored in yaml, but definitions of various actual *tools*, are predefined in our platform.
- 程序启动时，要对所有的 tool 做有效性检查
- 最终真实调用 LLM 时，List<ToolGroup> 会被展开为 List<Tool>

#### Concierge

- 含义： 对 model config 的封装 ；能作为 chat experience 的基准 ； 也能作为 workflow / job 的基座
- Format: yaml
- 字段： a Concierge = Identity + List<Impression> + List<ToolGroup> + List<Plugin>
- 注意 Impression、ToolGroup、Plugin 的 list，每个都存在先后顺序
- 注意 LLM 自身不被设计为可修改 Identity / Impression / Tools / Plugins，当前版本仅由开发者维护
- Concierge 或其他任何聊天流水线，均不支持“Skill”，完全脱离该体系
- 不等同于 chat / session 的 runtime，Concierge 是一个不包含上下文的静态定义
- Workflow、Job 等组件，也会使用 Concierge，原则上工作类 Concierge 应先于他们创建
- 预期平台会使用例如 Copilot 的 prompt，加上丰富的 VSCode coding tools, 来编写一个适用于编程类 Workflow 的 Concierge

#### Session

- 含义：对应真实接入到用户端的“会话”概念
- Format: GORM model + Postgres
- 字段：
  - id
    - session id, primary key, auto increment
  - source_concierge
    - initial concierge name
    - just for reference, no business influence
  - settings (JSON)
    - identity name
    - Impression list (string list containing impression names)
    - ToolGroup list (string list)
    - Plugin list (string list)
  - active_leaf_message_id
    - 对应 `chat_history` 中的某条消息的 id
    - 允许为 null
    - 通过 session id 查询 `chat_history` 后，前端/后端均通过 active_leaf_message_id 进行重建，可得到一条完整的消息链路
  - compression_id
    - nullable
    - 用于链接和查询 `compression` 表，获取压缩后的聊天记录
  - compression_last_message_id
    - nullable
    - 代表压缩的聊天记录的范围，截止到哪条 message
  - flag_archived
    - bool, default false
- initialization：
  - 用户新建会话时，创建 session，会携带 concierge 参数
  - 查询到该 Concierge 设定，我们得到 Identity + List<Impression> + List<ToolGroup> + List<Plugin>，使用这些作为创建 session 的初始值，即 session model 的 settings json
- session 的 settings，即 identity, impression, tool, plugin, 随时可能被修改
- 每次对话时，需要查询 session 的最新记录，确保使用最新的 settings

#### Job

- Meaning: 定时或长程任务的调度逻辑；Workflow 的触发器
- Format: yaml
- Fields:
  - 标题，概述，任务目标（可能是长文本）
  - bound workflows
    - Workflow 可能会需要多个输入参数，job 也可能执行多个 Workflow，这在 yaml 中应有体现；注意合理设计
    - 每个 Workflow，需要包含失败重试的设定，固定延迟多久，重试几次
    - Workflow 发生 fatal error，则不再重试
  - trigger condition
    - 需要实现： “日终/凌晨 且 5 小时无新消息”、“工作时间且闲置 2 小时以上”、“每天上午九点”等
    - 使用 expr-lang/expr 进行实现
    - 固定使用程序所在的主机的时区
  - max execution times per current timezone day

#### Workflow

- Workflow 是 Agent 在运行过程中调用的工作流水线，相关概念在 n8n / Dify 中都有实现，尽管它们的定义更为庞杂
- 本平台的 Workflow，将简化为通过自然语言编撰的、交由 LLM 执行的 todo list
- 字段：
  - Name
    - primary; use slug, no space, no less than 10 chars for uniqueness)
  - Description
    - 描述该工作流的用途；对 LLM 可见
  - Concierge
    - 使用平台的哪个 Concierge name
  - Input Schema
    - sample
  - Output Schema
    - sample
  - Steps
    - string list，代表具体的执行步骤
    - 每一行都仅使用自然语言表述
- Format: yaml

#### Chat history

- 使用 Postgres 对有效的聊天记录进行全量存储
- Fields:
  - id
    - primary key, auto increment
  - session_id
  - parent_message_id
    - nullable
  - timestamp
  - role
  - content
  - reasoning_content
  - tool_calls
  - etc.
- Index: session_id, timestamp

#### Compression

- 聊天记录的压缩存储
- 字段：
  - session_id
  - first_message_id
  - last_message_id
  - messages (stored as text)
    - list
    - each {"role":"xxx","content":"xxxxx"}
- Format: GORM + Postgres



### Explanation

#### Session

- 设计层面允许用户修改会话中，assistant/user 的历史消息。我们需要保存所有修改前/修改后的数据，以提供类似 DeepSeek 官网那样，用户可切换的选项
- 每个 session 会对应一个 active path，但假设用户修改了第 2/5 条 user message，原分支保留，regenerate from that checkpoint and activate a new path；同理用户切换回来时，展示原分支历史 messages；从旧分支继续会话/修改消息等操作时，activate that path
- Chat history 保留了全量的铺开的聊天记录，无法推断消息分支
- 通过 session 记录的 active_leaf_message_id，即最新活跃消息，即可完成 active path 的标记，和 main branch 消息记录的追溯
- 配合 chat history 中的 session_id index，可实现高效的聊天记录检索
- 前端被允许获取到隶属该 session 的全量聊天记录数据，以便实现 DeepSeek 官网那样的切换效果，在前端完成 active path selection
- 除了新会话，前端发送的每一个继续聊天的请求，都需要附带 active_leaf_message_id
- active path 的追溯方法，始终通过 session_id 查询数据库中的全量聊天记录，然后在内存中追溯，而非逐条查询数据库
- 如发生依赖不存在的情况，例如对应的 identity 文件缺失，则用户最新发送的聊天记录不落盘，不 fallback，并返回报警，提示用户切换 identity，其他情况类似
- session 的 compression handler (validator) 如下：

| session 的 <br />compression_id <br />不为 null？ （存在压缩缓存） | compression_last_message_id 在 <br />latest active_leaf_message_id <br />所执行的 active path 上？ （压缩缓存命中） |            代表场景            | 代表含义 |                           平台行为                           |
| :----------------------------------------------------------: | :----------------------------------------------------------: | :----------------------------: | :------: | :----------------------------------------------------------: |
|                              是                              |                              是                              |   发生过压缩的会话，继续聊天   | 正常执行 |                              无                              |
|                              是                              |                              否                              | 发生过压缩的会话，用户切换分支 | 压缩失效 | 用 session_id 查 compression 表，并在查询结果中，<br />filter whose `compression_last_message_id` is on active path, <br />select the most recent compression (if exists), <br />otherwise clear compression indexes of this session |
|                              否                              |                              /                               |           未发生压缩           |  短聊天  |                              无                              |

#### Job

- 程序启动/重启时检查第一轮 trigger condition，然后 ticker(randomly 40~80 minutes) 轮询检查
- Job 的执行，需要保留运行记录和日志，在数据库中持久化
- 一个 Workflow 的失败，不影响后续 Workflow 的执行
- 任务目标可能会作为 Workflow 的输入；计划会在新建 Job 之前，做一些专用于定时/长程任务的 Workflow，使 Job 成为 Workflow 的触发器

#### Chat history

明确不保存的聊天记录：

- /stop 或异常时的 incomplete assistant message 和相应的 user message
- 指令型的，non-llm, but disposable message and template responses



#### Compression

- 因 LLM context limit 通常低于 1M，在极端情况下（current context length > 80% max context length），我们需要对会话的历史内容进行压缩
- 暂不使用 tokenizer，先使用字数预估 context， context length ≈ len(runes) / estimate
- session 发生过压缩的情况下，会检查 unpacked compression messages + latest messages from next(compression_last_message_id) to (incoming user message) 的 context length ，但这个 unpack 与 compression module 无关，实际判断是否需要进行压缩的 handler，应属于 Plugin，在实际 LLM call 前，去检查准备传入的 messages
- 压缩步骤被设计为 direct LLM call，不使用平台 identity / concierge / etc. ，私有化的 system prompt 和 user prompt template ； 对外暴露一个 Compress(messages []ChatMessage, expectedLength int) 方法
- 具体的压缩方法，目前可用提示词模板实现，但需要标记 TODO，计划详细查阅 Codex、GitHub Copilot、Claude Code 的实现方法，进行最合理的实现
- 因上下文长度超限而自动触发压缩、但是压缩失败时，效果与`/stop`命令相同，终止执行且不改变 session 状态
- `Compression` messages 仅允许 `user` / `assistant` role，不允许 `system` role。



### Component

#### Plugin

- "旁路"插件，用于丰富 Agent 所能支持的功能，权限较大
- 仅供平台调度，对 Agent 不可见
- 示例 1： 每轮会话后，使用 xx 模型（usually minor），生成 session 摘要，在会话限制超过例如五分钟后，更新会话标题（不超过 20 字）和摘要（不超过 300 字）
- 示例 2： Storyline mode status plugin. 在 assistant 消息生成完毕后，通过 Plugin 的 LLM 旁路，生成（或更新） storyline 所需的状态数据，例如“血量：100，蓝量：57，体力：80，当前任务：击败 20 个哥布林，完成情况：5/20” ； 注意这部分数据，对会话所属的 Agent，可读，不可写，如果 LLM 自发生成了类似格式的 suffix，也需要 plugin 能识别到（via regex prefix）、重新生成、并覆盖。
- 示例 3： options plugin. 在assistant 消息生成完毕后，根据预设的路线，或用户后来指定的引导，生成 next user message 的 alternatives，以供用户参考或快速选取
- 总之就是实现对 session 的每个阶段的 hook，以便用它们做一些开放性的功能
- 包含以下 hook (each with before and after) : UserMessageIncoming (only after), ToolCall, ContextCompression, AssistantFirstCallLlm, AssistantContinuousCallLlm, AssistantMessageCompletion, AssistantMessageSent2User
- Plugin 始终阻塞会话流程，但每个 Plugin 都会配置超时时间
- Plugin 超时后，也不能影响会话，继续正常的执行流程，多 Plugin 时允许跳过失败 Plugin，插件之间不存在依赖关系
- Plugin 通常用于修改/覆写上下文，也可能用于日志
- Plugin timeout 通过 `context.Context` 传递；实现必须尽快响应 cancellation，但 pipeline 不得假设 cancellation 能强制终止任意 goroutine
- Plugin 自身的任何行为不会被 hook
- Plugin 允许静默失败，用户无感知，但后台需要有 WeCom Notification 来推送各类错误消息
- Plugin 有先后顺序，当前 Plugin 所接收到的和可修改的 Context，即为上一个 Plugin（如有） 的处理结果
- Plugin 被触发时，传入值（而非指针）；异常超时的 Plugin，context 取消，不改变上下文，上层进入下一 Plugin 或后续阶段，避免重复执行
- Plugin 代码无需读写静态配置文件，仅通过 Go package code 实现 Plugin interface
- 暴露查询方法，clients 可获取完整的已配置的 Plugin name list
- 注意设计上要 flexible，多预留，高权限，具备前瞻性

#### Memory

- chat history
  - 由 Postgres 根据所有 session 的 active path，导出一个日期视图的 view
  - LLM 主动检索时，可能使用关键词+正则，去尝试检索
  - chat history 检索能力需要 patch 成一个 system tool，由平台提供/实现
  - 该 tool 允许 client 提供 List<关键词> 、正则表达式、命中聊天记录前后多少条的内容 num_neighbour_messages；响应相关的聊天记录
  - 弱化该 tool 的定义，用于 specific chat history search，不直接包含关键词 memory
- project
  - 参考 Codex / ChatGPT 的 Project 设定 （重要；必须先行检索相关官方文档/资料/网络搜索）
  - 每个 project 为一个独立文件夹，且必须包含项目骨架文档（AGENTS.md），该文档需包含项目概述和索引
  - Project 只能在用户的明确授权下，由 Agent 进行创建
  - Project 并不会限制 Agent 的工作区域、范围或能力
  - Project 的记忆检索优先级，高于 raw chat history
- context
  - 涉及到因模型上下文长度限制，而需要压缩的情况，引导 LLM 将历史消息压缩为 messages 序列，即允许包含 role 为 user, assistant 的消息列表
  - “上下文压缩”功能作为一个`Workflow`
  - “提炼记忆”的功能作为一个类似但独立的`Workflow` ，也生成消息序列，用于生成`Impression`，前期只写不读；
  - “提炼记忆”的 Workflow，仅指引并限制 LLM 的生成 messages，仅包含 role: user / assistant，不包含 system



### Requested features

#### 修改上下文

- 尽管本文档暂不包含前端定义，但有必要在对外核心接口的注释中，进行备注
- 用户可对任意 user / assistant 的历史聊天记录进行修改，尽管这会 reduce cache hit rate and increase cost
- 自然地，能够支持 regenerate (last message) 和 修改问题后重新生成 这样的 basic features

#### 多模态处理能力

- 为不支持多模态的 LLM，适配多模态能力

- 对不支持 vision 的 model，调用 OCR，并将解析后的文本拼接到 user message
- 具体的拼接方法，标记为 TODO，后续查阅 DeepSeek 官方文档，进行实现
- 生成图片的流程： LLM ==> write prompt to generate image ==> text2image model like `gpt-image-2` ==> generated images ==> send files to channel (web/qq/etc.)
- 生成语音、视频的步骤也类似
- 除 OCR 需要处理 input handler 外，对于 output，只需要在消息终端做文件兼容层
- 生成多模态输出的方法都需要 patch 为系统内置的 tool call，以供 LLM 调用

#### 多会话支持

- `session` 系列命令： new, list (id and title), switch (by id), archive (remove from active sessions), delete (soft delete; no longer accessible for memory)
- 注意 session 保留 identity 信息，正如 GitHub Copilot 对某个 chat 会保留使用的模型那样，进入后自动切换并提示当前 identity
- 不影响用户 switch 后手动切换 agent identity

#### SubTask

- Agent 在运行过程中发起/调用的「子任务」
- 子任务至少包含以下字段： InitialPrompt （父进程原始 user message）, CurrentTask （当前任务概要；optional）, CurrentPrompt （当前任务命令） ； 未必都对 LLM 可见，后续再详细设计，但需要保留记录
- 子任务的消息存储模型，需要与主体会话分离，minor priority

#### All possible commands in User's view

- 客户端会发送的命令如下：
- /help - 显示操作指引
- /ping - 显示平台健康状况
- /stop - Stop current task
  - 终止当前任务
  - 传递 context，使该 session 下的各类任务，能尽快终止（不是所有操作都支持 cancellation，允许 trade-off）
  - Workflow 中的 bash script 应支持 termination (Ctrl+C)，用于中断
- /status - Show current session info (identity, concierge, impression, etc. expanded info), context usage, etc. in detail
  - Also we need a *warning stack* (in-memory ring buffer) from startup, to record stuff like model parameter degradation
  - Send recent 10 warning messages (buffer size) to user as well, when running `/status`
- /list [identity|impression|toolgroup|plugin|concierge|session|job|workflow|project] - 列出可用选项
  - 每个 list 都会包含 session-temporary id 序号，以便用户在会话内进行切换
- /detail [identity|impression|toolgroup|plugin|concierge|session|job|workflow|project] <id> - 展示某个 xx 的详情
- /switch [identity|concierge|session] <id> or <name> - 进行切换
  - identity / concierge 切换后，同步更新 session 配置
- /activate [impression|toolgroup|plugin] <id> or id list like "1,2,3" - 新增/激活相关能力
- /deactivate [impression|toolgroup|plugin] <id> or id list - 关闭相关能力
  - 此三项的 activate / deactivate，需要同步更新 session 设置
- /clear - 归档当前 session，并以当前 session 的设定，新开并进入一个 session，实现上下文清除的效果
- /new - 类似`/clear`，但以当前 Concierge 的设定开启新 session （当前 session 中所做的 config diff 将失效，以 Concierge 为准）
- 命令性质的用户输入，不能混入到 LLM 的 context
- 指令需要有 template response 回复给用户，但 response 不进 history，不落盘

#### Error logging

- 通过 WeCom webhook 推送运行过程中的各类报错/失败/需要关注的调试数据，即，需要有一个 utils 的 Warn / Error 方法，在全局的大部分代码中统一使用这些方法，并在实现层面绑定 WeCom，将运行中发生的 error 都推送出来
- WeCom webhook 的重试机制为，间隔 15s，重试不超过 6 次



### Must Exclude

- SQLite
  - 禁止使用 SQLite 数据库，只能使用 PostgreSQL
- HEARTBEAT.md
  - 禁用传统的 OpenClaw 家族 心跳文件；所有任务应该被归类为 Job (条件触发) / Workflow (仅供调用)
- Vector storage
  - trade-off
  - 业界通常通过 llm 生成一系列关键词，然后直接进行文本检索（这项能力包装应成 Tool）
  - 需要保持平台整体的轻量级，弱依赖，可部署在低成本设备
- SQL statement
  - 禁止出现任何形式的 SQL 语句或 sql 迁移文件。统一使用 GORM 进行模型定义、自动迁移、数据查询。



### Notes

- 模型调用侧，仅使用通用的 chat-completions 接口，不使用 responses 等容易造成兼容性问题的方案
- 个人项目，对各类配置文件（identity, concierge 等所有概念），仅使用 name 作为唯一标识
- 对于后续会提到的一些概念(identity, impression, tool group, plugin, concierge, workflow)，均使用名称作为uniqueness，使用静态配置文件内容作为 single source of truth，程序启动时从文件加载，不入库，也不在程序内或数据库中附加任何额外的非运行时信息
- 所有 yaml / toml 等 source of truth 静态配置文件，存放在同一个目录中方便管理，不设子目录，使用 prefix 来判断类型，例如 `identity-default.toml` ；注意验证文件后缀名，以及文件名去除 prefix 和 extension 后，在 case insensitive 的情况下，应与其 name 相同
- 业务数据/运行时的 data model (session, chat messages, compression)，主键采用自增整型 id，避免使用 uuid
- 个人使用，设计为不支持热重载，静态配置文件更新后，用户需要重启才会载入新配置；不设版本号，每次breaking更新，用户也会更新全量配置文件
- 弱化所有“审计”相关的要求
- 一切从简，尽可能轻量级，注重平台的灵活性和可拓展性
- [identity|impression|toolgroup|plugin|concierge|job|workflow] 等结构体，尽可能都以 toml / yaml 的格式进行读写
- 过程透明，可维护性高，便于调试



项目中提到的一些未被明确的草草带过的基础概念/模块，可询问用户进行确认，也可以检索 `~/github/picoclaw` 查看有无相关/类似的实现，PicoClaw 的很多功能都可以照抄，但其代码质量不高，如要参考，迁移前注意进行大幅度的简化和优化。