#!/usr/bin/env python3

"""Reaction-local hook entry.

平台会在收到表情事件后优先执行这个脚本。

stdin 会收到一段 JSON，例如：
 {
   "provider": "feishu",
   "conversationId": "...",
   "conversationType": "group",
   "messageType": "reaction",
   "messageId": "om_xxx",
   "rootMessageId": "om_root_xxx",
   "parentMessageId": "om_parent_xxx",
   "threadId": "omt_xxx",
   "senderType": "user",
   "senderId": "ou_xxx",
   "eventAction": "created",
   "reactionEmoji": "DONE",
   "text": "",
   "attachments": []
  }

stdout 留空表示忽略这次 reaction，不唤醒 opencode。

stdout 也可以输出 JSON：
- {"drop": true}
  显式忽略，不再继续处理。
- {"drop": true, "reactionEmoji": "EYES"}
  忽略，并给原消息补一个 reaction。
- {"drop": true, "replyText": "..."}
  忽略，并直接回一条文本。
- {"text": "改写后的文本"}
  把这次 reaction 改写成一条普通文本，再继续唤醒 opencode。
- {"text": "改写后的文本", "systemText": "给模型的 system 提示"}
  改写成普通文本并额外指定 system 提示。
"""


def main() -> int:
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
