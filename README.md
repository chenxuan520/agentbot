# Agent Bot

这个仓库是平台控制层，不直接承载业务 skill。本仓库负责把：

- IM provider
- per-conversation workspace
- agent backend
- scheduler
- 基础命令

串成一条稳定的消息处理链路。

当前已经真正接上的只有：

- IM: `feishu`
- agent backend: `opencode serve`

更细的设计/配置文档放在 `docs/`：

- `docs/architecture.md`
- `docs/configuration.md`

## 启动前先看这三件事

1. `opencode serve` 要先自己跑起来。
2. 先复制 `agent-bot.example.json` 为 `agent-bot.json`，再按你的环境填值。
3. `opencode` 自己的 provider/model 配置，不在这里配，仍然归 `opencode` 自己管。

## 快速启动

1. 先确认 `opencode serve` 地址可用：

```bash
go run ./cmd/agent-bot backend health
```

2. 再确认 Feishu 机器人凭证可用：

```bash
go run ./cmd/agent-bot provider feishu health
```

3. 启动主进程：

```bash
go run ./cmd/agent-bot run
```

这个命令会根据 `agent-bot.json` 里的 `runtime.defaultProvider` 决定跑哪个 IM listener。

当前如果你配置的是：

```json
{
  "runtime": {
    "defaultProvider": "feishu"
  }
}
```

那 `run` 启动的就是：

- Feishu 长连接 listener
- scheduler loop
- 平台本地 HTTP API（gin）
- 进程内「其他 bot 消息」轮询补偿：随 Feishu listener 一起起停，对开启了 `settings.acceptOtherBotMessages` 的会话生效，不需要额外拉起独立 poller 进程
- daemon 模式下，如果 Feishu listener 自己返回错误，当前进程会记录日志并按固定 backoff 重试，不会因为单次 listener 抖动直接退出整个 `agent-bot`

4. 如果只是做 smoke test，让它几秒后自动退出：

```bash
go run ./cmd/agent-bot run 5
```

## 后台脚本与 Watchdog

长期运行更推荐直接用仓库脚本，而不是手挂 `go run`：

```bash
./scripts/restart-opencode.sh
./scripts/restart-agent-bot.sh
./scripts/watch-opencode.sh start
./scripts/watch-agent-bot.sh start
```

- `scripts/watch-opencode.sh` 默认每 5 分钟检查一次 `GET /global/health`；若配置了 `providers.feishu.failureWebhookURL`，重启前会先发一条带 `agent-bot` 字样的飞书文本。
- `scripts/watch-agent-bot.sh` 默认每 10 秒用 `curl` 检查一次 `GET /health`；连续 3 次失败后才会自动调用 `scripts/restart-agent-bot.sh`。
- watchdog 默认跟随 `agent-bot.json -> server.host/server.port` 解析 health 地址；如果监听地址配成 `0.0.0.0` / `::`，脚本会自动改用 `127.0.0.1` 探活。
- agent-bot 进程刚启动后的前 20 秒会跳过 health check，避免 watchdog 在服务尚未 ready 时误判重启；health check 的单次超时默认是 5 秒。
- health check 失败会把失败次数和 `curl` 细节写进 `data/run/watch-agent-bot.log`；若配置了 `providers.feishu.failureWebhookURL`，真正触发重启前会先发一条带 `agent-bot` 字样的飞书文本。
- 可通过环境变量覆盖：`CHECK_INTERVAL_SECONDS=30 ./scripts/watch-agent-bot.sh start`、`FAILURES_BEFORE_RESTART=5 ./scripts/watch-agent-bot.sh start`、`STARTUP_GRACE_SECONDS=30 ./scripts/watch-agent-bot.sh start`、`HEALTH_TIMEOUT_SECONDS=8 ./scripts/watch-agent-bot.sh start`，或 `HEALTH_URL=http://127.0.0.1:18080/health ./scripts/watch-agent-bot.sh start`。
- 停止 watchdog：`./scripts/watch-agent-bot.sh stop`
- 快速判断“当前运行二进制是不是最新源码”：`./scripts/check-agent-bot-build.sh`。它会比对 git `HEAD`、二进制内嵌的 `vcs.revision/vcs.modified`、以及运行中进程实际绑定的 `/proc/<pid>/exe`；若输出 `status: drift_detected`，优先执行 `./scripts/restart-agent-bot.sh`。
- 相关 pid / 日志都在 `data/run/`，新增的 agent-bot watchdog 使用 `watch-agent-bot.pid` 和 `watch-agent-bot.log`。

## `agent-bot.json` 是干嘛的

`agent-bot.json` 只管平台层设置。

它负责这些事情：

- 默认用哪个 IM provider
- 默认用哪个 agent backend
- web 管理台项目级登录 token
- session token 的加密 secret
- Feishu bot 的 `appID/appSecret`
- `opencode serve` 的 base URL
- scheduler 轮询参数
- 本地 HTTP/API 服务监听地址

它**不负责**这些事情：

- `opencode` 自己的 provider/model 配置
- `opencode` 的 agent/model/variant 选择
- 路径类目录配置

原因：

- 路径类目录现在全部按代码默认值走
- `opencode` 的 provider/model 配置仍然归 `opencode.json` / `serve` 自己处理

## `agent-bot.json` 字段说明

当前文件长这样：

