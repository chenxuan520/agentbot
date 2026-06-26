---
name: session-settings
description: 查看或更新当前会话的 .session-setting.json。Use when the user asks to change session template, backend, agentsMode, replyMode, history TTL, group-message acceptance, interactive card handling, mounted skills, or mounted subagents.
---

# Session Settings

这个 skill 用来查看或更新当前会话的 `.session-setting.json`。

适用范围：

- `template`
- `agent.backend`
- `agent.agentsMode`
- `agent.opencodeConfig`
- `settings.replyMode`
- `settings.historyTTLHours`
- `settings.acceptGroupHumanMessagesWithoutMention`
- `settings.acceptOtherBotMessages`
- `settings.acceptInteractiveCardMessages`
- `mounts.skillIds`
- `mounts.subagentIds`

默认建议流程：

1. 先读取当前设置
2. 只修改用户明确要求的字段
3. 把完整 `settings` 文档写回
4. 保持 `rebuild=true`，让改动立即生效

## 先准备上下文

```bash
source .agents/runtime/context.env
```

## 读取当前设置

```bash
curl -sS "$AGENT_BOT_API_BASE_URL/api/v1/session/settings?provider=$AGENT_BOT_PROVIDER&conversationId=$AGENT_BOT_CONVERSATION_ID"
```

返回里最重要的是 `settings` 字段，它就是当前会话完整的 `.session-setting.json` 内容。

## 更新设置并立即生效

把修改后的完整 `settings` 文档原样写回：

```bash
curl -sS -X POST "$AGENT_BOT_API_BASE_URL/api/v1/session/settings" \
  -H 'Content-Type: application/json' \
  -d '{
    "provider": "'$AGENT_BOT_PROVIDER'",
    "conversationId": "'$AGENT_BOT_CONVERSATION_ID'",
    "settings": {
      "version": 1,
      "template": "default",
      "agent": {
        "backend": "opencode",
        "agentsMode": "template"
      },
      "settings": {
        "replyMode": "topic",
        "historyTTLHours": 24,
        "acceptGroupHumanMessagesWithoutMention": true,
        "acceptOtherBotMessages": true,
        "acceptInteractiveCardMessages": true
      },
      "mounts": {
        "skillIds": ["cron", "refuse", "session-settings"],
        "subagentIds": []
      }
    },
    "rebuild": true
  }'
```

说明：

- 这里是整份 `settings` 文档覆盖写回，不是局部 patch
- 不要丢掉用户没有要求修改的字段
- `agent.agentsMode=custom` 只表示当前 session 的 `AGENTS.md` 不再跟随 template；真正的提示词内容仍然需要改当前 workspace 的 `AGENTS.md`
- `rebuild=true` 会立即重建当前 workspace，并清空当前 active session；下一条普通消息会按新设置创建新 session
- 如果只想落盘、不立刻生效，可以传 `"rebuild": false`
