# Nginx Reverse Proxy

记录当前 `agent-bot` 的 nginx 反向代理方式，避免下次重装/迁移时忘记把 local-agent 的 WebSocket 路由配通。

这个文档对应的是前端仍由 Vite dev server（默认 `4173`）提供的场景。
如果你使用 GitHub Release 里的预编译包和 `web/dist/` 静态文件，请改看 `docs/release-deployment.md`。

## 目标

- 域名：`agent-bot.example.com`
- web 管理台：`http://127.0.0.1:4173`
- agent-bot 本地 API：`http://127.0.0.1:8080`

关键要求：

- `/` 继续走前端 `4173`
- `/api/` 必须**直接**走 `8080`
- `remote-agent` 的 `GET /api/v1/remote-agent/ws` 是 WebSocket，不能再经过 Vite dev server

如果把 `/api/` 也先转给 `4173`，普通 HTTP API 可能还能工作，但 `/api/v1/remote-agent/ws` 很容易在 websocket upgrade 时卡成 `504 Gateway Time-out`。

## 当前配置

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

`/etc/nginx/sites-available/default`：

```nginx
server {
    listen 80 default_server;
    listen [::]:80 default_server;

    server_name agent-bot.example.com _;

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
        proxy_pass http://127.0.0.1:4173;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
    }
}
```

## 应用配置

```bash
sudo nginx -t
sudo nginx -s reload
```

## 验证

普通 API：

```bash
curl -i http://agent-bot.example.com/api/v1/remote-agent/ws
```

未带 token 时，预期返回：

```text
HTTP/1.1 401 Unauthorized
{"error":"token is required"}
```

WebSocket upgrade 链路：

```bash
curl -i \
  -H "Connection: Upgrade" \
  -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" \
  -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
  http://agent-bot.example.com/api/v1/remote-agent/ws
```

未带 token 时，预期也应返回同样的 `401 token is required`。如果这里返回 `504 Gateway Time-out`，通常说明 `/api/` 还在经过 Vite，而不是直连 `8080`。

## 外部连接方式

当前 nginx 只监听 `80`，所以外部 local-agent 插件应使用：

```text
ws://agent-bot.example.com/api/v1/remote-agent/ws
```

推荐用 header 传 token：

```text
Authorization: Bearer <session_web_token>
```

如果以后补了 TLS，再切到：

```text
wss://agent-bot.example.com/api/v1/remote-agent/ws
```
