# Architecture

## 定位

`agent-bot` 是平台控制层，不是业务 skill 仓库。

- IM provider: 当前真实接入只有 `feishu`
- agent backend: 当前真实接入只有 `opencode serve`
- workspace: 每个 `provider + conversation_id` 一个稳定目录
- session: backend active session 按 TTL 轮换
- scheduler: 建任务、轮询、触发、回投消息或续跑 agent
- 其他 bot 消息轮询：进程内轮询“其他 bot 发的消息”（飞书长连接收不到 bot 间消息），回投到主链路。它属于飞书入站的一部分，跑在 `internal/gateway/feishu` 里（由 listener 持有、随长连接一起起停），不是独立顶层组件——以后别的 provider 接入时各自在自己的 gateway 里处理入站。`interactive` 卡片与长连接入口一样受 `acceptInteractiveCardMessages` 约束，`text`/`post` 只受 `acceptOtherBotMessages` 约束。单轮 tick 内对各会话的拉取是顺序的（投递异步），群很多/接口慢时有效间隔会被拉长但不丢消息；规模化时再把拉取并发化。详见 `AGENTS.md` 的“其他 bot 消息轮询补偿”。

## 主链路

统一入口最终都会走 `internal/flow`：

- Feishu 入站消息
- `session prompt`
- scheduler 的 `promptText`
- `internal/gateway/feishu` 内置轮询补投的其他 bot 消息（`settings.acceptOtherBotMessages`）

主流程大致是：

1. provider/gateway 解析入站消息
2. `progress.Accept` 做重复/过旧过滤
3. `flow.ProcessText` 先处理平台命令和 refuse
4. 普通消息执行 workspace `before_message.py`；表情事件执行 workspace `on_reaction.py`
5. 通过 `session.Service` 解析当前 workspace 和 active backend session
6. 调 `opencode serve` 的 REST API
7. 执行 workspace `after_reply.py`
8. provider 发回 IM

## Workspace 与 Session

workspace 目录固定在：

```text
data/chats/<provider>/<conversation-id>/
```

目录里最重要的几类内容：

- `.session-setting.json`: 当前会话的人工配置
- `opencode.json`: 当前 workspace 的 backend 配置
- `.agents/skills/`: 当前会话挂载的 platform skill 软链
- `.agents/agents/`: 当前会话挂载的 subagent 软链
- `<repo-id>`: 工作区根目录下、`mounts.repoIds` 挂载的共享 git 仓库软链，指向 `agents/repos/<repo-id>`
- `.agents/hooks/`: `before_message.py` / `on_reaction.py` / `after_reply.py`
- `.agents/memory/`: 会话私有记忆
- `.agents/runtime/`: 运行时上下文与调试产物

session 生命周期由 sqlite 中的 `conversation_workspaces` 记录驱动：

- `active_session_id`
- 兼容旧版的 `btw_session_id`（当前 `/btw` 运行时已改为按发送者维护独立 btw session）
- `last_message_at`
- `agent_backend`

`Prepare()` 会根据 `historyTTLHours` 判断是否继续沿用旧 session；需要新 session 时会重新创建，并绑定到当前会话。

另外，`/btw <text>` 会走独立的 BTW 槽位：

- 主普通消息继续复用/轮换 `active_session_id`
- `/btw` 只复用/创建当前发送者自己的 btw session
- `/btw-clear` 只清空当前发送者自己的 btw session
- BTW 槽位本身不改 `/clear`、`/new`、`/peek`、`/roles`、`/abort` 等既有命令语义

## 命令与 topic-session

普通消息在 `topic-session` 模式下按 topic 隔离 session，针对 session 的命令也随之 topic-aware（见 `internal/flow/command.go` 的 `resolveCommandSession`）：

- 在某个话题（thread）里执行 `/peek`、`/compress`、`/abort`、`/attach`、`/clear`、`/new`，一律作用于该话题对应的 topic session。
- 在话题外（顶层、无 thread 上下文）执行 `/peek`、`/compress`、`/abort`、`/attach`：不动任何 session，提示先进入具体话题。
- 在话题外执行 `/clear`、`/new`：清空该会话的所有 topic session 加主 session（整会话重置）。
- 其它回复模式（`direct` / `topic` / `thread`）行为不变，命令仍作用于主 `active_session_id`。
- `/info` 在话题内会额外显示 `topic_id` / `topic_session_id`。

## 同会话并发

同一个 `provider + conversation_id` 不会并行 prompt。

- gateway/listener 收到消息时会起 goroutine
- 但 `flow.promptConversation()` 内部会按 `provider:conversation_id` 加互斥锁

当前处理策略是：

- 当前正在跑的这一轮不会被中途插队
- 如果同一会话在这一轮处理期间又来了多条普通消息，它们会先各自经过 `before_message.py`
- 然后按到达顺序在下一轮逐条写回同一个 session：前 N-1 条使用 `opencode` 的 `noReply` 作为独立 user message 注入，最后一条再正常触发一次 AI 回复

所以同一个群聊/会话里，当前轮是串行的；后续忙时消息不会并发进同一个 active session，但可能在下一轮被合并进一次处理窗口，不过在 `opencode` session 历史里仍然是多条独立 user message。

## Scheduler

scheduler 不是纯内存。