```json
{
  "runtime": {
    "defaultProvider": "feishu",
    "defaultBackend": "opencode",
    "schedulerIntervalSeconds": 1,
    "schedulerBatchLimit": 50
  },
  "server": {
    "host": "127.0.0.1",
    "port": 8080
  },
  "auth": {
    "projectToken": "...",
    "secret": "..."
  },
  "web": {
    "baseURL": "https://agent-bot.example.com"
  },
  "providers": {
    "feishu": {
      "appID": "...",
      "appSecret": "...",
      "ackEmoji": "OnIt"
    }
  },
  "backends": {
    "opencode": {
      "baseURL": "http://localhost:4096"
    }
  }
}
```

各字段含义：

- `runtime.defaultProvider`
  - 默认 IM provider。
  - `go run ./cmd/agent-bot run` 会读这个值。
  - 当前只真正支持 `feishu`。

- `runtime.defaultBackend`
  - 默认 agent backend。
  - `go run ./cmd/agent-bot backend health` 不传名字时会读这个值。
  - 当前只真正支持 `opencode`。

- `runtime.schedulerIntervalSeconds`
  - daemon 模式下 scheduler loop 的轮询间隔。

- `runtime.schedulerBatchLimit`
  - 每轮最多处理多少条到期任务。

- `server.host`
  - 平台层本地 HTTP 服务监听地址。
  - `go run ./cmd/agent-bot run` 会用它启动本地 API。

- `server.port`
  - 平台层本地 HTTP 服务监听端口。

- `auth.projectToken`
  - web 管理台项目级 token。
  - 只负责登录 web 管理台，不负责 session token 的加密。

- `auth.secret`
  - session token 的加密 secret。
  - session token 会以 hash + 加密密文的形式存到 sqlite；`/info` 展示明文时会用它解密。

- `web.baseURL`
  - 可选的前端控制台基地址。
  - 配置后 `/info` 会额外输出一个基于当前 session token 拼接的 `console_url`，可直接跳转到前端。

- `providers.feishu.appID`
  - Feishu bot 的 app id。

- `providers.feishu.appSecret`
  - Feishu bot 的 app secret。

- `providers.feishu.ackEmoji`
  - 处理消息时默认加的 reaction。

- `providers.feishu.remoteAckEmoji`
  - 可选。消息被转发给「本地 agent（remote-agent）」处理时加的 reaction，用来和 bot 自己处理时的 `ackEmoji` 区分。
  - 不配置时默认回退到 `ackEmoji`（即与普通处理用同一个图标）。
  - 本地 agent 回投结果、或插件断开时，平台会自动撤掉这个 reaction。

- `providers.feishu.failureWebhookURL`
  - 可选。失败通知用的飞书机器人 webhook。
  - `watch-opencode.sh`、`watch-agent-bot.sh` 会在自动重启前发通知。
  - `agent-bot` 本体在 Feishu listener 重试、scheduler loop 基础设施错误时也会发通知。

- `backends.opencode.baseURL`
  - 运行中的 `opencode serve` 地址。

## 路径类目录为什么没放进配置

这些路径现在都不需要你配：

- workspace root
- sqlite DB 路径
- template 目录
- 平台 skill 目录
- 平台 subagent 目录

当前统一按仓库默认值走：

- `templates/`
- `data/chats/`
- `data/state.sqlite3`
- `agents/skills/`
- `agents/subagents/`

## `opencode` 配置和这个仓库的关系

这点很重要：

- `agent-bot.json` 只知道怎么连 `opencode serve`
- 它不管理 `opencode` 自己用什么 provider/model

也就是说：

- 你在这里配的是：
  - `http://localhost:4096`
- 你不在这里配的是：
  - OpenAI key
  - model 名称
  - provider 细节

这些仍然要由 `opencode` 自己的配置解决。

## 当前 workspace 形态

每个会话一个目录：

```text
data/chats/<provider>/<conversation-id>/
  AGENTS.md
  .session-setting.json
  opencode.json
  <repo-id> -> agents/repos/<repo-id>   # 可选：mounts.repoIds 挂载的共享 git 仓库软链
  .opencode -> .agents
  .agents/
    bin/agent-bot
    bin/rebuild-workspace
    hooks/before_message.py
    hooks/after_reply.py
    memory/
      info.md
    session-skills/
    agents/
    skills/
    runtime/
      context.env
      localapi.json
```

## `.session-setting.json` 是干嘛的

这是单个 session/workspace 的配置文件。

当前主要字段：

- `template`
- `agent.backend`
- `agent.agentsMode`
- `agent.opencodeConfig`
- `agent.opencodeHTTPTimeoutSeconds`
- `settings.replyMode`
- `settings.historyTTLHours`
- `settings.acceptGroupHumanMessagesWithoutMention`
- `settings.acceptOtherBotMessages`
- `settings.acceptInteractiveCardMessages`
- `settings.remoteEnabled`
- `mounts.skillIds`
- `mounts.subagentIds`
- `mounts.repoIds`

这份文件是人工可编辑的，但不会热更新。

