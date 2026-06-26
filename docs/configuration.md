# Configuration

## 全局配置

全局平台配置文件是仓库根的 `agent-bot.json`；仓库里自带一份可复制的 `agent-bot.example.json`。

它只放平台层配置，不放 `opencode` 自己的 provider/model 细节。

当前主要内容：

- `runtime.defaultProvider`
- `runtime.defaultBackend`
- `runtime.schedulerIntervalSeconds`
- `runtime.schedulerBatchLimit`
- `server.host`
- `server.port`
- `auth.projectToken`
- `auth.secret`
- `web.baseURL`
- `providers.feishu.*`
- `backends.opencode.baseURL`

其中：

- `auth.projectToken`
  - web 管理台的项目级登录 token
- `auth.secret`
  - 用于加密/解密 session token 的 secret
  - session token 不以明文落 sqlite，而是存 hash + 密文
- `web.baseURL`
  - 可选的前端控制台基地址，例如 `https://agent-bot.example.com`
  - 配置后 `/info` 会输出 `console_url=<baseURL>/?token=<session-token>`

## 会话配置

每个 workspace 的 `.session-setting.json` 是当前会话自己的人工配置。

当前常用字段：

- `template`
- `agent.backend`
- `agent.agentsMode`
- `agent.opencodeConfig`
- `agent.opencodeHTTPTimeoutSeconds`
- `settings.replyMode`（`direct` / `topic` / `thread` / `topic-session`；`topic-session` 在话题内回复的基础上，按 topic 隔离 backend session，详见 `README.md` 的“群聊回复策略”）
- `settings.historyTTLHours`
- `settings.acceptGroupHumanMessagesWithoutMention`
- `settings.acceptOtherBotMessages`（开启后平台在 `internal/gateway/feishu` 内置的轮询线程会补投其他 bot 发的消息；其中 `interactive` 卡片额外受 `acceptInteractiveCardMessages` 约束，`text`/`post` 不受约束）
- `settings.acceptInteractiveCardMessages`
- `settings.remoteEnabled`（是否允许本地 agent 插件接管该会话，默认 `false`，详见 `README.md` 的“本地 agent 接入（remote-agent）”）
- `mounts.skillIds`
- `mounts.subagentIds`
- `mounts.repoIds`

## `settings.remoteEnabled`

控制该会话是否允许接入“本地 agent 插件”。

- 默认 `false`。为 `false` 时是安全闸：本地插件即使拿到该会话的 session token，连 `/api/v1/remote-agent/ws` 也会被拒，会话永远走默认 backend。
- 为 `true` 时，本地插件用该会话的 session token 连上 WS 即把会话切到本地 agent（连接=绑定，1:1）；断开自动回退给 bot。
- 它是“受开关 gate 的路由覆盖层”，不改 `agent.backend`、不动 opencode session；切换可来回、不破坏 bot 侧上下文。
- 飞书命令 `/connect-bot`、`/connect-local` 做手动切换；`/disconnect-local` 强制关闭插件 WS 连接（区别于 `/connect-bot` 只改路由不断连，插件若自动重连会再次接管）；`/info` 的 `remote_enabled` / `remote_route` 反映当前状态。
- `local` 模式下消息被转发给本地 agent 时，平台会给入站消息加一个「处理中」reaction（emoji 取全局 `providers.feishu.remoteAckEmoji`，未配置时回退到 `ackEmoji`），等本地 agent 回投或插件断开时自动撤掉。

## `mounts.repoIds`

把共享 git 仓库以软链挂进当前 workspace，和 `skillIds` / `subagentIds` 是同一套挂载模型：

