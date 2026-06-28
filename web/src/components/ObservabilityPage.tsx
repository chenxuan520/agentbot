import { useCallback, useEffect, useMemo, useState } from 'react'

import { ApiClient } from '../api'
import type { ObservabilityEvent, ObservabilityHealthItem, ObservabilitySnapshot } from '../types'

interface ObservabilityPageProps {
  api: ApiClient
}

const refreshIntervalMs = 15000

function formatTime(value: string): string {
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) {
    return value
  }
  return parsed.toLocaleString()
}

function formatUptime(startedAt: string, now: string): string {
  const start = new Date(startedAt).getTime()
  const end = new Date(now).getTime()
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) {
    return '-'
  }
  let seconds = Math.floor((end - start) / 1000)
  const days = Math.floor(seconds / 86400)
  seconds -= days * 86400
  const hours = Math.floor(seconds / 3600)
  seconds -= hours * 3600
  const minutes = Math.floor(seconds / 60)
  const parts: string[] = []
  if (days > 0) parts.push(`${days}d`)
  if (hours > 0) parts.push(`${hours}h`)
  parts.push(`${minutes}m`)
  return parts.join(' ')
}

function HealthBadge({ label, item }: { label: string; item: ObservabilityHealthItem }) {
  return (
    <div className={item.ok ? 'obs-health-card ok' : 'obs-health-card down'}>
      <div className="obs-health-head">
        <span className="eyebrow">{label}</span>
        <span className={item.ok ? 'obs-dot ok' : 'obs-dot down'} aria-hidden="true" />
      </div>
      <strong>{item.name || '-'}</strong>
      <div className="muted small">{item.ok ? 'healthy' : item.error || 'unhealthy'}</div>
    </div>
  )
}

function severityClass(severity: string): string {
  switch (severity) {
    case 'error':
      return 'obs-sev error'
    case 'warn':
      return 'obs-sev warn'
    default:
      return 'obs-sev info'
  }
}

export function ObservabilityPage({ api }: ObservabilityPageProps) {
  const [snapshot, setSnapshot] = useState<ObservabilitySnapshot | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [autoRefresh, setAutoRefresh] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.getObservability()
      setSnapshot(result)
      setError('')
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : '读取诊断信息失败')
    } finally {
      setLoading(false)
    }
  }, [api])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (!autoRefresh) {
      return
    }
    const timer = window.setInterval(() => {
      void load()
    }, refreshIntervalMs)
    return () => window.clearInterval(timer)
  }, [autoRefresh, load])

  const counters = useMemo(() => {
    if (!snapshot) {
      return [] as Array<{ key: string; value: number }>
    }
    return Object.entries(snapshot.counters)
      .map(([key, value]) => ({ key, value }))
      .sort((left, right) => right.value - left.value || left.key.localeCompare(right.key))
  }, [snapshot])

  const events: ObservabilityEvent[] = snapshot?.events ?? []

  return (
    <div className="obs-layout">
      <div className="sidebar-header">
        <div>
          <div className="eyebrow">Observability</div>
          <h2>诊断与失败</h2>
          <p className="muted small">汇总后端/IM 健康、失败计数与最近被记录的错误事件，避免静默失败。</p>
        </div>
        <div className="inline-actions">
          <label className="obs-auto-toggle muted small">
            <input type="checkbox" checked={autoRefresh} onChange={(event) => setAutoRefresh(event.target.checked)} />
            自动刷新
          </label>
          <button type="button" className="toolbar-button subtle" onClick={() => void load()} disabled={loading}>
            {loading ? '刷新中...' : '刷新'}
          </button>
        </div>
      </div>

      {error ? <div className="info-banner">{error}</div> : null}

      {snapshot ? (
        <>
          <div className="obs-health-grid">
            <HealthBadge label="Backend" item={snapshot.health.backend} />
            <HealthBadge label="Provider" item={snapshot.health.provider} />
            <div className="obs-health-card">
              <div className="obs-health-head">
                <span className="eyebrow">Daemon</span>
              </div>
              <strong>uptime {formatUptime(snapshot.startedAt, snapshot.now)}</strong>
              <div className="muted small">since {formatTime(snapshot.startedAt)}</div>
            </div>
          </div>

          <div className="settings-card">
            <div className="panel-title">失败计数（category / severity）</div>
            {counters.length === 0 ? (
              <div className="empty-state compact">还没有记录任何失败。</div>
            ) : (
              <div className="obs-counter-grid">
                {counters.map((counter) => (
                  <div key={counter.key} className="obs-counter">
                    <span className="obs-counter-value">{counter.value}</span>
                    <span className="muted small mono">{counter.key}</span>
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="settings-card">
            <div className="panel-title">最近事件（最新在前，最多 500 条）</div>
            {events.length === 0 ? (
              <div className="empty-state compact">没有最近事件。</div>
            ) : (
              <div className="obs-events">
                <table className="obs-table">
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>级别</th>
                      <th>类别</th>
                      <th>会话</th>
                      <th>摘要</th>
                    </tr>
                  </thead>
                  <tbody>
                    {events.map((event, index) => (
                      <tr key={`${event.time}-${index}`}>
                        <td className="mono small nowrap">{formatTime(event.time)}</td>
                        <td>
                          <span className={severityClass(event.severity)}>{event.severity}</span>
                        </td>
                        <td className="mono small">{event.category}</td>
                        <td className="mono small">{event.conversationId ? `${event.provider ?? ''}/${event.conversationId}` : '-'}</td>
                        <td>
                          <div>{event.summary}</div>
                          {event.detail ? <div className="muted small obs-detail">{event.detail}</div> : null}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      ) : loading ? (
        <div className="empty-state large">加载诊断信息中...</div>
      ) : (
        <div className="empty-state large">暂无诊断数据。</div>
      )}
    </div>
  )
}
