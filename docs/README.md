# Docs Index

- `architecture.md`: 平台整体设计、关键链路、workspace/session/scheduler 运行模型
- `configuration.md`: 全局配置、会话配置、workspace 生成物、`opencode.json` merge 规则
- `release-deployment.md`: GitHub Release 预编译包的部署方式，包含 `web/dist` 的静态部署和 `/api/` 反代示例
- `nginx-reverse-proxy.md`: 开发态 / Vite dev server 场景的 nginx 反代方式，包含 `/api/` 直连 `8080` 和 remote-agent WebSocket 配置
- `remote-agent-plugin.md`: 本地 agent 插件接入协议（WS 接口、鉴权、消息格式、生命周期、参考实现）——给插件实现者的完整规范
- `scheduler.md`: 定时任务持久化、cron、timezone、prompt 文件目录与触发模型
- `dev-port-forward.md`: 把远端调试端口（如 Vite `4173`）转到本机浏览器；为什么不走 jump、`target` ssh 别名、排障 checklist
`README.md` 保留启动和快速使用说明；更细的设计/配置说明统一放在这个目录下维护。