- 群聊默认还是必须 `@` 机器人。
- 如果希望某个会话的新 session 使用不同的 `opencode` 模型细项（例如 `reasoningEffort`、`thinking.budgetTokens`、`variant` 对应的 provider/model 配置），可以在 `.session-setting.json` 里写 `agent.opencodeConfig`，它会作为当前会话专属 patch merge 到 workspace 的 `opencode.json`。
- 如果希望某个会话的 `AGENTS.md` 和 template 脱钩，并且只让当前会话自己修改，可以把 `agent.agentsMode` 设成 `custom`；默认 `template` 模式下它会继续跟随 template。
- 如果希望某个会话的 `opencode serve` HTTP 超时和其他会话不同，可以设置 `agent.opencodeHTTPTimeoutSeconds`；未设置时默认还是 `300` 秒。
- 如果希望某个会话在群里处理“人发但没 `@` 机器人”的消息，把 `settings.acceptGroupHumanMessagesWithoutMention` 设成 `true`。
- 如果希望某个会话补投“其他 bot 发的消息”，把 `settings.acceptOtherBotMessages` 设成 `true`；飞书长连接收不到 bot 间消息，所以平台在飞书 gateway 内置一个轮询 goroutine（`internal/gateway/feishu`，由 listener 持有、随长连接一起起停），周期性拉取这些会话的最新消息，把最近一条“其他 bot（`senderType=app` 且不是自己）”发的消息通过现有处理链路回投。这个开关由轮询线程实时读取 `.session-setting.json`，改完下一轮（默认每秒）即生效，无需 rebuild。其中 `interactive` 卡片还会额外受 `settings.acceptInteractiveCardMessages` 约束（和长连接入口一致）：该开关为 `false` 时不补卡片，但其他 bot 的 `text`/`post` 仍会补。
- 如果希望某个会话接收 `interactive` 卡片消息，把 `settings.acceptInteractiveCardMessages` 设成 `true`。
- 如果希望某个会话接入“本地 agent 插件”（用电脑上的本地 agent 接管这个会话），把 `settings.remoteEnabled` 设成 `true`；它是这个功能的开关闸，关闭时本地插件即使拿 token 也连不进来。详见下面的“本地 agent 接入（remote-agent）”。
- 除 `settings.acceptOtherBotMessages`（轮询线程实时读取）外，`agent.opencodeConfig`、`agent.agentsMode` 和上面这些开关都需要 rebuild 该 workspace 后生效；rebuild 会清空当前 active session，所以下一条普通消息会按新的配置创建新 session。
- 如果希望 agent 在会话里自行读取/修改这些设置，可以挂载平台 skill `session-settings`，它会通过本地 API 读写当前会话的完整 `.session-setting.json`。
- 如果希望某个会话能直接访问某个 git 仓库，先在服务器上 `git clone <repo-url> agents/repos/<id>`，再把 `<id>` 填进 `mounts.repoIds`；rebuild 后 workspace 根目录会软链出 `<id>` 指向 `agents/repos/<id>`，和 skill/subagent 同一套挂载模型。多个 session 挂同一个 repo 会共享同一份工作区（偏只读/看代码；并发改会互相踩），`scripts/pull-workspace-repos.sh` 会对这些软链做 `git pull --ff-only` 兜底。

改完之后要通过这些命令生效：

```bash
go run ./cmd/agent-bot workspace rebuild <provider> <conversation-id>
```

如果你就在这个 chat 的 workspace 目录里，直接执行下面这个脚本就行：

```bash
./.agents/bin/rebuild-workspace
```

`before_message.py` / `after_reply.py` 不需要 rebuild。你直接改当前 workspace 里的

```text
.agents/hooks/before_message.py
.agents/hooks/after_reply.py
```

下一条普通消息会按新的 `before_message.py` 生效；下一条 agent 回复会按新的 `after_reply.py` 生效。

如果只是想切换当前会话的 role（即切换 `AGENTS.md`、skill 挂载和模板里的其他 `.agents/*` 预设），可以直接在聊天里执行：

```text
/roles
/roles <template-name>
```

当前 `/roles` 会：

- 当前会话是 `template` 模式时：把 workspace 根目录的 `AGENTS.md` 切到目标 template 的 `AGENTS.md`
- 当前会话是 `custom` 模式时：保留当前 session 自己的 `AGENTS.md`，不跟目标 template 同步
- 切换到目标 template 的 `mounts.skillIds`
- 切换到目标 template 的 `mounts.subagentIds`
- 切换到目标 template 的 `mounts.repoIds`
- 刷新目标 template 里的其他 `.agents/*` 预设文件
- 保留当前会话的 `.agents/memory/`
- 保留当前会话的 hooks，不跟着 role 切换
- 清空当前 active session，下一条普通消息按新 role 创建 session

## 命令参考

### workspace

生成或补齐一个 workspace：

```bash
go run ./cmd/agent-bot workspace ensure feishu demo-chat
```

按 `.session-setting.json` 重建 workspace：

```bash
go run ./cmd/agent-bot workspace rebuild feishu demo-chat
```

如果当前 shell 已经在某个 chat 的 workspace 里，也可以直接用：

```bash
./.agents/bin/rebuild-workspace
```

### backend

检查默认 backend：

```bash
go run ./cmd/agent-bot backend health
```

显式指定 backend：

```bash
go run ./cmd/agent-bot backend health opencode
```

### session

确保当前会话有 active backend session：

```bash
go run ./cmd/agent-bot session ensure-active feishu demo-chat
```

直接通过 `opencode serve` 发一条文本并拿回复：

```bash
go run ./cmd/agent-bot session prompt feishu demo-chat "只回复 OK"
```

### provider

检查 Feishu 凭证：

```bash
go run ./cmd/agent-bot provider feishu health
```

手动跑一条最小文本处理链路：

```bash
go run ./cmd/agent-bot provider feishu process-text <conversation-id> <direct|group|thread> <message-id> "用户文本"
```

只启动 Feishu 长连接 listener：

```bash
go run ./cmd/agent-bot provider feishu listen
```

