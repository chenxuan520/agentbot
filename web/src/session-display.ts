export function chatModeLabel(chatMode?: string): string {
  const normalized = chatMode?.trim().toLowerCase() ?? ''
  if (!normalized) {
    return ''
  }
  if (normalized === 'p2p') {
    return '私聊'
  }
  return '群聊'
}
