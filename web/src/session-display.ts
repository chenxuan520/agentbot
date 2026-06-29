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

// isSuspectedLeftGroup flags a session where Feishu still returns a chat name
// but no chat_mode. In practice that happens after the bot is removed from a
// group: GetChat (chat info) still works tenant-wide and yields the name, while
// chat_mode is no longer returned. It must be fed the live-resolved values, not
// cached/fallback ones, so a plain "resolve failed" (empty name) is not flagged.
export function isSuspectedLeftGroup(resolved?: { displayName?: string; chatMode?: string } | null): boolean {
  if (!resolved) {
    return false
  }
  const name = resolved.displayName?.trim() ?? ''
  const mode = resolved.chatMode?.trim() ?? ''
  return name !== '' && mode === ''
}
