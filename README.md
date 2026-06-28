# Agent Bot

`agent-bot` 是一个把 AI agent 接进飞书会话的控制层平台。

它负责把这些能力串起来：

- Feishu 入站消息
- 每个会话独立 workspace
- `opencode serve` backend session
- scheduler 定时任务
- 本地 HTTP API
- web 管理台

这个仓库刻意只做平台层，不内置业务模板、业务 skill 或业务脚本。

## 当前状态

- 当前正式接好的 IM provider：`feishu`
- 当前正式接好的 agent backend：`opencode serve`
- 其他 provider/backend 不是“配一下就能用”，需要补代码接入

## 适合什么场景

- 想把一个 AI agent 变成飞书里的群聊 / 私聊 bot
- 想让每个会话有自己的角色、记忆、hook、模型配置和 repo 挂载
- 想给 agent 增加定时任务、会话级设置、私有 skill、管理台
- 想把“平台层”和“业务 skill 层”拆开维护

## 核心模型

这套系统有四个最重要的概念：

1. 一个飞书会话对应一个稳定 workspace
2. 一个 workspace 通过 `.session-setting.json` 决定当前会话怎么运行
3. 消息、scheduler 续跑、其他 bot 消息补投，最终都走同一条 `internal/flow` 主链路
4. `opencode` 作为 backend，真正的 provider/model 配置仍由 `opencode` 自己管理

## 特性概览

- 每个会话独立 workspace，隔离 memory / hooks / mounted skills / mounted repos
- 支持角色模板（template / role）和会话自定义 `AGENTS.md`
- 支持会话级 `opencodeConfig` patch，可让不同 session 用不同模型或不同 model options
- 支持 `direct` / `topic` / `thread` / `topic-session` 回复模式
- 支持 `/btw` 这种“同会话下按发送者隔离的额外 session”
- 支持 Python hooks：`before_message.py`、`on_reaction.py`、`after_reply.py`
- 支持会话级 scheduler：一次性任务、cron、prompt replay、notify replay
- 支持其他 bot 消息补投
- 支持 remote-agent：本地 agent 插件通过 WebSocket 接管某个会话
- 支持 web 管理台：Sessions / Roles / Skills / Repos / Scripts / Subagents

## 仓库结构

```text
cmd/agent-bot/              CLI 和 daemon 入口
internal/config/            全局配置加载
internal/workspace/         workspace 生成、rebuild、挂载
internal/session/           session 生命周期与 TTL
internal/flow/              主消息处理链路
internal/gateway/feishu/    Feishu listener、解析、补投轮询
internal/scheduler/         定时任务存储、轮询、执行
internal/localapi/          本地 API 与 web admin 接口
internal/remoteagent/       本地 agent 插件 WS hub
templates/default/          默认模板
agents/skills/              平台级可复用 skills
agents/subagents/           平台级 subagent 定义
web/                        React + Vite 管理台
docs/                       详细设计与运行文档
```

## 依赖要求

- Go 1.25+
- Node.js 18+（只在需要 web 管理台时）
- Python 3（用于 hooks / 辅助脚本 / watchdog 脚本）
- 本机可执行的 `opencode` 命令
- 一个已配置好的 Feishu bot app

## 5 分钟跑起来

### 1. 准备配置

复制示例配置：

```bash
cp agent-bot.example.json agent-bot.json
```

至少填这些字段：

- `auth.projectToken`
- `auth.secret`
- `providers.feishu.appID`
- `providers.feishu.appSecret`
- `backends.opencode.baseURL`

注意：

- `agent-bot.json` 只管平台层配置
- `opencode` 自己的 provider / model / API key 配置，不在这里写

### 2. 先启动 `opencode serve`

推荐直接用仓库脚本：

```bash
./scripts/restart-opencode.sh
```

验证：

```bash
go run ./cmd/agent-bot backend health
```

### 3. 验证 Feishu 凭证

```bash
go run ./cmd/agent-bot provider feishu health
```

### 4. 启动 `agent-bot`

前台启动：

```bash
go run ./cmd/agent-bot run
```

只做 smoke test：

```bash
go run ./cmd/agent-bot run 5
```

