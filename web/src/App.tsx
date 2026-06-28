import { useEffect, useRef, useState } from 'react'

import { ApiClient } from './api'
import { LoginScreen } from './components/LoginScreen'
import { ObservabilityPage } from './components/ObservabilityPage'
import { RepoLibraryPage } from './components/RepoLibraryPage'
import { RolesLibraryPage } from './components/RolesLibraryPage'
import { ScriptsPage } from './components/ScriptsPage'
import { SessionDetail } from './components/SessionDetail'
import { SessionsPage } from './components/SessionsPage'
import { SkillsLibraryPage } from './components/SkillsLibraryPage'
import { SubagentsLibraryPage } from './components/SubagentsLibraryPage'
import { showErrorToast, showSuccessToast } from './toast'
import type { MeResponse } from './types'

const storageKey = 'agent-bot-admin-token'
const pageQueryParam = 'page'

type PageKey = 'sessions' | 'roles' | 'skills' | 'repos' | 'scripts' | 'subagents' | 'observability'
type SessionPortalPageKey = 'session' | 'skills' | 'subagents'
const defaultProjectPage: PageKey = 'sessions'
const defaultSessionPortalPage: SessionPortalPageKey = 'session'

const projectNavItems: ReadonlyArray<{ key: PageKey; label: string }> = [
  { key: 'sessions', label: 'Sessions' },
  { key: 'roles', label: 'Roles' },
  { key: 'skills', label: 'Skills' },
  { key: 'repos', label: 'Repos' },
  { key: 'scripts', label: 'Scripts' },
  { key: 'subagents', label: 'Subagents' },
  { key: 'observability', label: '诊断' },
]

const sessionPortalNavItems: ReadonlyArray<{ key: SessionPortalPageKey; label: string }> = [
  { key: 'session', label: 'Session' },
  { key: 'skills', label: 'Skills' },
  { key: 'subagents', label: 'Subagents' },
]

const projectPageSet = new Set<PageKey>(projectNavItems.map((item) => item.key))
const sessionPortalPageSet = new Set<SessionPortalPageKey>(sessionPortalNavItems.map((item) => item.key))

interface SlidingNavProps {
  items: ReadonlyArray<{ key: string; label: string }>
  activeKey: string
  onChange: (key: string) => void
}

function SlidingNav({ items, activeKey, onChange }: SlidingNavProps) {
  const navRef = useRef<HTMLElement | null>(null)
  const buttonRefs = useRef<Record<string, HTMLButtonElement | null>>({})
  const [indicator, setIndicator] = useState<{ left: number; width: number } | null>(null)

  useEffect(() => {
    function syncIndicator() {
      const navElement = navRef.current
      const activeButton = buttonRefs.current[activeKey]
      if (!navElement || !activeButton) {
        setIndicator(null)
        return
      }
      setIndicator({ left: activeButton.offsetLeft, width: activeButton.offsetWidth })
    }

    syncIndicator()
    const frame = window.requestAnimationFrame(() => {
      syncIndicator()
      buttonRefs.current[activeKey]?.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'nearest' })
    })
    window.addEventListener('resize', syncIndicator)
    return () => {
      window.cancelAnimationFrame(frame)
      window.removeEventListener('resize', syncIndicator)
    }
  }, [activeKey, items])

  return (
    <nav ref={navRef} className="nav-links nav-links-inline sliding-nav">
      <span
        aria-hidden="true"
        className={`nav-active-pill${indicator ? ' visible' : ''}`}
        style={indicator ? { width: `${indicator.width}px`, transform: `translateX(${indicator.left}px)` } : undefined}
      />
      {items.map((item) => (
        <button
          key={item.key}
          ref={(element) => {
            buttonRefs.current[item.key] = element
          }}
          type="button"
          className={activeKey === item.key ? 'nav-link active' : 'nav-link'}
          onClick={() => onChange(item.key)}
        >
          {item.label}
        </button>
      ))}
    </nav>
  )
}

function loadStoredToken(): string {
  return localStorage.getItem(storageKey) ?? ''
}

function loadTokenFromQuery(): string {
  if (typeof window === 'undefined') {
    return ''
  }
  const value = new URLSearchParams(window.location.search).get('token') ?? ''
  return value.trim()
}

function loadPageFromQuery(): string {
  if (typeof window === 'undefined') {
    return ''
  }
  return (new URLSearchParams(window.location.search).get(pageQueryParam) ?? '').trim()
}

function normalizeProjectPage(value?: string | null): PageKey {
  const candidate = (value ?? '').trim()
  return projectPageSet.has(candidate as PageKey) ? (candidate as PageKey) : defaultProjectPage
}

function normalizeSessionPortalPage(value?: string | null): SessionPortalPageKey {
  const candidate = (value ?? '').trim()
  return sessionPortalPageSet.has(candidate as SessionPortalPageKey) ? (candidate as SessionPortalPageKey) : defaultSessionPortalPage
}

function writePageToQuery(page: string, defaultPage: string) {
  if (typeof window === 'undefined') {
    return
  }
  const url = new URL(window.location.href)
  if (!page || page === defaultPage) {
    url.searchParams.delete(pageQueryParam)
  } else {
    url.searchParams.set(pageQueryParam, page)
  }
  const nextURL = `${url.pathname}${url.search}${url.hash}`
  const currentURL = `${window.location.pathname}${window.location.search}${window.location.hash}`
  if (nextURL !== currentURL) {
    window.history.replaceState({}, '', nextURL)
  }
}

function clearTokenFromQuery() {
  if (typeof window === 'undefined') {
    return
  }
  const url = new URL(window.location.href)
  if (!url.searchParams.has('token')) {
    return
  }
  url.searchParams.delete('token')
  window.history.replaceState({}, '', `${url.pathname}${url.search}${url.hash}`)
}

