import Editor from '@monaco-editor/react'
import { useEffect, useState } from 'react'

import { ApiClient } from '../api'
import { showSuccessToast } from '../toast'
import type { SubagentDetail, SubagentSummary } from '../types'
import { ConfirmDialog } from './ConfirmDialog'

interface SubagentsLibraryPageProps {
  api: ApiClient
  canManage?: boolean
}

export function SubagentsLibraryPage({ api, canManage = true }: SubagentsLibraryPageProps) {
  const [items, setItems] = useState<SubagentSummary[]>([])
  const [selectedSubagentID, setSelectedSubagentID] = useState('')
  const [detail, setDetail] = useState<SubagentDetail | null>(null)
  const [content, setContent] = useState('')
  const [newSubagentID, setNewSubagentID] = useState('')
  const [loading, setLoading] = useState(true)
  const [detailLoading, setDetailLoading] = useState(false)
  const [creating, setCreating] = useState(false)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [message, setMessage] = useState('')

  async function loadSubagents(preferredSubagentID?: string) {
    setLoading(true)
    setMessage('')
    try {
      const nextItems = await api.listSubagents()
      setItems(nextItems)
      setSelectedSubagentID((current) => {
        if (preferredSubagentID && nextItems.some((item) => item.id === preferredSubagentID)) {
          return preferredSubagentID
        }
        if (current && nextItems.some((item) => item.id === current)) {
          return current
        }
        return nextItems[0]?.id ?? ''
      })
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取 subagents 失败')
      setItems([])
      setSelectedSubagentID('')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadSubagents()
  }, [])

  useEffect(() => {
    if (!selectedSubagentID) {
      setDetail(null)
      setContent('')
      setDirty(false)
      return
    }
    let cancelled = false
    async function loadSubagentDetail() {
      setDetailLoading(true)
      try {
        const nextDetail = await api.getSubagentDetail(selectedSubagentID)
        if (cancelled) {
          return
        }
        setDetail(nextDetail)
        setContent(nextDetail.content)
        setDirty(false)
      } catch (error) {
        if (!cancelled) {
          setMessage(error instanceof Error ? error.message : '读取 subagent 详情失败')
          setDetail(null)
          setContent('')
          setDirty(false)
        }
      } finally {
        if (!cancelled) {
          setDetailLoading(false)
        }
      }
    }
    void loadSubagentDetail()
    return () => {
      cancelled = true
    }
  }, [api, selectedSubagentID])

  async function handleCreate() {
    const nextID = newSubagentID.trim()
    if (!nextID) {
      setMessage('请先输入 subagent id。')
      return
    }
    setCreating(true)
    setMessage('')
    try {
      const created = await api.createSubagent(nextID, '')
      setNewSubagentID('')
      await loadSubagents(created.id)
      showSuccessToast(`已创建 subagent: ${created.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '创建 subagent 失败')
    } finally {
      setCreating(false)
    }
  }

  async function handleSave() {
    if (!detail || !canManage || detail.readOnly) {
      return
    }
    setSaving(true)
    setMessage('')
    try {
      const updated = await api.updateSubagent(detail.id, content)
      setDetail(updated)
      setContent(updated.content)
      setDirty(false)
      await loadSubagents(updated.id)
      showSuccessToast(`已保存 subagent: ${updated.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '保存 subagent 失败')
    } finally {
      setSaving(false)
    }
  }

  async function handleDelete() {
    if (!detail || !canManage || detail.readOnly) {
      return
    }
    setDeleting(true)
    setMessage('')
    try {
      const deletedID = detail.id
      await api.deleteSubagent(deletedID)
      setDetail(null)
      setContent('')
      setDirty(false)
      await loadSubagents()
      showSuccessToast(`已删除 subagent: ${deletedID}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '删除 subagent 失败')
    } finally {
      setDeleting(false)
    }
  }

  async function handleDeleteConfirm() {
    await handleDelete()
    setShowDeleteConfirm(false)
  }

  const readOnly = !canManage || detail?.readOnly === true

  return (
    <div className="roles-layout">
      <aside className="roles-sidebar">
        <div className="sidebar-header">
          <div>
            <div className="eyebrow">Subagents</div>
            <h2>Subagent 管理</h2>
            <p className="muted small">浏览和编辑 `agents/subagents/` 下的平台级 subagent 定义，整体布局和 Sessions 保持一致。</p>
          </div>
          <button type="button" className="toolbar-button subtle" onClick={() => void loadSubagents()} disabled={loading}>
            {loading ? '刷新中...' : '刷新'}
          </button>
        </div>
        <div className="sidebar-summary muted small">{items.length} subagents</div>
        {!canManage ? <div className="warning-banner">当前 token 为只读权限，可浏览 subagent，但不能新建、编辑或删除。</div> : null}
        {message ? <div className="info-banner">{message}</div> : null}

          {canManage ? (
            <div className="settings-card role-create-card">
              <div>
                <div className="panel-title">新建 Subagent</div>
                <p className="muted small">新建后会生成一个 Markdown 文件，默认带 `mode: subagent` frontmatter。</p>
              </div>

              <label className="role-form-field">
                <span>Subagent ID</span>
                <input value={newSubagentID} onChange={(event) => setNewSubagentID(event.target.value)} placeholder="例如 code-helper" />
              </label>

              <button type="button" onClick={() => void handleCreate()} disabled={creating || !newSubagentID.trim()}>
                {creating ? '创建中...' : '创建 Subagent'}
              </button>
            </div>
          ) : null}

          <div className="settings-card roles-list-card">
            <div className="settings-card-header">
              <div>
                <h3>现有 Subagents</h3>
                <p className="muted small">选中后在右侧查看或编辑定义文件。</p>
              </div>
            </div>

            {loading ? <div className="empty-state compact">加载 subagents 中...</div> : null}
            {!loading && !items.length ? <div className="empty-state compact">当前还没有 subagent。</div> : null}

            {!loading && items.length > 0 ? (
              <div className="roles-list">
                {items.map((item) => {
                  const active = item.id === selectedSubagentID
                  return (
                    <button
                      key={item.id}
                      type="button"
                      className={active ? 'session-list-item active' : 'session-list-item'}
                      onClick={() => setSelectedSubagentID(item.id)}
                    >
                      <div className="session-list-head">
                        <div className="session-list-title">{item.title}</div>
                        {item.mode ? <span className="meta-chip slim">{item.mode}</span> : null}
                      </div>
                      <div className="session-list-meta mono">{item.id}</div>
                      {item.description ? <div className="session-list-meta">{item.description}</div> : null}
                    </button>
                  )
                })}
              </div>
            ) : null}
          </div>
      </aside>

      <section className="roles-main">
          {!selectedSubagentID ? <div className="empty-state large">选择一个 subagent 查看内容。</div> : null}
          {selectedSubagentID && detailLoading ? <div className="empty-state large">读取 subagent 详情中...</div> : null}
          {selectedSubagentID && !detailLoading && detail ? (
            <div className="detail-shell role-detail-shell">
              <div className="detail-header role-detail-header">
                <div>
                  <div className="eyebrow">Subagent Detail</div>
                  <h2>{detail.title}</h2>
                  <div className="muted mono session-id-line">{detail.path}</div>
                  <div className="detail-meta">
                    <span className="meta-chip">{detail.id}</span>
                    {detail.mode ? <span className="meta-chip">{detail.mode}</span> : null}
                    <span className="meta-chip">updated {detail.updatedAt ? new Date(detail.updatedAt).toLocaleString() : '-'}</span>
                    {readOnly ? <span className="meta-chip">read only</span> : null}
                  </div>
                </div>

                <div className="role-detail-actions">
                  {!readOnly ? (
                    <button type="button" onClick={() => void handleSave()} disabled={saving || !dirty}>
                      {saving ? '保存中...' : '保存'}
                    </button>
                  ) : null}
                  {!readOnly ? (
                    <button type="button" className="danger-button" onClick={() => setShowDeleteConfirm(true)} disabled={deleting}>
                      {deleting ? '删除中...' : '删除'}
                    </button>
                  ) : null}
                </div>
              </div>

              {dirty ? <div className="muted small">有未保存改动。</div> : null}
              {detail.description ? <div className="muted small">{detail.description}</div> : null}

              <div className="role-editor-card role-editor-single">
                <div className="editor-toolbar">
                  <div>
                    <div className="panel-title">{detail.id}.md</div>
                    <div className="muted small">Markdown 文件中可以继续维护 frontmatter 和正文内容。</div>
                  </div>
                </div>
                <div className="monaco-panel role-prompt-panel">
                  <Editor
                    height="100%"
                    language="markdown"
                    value={content}
                    onChange={(value) => {
                      setContent(value ?? '')
                      setDirty(true)
                    }}
                    theme="vs-dark"
                    options={{
                      readOnly,
                      minimap: { enabled: false },
                      fontSize: 13,
                      automaticLayout: true,
                      scrollBeyondLastLine: false,
                      wordWrap: 'on',
                    }}
                  />
                </div>
              </div>
            </div>
          ) : null}
      </section>
      <ConfirmDialog
        open={showDeleteConfirm && !!detail}
        title="删除 Subagent"
        description={detail ? `确认删除 subagent \`${detail.id}\` 吗？` : ''}
        confirmLabel="确认删除"
        loading={deleting}
        onCancel={() => setShowDeleteConfirm(false)}
        onConfirm={() => void handleDeleteConfirm()}
      />
    </div>
  )
}
