#!/usr/bin/env python3

"""Chat-local hook entry.

平台会在普通消息发给 opencode 之前执行这个脚本。

stdin 会收到一段 JSON，例如：
 {
   "provider": "feishu",
   "conversationId": "...",
   "conversationType": "group",
   "messageType": "text",
   "messageId": "om_xxx",
   "rootMessageId": "om_root_xxx",
   "parentMessageId": "om_parent_xxx",
   "threadId": "omt_xxx",
   "senderType": "user",
   "senderId": "ou_xxx",
   "text": "用户原始文本",
   "attachments": [{"mime": "image/png", "filename": "a.png", "url": "file:///..."}]
  }

stdout 留空表示不改动，继续按原消息处理。

stdout 也可以输出 JSON：
- {"drop": true}
  直接拦截，不再进入 opencode。
- {"drop": true, "reactionEmoji": "ENOUGH"}
  直接拦截，并给这条消息补一个 reaction。
- {"drop": true, "replyText": "..."}
  拦截，并回一条文本给当前聊天。
- {"text": "改写后的文本"}
  改写发给 opencode 的文本内容。
- {"text": "改写后的文本", "systemText": "给模型的 system 提示"}
  改写发给 opencode 的文本内容，并额外指定这条消息的 system 提示。

当前只支持改写 text，不支持改 attachments。

如果 messageType 是 `interactive`，那么 `text` 里会是卡片消息的原始 content JSON。

如果当前消息是在某个 topic / thread 下回复，`rootMessageId` / `parentMessageId` /
`threadId` 会把回复上下文一起带进来，hook 可以自己据此关联原始消息。
"""


def main() -> int:
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