后台运行推荐用脚本：

```bash
./scripts/restart-agent-bot.sh
```

验证：

```bash
curl -sS http://127.0.0.1:8080/health
```

### 5. 启动 web 管理台（可选）

首次安装依赖：

```bash
cd web
npm install
cd ..
```

开发模式启动：

```bash
./scripts/restart-web.sh
```

默认地址：

- `http://127.0.0.1:4173`

## 推荐后台运行方式

长期运行推荐用仓库脚本，而不是自己手写 `nohup`：

```bash
./scripts/restart-opencode.sh
./scripts/restart-agent-bot.sh
./scripts/restart-web.sh
./scripts/watch-opencode.sh start
./scripts/watch-agent-bot.sh start
```

说明：

- `restart-opencode.sh` 会后台启动 `opencode serve`，默认监听 `127.0.0.1:4096`
- `restart-agent-bot.sh` 会先 `go build`，再后台启动 daemon，默认监听 `127.0.0.1:8080`
- `restart-web.sh` 会后台启动 Vite dev server，默认监听 `127.0.0.1:4173`
- `watch-opencode.sh` 和 `watch-agent-bot.sh` 是看门狗脚本
- 运行日志和 pid 在 `data/run/`

## 配置模型

### 全局配置：`agent-bot.json`

全局平台配置文件位于仓库根：

```text
agent-bot.json
```

它负责：

- 默认 provider / backend
- 本地 API 监听地址
- scheduler 基本参数
- project token 与 session token 加密 secret
- Feishu bot 凭证
- `opencode serve` 的 base URL
- web base URL

示例：

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
    "projectToken": "CHANGE_ME",
    "secret": "CHANGE_ME"
  },
  "providers": {
    "feishu": {
      "appID": "cli_xxx",
      "appSecret": "xxx"
    }
  },
  "backends": {
    "opencode": {
      "baseURL": "http://127.0.0.1:4096"
    }
  }
}
```

### 会话配置：`.session-setting.json`

每个会话都有自己的一份：

```text
data/chats/<provider>/<conversation-id>/.session-setting.json
```

它控制：

- 当前 template
- 当前 backend
- 当前会话的 `AGENTS.md` 是否跟随 template
- 当前会话的 `opencodeConfig` patch
- 回复模式 / TTL / 是否接受未 @ 消息 / 是否接受其他 bot 消息 / 是否接受 interactive / 是否启用 remote-agent
- skill / subagent / repo 挂载

最常用的几个字段：

- `template`
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

### 每个 session 用不同模型

支持。

你可以在某个会话的 `.session-setting.json` 里写 `agent.opencodeConfig`，它会 merge 到该会话 workspace 的 `opencode.json`。

例如：

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

也就是说：

- 平台支持“每个 session 一份不同的 `opencode` patch”
- 真正的 provider/model schema 仍然跟 `opencode` 自己的配置格式走

## Workspace 结构

一个会话对应一个稳定目录：

```text
data/chats/<provider>/<conversation-id>/
  AGENTS.md
  .session-setting.json
  opencode.json
  .opencode -> .agents
  .agents/
    bin/agent-bot
    bin/rebuild-workspace
    hooks/
      before_message.py
      on_reaction.py
      after_reply.py
    memory/
      info.md
    session-skills/
    skills/
    agents/
    runtime/
      context.env
      localapi.json
