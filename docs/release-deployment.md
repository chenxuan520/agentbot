# Release Deployment

这个文档对应 GitHub Release 里的预编译包，不是开发态的 Vite dev server 部署。

## Release 包包含什么

每个 release 资产都会按系统/架构分别打包，包含这些内容：

- 预编译 `agent-bot` 二进制（Windows 为 `agent-bot.exe`）
- 已构建好的 `web/dist/` 静态前端
- `templates/`
- `agents/`（包含公共 `skills/`、`subagents/`、占位 `memory/`，以及空的 `repos/`）
- `scripts/`（当前只放适合预编译包的辅助脚本：`restart-opencode.sh`、`watch-opencode.sh`、`pull-workspace-repos.sh`）
- `docs/`
- `README.md`
- `agent-bot.example.json`
- 预建空目录：`agents/repos/`、`data/chats/`、`data/run/`

注意：release 包**不会**包含你的运行时状态，例如 `data/state.sqlite3`、会话工作区、日志、已 clone 的 repo。

补充：release 包不会再带那些依赖源码树重新 `go build`、或依赖 Vite dev server 的仓库脚本。预编译包的标准启动入口仍然是 `./agent-bot run`。

## 1. 解压并准备配置

选择与你机器匹配的 release 资产，解压后进入解压目录：

```bash
cp agent-bot.example.json agent-bot.json
```

至少补齐这些字段：

- `auth.projectToken`
- `auth.secret`
- `providers.feishu.appID`
- `providers.feishu.appSecret`
- `backends.opencode.baseURL`
- `web.baseURL`

`agent-bot` 默认把**当前工作目录**当作根目录；如果你想从别的目录启动，可以额外设置：

```bash
export AGENT_BOT_ROOT=/abs/path/to/agent-bot-release
```

## 2. 启动 opencode serve

release 包不内置 `opencode` 可执行文件，仍然要求目标机器已安装 `opencode`。

示例：

```bash
opencode serve --hostname 127.0.0.1 --port 4096
```

## 3. 启动 agent-bot

Linux / macOS：

```bash
./agent-bot run
```

Windows PowerShell：

```powershell
.\agent-bot.exe run
```

## 4. 提供 web/dist 并把 /api 反代到 8080

release 包里的前端已经编成 `web/dist/`。它默认使用同源 `/api/v1/...`，所以部署时应让：

- `/` 提供 `web/dist/`
- `/api/` 直接转发到 `agent-bot` 的 `8080`
- `GET /api/v1/remote-agent/ws` 也必须直达 `8080`

一个可直接落地的 nginx 例子：

`/etc/nginx/nginx.conf`：

```nginx
http {
    map $http_upgrade $connection_upgrade {
        default upgrade;
        '' close;
    }

    include /etc/nginx/conf.d/*.conf;
    include /etc/nginx/sites-enabled/*;
}
```

站点配置：

```nginx
server {
    listen 80;
    listen [::]:80;

    server_name agent-bot.example.com;
    root /opt/agent-bot/web/dist;
    index index.html;

    location ^~ /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 75s;
        proxy_send_timeout 75s;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

如果你的 nginx 已经在 `http` 级别定义过 `$connection_upgrade`，复用现有配置即可。

## 5. 验证

后端健康检查：

```bash
curl -sS http://127.0.0.1:8080/health
```

反代后的 API：

```bash
curl -i http://agent-bot.example.com/api/v1/remote-agent/ws
```

未带 token 时，预期返回 `401 Unauthorized`，而不是 `404` 或 `504`。

## 相关文档

- `README.md`：快速启动和配置总览
- `docs/configuration.md`：配置字段与 workspace 约定
- `docs/remote-agent-plugin.md`：local-agent WebSocket 协议
- `docs/nginx-reverse-proxy.md`：开发态 / Vite dev server 场景下的反代示例
