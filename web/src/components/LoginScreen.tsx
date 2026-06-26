import { FormEvent, ReactNode, useEffect, useState } from 'react'

interface LoginScreenProps {
  error: string
  loading: boolean
  onSubmit: (token: string) => void
}

// Replace these with your published docs / repo URLs when deploying the console.
const REPO_URL = 'https://github.com/chenxuan520/agentbot'
const DOC_URL = 'https://github.com/chenxuan520/agentbot#readme'
// One shared brand image lives in public/ so the same file backs the hero, the
// nav logo, and the favicon (index.html) — only one image asset in the repo.
const brandImageWebp = '/agent-bot-icon.webp'

const svgProps = {
  width: 20,
  height: 20,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.7,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
}

const IconGrid = (
  <svg {...svgProps}>
    <rect x="3" y="3" width="7" height="7" rx="1.6" />
    <rect x="14" y="3" width="7" height="7" rx="1.6" />
    <rect x="3" y="14" width="7" height="7" rx="1.6" />
    <rect x="14" y="14" width="7" height="7" rx="1.6" />
  </svg>
)

const IconRole = (
  <svg {...svgProps}>
    <circle cx="12" cy="8" r="3.4" />
    <path d="M5.5 20c0-3.6 2.9-6.5 6.5-6.5s6.5 2.9 6.5 6.5" />
  </svg>
)

const IconPrompt = (
  <svg {...svgProps}>
    <path d="M7 4h7l4 4v12H7z" />
    <path d="M14 4v4h4" />
    <path d="M9.5 13l-1.6 1.6L9.5 16" />
    <path d="M14.5 13l1.6 1.6L14.5 16" />
  </svg>
)

interface FeatureItem {
  icon: ReactNode
  title: string
  desc: string
}

const designPoints: FeatureItem[] = [
  {
    icon: IconGrid,
    title: '会话维度，而非 Bot 维度',
    desc: '每个私聊 / 群 / 话题都是独立 workspace，角色、记忆、配置互不共享。',
  },
  {
    icon: IconRole,
    title: 'Role 角色可复用',
    desc: 'AGENTS.md + 默认设置 + 默认技能打包成角色，一键套用，切角色仍保留记忆。',
  },
  {
    icon: IconPrompt,
    title: 'Prompt 完全透明',
    desc: '平台 / Role / 会话三层 Prompt 逐层叠加，每一层都能在后台看到并修改。',
  },
]

export function LoginScreen({ error, loading, onSubmit }: LoginScreenProps) {
  const [token, setToken] = useState('')
  const [showLogin, setShowLogin] = useState(Boolean(error))

  useEffect(() => {
    if (error) {
      setShowLogin(true)
    }
  }, [error])

  useEffect(() => {
    if (!showLogin) {
      return
    }
    function handleKey(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setShowLogin(false)
      }
    }
    window.addEventListener('keydown', handleKey)
    return () => window.removeEventListener('keydown', handleKey)
  }, [showLogin])

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    onSubmit(token)
  }

  return (
    <div className="landing">
      <header className="landing-nav">
        <a className="landing-brand" href="#top">
          <span className="landing-logo" aria-hidden="true">
			<img src={brandImageWebp} alt="" width={32} height={32} draggable={false} />
          </span>
          <span className="landing-wordmark">Agent Bot</span>
        </a>
        <nav className="landing-nav-links">
          <a className="landing-nav-link" href="#features">设计</a>
          <a className="landing-nav-link" href={DOC_URL} target="_blank" rel="noreferrer">
            指南
          </a>
          <button type="button" className="landing-nav-cta" onClick={() => setShowLogin(true)}>
            登录 →
          </button>
        </nav>
      </header>

      <main className="landing-main" id="top">
        <section className="hero">
          <div className="hero-mascot">
			<img src={brandImageWebp} alt="Agent Bot" width={160} height={160} draggable={false} />
          </div>
          <span className="hero-eyebrow mono">// AI AGENT × FEISHU</span>
          <h1 className="hero-title">
            把 AI Agent 接进飞书，
            <br />以<span className="hero-title-accent">「会话」</span>为单位组织一切
          </h1>
          <p className="hero-lead">
            飞书消息、独立会话工作区、Agent 后端与定时任务，串成一条稳定链路。每个私聊、群、话题都有自己的角色、记忆与配置，互不干扰。
          </p>
          <div className="hero-actions">
            <button type="button" className="btn-primary" onClick={() => setShowLogin(true)}>
              进入管理台
            </button>
            <a className="btn-ghost" href={DOC_URL} target="_blank" rel="noreferrer">
              使用指南 ↗
            </a>
          </div>
          <div className="hero-flowline mono" aria-hidden="true">
            <span>feishu</span>
            <i>▸</i>
            <span>internal/flow</span>
            <i>▸</i>
            <span>opencode&nbsp;serve</span>
          </div>
        </section>

        <section className="landing-section" id="features">
          <span className="section-label mono">// 核心设计</span>
          <div className="min-grid">
            {designPoints.map((item) => (
              <article key={item.title} className="min-card">
                <span className="min-icon">{item.icon}</span>
                <h3 className="min-title">{item.title}</h3>
                <p className="muted small">{item.desc}</p>
              </article>
            ))}
          </div>
        </section>
      </main>

      <footer className="landing-footer">
        <span className="muted small">Agent Bot · Feishu + opencode serve</span>
        <div className="landing-footer-links">
          <a className="landing-nav-link" href={DOC_URL} target="_blank" rel="noreferrer">
            指南
          </a>
          <a className="landing-nav-link" href={REPO_URL} target="_blank" rel="noreferrer">
            源码
          </a>
        </div>
      </footer>

      {showLogin ? (
        <div className="modal-backdrop" role="presentation" onClick={() => setShowLogin(false)}>
          <div
            className="modal-card login-modal"
            role="dialog"
            aria-modal="true"
            aria-label="管理台登录"
            onClick={(event) => event.stopPropagation()}
          >
            <div className="login-modal-head">
              <div>
                <div className="eyebrow mono">管理台登录</div>
                <h2 className="login-panel-title">Token 登录</h2>
              </div>
              <button type="button" className="modal-close" onClick={() => setShowLogin(false)} aria-label="关闭">
                ✕
              </button>
            </div>
            <p className="muted small">支持项目级 token 与 session 级 token；session token 登录后只看到自己那个会话。</p>
            <form onSubmit={handleSubmit} className="login-form">
              <label>
                <span>访问 Token</span>
                <textarea
                  value={token}
                  onChange={(event) => setToken(event.target.value)}
                  placeholder="粘贴 project token 或 session token"
                  rows={4}
                  autoFocus
                />
              </label>
              <button type="submit" className="login-submit" disabled={loading || token.trim() === ''}>
                {loading ? '登录中…' : '进入管理台'}
              </button>
            </form>
            <div className="login-token-hints">
              <div className="login-token-hint">
                <span className="meta-chip accent slim">project</span>
                <span className="muted small">浏览全部会话，管理 Role / Skill / Subagent。</span>
              </div>
              <div className="login-token-hint">
                <span className="meta-chip slim">session</span>
                <span className="muted small">只暴露当前会话，安全自助调试。</span>
              </div>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  )
}