export default function App() {
  const authIntentRef = useRef<'' | 'login'>('')
  const [token, setToken] = useState(() => loadTokenFromQuery() || loadStoredToken())
  const [api, setApi] = useState<ApiClient | null>(() => {
    const initialToken = loadTokenFromQuery() || loadStoredToken()
    return initialToken ? new ApiClient(initialToken) : null
  })
  const [me, setMe] = useState<MeResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [page, setPage] = useState<PageKey>(() => normalizeProjectPage(loadPageFromQuery()))
  const [sessionPage, setSessionPage] = useState<SessionPortalPageKey>(() => normalizeSessionPortalPage(loadPageFromQuery()))

  useEffect(() => {
    const queryToken = loadTokenFromQuery()
    if (!queryToken) {
      return
    }
    setToken((current) => {
      if (current === queryToken) {
        return current
      }
      return queryToken
    })
    localStorage.setItem(storageKey, queryToken)
    clearTokenFromQuery()
  }, [])

  useEffect(() => {
    if (!token) {
      setApi(null)
      setMe(null)
      return
    }
    setApi(new ApiClient(token))
  }, [token])

  useEffect(() => {
    if (!api) {
      return
    }
    const verifiedAPI = api
    let cancelled = false
    async function verify() {
      setLoading(true)
      setError('')
      try {
        const result = await verifiedAPI.getMe()
        if (!cancelled) {
          setMe(result)
          if (authIntentRef.current === 'login') {
            showSuccessToast(result.scope === 'session' ? '登录成功，已进入当前会话' : '登录成功')
            authIntentRef.current = ''
          }
        }
      } catch (nextError) {
        if (!cancelled) {
          const message = nextError instanceof Error ? nextError.message : '登录失败'
          setError(message)
          setMe(null)
          setToken('')
          localStorage.removeItem(storageKey)
          if (authIntentRef.current === 'login') {
            showErrorToast(message)
            authIntentRef.current = ''
          }
        }
      } finally {
        if (!cancelled) {
          setLoading(false)
        }
      }
    }
    void verify()
    return () => {
      cancelled = true
    }
  }, [api])

  useEffect(() => {
    if (!me || me.scope === 'session') {
      return
    }
    writePageToQuery(page, defaultProjectPage)
  }, [me, page])

  useEffect(() => {
    if (!me || me.scope !== 'session') {
      return
    }
    writePageToQuery(sessionPage, defaultSessionPortalPage)
  }, [me, sessionPage])

  function handleLogin(nextToken: string) {
    const trimmed = nextToken.trim()
    authIntentRef.current = 'login'
    setError('')
    setToken(trimmed)
    localStorage.setItem(storageKey, trimmed)
  }

  function handleLogout() {
    authIntentRef.current = ''
    setToken('')
    setMe(null)
    setPage(defaultProjectPage)
    setSessionPage(defaultSessionPortalPage)
    setError('')
    localStorage.removeItem(storageKey)
    showSuccessToast('已退出登录')
  }

  if (!token || !api) {
    return <LoginScreen error={error} loading={loading} onSubmit={handleLogin} />
  }

  if (loading || !me) {
    return <div className="loading-shell">验证 token 中...</div>
  }

  if (me.scope === 'session' && me.provider && me.conversationId) {
    return (
      <div className="single-session-shell">
        <header className="topbar">
          <div className="app-topbar-left session-topbar-left">
            <div>
              <div className="eyebrow">Session Portal</div>
              <strong>{me.conversationId}</strong>
              <div className="brand-subtitle">当前会话的配置、技能和调试入口</div>
            </div>
            <SlidingNav items={sessionPortalNavItems} activeKey={sessionPage} onChange={(key) => setSessionPage(key as SessionPortalPageKey)} />
          </div>
          <button type="button" onClick={handleLogout}>
            退出
          </button>
        </header>
        <main className="single-session-main">
          {sessionPage === 'session' ? <SessionDetail api={api} sessionRef={{ provider: me.provider, conversationId: me.conversationId }} scope="session" /> : null}
          {sessionPage === 'skills' ? <SkillsLibraryPage api={api} canManage={false} /> : null}
          {sessionPage === 'subagents' ? <SubagentsLibraryPage api={api} canManage={false} /> : null}
        </main>
      </div>
    )
  }

  return (
    <div className="app-shell">
      <header className="topbar app-topbar">
        <div className="app-topbar-left">
          <div className="brand-block">
			<div className="eyebrow">Agent Bot</div>
            <strong>控制台</strong>
            <div className="brand-subtitle">统一管理 sessions、roles、skills、scripts 和 subagents</div>
          </div>
          <SlidingNav items={projectNavItems} activeKey={page} onChange={(key) => setPage(key as PageKey)} />
        </div>
        <div className="app-topbar-right">
          <span className="meta-chip accent">project token</span>
          <button type="button" onClick={handleLogout} className="logout-button compact">
            退出
          </button>
        </div>
      </header>
      <main className="app-main">
        {page === 'sessions' ? <SessionsPage api={api} /> : null}
        {page === 'roles' ? <RolesLibraryPage api={api} /> : null}
        {page === 'skills' ? <SkillsLibraryPage api={api} canManage /> : null}
        {page === 'repos' ? <RepoLibraryPage api={api} /> : null}
        {page === 'scripts' ? <ScriptsPage api={api} /> : null}
        {page === 'subagents' ? <SubagentsLibraryPage api={api} canManage /> : null}
        {page === 'observability' ? <ObservabilityPage api={api} /> : null}
      </main>
    </div>
  )
}