smoke test：

```bash
go run ./cmd/agent-bot provider feishu listen 5
```

### run

启动主进程：

```bash
go run ./cmd/agent-bot run
```

它会按 `agent-bot.json` 的 `runtime.defaultProvider` 启动对应 listener，并同时跑 scheduler。`feishu` 下还会随 listener 一起跑进程内的「其他 bot 消息」轮询补偿（`internal/gateway/feishu`），不用再单独起外部 poller 脚本。

同时它也会启动平台本地 HTTP API（gin），默认地址来自：

- `server.host`
- `server.port`

smoke test：

```bash
go run ./cmd/agent-bot run 5
```

### schedule

建任务：

```bash
go run ./cmd/agent-bot schedule create feishu demo-chat 2026-05-06T10:00:00+08:00 reminder.follow_up '{"replyMessageID":"om_xxx","promptText":"15分钟到了，请检查待办是否已经处理完，并把结论回复到当前会话"}'
```

建 cron 周期任务：

```bash
go run ./cmd/agent-bot schedule create-cron feishu demo-chat '0 8 * * *' Asia/Shanghai report.daily_summary '{"replyMessageID":"om_xxx","promptText":"每天早上 8 点汇总昨天遗留事项，并把摘要回复到当前会话"}'
```

如果是一次性任务，`payload.promptText` 可以继续直接放在 job payload 里。

凡是会发消息的 schedule payload，都必须显式带 `payload.replyMessageID`：

- 回原 topic / thread：传原消息 id
- 故意直接发群：显式传空串 `""`

如果是 cron 周期任务，平台会把 prompt 物化到 `data/scheduler/prompts/` 下的专门目录里，再把 job payload 里的引用改成 `promptFile`。

查看某个会话自己的活动任务列表（默认只含 `pending` / `running`）：

```bash
go run ./cmd/agent-bot schedule list feishu demo-chat
```

如果当前 shell 已经在某个 chat 的 workspace 里，也可以直接用：

```bash
./.agents/bin/agent-bot schedule list
```

如果你明确要看历史 `done` / `cancelled` 任务：

```bash
go run ./cmd/agent-bot schedule list feishu demo-chat --all
```

查看到期任务：

```bash
go run ./cmd/agent-bot schedule due
```

手动跑一轮 worker：

```bash
go run ./cmd/agent-bot schedule worker 1 10
```

## 基础指令

当前聊天里已支持：

- `/help`
- `/info`
- `/peek`
- `/compress`
- `/new`
- `/attach <session-id>`（仅同一 workspace path）
- `/clear`
- `/abort`
- `/unblock`
- `/btw <text>`（使用你在当前对话里的独立 btw session 处理这条消息，不影响主 session）
- `/btw-clear`（只清空你在当前对话里的 btw session）
- `/connect-local`（把当前会话切到已连接的本地 agent 插件）
- `/connect-bot`（切回 bot 处理；本地 agent 插件仍保持连接）
- `/disconnect-local`（强制断开本地 agent 插件连接；与 `/connect-bot` 不同，它会真正关闭 WS，插件主动吐的输出也会停。插件若自动重连会再次接管）

说明：

- `/btw` 是最小版双 session 能力：同一个会话/workspace 下，主 `active_session_id` 继续按会话共享；`/btw` 额外按发送者维护独立的 btw session
- `/btw <text>` 只影响当前发送者自己的 btw session，不会改动主 `active_session_id`
- `/btw-clear` 只清掉当前发送者自己的 btw session，不影响主 session
- `/info` 会同时显示主 `active_session_id`，以及当前发送者对应的 `btw_session_id`
- 现阶段 `/clear`、`/new`、`/peek`、`/roles`、`/abort` 等现有命令语义保持不变，仍只围绕主 session

## Feishu 入站能力

当前支持：

- `text`
- `post`
- `image`
- `interactive`

说明：

- `post` 会提取文本和图片节点
- `image` 会先下载临时文件，再作为 `opencode serve` 的 `file` part 发到 session
- `interactive` 默认不处理；只有当前会话的 `.session-setting.json` 里 `settings.acceptInteractiveCardMessages=true` 才会进入处理链路
- `interactive` 的原始 `content` 会作为 `text` 传给 `before_message.py`，可由 hook 解析、拦截并直接回复；如果 hook 不拦截，才会继续进入 `opencode`
- 如果这条消息本身属于某个 topic / thread 回复，平台也会把 `rootMessageId`、`parentMessageId`、`threadId` 一起传给 `before_message.py`
- 群聊默认只处理“人 @ 机器人”的消息
- 如果某个会话的 `.session-setting.json` 里 `settings.acceptGroupHumanMessagesWithoutMention=true`，rebuild 后该群/话题里的普通人类消息也会进入处理链路
- 如果某个会话的 `.session-setting.json` 里 `settings.acceptOtherBotMessages=true`，平台内置的轮询线程会把该会话里“其他 bot（`senderType=app` 且不是自己）”发的最新一条消息回投到现有处理链路；飞书长连接本身收不到 bot 间消息，只能靠这条轮询补偿，每轮只补最新一条、已处理过的不回头补。其中 `interactive` 卡片与长连接入口同样受 `settings.acceptInteractiveCardMessages` 约束：关掉时不补卡片，但其他 bot 的 `text`/`post` 仍补

## 本地 agent 接入（remote-agent）

