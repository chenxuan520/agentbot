import { useEffect, useState } from 'react'

import { ApiClient } from '../api'
import { showSuccessToast } from '../toast'
import type { RepoSummary } from '../types'

interface RepoLibraryPageProps {
  api: ApiClient
  canManage?: boolean
}

export function RepoLibraryPage({ api, canManage = true }: RepoLibraryPageProps) {
  const [items, setItems] = useState<RepoSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [message, setMessage] = useState('')
  const [cloneUrl, setCloneUrl] = useState('')
  const [cloneId, setCloneId] = useState('')
  const [cloning, setCloning] = useState(false)
  const [query, setQuery] = useState('')
  const [busyRepo, setBusyRepo] = useState('')
  const [branchCache, setBranchCache] = useState<Record<string, string[]>>({})
  const [branchSel, setBranchSel] = useState<Record<string, string>>({})

  const filteredItems = items.filter((item) => {
    const q = query.trim().toLowerCase()
    if (!q) return true
    return item.id.toLowerCase().includes(q) || (item.branch ?? '').toLowerCase().includes(q)
  })

  async function loadRepos() {
    setLoading(true)
    setMessage('')
    try {
      setItems(await api.listRepos())
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取 repo 列表失败')
      setItems([])
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadRepos()
  }, [])

  async function handleClone() {
    const url = cloneUrl.trim()
    if (!url) {
      setMessage('请先填写仓库地址。')
      return
    }
    setCloning(true)
    setMessage('')
    try {
      const created = await api.cloneRepo(url, cloneId.trim() || undefined)
      setCloneUrl('')
      setCloneId('')
      await loadRepos()
      showSuccessToast(`已 clone: ${created.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : 'clone 仓库失败')
    } finally {
      setCloning(false)
    }
  }

  async function ensureBranches(id: string) {
    if (branchCache[id]) {
      return
    }
    try {
      const data = await api.listRepoBranches(id)
      setBranchCache((prev) => ({ ...prev, [id]: data.branches }))
      setBranchSel((prev) => ({ ...prev, [id]: prev[id] ?? data.current }))
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取分支失败')
    }
  }

  async function handlePull(id: string) {
    setBusyRepo(id)
    setMessage('')
    try {
      const res = await api.pullRepo(id)
      const tail = res.output ? res.output.split('\n').filter(Boolean).slice(-1)[0] : ''
      await loadRepos()
      showSuccessToast(`已 pull ${id}（${res.branch}）${tail ? ': ' + tail : ''}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : 'pull 失败')
    } finally {
      setBusyRepo('')
    }
  }

  async function handleCheckout(id: string, fallbackBranch: string) {
    const branch = (branchSel[id] ?? fallbackBranch).trim()
    if (!branch) {
      setMessage('请先选择分支。')
      return
    }
    setBusyRepo(id)
    setMessage('')
    try {
      const res = await api.checkoutRepoBranch(id, branch)
      await loadRepos()
      showSuccessToast(`${id} 已切换到 ${res.branch}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '切换分支失败')
    } finally {
      setBusyRepo('')
    }
  }

  return (
    <div className="repo-library-page">
      <div className="settings-card">
        <div className="settings-card-header">
          <div>
            <div className="eyebrow">Repos</div>
            <h2>共享代码仓库</h2>
            <p className="muted">
              列出服务器 `agents/repos/` 下的 git 仓库。clone 进去后，可在某个 session 的 `Repos` 标签里勾选挂载；挂载会以软链接进 workspace 根目录，并被 `pull-workspace-repos.sh` 自动 `git pull` 兜底。
            </p>
          </div>
          <div className="inline-actions">
            <button type="button" className="toolbar-button subtle" onClick={() => void loadRepos()} disabled={loading}>
              {loading ? '刷新中...' : '刷新'}
            </button>
          </div>
        </div>

        {canManage ? (
          <div className="repo-add-hint repo-clone-form">
            <div className="panel-title">Clone 仓库到系统</div>
            <p className="muted small">
              把 git 仓库 clone 到系统维护的 `agents/repos/`。只支持 `https` / `http` / `ssh` / `git` 协议与 `user@host:path` scp 形式，会拦截 `ext::` / `file://` 等可执行命令的危险传输。
            </p>
            <label className="role-form-field">
              <span>仓库地址</span>
              <input
                value={cloneUrl}
                onChange={(event) => setCloneUrl(event.target.value)}
                placeholder="https://github.com/org/repo.git 或 git@host:org/repo.git"
                disabled={cloning}
              />
            </label>
            <label className="role-form-field">
              <span>Repo ID（可选，留空按地址自动取）</span>
              <input
                value={cloneId}
                onChange={(event) => setCloneId(event.target.value)}
                placeholder="例如 service-a；只能用字母、数字、- _ ."
                disabled={cloning}
              />
            </label>
            <button type="button" onClick={() => void handleClone()} disabled={cloning || !cloneUrl.trim()}>
              {cloning ? 'Clone 中...（大仓库可能要等几分钟）' : 'Clone'}
            </button>
            <p className="muted small">clone 落到 `agents/repos/&lt;id&gt;`；之后在某个 session 的 `Repos` 标签勾选挂载。多个 session 挂同一个 repo 会共享同一份工作区。</p>
          </div>
        ) : (
          <div className="repo-add-hint">
            <div className="panel-title">如何新增</div>
            <p className="muted small">在服务器上把仓库 clone 到 `agents/repos/`，目录名即 repo id：</p>
            <pre className="code-snippet mono">git clone &lt;repo-url&gt; agents/repos/&lt;id&gt;</pre>
            <p className="muted small">或复用已有 checkout（不再 clone 一份），软链进来即可：</p>
            <pre className="code-snippet mono">ln -s /abs/path/to/repo agents/repos/&lt;id&gt;</pre>
          </div>
        )}

        {message ? <div className="info-banner">{message}</div> : null}
        {loading ? <div className="empty-state compact">加载 repo 列表中...</div> : null}
        {!loading && items.length === 0 ? <div className="empty-state compact">`agents/repos/` 下还没有 repo。</div> : null}

        {!loading && items.length > 0 ? (
          <>
            <label className="role-form-field repo-search">
              <span>搜索</span>
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="按 repo id 或分支过滤"
              />
            </label>
            {filteredItems.length === 0 ? (
              <div className="empty-state compact">没有匹配 “{query}” 的 repo。</div>
            ) : (
              <div className="skills-list repo-list">
                {filteredItems.map((item) => {
                  const selected = branchSel[item.id] ?? item.branch
                  const options = branchCache[item.id] ?? [item.branch].filter(Boolean)
                  const busy = busyRepo === item.id
                  return (
                    <div key={item.id} className="session-list-item repo-list-item">
                      <div className="session-list-head">
                        <div className="session-list-title mono">{item.id}</div>
                        <span className="meta-chip slim">{item.hasGit ? (item.branch || 'git') : '非 git'}</span>
                      </div>
                      <div className="session-list-meta muted small">
                        {item.hasGit ? (item.branch ? `branch: ${item.branch}` : '已检测到 .git') : '目录下没有 .git，pull/切换会跳过'}
                      </div>
                      {canManage && item.hasGit ? (
                        <div className="repo-row-actions">
                          <button
                            type="button"
                            className="toolbar-button subtle"
                            onClick={() => void handlePull(item.id)}
                            disabled={busy}
                          >
                            {busy ? '处理中...' : 'Pull'}
                          </button>
                          <select
                            className="repo-branch-select mono"
                            value={selected}
                            disabled={busy}
                            onMouseDown={() => void ensureBranches(item.id)}
                            onFocus={() => void ensureBranches(item.id)}
                            onChange={(event) =>
                              setBranchSel((prev) => ({ ...prev, [item.id]: event.target.value }))
                            }
                          >
                            {options.map((branch) => (
                              <option key={branch} value={branch}>
                                {branch}
                              </option>
                            ))}
                          </select>
                          <button
                            type="button"
                            className="toolbar-button subtle"
                            onClick={() => void handleCheckout(item.id, item.branch)}
                            disabled={busy || selected === item.branch}
                          >
                            切换分支
                          </button>
                        </div>
                      ) : null}
                    </div>
                  )
                })}
              </div>
            )}
            {canManage ? (
              <p className="muted small">
                Pull = `git pull --ff-only`，切换分支 = `git checkout`；都直接作用在共享仓库上，会影响所有挂了它的 session（软链指向同一份工作区）。有未提交改动时 git 会拒绝、不会强制覆盖。
              </p>
            ) : null}
          </>
        ) : null}
      </div>
    </div>
  )
}
