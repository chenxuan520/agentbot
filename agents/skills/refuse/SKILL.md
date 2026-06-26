---
name: refuse
description: 给当前会话设置临时拒绝唤醒或查看拒绝规则。Use when the user asks to mute, pause, block, refuse, silence, temporarily stop agent responses, or inspect the active refusal control for this conversation.
---

# Refuse

这个 skill 用来给当前会话设置一段时间的拒绝唤醒。

规则生效后，平台会在业务层直接拦截新入站消息，不再进入 agent，并给被拦截的消息加一个 `SHHH` reaction。

如果要提前解除，直接在当前聊天里发 `/unblock`。

## 先准备上下文

```bash
source .agents/runtime/context.env
```

## 创建规则

`untilAt` 必须明确给出屏蔽结束时间。

```bash
curl -sS -X POST "$AGENT_BOT_API_BASE_URL/api/v1/control/refuse" \
  -H 'Content-Type: application/json' \
  -d '{
    "provider": "'$AGENT_BOT_PROVIDER'",
    "conversationId": "'$AGENT_BOT_CONVERSATION_ID'",
    "untilAt": "2026-05-05T12:00:00+08:00",
    "reason": "大规模报警，先暂停 30 分钟"
  }'
```

## 查看规则

```bash
curl -sS "$AGENT_BOT_API_BASE_URL/api/v1/control/active?provider=$AGENT_BOT_PROVIDER&conversationId=$AGENT_BOT_CONVERSATION_ID"
```