让电脑上的本地 agent（各家 agent 自己做插件）通过 WebSocket 长连接接入 agent-bot，把一个飞书会话变成本地 agent 的远程镜像 + 遥控：本地 session 的输出推到飞书，你在飞书发的消息注入本地 session。

形态约定：

- 这是一个**受开关 gate 的路由覆盖层**，不是新的 backend。会话的 `agent.backend` 始终不变（默认 `opencode`），opencode session 也全程保留；“本地 agent”只是叠加的运行期路由模式。
- 寻址 + 鉴权直接复用会话的 session token：插件用目标会话 `/info` 里的 `session_web_token` 连 WS，连接即绑定到该会话。
- 接入点：`GET /api/v1/remote-agent/ws?token=<session-token>`（也支持 `Authorization: Bearer <token>`）。只接受 session token；`settings.remoteEnabled=false` 时直接拒。
- 如果通过域名反代暴露这个 WS，nginx 必须让 `/api/` **直接**代理到 `127.0.0.1:8080` 并转发 websocket upgrade，不要让 `/api/` 再经过 Vite `4173`；现成配置见 `docs/nginx-reverse-proxy.md`。
- 1:1：一个飞书会话同一时刻只镜像一个本地 session。

路由状态机（`disabled` / `bot` / `local`）：

- `settings.remoteEnabled=false` → `disabled`：永远走 bot，插件连接被拒。
- 插件连上 → 自动切 `local`（推一条“本地 agent 已连接”提示）。
- 插件断开 → 自动切回 `bot`（推一条“本地 agent 已断开”提示），消息自动回退给 bot 正常回答（不是拒绝）。
- `/connect-bot` → 手动切回 `bot`，即使插件还连着；`/connect-local` → 切回 `local`（插件没连会报错）。
- `/disconnect-local` → 强制关闭插件 WS 连接（区别于 `/connect-bot` 只改路由不断连）；断开后走标准 teardown（回退 bot、清挂起 reaction、推「已断开」）。注意：插件若实现了自动重连，会重新连上并再次接管。
- “最后事件/命令 wins”：连接/断开事件或手动命令，谁最后发生听谁的。
- `local` 模式下，本地 agent 的每条回复末尾会带 `— 来自 local agent` 标注；`/info` 会显示 `remote_enabled` 和 `remote_route`。
- `local` 模式下，消息被转发给本地 agent 时，平台会给这条入站消息加一个「处理中」reaction（用 `providers.feishu.remoteAckEmoji`，不配置则等于 `ackEmoji`），等本地 agent 回投结果或插件断开时自动撤掉，这样你能看出它确实在处理。对应关系是会话级的（不是逐条消息精确），短时间连发多条时会一并清除。
- web 管理台的会话 Settings 页有「允许本地 agent 接入」开关，以及一个实时状态面板（在线/离线、当前 local/bot、已连插件的 agent/session/title），每几秒自动刷新。

WS 协议（JSON，按 `type` 分发）：

- 插件 → server：`{"type":"attach","agentId":"macbook","sessionId":"<local-session>","title":"..."}`、`{"type":"message","sessionId":"<local-session>","text":"..."}`
- server → 插件：`{"type":"prompt","promptId":"...","sessionId":"<local-session>","text":"..."}`
- 1:1 下 `sessionId` 主要是信息性的，入站 `prompt` 由插件自己路由到当前本地 session。

开启步骤：在目标会话把 `.session-setting.json` 的 `settings.remoteEnabled` 设成 `true` 并 rebuild → `/info` 拿到 `session_web_token` → 在电脑插件里用这个 token 连 `/api/v1/remote-agent/ws`。

要为某个本地 agent 实现插件，看 `docs/remote-agent-plugin.md`：它是完整的接入协议规范（WS 接口、鉴权、消息格式、生命周期、保活、参考实现），可直接交给实现者按它落地。

## 群聊回复策略

由 workspace 的 `.session-setting.json` 决定：

- `direct`
  - 回复原消息
  - 并 @ 发送人

- `topic` / `thread`
  - `reply_in_thread=true`
  - 以话题形式回复，整个会话仍共享同一个 active session

- `topic-session`
  - `reply_in_thread=true`，回复方式同 `topic`
  - 额外按 topic 隔离 session：每个 topic（以 `rootMessageId` 为 key，fallback 到 `threadId`/消息自身 id）维护独立的 backend session
  - 发在群里、还没进 topic 的普通消息会开一个新 topic + 新 session；同一 topic 内的后续消息复用该 session
  - `/btw` 仍按发送人维护各自的 btw session，与 topic session 正交
  - topic session 不按 `historyTTLHours` 轮换（开新 topic 即是开新 session）
  - 只有 `/info` 感知所在 topic 并展示 `topic_id` / `topic_session_id`；`/peek`、`/new`、`/clear`、`/compress` 仍作用于主 session

## 定时任务触发结果

触发日志会落到：

```text
.agents/runtime/triggered-jobs.jsonl
```

payload 现在支持两种常见形态：

- `notifyText`
  - 到点后直接回投一条消息

- `promptText` / `promptFile`
  - 到点后重新唤起 agent，在当前会话里继续跑

另外现在也支持 cron 周期任务：

- 通过 `cron + timezone` 指定周期
- `runAt` 和 `cron` 二选一
- 周期任务的 prompt 会落到 `data/scheduler/prompts/` 下的专用目录里，再由 job payload 里的 `promptFile` 引用

更详细说明见 `docs/scheduler.md`。

### TODO

