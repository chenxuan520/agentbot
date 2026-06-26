# Agent Bot Repo Guide

## Scope

- This repository is the platform control layer, not a business-skill repository.
- It connects the IM provider, per-conversation workspace, session lifecycle, scheduler, and agent backend into one message-processing pipeline.

## Current Supported Runtime

- IM provider: `feishu`
- Agent backend: `opencode serve`
- Workspace: one stable directory per `provider + conversation_id`
- Scheduler: one-time jobs, cron jobs, polling, trigger, and replay into the normal flow

## Key Constraints

- Keep the `opencode` integration on `opencode serve` REST APIs; do not reintroduce `opencode run`.
- `.session-setting.json` is the session-owned editable config. Runtime state belongs in sqlite or `.agents/runtime/`.
- Private memory stays inside the current workspace under `.agents/memory/`.
- Rebuilds must preserve session-owned state such as memory, hooks, session skills, and runtime artifacts.
- Prefer the smallest correct change. Do not add product-specific templates, skills, secrets, or environment-specific URLs.

## Common Paths

- `cmd/agent-bot/`: CLI and daemon entry
- `internal/config/`: global config loading
- `internal/workspace/`: workspace generation and rebuild
- `internal/session/`: session lifecycle and TTL rotation
- `internal/flow/`: main text-message processing chain
- `internal/gateway/feishu/`: Feishu inbound listener and parser
- `internal/scheduler/`: scheduled job store, polling, and handlers
- `web/`: React + Vite admin console
- `templates/default/`: default session template
- `agents/skills/`: platform-level reusable skills
- `agents/subagents/`: platform-level subagent definitions

## Run And Restart

- `./scripts/restart-opencode.sh`: starts `opencode serve` on `127.0.0.1:4096`
- `./scripts/restart-agent-bot.sh`: builds and starts the daemon on `127.0.0.1:8080`
- `./scripts/restart-web.sh`: starts the web console on `127.0.0.1:4173`
- `./scripts/watch-opencode.sh start`: starts the `opencode` watchdog
- `./scripts/watch-agent-bot.sh start`: starts the `agent-bot` watchdog

## Docs Discipline

- Update `README.md` when CLI behavior, config fields, or workspace layout changes.
- Keep detailed design and operations docs in `docs/`.