- 仓库源目录是仓库根的 `agents/repos/<id>`，和 `agents/skills` 同级（代码默认值、不可配；已加进 `.gitignore`，真实 clone 不会进 agent-bot 自己的 git）。
- 先把仓库放进 `agents/repos/<id>`，三种方式都行：在服务器上 `git clone <repo-url> agents/repos/<id>`；在 web 管理台 `Repos` 页用 project token 填地址 clone（接口 `POST /api/v1/admin/repos`，只允许 `https`/`http`/`ssh`/`git` 与 scp 形式、拦截 `ext::`/`file://` 等危险传输）；或把已有 checkout 软链进来 `ln -s /abs/path/to/repo agents/repos/<id>`（`agents/repos/<id>` 自身是 symlink 也会被列表和挂载识别，`os.Stat` 跟随软链；适合复用 GOPATH 等已有源码树、不想再 clone 一份）。放好后把 `<id>` 填进 `mounts.repoIds`；rebuild 后 workspace 根目录会出现 `<id>` 软链指向 `agents/repos/<id>`。
- `<id>` 只能用字母、数字、`-`、`_`、`.`，且不能和 `AGENTS.md` / `opencode.json` / `.session-setting.json` / `.agents` / `.opencode` / `.git` 等保留名冲突（冲突会被跳过、不覆盖）。
- 软链是 runtime 衍生物，每次 reconcile 全量重建；删软链不动真实 repo 数据（`os.RemoveAll` 对软链只删 link）。
- 多个 session 挂同一个 `<id>` 会共享同一份物理工作区——适合只读看代码 / 偶尔 `git pull`；多个会话并发改同一个 repo 会互相踩。
- `scripts/pull-workspace-repos.sh <workspace>` 会遍历 workspace 根目录软链、对其中的 git repo 执行 `git pull --ff-only` 做兜底。
- web 管理台 `Repos` 页（project token）支持按 id/分支搜索，并能对单个仓库 `Pull`（`git pull --ff-only`）和切换分支（`git checkout`，下拉列出本地+远端分支）；这些操作直接作用在共享工作区上、会影响所有挂载它的 session，有未提交改动时 git 会拒绝而不会强制覆盖。

## `agent.opencodeConfig`

这是当前会话专属的 `opencode.json` patch。

平台在生成 workspace `opencode.json` 时会按下面顺序 merge：

1. 基础 schema：`{"$schema":"https://opencode.ai/config.json"}`
2. 全局 `cfg.OpencodeConfig`
3. 当前会话的 `settings.agent.opencodeConfig`

所以它适合做“每个群聊不同”的 backend 细项，比如：

- OpenAI `reasoningEffort`
- Anthropic `thinking.budgetTokens`
- model `variants`
- provider/model 级其他 `options`

示例：

```json
{
  "agent": {
    "backend": "opencode",
    "opencodeConfig": {
      "provider": {
        "openai": {
          "models": {
            "gpt-5": {
              "options": {
                "reasoningEffort": "high",
                "textVerbosity": "low"
              }
            }
          }
        }
      }
    }
  }
}
```

## `agent.opencodeHTTPTimeoutSeconds`

这是当前会话专属的 `opencode serve` HTTP 请求超时，单位是秒。

- 只影响平台调用 `opencode serve` 的 HTTP client 超时
- 不会写进 `opencode.json`
- 未设置或设为 `0` 时，继续使用默认值 `300` 秒

示例：

```json
{
  "agent": {
    "backend": "opencode",
    "opencodeHTTPTimeoutSeconds": 900
  }
}
```

另一个例子：

```json
{
  "agent": {
    "backend": "opencode",
    "opencodeConfig": {
      "provider": {
        "anthropic": {
          "models": {
            "claude-sonnet-4-5-20250929": {
              "options": {
                "thinking": {
                  "type": "enabled",
                  "budgetTokens": 8000
                }
              }
            }
          }
        }
      }
    }
  }
}
```

## `agent.agentsMode`

这是当前会话的 `AGENTS.md` 跟 template 还是走 session 自定义的模式位。

- `template`: 当前 session 的 `AGENTS.md` 跟随 template；`/roles` 或 rebuild 时会按 template 刷新
- `custom`: 当前 session 的 `AGENTS.md` 归自己所有；rebuild 和 `/roles` 都会保留这份内容，不影响其他 session

示例：

```json
{
  "agent": {
    "backend": "opencode",
    "agentsMode": "custom"
  }
}
```