- 当前 scheduler 触发 `promptText` / `notifyText` 时，只能按 `conversation_id` 回投，不能可靠定位原始 `topic/thread`。
- 后续需要为 payload 增加 `replyTarget`（至少包含 `messageId`、`inThread`，必要时补充 mention / thread 锚点），并把当前入站消息的 reply 上下文暴露给 workspace runtime/skills，方便建任务时自动带上。
- 后续想加一个 `/reply` 指令：通过 websocket 方式接到本地 `opencode` 的消息连接，并绑定到某个本地端口上，用来做更直接的消息联动；当前先不实现，只记录这个方向。

`replyMode=topic-session`（基础版已实现）后续待办 / 已知 gap：

- topic session 没有生命周期管理：不按 `historyTTLHours` 轮换，也没有上限 / LRU，活跃群里会随顶层消息无界增长（`conversation_topic_sessions` 行 + 对应 opencode session 永久累积，只有整会话 rebuild/delete 才清）。后续给它接入 TTL 轮换或加最大条数 / LRU 清理。
- `/abort` 在该模式下只 abort main 的 `ActiveSessionID`，停不掉正在跑的 topic prompt（main 平时为空）。后续让 `/abort` 按当前所在 topic 找到 session 再中断。
- scheduler 续跑走 `PromptConversation`（main session），与各 topic 的对话上下文割裂；待 `replyTarget` 落地后再定位到具体 topic session。
- `/peek` / `/new` / `/clear` / `/compress` 在该模式下仍作用于 main session（已确认暂不改），在 topic 内基本无效。
- topic key 依赖飞书 `root_id` 稳定；若某些回复场景 `root_id` 漂移，同一逻辑话题可能分裂成多个 session。
- 该模式不做消息合并：同一 topic 内快速连发会逐条回复，而不是合并成一次。
- List / web 看不到 topic session（不像 btw 有 `<per-user>` 标记），可观测性待补。

## 平台本地 API

主进程启动后，会暴露一个本地 HTTP API 给 agent/skill 调用。

当前 health：

```bash
curl -sS http://127.0.0.1:8080/health
```

workspace 里会自动生成：

```text
.agents/runtime/context.env
.agents/runtime/localapi.json
```

其中 `context.env` 会提供：

- `AGENT_BOT_API_BASE_URL`
- `AGENT_BOT_PROVIDER`
- `AGENT_BOT_CONVERSATION_ID`

现在平台 skill 应该优先调用这个本地 HTTP API，而不是直接调 `./.agents/bin/agent-bot ...`。

当前和 scheduler 相关的会话级接口有：

- `POST /api/v1/schedule`
  - 给当前会话建任务
- `GET /api/v1/schedule?provider=...&conversationId=...`
  - 默认只返回这个 session 自己仍在生效的任务（`pending` / `running`）
  - 如果要显式查看历史 `done` / `cancelled`，追加 `&includeDone=1`
- `PUT /api/v1/schedule`
  - 修改某个任务当前保存的 `promptText` / `promptFile` 对应提示词，或 `notifyText`
  - 周期任务如果已经物化成 `promptFile`，会原地改写该 prompt 文件内容
- `POST /api/v1/schedule/cancel`
  - 取消某个任务

当前也有 provider 辅助接口：

- `GET /api/v1/provider/chat-members?provider=...&conversationId=...`
  - 读取当前群成员
- `GET /api/v1/provider/chat-messages?provider=...&conversationId=...`
  - 读取当前群最近消息，支持 `startTime`、`endTime`、`sortType`、`pageSize`、`pageToken`、`cardMsgContentType`
- `POST /api/v1/provider/recall-message`
  - 通过当前 provider 直接撤回一条消息，body 至少包含 `provider` 和 `messageId`
- `POST /api/v1/provider/mock-message`
  - 把一条消息按本地 mock 入站重新喂给处理链路，便于做群卡片轮询补偿

当前也有一个会话设置接口：

- `GET /api/v1/session/settings?provider=...&conversationId=...`
  - 读取当前会话完整的 `.session-setting.json`
- `POST /api/v1/session/settings`
  - 写回完整的 `.session-setting.json`
  - 默认会自动 rebuild 当前 workspace，让新设置立即生效

当前也有一组给 web 管理台用的 admin 接口，需要带 `Authorization: Bearer <token>`：

- `GET /api/v1/admin/me`
  - 校验 token，并返回当前 scope（`project` / `session`）
- `GET /api/v1/admin/sessions`
  - 项目级 token 列出全部 session
- `POST /api/v1/admin/sessions/display-names`
  - 后台批量解析 session 展示名（群名 / 用户名），供 web 列表异步补全
- `GET /api/v1/admin/sessions/:provider/:conversationId`
  - 读取单个 session 详情、模板列表和当前 session token
- `DELETE /api/v1/admin/sessions/:provider/:conversationId`
  - 删除某个 session 的 workspace 和相关持久化状态；下次访问会按默认流程重建
- `GET /api/v1/admin/sessions/:provider/:conversationId/session-skills`
- `POST /api/v1/admin/sessions/:provider/:conversationId/session-skills`
- `POST /api/v1/admin/sessions/:provider/:conversationId/session-skills/upload`
- `GET /api/v1/admin/sessions/:provider/:conversationId/session-skills/:skillId`
- `GET /api/v1/admin/sessions/:provider/:conversationId/session-skills/:skillId/files`
- `GET /api/v1/admin/sessions/:provider/:conversationId/session-skills/:skillId/files/content?path=...`
- `PUT /api/v1/admin/sessions/:provider/:conversationId/session-skills/:skillId/files/content`
- `DELETE /api/v1/admin/sessions/:provider/:conversationId/session-skills/:skillId`
  - project / session token 都可管理当前 session 独有的私有 skill；支持直接创建带标准 frontmatter 的默认 `SKILL.md`，也支持上传 skill zip；这些 skill 自动挂载到当前 session，但不写进 `.session-setting.json`
