---
name: cron
description: 创建、查看、取消或更新当前会话的平台级定时任务。Use when the user asks to schedule a one-time reminder, create a cron job, modify scheduled prompt or notify text, list scheduled jobs, or cancel a job.
---

# Cron

这个 skill 用来给当前会话创建、查看或取消平台级定时任务。

## 先准备上下文

```bash
source .agents/runtime/context.env
```

## 创建任务

```bash
curl -sS -X POST "$AGENT_BOT_API_BASE_URL/api/v1/schedule" \
  -H 'Content-Type: application/json' \
  -d '{
    "provider": "'$AGENT_BOT_PROVIDER'",
    "conversationId": "'$AGENT_BOT_CONVERSATION_ID'",
    "runAt": "2026-05-06T10:00:00+08:00",
    "route": "reminder.follow_up",
    "payload": {
      "replyMessageID": "om_xxx",
      "promptText": "15分钟到了，请检查这条待办是否已经处理完成，并把结论回复到当前会话"
    }
  }'
```

规则：所有会发消息的 schedule payload 都必须显式带 `payload.replyMessageID`。

- 回原 topic / thread：填原消息 `message_id`
- 故意直接发群：显式填空串 `"replyMessageID": ""`
- 不允许省略这个字段；省略会被本地 API 拒绝

如果只想回投一条普通提醒，把 `payload.promptText` 改成 `payload.notifyText`。

一次性任务如果带 `promptText`，可以直接内联在 payload 里；不用额外落文件。

如果是 cron 周期任务，平台会自动把 prompt 落到专门的 prompt 文件目录里，再把 job payload 改成 `promptFile` 引用；不用手动管文件路径。

## 创建 cron 周期任务

```bash
curl -sS -X POST "$AGENT_BOT_API_BASE_URL/api/v1/schedule" \
  -H 'Content-Type: application/json' \
  -d '{
    "provider": "'$AGENT_BOT_PROVIDER'",
    "conversationId": "'$AGENT_BOT_CONVERSATION_ID'",
    "cron": "0 8 * * *",
    "timezone": "Asia/Shanghai",
    "route": "report.daily_summary",
    "payload": {
      "replyMessageID": "om_xxx",
      "promptText": "每天早上 8 点汇总昨天遗留事项，并把摘要回复到当前会话"
    }
  }'
```

说明：

- `cron` 和 `runAt` 二选一
- 周期任务必须显式带 `timezone`
- `timezone` 推荐用 IANA 名称，例如 `Asia/Shanghai`、`UTC`、`Europe/Berlin`

## 查看当前会话任务

这个列表默认只会返回当前 session 自己还在生效的任务（`pending` / `running`），不会看到其他会话的任务，也不会默认把一大堆 `done` 历史任务暴露出来。

```bash
curl -sS "$AGENT_BOT_API_BASE_URL/api/v1/schedule?provider=$AGENT_BOT_PROVIDER&conversationId=$AGENT_BOT_CONVERSATION_ID"
```

如果你确实需要看历史 `done` / `cancelled` 任务，再显式带：

```bash
curl -sS "$AGENT_BOT_API_BASE_URL/api/v1/schedule?provider=$AGENT_BOT_PROVIDER&conversationId=$AGENT_BOT_CONVERSATION_ID&includeDone=1"
```

## 取消任务

```bash
curl -sS -X POST "$AGENT_BOT_API_BASE_URL/api/v1/schedule/cancel" \
  -H 'Content-Type: application/json' \
  -d '{"jobId":"<job-id>"}'
```

## 修改已有任务提示词 / 提醒文本

如果一个任务已经建好了，只想改它当前保存的提示词或提醒文本，可以直接更新内容：

```bash
curl -sS -X PUT "$AGENT_BOT_API_BASE_URL/api/v1/schedule" \
  -H 'Content-Type: application/json' \
  -d '{
    "jobId": "<job-id>",
    "kind": "prompt",
    "content": "新的提示词内容"
  }'
```

说明：

- `kind=prompt`：修改 `promptText`，或修改已经物化到 `promptFile` 的 cron 提示词
- `kind=notify`：修改 `notifyText`
- 这里只改已有内容，不负责把 `prompt` / `notify` 两种类型互相切换
