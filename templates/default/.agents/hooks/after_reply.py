#!/usr/bin/env python3

"""Chat-local outgoing hook.

平台会在 opencode 产出回复后、真正发给 IM 前执行这个脚本。

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
  "text": "用户原始输入",
  "systemText": "给这条消息的 system 提示",
  "replyText": "opencode 原始回复"
}

stdout 留空表示不改动，继续按原回复发送。

stdout 也可以输出 JSON：
- {"replyText": "改写后的回复"}
- {"mentionUserId": "ou_xxx"}
- {"mentionUserIds": ["ou_a", "ou_b"]}
- {"replyText": "改写后的回复", "mentionUserId": "ou_xxx"}
- {"replyText": "改写后的回复", "mentionUserIds": ["ou_a", "ou_b"]}

运行时环境变量会额外注入：
- `AGENT_BOT_WORKSPACE`
- `AGENT_BOT_API_BASE_URL`
- `AGENT_BOT_PROVIDER`
- `AGENT_BOT_CONVERSATION_ID`
- `AGENT_BOT_ROOT_MESSAGE_ID`
- `AGENT_BOT_PARENT_MESSAGE_ID`
- `AGENT_BOT_THREAD_ID`

如果 hook 需要查当前群成员，可以请求：
- `GET $AGENT_BOT_API_BASE_URL/api/v1/provider/chat-members?provider=$AGENT_BOT_PROVIDER&conversationId=$AGENT_BOT_CONVERSATION_ID`
"""


def main() -> int:
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