源数据持久化在 sqlite：

- 文件：`data/state.sqlite3`
- 表：`scheduled_jobs`

job 字段包括：

- `id`
- `provider`
- `conversation_id`
- `route`
- `payload`
- `run_at`
- `status`
- `created_at`
- `updated_at`

worker loop 每轮会：

1. 查询 `status = pending AND run_at <= now` 的任务
2. 先标成 `running`
3. 执行 handler
4. 成功则标 `done`，失败则标 `failed`

这意味着：

- 进程重启后，`pending` 任务还在，不会丢
- 如果服务停机错过了触发时间，重启后下一轮会补跑这些 `run_at <= now` 的 `pending` 任务
- `.agents/runtime/triggered-jobs.jsonl` 只是触发日志，不是任务真实来源

崩溃恢复（running 回收）：

- 如果进程在任务被标成 `running` 之后、但在 `done/failed` 之前崩掉，该行会停留在 `running`
- daemon 是单实例（只有一个 `Loop`、一个 sqlite writer），所以启动时看到的任何 `running` 都是上次崩溃的残留
- `runDaemon` 在启动 `Loop` 之前会调一次 `Runner.Recover()`：把残留 `running` 回收成 `pending`，下一轮自然补跑（见 `Store.ReclaimRunningJobs`）
- 这把 scheduler 的语义从“崩了就静默丢”变成 **at-least-once**：崩溃可能发生在副作用已执行、终态还没写之间，因此恢复后可能重跑一次（notify / prompt replay 等 handler 需容忍一次重复）
- 每次回收会 `attempts + 1`；超过 `DefaultMaxJobAttempts`（5）的“毒任务”会被 dead-letter 成 `failed` 并通过 failure notifier 上报，避免 启动→回收→再崩 的死循环
- `.agents/runtime/triggered-jobs.jsonl` 仍只是触发日志，不是任务真实来源
- 更详细的 scheduler 说明见 `docs/scheduler.md`

## 可观测性（别再静默失败）

`internal/observability` 是一个进程内的失败记录器（无外部依赖、并发安全）：

- 维护一个有上限的环形 buffer（最近 500 条事件）+ 按 `category/severity` 的单调计数器
- 关键“容易被吞掉”的失败点都会写一条记录：scheduler 任务失败 / reschedule 失败 / 崩溃回收、display-name 解析失败、Feishu listener 入站处理失败、daemon 级 failure notify（listener/scheduler loop）
- `notifyFailure` 会**先记录再判断 webhook**，所以即使没配 failure webhook，事件也不会丢
- 本地 API `GET /api/v1/admin/observability`（project token）返回计数器、最近事件，以及对默认 backend / provider 的一次 best-effort 健康探测
- web 管理台新增「诊断」页：健康徽标、失败计数、最近事件表，支持自动刷新

## Hooks

当前支持三个 workspace 级 Python hook：

- `.agents/hooks/before_message.py`
  - 在消息进入 `opencode` 前执行
  - 可拦截、改写文本、补 reaction
- `.agents/hooks/on_reaction.py`
  - 在表情事件到达时执行
  - 默认不会自动唤醒 `opencode`；只有 hook 显式返回 `text` 时才会继续进入主链路
- `.agents/hooks/after_reply.py`
  - 在 `opencode` 产出回复后、真正发给 IM 前执行
  - 可改写回复文本或指定 `mentionUserId` / `mentionUserIds`
  - 可读取 thread/topic 上下文，并可通过本地 API 查询当前群成员

## Backend 约束

`opencode` 集成只能走 `opencode serve` REST API。

当前实际使用的接口：

- `GET /global/health`
- `POST /session?directory=...`
- `POST /instance/dispose?directory=...`
- `POST /session/{sessionID}/message?directory=...`
- `POST /session/{sessionID}/abort`

## Remote Agent 接入

`internal/remoteagent` 让电脑上的本地 agent 插件通过 WebSocket 长连接接入，把一个会话变成本地 agent 的远程镜像 + 遥控。它是一个受 `settings.remoteEnabled` gate 的**路由覆盖层**，不是新的 backend：会话的 `agent.backend` 和 opencode session 始终保留。

- 接入点在本地 API：`GET /api/v1/remote-agent/ws`，用会话的 session token 鉴权（连接即绑定到该 `ref`，1:1）。
- 路由状态在 hub 内存里：插件连上→`local`，断开→`bot`，外加 `/connect-bot` / `/connect-local` 手动覆盖，“最后事件/命令 wins”。
- 入站：`flow.ProcessText` 在 `before_message.py` 之后按路由分叉——`local` 时把文本投给插件（`session.prompt`）即返回，不走同步 backend；`bot` 或插件未连时回退到默认 backend。
- 出站：插件上报 `session.message`，hub 经 `flow.SendAgentMessageToProvider` 用 provider 推一条新消息（末尾带 `— 来自 local agent` 标注）；连接/断开各推一条切换提示。
- flow 与 hub 通过 main 注入的接口互调（flow 持有 `remoteRouter`、hub 持有 send 回调），不产生 import 环；listener 不参与。

面向插件实现者的完整协议规范（WS 接口、鉴权、消息格式、生命周期、保活、参考实现）见 `docs/remote-agent-plugin.md`。