## 生效时机

下面这些改动都不是热更新：

- `.session-setting.json`
- `mounts.skillIds`
- `mounts.subagentIds`
- `mounts.repoIds`
- `agent.agentsMode`
- `agent.opencodeConfig`

改完后要 rebuild workspace：

```bash
go run ./cmd/agent-bot workspace rebuild <provider> <conversation-id>
```

或者在 workspace 里直接执行：

```bash
./.agents/bin/rebuild-workspace
```

rebuild 会：

- 重新生成 workspace 结构
- 保留 `.agents/memory/`
- 保留 `.agents/hooks/`
- 清空当前 active session 绑定

## Hooks

当前 workspace 支持三个 Python hook：

- `.agents/hooks/before_message.py`
  - 普通消息进入 `opencode` 前执行
  - 可拦截、改写文本、指定单条消息的 `systemText`、补 reaction
- `.agents/hooks/on_reaction.py`
  - 表情事件到达时执行
  - 默认只做 hook 内部处理，不会自动唤醒 `opencode`
  - 只有 hook 显式返回 `{"text":"..."}` 时，才会把该表情事件改写成一条普通文本继续进入主链路
- `.agents/hooks/after_reply.py`
  - `opencode` 产出回复后、真正发给 IM 前执行
  - 可改写回复文本或指定 `mentionUserId` / `mentionUserIds`

`after_reply.py` 运行时也会拿到当前消息的 `messageId/rootMessageId/parentMessageId/threadId`，以及 `AGENT_BOT_API_BASE_URL`、`AGENT_BOT_PROVIDER`、`AGENT_BOT_CONVERSATION_ID` 这些环境变量，必要时可以通过本地 API 查询当前群成员，再把结果转成 `mentionUserId` 或 `mentionUserIds`。

因此下一条普通消息会按新的 `opencode.json` 创建新 session。

## Role 切换

当前会话的 role 以 template 为基础。

聊天里可用：

- `/roles`: 查看当前 template 和可切换 template 列表
- `/roles <template-name>`: 切换当前会话 role

当前 role 切换语义是：

- 当前会话是 `template` 模式时：把 workspace 根目录的 `AGENTS.md` 切到目标 template 的 `AGENTS.md`
- 当前会话是 `custom` 模式时：保留当前 session 自己的 `AGENTS.md`
- 切换 `mounts.skillIds`
- 切换 `mounts.subagentIds`
- 切换 `mounts.repoIds`
- 刷新 template 自带的其他 `.agents/*` 预设文件
- 保留 `.agents/memory/`
- 保留 `.agents/hooks/`

也就是说，当前 `/roles` 不会切 hook；hook 仍然属于当前会话自己的本地定制。

## BTW Session

当前支持最小版双 session：

- 主 session：`active_session_id`
- btw session：运行时按发送者单独维护

聊天里可用：

- `/btw <text>`：使用当前发送者自己的独立 btw session 处理这条消息，不影响主 session
- `/btw-clear`：只清空当前发送者自己的 btw session

当前约束：

- `/info` 会显示当前发送者对应的 `btw_session_id`
- `/clear`、`/new`、`/peek`、`/roles`、`/abort` 仍保持主 session 语义，不自动处理 btw session
- btw session 不复用主 session 的 `historyTTLHours` 轮换逻辑；它只在当前发送者显式 `/btw-clear` 时清空，或被该发送者新的 `/btw` 继续复用

## Hooks

这两个 hook 不需要 rebuild：

- `.agents/hooks/before_message.py`
- `.agents/hooks/after_reply.py`

直接修改文件后，下一条消息/下一条回复就按新逻辑生效。

`after_reply.py` 运行时也会拿到当前消息的 `messageId/rootMessageId/parentMessageId/threadId`，以及 `AGENT_BOT_API_BASE_URL`、`AGENT_BOT_PROVIDER`、`AGENT_BOT_CONVERSATION_ID` 这些环境变量，必要时可以通过本地 API 查询当前群成员，再把结果转成 `mentionUserId` 或 `mentionUserIds`。
