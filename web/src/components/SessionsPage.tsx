import { useEffect, useState } from 'react'

import { ApiClient } from '../api'
import { chatModeLabel, isSuspectedLeftGroup } from '../session-display'
import type { SessionRef, SessionSummary } from '../types'
import { SessionDetail } from './SessionDetail'

interface SessionsPageProps {
  api: ApiClient
}

export function SessionsPage({ api }: SessionsPageProps) {
  const [items, setItems] = useState<SessionSummary[]>([])
  const [displayInfo, setDisplayInfo] = useState<Record<string, { displayName?: string; chatMode?: string }>>({})
  const [query, setQuery] = useState('')
  const [selectedRef, setSelectedRef] = useState<SessionRef | null>(null)
  const [loading, setLoading] = useState(true)
  const [message, setMessage] = useState('')

  async function loadSessions() {
    setLoading(true)
    setMessage('')
    try {
      const nextItems = (await api.listSessions()).filter((item) => {
        return item.provider && item.provider !== 'undefined' && item.conversationId && item.conversationId !== 'undefined'
      })
      setItems(nextItems)
      void loadDisplayNames(nextItems)
      setSelectedRef((current) => {
        if (current && nextItems.some((item) => item.provider === current.provider && item.conversationId === current.conversationId)) {
          return current
        }
        if (nextItems.length === 0) {
          return null
        }
        return { provider: nextItems[0].provider, conversationId: nextItems[0].conversationId }
      })
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取 session 列表失败')
    } finally {
      setLoading(false)
    }
  }

  async function handleSessionDeleted() {
    await loadSessions()
  }

  function rememberDisplayInfo(ref: SessionRef, info: { displayName?: string; chatMode?: string }) {
    const nextDisplayName = info.displayName?.trim() ?? ''
    const nextChatMode = info.chatMode?.trim() ?? ''
    if (!nextDisplayName && !nextChatMode) {
      return
    }
    const key = `${ref.provider}:${ref.conversationId}`
    setDisplayInfo((current) => {
      const currentDisplayName = current[key]?.displayName?.trim() ?? ''
      const currentChatMode = current[key]?.chatMode?.trim() ?? ''
      if (currentDisplayName === nextDisplayName && currentChatMode === nextChatMode) {
        return current
      }
      return {
        ...current,
        [key]: {
          displayName: nextDisplayName,
          chatMode: nextChatMode,
        },
      }
    })
    setItems((current) =>
      current.map((item) =>
        item.provider === ref.provider && item.conversationId === ref.conversationId
          ? {
              ...item,
              displayName: nextDisplayName || item.displayName,
              chatMode: nextChatMode || item.chatMode,
            }
          : item,
      ),
    )
  }

  async function loadDisplayNames(nextItems: SessionSummary[]) {
    try {
      const resolved = await api.resolveSessionDisplayNames(
        nextItems.map((item) => ({ provider: item.provider, conversationId: item.conversationId })),
      )
      setDisplayInfo((current) => {
        const next = { ...current }
        for (const item of nextItems) {
          delete next[`${item.provider}:${item.conversationId}`]
        }
        for (const item of resolved) {
          const displayName = item.displayName?.trim() ?? ''
          const chatMode = item.chatMode?.trim() ?? ''
          if (displayName || chatMode) {
            next[`${item.provider}:${item.conversationId}`] = { displayName, chatMode }
          }
        }
        return next
      })
    } catch {
      setDisplayInfo((current) => {
        const next = { ...current }
        for (const item of nextItems) {
          delete next[`${item.provider}:${item.conversationId}`]
        }
        return next
      })
    }
  }

  useEffect(() => {
    void loadSessions()
  }, [])

  const needle = query.trim().toLowerCase()
  const filteredItems = !needle
    ? items
    : items.filter((item) => {
        return (
          (item.provider ?? '').toLowerCase().includes(needle) ||
          ((displayInfo[`${item.provider}:${item.conversationId}`]?.displayName?.trim() || item.displayName || '')).toLowerCase().includes(needle) ||
          (item.conversationId ?? '').toLowerCase().includes(needle) ||
          (item.template ?? '').toLowerCase().includes(needle) ||
          (item.replyMode ?? '').toLowerCase().includes(needle)
        )
      })

  return (
    <div className={selectedRef ? 'sessions-layout has-selection' : 'sessions-layout'}>
      <aside className="sessions-sidebar">
        <div className="sidebar-header">
          <div>
            <div className="eyebrow">Sessions</div>
            <h2>会话管理</h2>
            <p className="muted small">按群名、会话 ID 或 template 快速定位。</p>
          </div>
          <button type="button" className="toolbar-button subtle" onClick={() => void loadSessions()}>
            刷新
          </button>
        </div>
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索会话ID / 群名 / 用户名 / template" />
        <div className="sidebar-summary muted small">
          {filteredItems.length} / {items.length} sessions
        </div>
        {message ? <div className="error-banner">{message}</div> : null}
        {loading ? <div className="empty-state compact">加载列表中...</div> : null}
        {!loading && filteredItems.length === 0 ? <div className="empty-state compact">没有匹配的 session。</div> : null}
        <div className="session-list">
          {filteredItems.map((item) => {
            const active = selectedRef?.provider === item.provider && selectedRef?.conversationId === item.conversationId
            const resolvedInfo = displayInfo[`${item.provider}:${item.conversationId}`]
            const displayName = resolvedInfo?.displayName?.trim() || item.displayName || item.conversationId
            const chatMode = resolvedInfo?.chatMode ?? item.chatMode ?? ''
            const suspectedLeft = isSuspectedLeftGroup(resolvedInfo)
            const providerSummary = [item.provider, item.template, chatModeLabel(chatMode)].filter(Boolean).join(' · ')
            return (
              <button
                key={`${item.provider}:${item.conversationId}`}
                type="button"
                className={active ? 'session-list-item active' : 'session-list-item'}
                onClick={() => setSelectedRef({ provider: item.provider, conversationId: item.conversationId })}
              >
                <div className="session-list-head">
                  <div
                    className={suspectedLeft ? 'session-list-title session-list-title-left' : 'session-list-title'}
                    title={suspectedLeft ? `${displayName}（已退群）` : displayName}
                  >
                    {displayName}
                  </div>
                  <span className="meta-chip slim">{item.replyMode || '-'}</span>
                </div>
                <div className="session-list-meta">{providerSummary}</div>
                <div className="session-list-meta">last active: {item.lastMessageAt ? new Date(item.lastMessageAt).toLocaleString() : '-'}</div>
              </button>
            )
          })}
        </div>
      </aside>

      <section className="sessions-content">
        {selectedRef ? (
          <SessionDetail
            api={api}
            sessionRef={selectedRef}
            summary={items.find((item) => item.provider === selectedRef.provider && item.conversationId === selectedRef.conversationId)}
            scope="project"
            onBack={() => setSelectedRef(null)}
            onDisplayInfoResolved={rememberDisplayInfo}
            onSessionChanged={loadSessions}
            onSessionDeleted={handleSessionDeleted}
          />
        ) : (
          <div className="empty-state large">选择一个 session 查看详情。</div>
        )}
      </section>
    </div>
  )
}