```

几个重要约束：

- 每个会话一个 workspace
- rebuild 会刷新 template-owned 内容，但会保留 memory / hooks / session-skills / runtime
- `mounts.repoIds` 会把 `agents/repos/<id>` 软链进 workspace 根目录

## 消息处理链路

统一入口最终都会走 `internal/flow`：

1. provider / gateway 解析入站消息
2. 平台命令和 refuse 规则先处理
3. 普通消息走 `before_message.py`；reaction 事件走 `on_reaction.py`
4. 解析当前 workspace、session 和路由模式
5. 调 `opencode serve`
6. 走 `after_reply.py`
7. provider 发回 IM

同一条链路也被这些场景复用：

- scheduler 的 `promptText`
- scheduler 的 `notifyText`
- 其他 bot 消息补投
- remote-agent 回投

## 回复模式与 session 模式

当前支持四种回复模式：

- `direct`
- `topic`
- `thread`
- `topic-session`

其中 `topic-session` 的含义是：

- 回复仍然在 thread / topic 里
- 但 backend session 不再整会话共享，而是按 topic 隔离

在 `topic-session` 模式下，针对 session 的命令也按 topic 生效：

- 在某个话题里执行 `/peek`、`/compress`、`/abort`、`/attach`、`/clear`、`/new`，都作用于该话题自己的 session
- 在话题外执行 `/peek`、`/compress`、`/abort`、`/attach` 会提示先进入具体话题
- 在话题外执行 `/clear`、`/new` 则重置整个会话的所有话题 session

除此之外，还支持：

- `/btw <text>`：同一会话里为“当前发送者”单独维护一个 btw session

## Web 管理台能做什么

项目级 token 登录后，可以管理：

- Sessions：查看和修改 settings、token、memory、hooks、session transcript、session skills、schedule
- Roles：管理 `templates/` 下的 role / template
- Skills：查看和维护公共 skill
- Repos：管理共享 repo 挂载源
- Scripts：查看和维护系统级脚本
- Subagents：查看和维护 subagent 定义

session token 登录后：

- 只能看到自己那个会话
- 适合会话自助调试

## 常用 CLI 命令

检查 backend：

```bash
go run ./cmd/agent-bot backend health
```

检查 Feishu 凭证：

```bash
go run ./cmd/agent-bot provider feishu health
```

生成或补齐 workspace：

```bash
go run ./cmd/agent-bot workspace ensure feishu demo-chat
```

按当前 settings rebuild workspace：

```bash
go run ./cmd/agent-bot workspace rebuild feishu demo-chat
```

直接对某个会话发一条文本：

```bash
go run ./cmd/agent-bot session prompt feishu demo-chat "只回复 OK"
```

查看某个会话的活动任务：

```bash
go run ./cmd/agent-bot schedule list feishu demo-chat
```

创建一次性任务：

```bash
go run ./cmd/agent-bot schedule create feishu demo-chat 2026-05-06T10:00:00+08:00 reminder.follow_up '{"replyMessageID":"om_xxx","promptText":"15分钟后提醒我继续处理"}'
```

## Hooks

每个会话支持三个 Python hooks：

- `.agents/hooks/before_message.py`
- `.agents/hooks/on_reaction.py`
- `.agents/hooks/after_reply.py`

职责分别是：

- `before_message.py`：消息进 backend 前拦截 / 改写 / 注入 systemText
- `on_reaction.py`：处理 reaction 事件
- `after_reply.py`：回复发回 IM 前改写 / 指定 mention 用户

这些 hooks 直接改 workspace 里的文件即可，不需要额外发布。

## Remote Agent

`remote-agent` 允许电脑上的本地 agent 插件通过 WebSocket 接管某个飞书会话。

它的定位不是新 backend，而是一个运行时路由覆盖层：

- `agent.backend` 仍然是原来的 backend
- `opencode` session 仍然保留
- 插件连接上后，入站消息转发给本地 agent
- 插件断开后，自动回退给 bot

入口：

- `GET /api/v1/remote-agent/ws`

详细协议见：

- `docs/remote-agent-plugin.md`

## 更详细的文档

- `docs/architecture.md`：整体架构与主链路
- `docs/configuration.md`：配置模型与 workspace 约定
- `docs/scheduler.md`：scheduler 的存储与触发语义
- `docs/remote-agent-plugin.md`：remote-agent 插件协议
- `docs/nginx-reverse-proxy.md`：反向代理示例
- `docs/dev-port-forward.md`：开发环境端口转发

## 开发与验证

后端测试：

```bash
go test ./...
```

前端构建：

```bash
cd web
npm install
npm run build
```

## 设计原则

- 平台层不塞业务逻辑
- 业务差异尽量通过 template / skill / hook 注入
- 一切会话差异尽量显式落在 workspace 与 `.session-setting.json`
- 优先保持最小、可解释、可验证的实现
