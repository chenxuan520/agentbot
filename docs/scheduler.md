# Scheduler

## 能力范围

当前 scheduler 支持两类任务：

- 一次性任务：指定 `runAt`
- 周期任务：指定 `cron` + `timezone`

创建约束：

- 一次性任务的 `runAt` 不能早于当前时间；过去时间会在创建时被拒绝，不会被当成立即触发任务
- `cron` 周期任务不会直接存用户传入的某个历史时刻，而是按当前时间计算下一次未来触发时间

当前常见 payload：

- `promptText`: 到点后续跑 agent
- `promptFile`: 到点后从文件读取 prompt，再续跑 agent
- `notifyText`: 到点后只回投普通提醒

## 持久化

任务元数据持久化在：

- `data/state.sqlite3`
- 表：`scheduled_jobs`

说明：

- `payload` 在 sqlite 里是 JSON 字符串
- 一次性任务可以继续直接把 `promptText` 留在 payload 里
- 周期性 cron 任务如果带 prompt，会强制物化成 prompt 文件，job payload 里保存 `promptFile`

周期任务 prompt 文件目录固定在：

```text
data/scheduler/prompts/<provider>/<conversation-id>/
```

## 时区

周期任务必须显式带 `timezone`。

例如：

- `cron: "0 8 * * *"`
- `timezone: "Asia/Shanghai"`

平台不会偷用机器本地时区来解释 cron 表达式。

首次触发时间和后续每次 `next runAt` 都按这个 `timezone` 计算，再统一转换成 UTC 存库。

## 执行模型

worker 每轮会：

1. 查询 `pending` 且 `run_at <= now` 的任务
2. 先标成 `running`
3. 执行 handler
4. 一次性任务：标成 `done`
5. 周期任务：计算下一次触发时间，写回 `run_at`，状态重新置回 `pending`

因此：

- 进程重启后，`pending` 任务不会丢
- 对已经落库的 `pending` 任务，如果服务停机错过了触发时间，会在下一轮补跑
- 周期任务使用同一个 job id 循环 reschedule，不会每次新建一条新记录
- 一次性任务执行完成后会保留在 sqlite 里并标成 `done`，默认列表接口不会把这些历史任务返回给 agent
- 新创建的一次性任务不会允许直接写成过去时间，所以不会通过“建一个过去时间任务”来触发立即执行

## 当前边界

- 如果进程在 job 被标成 `running` 后崩掉，这条任务会卡在 `running`
- 现在还没有自动把历史 `running` 任务恢复为 `pending` 的机制
- `triggered-jobs.jsonl` 只是触发日志，不是任务主存储

## API 形态

一次性：

```json
{
  "provider": "feishu",
  "conversationId": "oc_xxx",
  "runAt": "2026-05-06T10:00:00+08:00",
  "route": "reminder.follow_up",
  "payload": {
    "promptText": "15分钟到了，请检查这条待办是否已经处理完成，并把结论回复到当前会话"
  }
}
```

周期：

```json
{
  "provider": "feishu",
  "conversationId": "oc_xxx",
  "cron": "0 8 * * *",
  "timezone": "Asia/Shanghai",
  "route": "report.daily_summary",
  "payload": {
    "promptText": "每天早上8点汇总昨天遗留事项，并给出摘要"
  }
}
```

`runAt` 和 `cron` 二选一，不能同时传。

补充：

- `runAt` 必须是合法 RFC3339 时间串，并且不能早于当前时间
- `cron` 的首次触发时间总是按创建当下的 `now` 和给定 `timezone` 往后计算

## 修改已有任务内容

现在也支持修改一个已存在任务的提示词或提醒文本：

```json
{
  "jobId": "job_xxx",
  "kind": "prompt",
  "content": "新的提示词内容"
}
```

说明：

- `kind` 当前支持 `prompt` / `notify`
- 这是“改已有内容”，不是把 `prompt` 和 `notify` 互相转换
- 如果原任务是 cron，并且 prompt 已经被物化成 `promptFile`，平台会直接改写该文件内容