- `GET /api/v1/admin/sessions/:provider/:conversationId/session-data/export`
- `POST /api/v1/admin/sessions/:provider/:conversationId/session-data/import`
  - 导出/导入当前 session 的特有数据包；当前包含私有 skill、memory、hooks，不包含公共 skill
- `PUT /api/v1/admin/sessions/:provider/:conversationId/settings`
  - 更新 settings，并立即 rebuild 生效
- `POST /api/v1/admin/sessions/:provider/:conversationId/token/rotate`
  - 轮换当前 session token
- `GET /api/v1/admin/sessions/:provider/:conversationId/files/memory`
- `GET /api/v1/admin/sessions/:provider/:conversationId/files/memory/content?path=...`
- `PUT /api/v1/admin/sessions/:provider/:conversationId/files/memory/content`
  - 管理 `.agents/memory/` 文件区
- `GET /api/v1/admin/sessions/:provider/:conversationId/files/hooks`
- `GET /api/v1/admin/sessions/:provider/:conversationId/files/hooks/content?path=...`
- `PUT /api/v1/admin/sessions/:provider/:conversationId/files/hooks/content`
  - 管理 `before_message.py` / `on_reaction.py` / `after_reply.py`
- `GET /api/v1/admin/roles`
- `POST /api/v1/admin/roles`
- `GET /api/v1/admin/roles/:roleId`
- `PUT /api/v1/admin/roles/:roleId`
- `DELETE /api/v1/admin/roles/:roleId`
  - 管理 `templates/<role>/AGENTS.md` 和默认 `.session-setting.json`
  - 新建 role 会先复制一个现有 template
  - `default` 或仍被 session 引用的 role 会拒绝删除
- `GET /api/v1/admin/subagents`
  - 列出仓库根 `agents/subagents/` 下可挂载的 subagent 定义；project / session token 都可读
- `POST /api/v1/admin/subagents`
- `GET /api/v1/admin/subagents/:subagentId`
- `PUT /api/v1/admin/subagents/:subagentId`
- `DELETE /api/v1/admin/subagents/:subagentId`
  - project token 可新建、编辑、删除 subagent；session token 只读查看
- `GET /api/v1/admin/skills`
  - 列出公共 skill 仓库；project / session token 都可读
- `POST /api/v1/admin/skills`
  - 仅 project token 可直接新建一个带标准 frontmatter 的默认 `SKILL.md` 公共 skill
- `POST /api/v1/admin/skills/upload`
  - 仅 project token 可上传 zip 到公共 skill 仓库；`skillId` 取 zip 根目录名，同名拒绝
- `GET /api/v1/admin/skills/:skillId`
- `GET /api/v1/admin/skills/:skillId/files`
- `GET /api/v1/admin/skills/:skillId/files/content?path=...`
  - project / session token 都可只读查看 skill 详情和文件内容
- `PUT /api/v1/admin/skills/:skillId/files/content`
- `DELETE /api/v1/admin/skills/:skillId`
  - 仅 project token 可编辑或删除公共 skill
- `GET /api/v1/admin/repos`
  - 列出 `agents/repos/` 下可挂载的共享 git 仓库（`id` / 当前 `branch` / `hasGit`）；project / session token 都可读。挂载通过会话 `mounts.repoIds` 控制
- `POST /api/v1/admin/repos`
  - 仅 project token 可把仓库 `git clone` 进 `agents/repos/<id>`（body：`{"url":"...","id":"可选"}`，`id` 留空按地址自动取）
  - 防注入：URL 不能以 `-` 开头、不含控制字符，只允许 `https`/`http`/`ssh`/`git` 协议与 `user@host:path` scp 形式，显式拦截 `ext::`/`file://`/`fd::` 等可执行命令的传输；clone 用 `exec` 数组参数 + `--` 分隔 + `GIT_ALLOW_PROTOCOL` 兜底，绝不过 shell；`id` 走和挂载一致的安全校验和保留名拦截，clone 到临时目录后再 rename 到位，5 分钟超时
- `GET /api/v1/admin/repos/:repoId/branches`
  - 列出该仓库可切换的分支（本地 head + 远端跟踪分支去掉 remote 前缀、去重排序）和当前分支；project / session token 都可读
- `POST /api/v1/admin/repos/:repoId/pull`
  - 仅 project token，对仓库执行 `git pull --ff-only`（5 分钟超时）；非 fast-forward / 有冲突时返回 409 并带 git 报错，不强制覆盖
- `POST /api/v1/admin/repos/:repoId/checkout`
  - 仅 project token，`git checkout <branch>`（body：`{"branch":"..."}`）；分支名走保守白名单校验（禁止 `-` 开头、空格/控制字符、`..`/`//`/`@{`/`.lock` 等），git 参数走 `exec` 数组不过 shell；有未提交改动导致切换失败时返回 409
  - 注意：repo 多为软链，pull/checkout 直接作用在共享工作区上，会影响所有挂载它的 session
- `GET /api/v1/admin/scripts`
- `GET /api/v1/admin/scripts/content?path=...`
- `PUT /api/v1/admin/scripts/content`
  - project token 可查看和编辑仓库根 `scripts/` 下的系统级脚本

## Web 管理台

前端工程在：

```text
web/
```

本地开发：

```bash
cd web
npm install
npm run dev
```

默认会通过 Vite proxy 把 `/api` 代理到 `http://127.0.0.1:8080`。

如果需要后台重启前端开发服务，可以直接使用仓库脚本：

```bash
./scripts/restart-web.sh
```

- PID 文件：`data/run/web.pid`
- 日志文件：`data/run/web.log`

项目级 token 登录后，当前管理台支持五类后台能力：

- Sessions：查看和修改单个会话的 settings / token / memory / hooks / 私有 skills / schedule / session data export-import
- Roles：管理 `templates/` 下的 role(template)，直接编辑 `AGENTS.md` 和默认 settings（含默认挂载的 skills / subagents / repos）
- Subagents：管理仓库根 `agents/subagents/` 下的 subagent 定义
- Skills：上传和查看公共 skill 仓库
- Scripts：查看和编辑仓库根 `scripts/` 下的系统级脚本

session token 登录后，当前单会话门户也可以只读浏览公共 skill / subagent 仓库，但不能新增、编辑或删除。

生产构建：

```bash
cd web
npm run build
```

## Python Hook

每个 chat 固定预留一个 Python hook：

```text
.agents/hooks/before_message.py
```

平台会在普通消息进入 `opencode` 之前执行它。

- 内建斜杠命令还是先在平台层处理
- `refuse` 命中后不会再进入这个 hook

hook 约定：

- stdin: 当前消息 JSON
  - `provider`
  - `conversationId`
  - `conversationType`
  - `messageType`
  - `messageId`
  - `rootMessageId`
  - `parentMessageId`
  - `threadId`
  - `senderType`
  - `senderId`
  - `text`
  - `attachments`
- stdout 为空: 不改动，继续处理
- stdout = `{"drop":true}`: 直接拦截，不再进入 `opencode`
- stdout = `{"drop":true,"reactionEmoji":"ENOUGH"}`: 拦截并给这条消息补一个 reaction
- stdout = `{"drop":true,"replyText":"..."}`: 拦截并回一条文本
- stdout = `{"text":"..."}`: 改写发给 `opencode` 的文本
- stdout = `{"text":"...","systemText":"..."}`: 改写发给 `opencode` 的文本，并额外指定这条消息的 system 提示

当前只支持改写 `text`，不支持改 `attachments`。

`.agents/hooks/on_reaction.py` 会在平台收到表情事件后执行。

- 前提：除了开通 `im:message.reactions:read` 或 `im:message:readonly` 这类权限外，还必须在飞书开放平台的事件订阅里显式添加 `im.message.reaction.created_v1` / `im.message.reaction.deleted_v1`（后台名称分别是“消息被reaction”/“消息被取消reaction”）。仅有权限但未订阅事件时，长连接侧不会收到 reaction 业务事件，日志里通常只会看到 ws 控制帧（如 `pong`），不会进入 `on_reaction.py`。
- stdin: `provider`、`conversationId`、`conversationType`、`messageType=reaction`、`messageId`、`rootMessageId`、`parentMessageId`、`threadId`、`senderType`、`senderId`、`eventAction`、`reactionEmoji`
- stdout 为空: 忽略这次表情事件，不唤醒 `opencode`
- stdout = `{"drop":true}`: 显式忽略
- stdout = `{"drop":true,"reactionEmoji":"EYES"}`: 忽略，并给原消息补一个 reaction
- stdout = `{"drop":true,"replyText":"..."}`: 忽略，并直接回一条文本
- stdout = `{"text":"..."}`: 把这次表情事件改写成一条普通文本，再继续唤醒 `opencode`
- stdout = `{"text":"...","systemText":"..."}`: 改写成普通文本，并额外指定给模型的 system 提示

`.agents/hooks/after_reply.py` 会在 `opencode` 产出回复后、真正发给 IM 前执行。

- stdin: `provider`、`conversationId`、`conversationType`、`messageType`、`messageId`、`rootMessageId`、`parentMessageId`、`threadId`、`senderType`、`senderId`、`text`、`systemText`、`replyText`
- stdout 为空: 不改动，继续发送原回复
- stdout = `{"replyText":"..."}`: 改写即将发送的回复
- stdout = `{"mentionUserId":"ou_xxx"}`: 指定一个真 `@` 的用户
- stdout = `{"mentionUserIds":["ou_a","ou_b"]}`: 指定多个真 `@` 的用户
- stdout = `{"replyText":"...","mentionUserId":"ou_xxx"}`: 同时改写回复并指定一个真 `@`
- stdout = `{"replyText":"...","mentionUserIds":["ou_a","ou_b"]}`: 同时改写回复并指定多个真 `@`
- 运行时会额外注入 `AGENT_BOT_API_BASE_URL`、`AGENT_BOT_PROVIDER`、`AGENT_BOT_CONVERSATION_ID`、`AGENT_BOT_ROOT_MESSAGE_ID`、`AGENT_BOT_PARENT_MESSAGE_ID`、`AGENT_BOT_THREAD_ID`
- 如果 hook 需要查当前群成员，可调用 `GET $AGENT_BOT_API_BASE_URL/api/v1/provider/chat-members?provider=...&conversationId=...`

## 验证

```bash
go test ./...
```
