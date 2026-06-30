import Editor from '@monaco-editor/react'
import { ChangeEvent, useEffect, useMemo, useState } from 'react'

import { ApiClient } from '../api'
import { showSuccessToast } from '../toast'
import type { SessionRef, SkillDetail, SkillSummary, WorkspaceFileItem } from '../types'
import { ConfirmDialog } from './ConfirmDialog'

interface SessionSkillsPanelProps {
  api: ApiClient
  sessionRef: SessionRef
}

interface FileTreeNode {
  name: string
  path: string
  kind: 'directory' | 'file'
  children: FileTreeNode[]
  file?: WorkspaceFileItem
}

function collectDirectoryPaths(node: FileTreeNode): string[] {
  if (node.kind !== 'directory') {
    return []
  }
  return [node.path, ...node.children.flatMap((child) => collectDirectoryPaths(child))]
}

function filterTree(node: FileTreeNode, query: string): FileTreeNode | null {
  const normalizedQuery = query.trim().toLowerCase()
  if (!normalizedQuery) {
    return node
  }
  const selfMatches = node.name.toLowerCase().includes(normalizedQuery) || node.path.toLowerCase().includes(normalizedQuery)
  if (node.kind === 'file') {
    return selfMatches ? node : null
  }
  if (selfMatches) {
    return node
  }
  const children = node.children
    .map((child) => filterTree(child, normalizedQuery))
    .filter((child): child is FileTreeNode => child !== null)
  if (children.length === 0) {
    return null
  }
  return {
    ...node,
    children,
  }
}

function languageForPath(path: string): string {
  if (path.endsWith('.py')) {
    return 'python'
  }
  if (path.endsWith('.json') || path.endsWith('.jsonc')) {
    return 'json'
  }
  if (path.endsWith('.yaml') || path.endsWith('.yml')) {
    return 'yaml'
  }
  if (path.endsWith('.sh')) {
    return 'shell'
  }
  if (path.endsWith('.ts')) {
    return 'typescript'
  }
  if (path.endsWith('.js')) {
    return 'javascript'
  }
  return 'markdown'
}

function buildFileTree(rootName: string, files: WorkspaceFileItem[]): FileTreeNode {
  const root: FileTreeNode = {
    name: rootName,
    path: rootName,
    kind: 'directory',
    children: [],
  }

  for (const file of files) {
    const parts = file.path.split('/').filter(Boolean)
    if (parts.length === 0) {
      continue
    }

    let current = root
    let currentPath = ''
    for (let index = 0; index < parts.length; index += 1) {
      const part = parts[index]
      const isLeaf = index === parts.length - 1
      currentPath = currentPath ? `${currentPath}/${part}` : part
      let child = current.children.find((item) => item.name === part && item.kind === (isLeaf ? 'file' : 'directory'))
      if (!child) {
        child = {
          name: part,
          path: currentPath,
          kind: isLeaf ? 'file' : 'directory',
          children: [],
        }
        current.children.push(child)
      }
      if (isLeaf) {
        child.file = file
      }
      current = child
    }
  }

  const sortNode = (node: FileTreeNode) => {
    node.children.sort((left, right) => {
      if (left.kind !== right.kind) {
        return left.kind === 'directory' ? -1 : 1
      }
      return left.name.localeCompare(right.name)
    })
    node.children.forEach(sortNode)
  }
  sortNode(root)
  return root
}

function pickDefaultSkillFilePath(files: WorkspaceFileItem[]): string {
  return files.find((item) => item.path === 'SKILL.md')?.path ?? files.find((item) => item.exists)?.path ?? files[0]?.path ?? ''
}

function normalizeRelativeFilePathInput(value: string): string | null {
  const parts: string[] = []
  for (const part of value.trim().split('/')) {
    if (!part || part === '.') {
      continue
    }
    if (part === '..') {
      if (parts.length === 0) {
        return null
      }
      parts.pop()
      continue
    }
    parts.push(part)
  }
  return parts.length > 0 ? parts.join('/') : null
}

export function SessionSkillsPanel({ api, sessionRef }: SessionSkillsPanelProps) {
  const [items, setItems] = useState<SkillSummary[]>([])
  const [selectedSkillID, setSelectedSkillID] = useState('')
  const [detail, setDetail] = useState<SkillDetail | null>(null)
  const [newSkillID, setNewSkillID] = useState('')
  const [files, setFiles] = useState<WorkspaceFileItem[]>([])
  const [selectedPath, setSelectedPath] = useState('')
  const [expandedDirectories, setExpandedDirectories] = useState<string[]>([])
  const [fileQuery, setFileQuery] = useState('')
  const [content, setContent] = useState('')
  const [loading, setLoading] = useState(true)
  const [detailLoading, setDetailLoading] = useState(false)
  const [contentLoading, setContentLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [creating, setCreating] = useState(false)
  const [creatingFile, setCreatingFile] = useState(false)
  const [deletingFile, setDeletingFile] = useState(false)
  const [newFilePath, setNewFilePath] = useState('')
  const [showDeleteFileConfirm, setShowDeleteFileConfirm] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [message, setMessage] = useState('')
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)

  const tree = useMemo(() => buildFileTree(detail?.id ?? 'skill', files), [detail?.id, files])
  const normalizedFileQuery = fileQuery.trim().toLowerCase()
  const filteredTree = useMemo(() => filterTree(tree, normalizedFileQuery), [tree, normalizedFileQuery])
  const allDirectoryPaths = useMemo(() => collectDirectoryPaths(tree), [tree])

  async function loadSessionSkills(preferredSkillID?: string) {
    setLoading(true)
    setMessage('')
    try {
      const nextItems = await api.listSessionSkills(sessionRef)
      setItems(nextItems)
      setSelectedSkillID((current) => {
        if (preferredSkillID && nextItems.some((item) => item.id === preferredSkillID)) {
          return preferredSkillID
        }
        if (current && nextItems.some((item) => item.id === current)) {
          return current
        }
        return nextItems[0]?.id ?? ''
      })
    } catch (error) {
      setItems([])
      setSelectedSkillID('')
      setMessage(error instanceof Error ? error.message : '读取会话私有 skill 失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadSessionSkills()
  }, [api, sessionRef.conversationId, sessionRef.provider])

  async function handleCreate() {
    const nextID = newSkillID.trim()
    if (!nextID) {
      setMessage('请先输入 skill id。')
      return
    }
    setCreating(true)
    setMessage('')
    try {
      const created = await api.createSessionSkill(sessionRef, nextID, '')
      setNewSkillID('')
      await loadSessionSkills(created.id)
      showSuccessToast(`已创建会话私有 skill: ${created.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '创建会话私有 skill 失败')
    } finally {
      setCreating(false)
    }
  }

  async function handleUpload(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) {
      return
    }
    setUploading(true)
    setMessage('')
    try {
      const uploaded = await api.uploadSessionSkill(sessionRef, file)
      await loadSessionSkills(uploaded.id)
      showSuccessToast(`已导入会话私有 skill: ${uploaded.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '导入会话私有 skill 失败')
    } finally {
      setUploading(false)
    }
  }

  useEffect(() => {
    if (!selectedSkillID) {
      setDetail(null)
      setFiles([])
      setSelectedPath('')
      setExpandedDirectories([])
      setFileQuery('')
      setNewFilePath('')
      setContent('')
      setDirty(false)
      return
    }
    let cancelled = false
    async function loadDetail() {
      setDetailLoading(true)
      try {
        const [nextDetail, nextFiles] = await Promise.all([
          api.getSessionSkillDetail(sessionRef, selectedSkillID),
          api.listSessionSkillFiles(sessionRef, selectedSkillID),
        ])
        if (cancelled) {
          return
        }
        setDetail(nextDetail)
        setFiles(nextFiles)
        setExpandedDirectories([])
        setFileQuery('')
        setNewFilePath('')
        setSelectedPath((current) => {
          if (current && nextFiles.some((item) => item.path === current)) {
            return current
          }
          return pickDefaultSkillFilePath(nextFiles)
        })
        setDirty(false)
      } catch (error) {
        if (!cancelled) {
          setMessage(error instanceof Error ? error.message : '读取会话私有 skill 详情失败')
          setDetail(null)
          setFiles([])
          setSelectedPath('')
          setContent('')
          setDirty(false)
        }
      } finally {
        if (!cancelled) {
          setDetailLoading(false)
        }
      }
    }
    void loadDetail()
    return () => {
      cancelled = true
    }
  }, [api, selectedSkillID, sessionRef])

  useEffect(() => {
    const nextPath = pickDefaultSkillFilePath(files)
    if (selectedPath && files.some((item) => item.path === selectedPath)) {
      return
    }
    if (nextPath !== selectedPath) {
      setSelectedPath(nextPath)
    }
  }, [files, selectedPath])

  useEffect(() => {
    if (!selectedSkillID || !selectedPath) {
      setContent('')
      setDirty(false)
      return
    }
    let cancelled = false
    async function loadContent() {
      setContentLoading(true)
      try {
        const result = await api.getSessionSkillFileContent(sessionRef, selectedSkillID, selectedPath)
        if (cancelled) {
          return
        }
        setContent(result.content)
        setDirty(false)
      } catch (error) {
        if (!cancelled) {
          setMessage(error instanceof Error ? error.message : '读取会话私有 skill 文件失败')
          setContent('')
        }
      } finally {
        if (!cancelled) {
          setContentLoading(false)
        }
      }
    }
    void loadContent()
    return () => {
      cancelled = true
    }
  }, [api, selectedPath, selectedSkillID, sessionRef])

  async function handleSave() {
    if (!detail || !selectedPath) {
      return
    }
    setSaving(true)
    setMessage('')
    try {
      await api.updateSessionSkillFileContent(sessionRef, detail.id, selectedPath, content)
      const nextDetail = await api.getSessionSkillDetail(sessionRef, detail.id)
      const nextFiles = await api.listSessionSkillFiles(sessionRef, detail.id)
      setDetail(nextDetail)
      setFiles(nextFiles)
      setDirty(false)
      showSuccessToast(`已保存 ${selectedPath}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '保存会话私有 skill 文件失败')
    } finally {
      setSaving(false)
    }
  }

  async function handleDeleteSkill() {
    if (!detail) {
      return
    }
    setDeleting(true)
    setMessage('')
    try {
      const deletedID = detail.id
      await api.deleteSessionSkill(sessionRef, deletedID)
      setDetail(null)
      setFiles([])
      setSelectedPath('')
      setContent('')
      setDirty(false)
      await loadSessionSkills()
      showSuccessToast(`已删除会话私有 skill: ${deletedID}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '删除会话私有 skill 失败')
    } finally {
      setDeleting(false)
    }
  }

  async function handleDeleteConfirm() {
    await handleDeleteSkill()
    setShowDeleteConfirm(false)
  }

  async function handleCreateFile() {
    if (!detail) {
      return
    }
    const rawPath = newFilePath.trim()
    if (!rawPath) {
      setMessage('请输入新文件的相对路径。')
      return
    }
    if (rawPath.startsWith('/')) {
      setMessage('请使用相对路径，例如 scripts/run.sh。')
      return
    }
    const path = normalizeRelativeFilePathInput(rawPath)
    if (!path) {
      setMessage('请输入合法的相对文件路径，例如 scripts/run.sh。')
      return
    }
    if (files.some((item) => item.path === path)) {
      setMessage(`文件 ${path} 已存在。`)
      return
    }
    setCreatingFile(true)
    setMessage('')
    try {
      await api.createSessionSkillFile(sessionRef, detail.id, path, '')
      const nextFiles = await api.listSessionSkillFiles(sessionRef, detail.id)
      setFiles(nextFiles)
      setSelectedPath(path)
      setNewFilePath('')
      showSuccessToast(`已创建 ${path}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '新建文件失败')
    } finally {
      setCreatingFile(false)
    }
  }

  async function handleDeleteFile() {
    if (!detail || !selectedPath || selectedPath === 'SKILL.md') {
      return
    }
    setDeletingFile(true)
    setMessage('')
    try {
      const removed = selectedPath
      await api.deleteSessionSkillFile(sessionRef, detail.id, removed)
      const nextFiles = await api.listSessionSkillFiles(sessionRef, detail.id)
      setFiles(nextFiles)
      setSelectedPath(pickDefaultSkillFilePath(nextFiles))
      showSuccessToast(`已删除 ${removed}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '删除文件失败')
    } finally {
      setDeletingFile(false)
    }
  }

  async function handleDeleteFileConfirm() {
    await handleDeleteFile()
    setShowDeleteFileConfirm(false)
  }

  function toggleDirectory(path: string) {
    setExpandedDirectories((current) => {
      if (current.includes(path)) {
        return current.filter((item) => item !== path)
      }
      return [...current, path]
    })
  }

  function expandAll() {
    setExpandedDirectories(allDirectoryPaths)
  }

  function collapseAll() {
    setExpandedDirectories([])
  }

  function renderTreeNode(node: FileTreeNode, depth: number, forceExpand = false): JSX.Element {
    if (node.kind === 'directory') {
      const expanded = forceExpand || expandedDirectories.includes(node.path)
      return (
        <div key={node.path} className="tree-node-group">
          <button
            type="button"
            className={expanded ? 'tree-directory-row tree-directory-button expanded' : 'tree-directory-row tree-directory-button'}
            style={{ paddingLeft: `${depth * 14 + 10}px` }}
            onClick={() => toggleDirectory(node.path)}
          >
            <span className="tree-expand-icon">{expanded ? '▾' : '▸'}</span>
            <span className="tree-kind dir">DIR</span>
            <span className="tree-label">{node.name}</span>
          </button>
          {expanded ? node.children.map((child) => renderTreeNode(child, depth + 1, forceExpand)) : null}
        </div>
      )
    }

    const active = node.path === selectedPath
    return (
      <button
        key={node.path}
        type="button"
        className={active ? 'tree-file-row active' : 'tree-file-row'}
        style={{ paddingLeft: `${depth * 14 + 10}px` }}
        onClick={() => setSelectedPath(node.path)}
      >
        <span className="tree-kind file">FILE</span>
        <span className="tree-file-main">
          <span className="tree-label">{node.name}</span>
          <span className="tree-meta">{node.file?.size ?? 0} B</span>
        </span>
      </button>
    )
  }

  return (
    <div className="session-skills-layout">
      <aside className="session-skills-sidebar">
        <div className="settings-card role-create-card">
          <div>
            <div className="panel-title">新建私有 Skill</div>
            <p className="muted small">创建或导入后会自动挂到当前 session，不需要写进 `.session-setting.json`。</p>
          </div>
          <label className="role-form-field">
            <span>Skill ID</span>
            <input value={newSkillID} onChange={(event) => setNewSkillID(event.target.value)} placeholder="例如 release-helper-local" />
          </label>
          <div className="inline-actions">
            <button type="button" onClick={() => void handleCreate()} disabled={creating || !newSkillID.trim()}>
              {creating ? '创建中...' : '创建 Skill'}
            </button>
            <label className={uploading ? 'upload-button disabled' : 'upload-button'}>
              <input type="file" accept=".zip,application/zip" onChange={handleUpload} disabled={uploading} />
              {uploading ? '导入中...' : '导入 ZIP'}
            </label>
          </div>
        </div>

        {message ? <div className="info-banner">{message}</div> : null}

        <div className="settings-card session-skills-list-card">
          <div className="settings-card-header">
            <div>
              <h3>会话私有 Skills</h3>
              <p className="muted small">这些 skill 只保存在当前 session 的 workspace 中。</p>
            </div>
            <button type="button" className="toolbar-button subtle" onClick={() => void loadSessionSkills()} disabled={loading}>
              {loading ? '刷新中...' : '刷新'}
            </button>
          </div>

          {loading ? <div className="empty-state compact">加载私有 skills 中...</div> : null}
          {!loading && items.length === 0 ? <div className="empty-state compact">当前还没有会话私有 skill。</div> : null}

          {!loading && items.length > 0 ? (
            <div className="skills-list">
              {items.map((item) => {
                const active = item.id === selectedSkillID
                return (
                  <button
                    key={item.id}
                    type="button"
                    className={active ? 'session-list-item active' : 'session-list-item'}
                    onClick={() => setSelectedSkillID(item.id)}
                  >
                    <div className="session-list-head">
                      <div className="session-list-title">{item.title}</div>
                      <span className="meta-chip slim">private</span>
                    </div>
                    <div className="session-list-meta mono">{item.id}</div>
                  </button>
                )
              })}
            </div>
          ) : null}
        </div>
      </aside>

      <section className="session-skills-main">
        {!selectedSkillID ? <div className="empty-state large">选择一个会话私有 skill 查看内容。</div> : null}
        {selectedSkillID && detailLoading ? <div className="empty-state large">读取会话私有 skill 详情中...</div> : null}
        {selectedSkillID && !detailLoading && detail ? (
          <div className="settings-card session-skills-detail-card">
            <div className="detail-header">
              <div>
                <div className="eyebrow">Session Skill</div>
                <h2>{detail.title}</h2>
                <div className="muted mono session-id-line">.agents/session-skills/{detail.id}</div>
                <div className="detail-meta">
                  <span className="meta-chip">{detail.id}</span>
                  <span className="meta-chip">updated {detail.updatedAt ? new Date(detail.updatedAt).toLocaleString() : '-'}</span>
                </div>
              </div>
              <button type="button" className="danger-button" onClick={() => setShowDeleteConfirm(true)} disabled={deleting}>
                {deleting ? '删除中...' : '删除 Skill'}
              </button>
            </div>

            <div className="file-editor-shell">
              <div className="file-list-panel">
                <div className="skill-files-toolbar">
                  <div className="tree-panel-toolbar">
                    <div className="panel-title">文件树</div>
                    <div className="inline-actions tree-panel-actions">
                      <span className="tree-action-buttons">
                        <button type="button" className="tree-action-button" onClick={expandAll} disabled={tree.children.length === 0}>
                          全部展开
                        </button>
                        <button type="button" className="tree-action-button" onClick={collapseAll} disabled={expandedDirectories.length === 0}>
                          全部折叠
                        </button>
                      </span>
                    </div>
                  </div>
                  <input value={fileQuery} onChange={(event) => setFileQuery(event.target.value)} placeholder="搜索文件名或路径" />
                  <div className="skill-newfile-row">
                    <input
                      value={newFilePath}
                      onChange={(event) => setNewFilePath(event.target.value)}
                      onKeyDown={(event) => {
                        if (event.key === 'Enter') {
                          event.preventDefault()
                          void handleCreateFile()
                        }
                      }}
                      placeholder="新建文件，如 scripts/run.sh"
                    />
                    <button
                      type="button"
                      className="toolbar-button subtle"
                      onClick={() => void handleCreateFile()}
                      disabled={creatingFile || !newFilePath.trim()}
                    >
                      {creatingFile ? '创建中...' : '新建文件'}
                    </button>
                  </div>
                </div>
                <div className="file-list">
                  {filteredTree && files.length > 0 ? (
                    <div className="file-tree">
                      <button
                        type="button"
                        className={normalizedFileQuery.length > 0 || expandedDirectories.includes(tree.path) ? 'tree-directory-row tree-directory-button root expanded' : 'tree-directory-row tree-directory-button root'}
                        onClick={() => toggleDirectory(tree.path)}
                      >
                        <span className="tree-expand-icon">{normalizedFileQuery.length > 0 || expandedDirectories.includes(tree.path) ? '▾' : '▸'}</span>
                        <span className="tree-kind dir">DIR</span>
                        <span className="tree-label">{tree.name}</span>
                      </button>
                      {normalizedFileQuery.length > 0 || expandedDirectories.includes(tree.path)
                        ? filteredTree.children.map((child) => renderTreeNode(child, 1, normalizedFileQuery.length > 0))
                        : null}
                    </div>
                  ) : files.length > 0 ? (
                    <div className="empty-state compact">没有命中的文件。</div>
                  ) : (
                    <div className="empty-state compact">当前 skill 没有可展示文件。</div>
                  )}
                </div>
              </div>

              <div className="editor-panel">
                <div className="editor-toolbar">
                  <div>
                    <div className="panel-title">{selectedPath || '未选择文件'}</div>
                    {dirty ? <div className="muted small">有未保存改动</div> : null}
                  </div>
                  <div className="inline-actions">
                    {selectedPath && selectedPath !== 'SKILL.md' ? (
                      <button
                        type="button"
                        className="danger-button"
                        onClick={() => setShowDeleteFileConfirm(true)}
                        disabled={deletingFile}
                      >
                        {deletingFile ? '删除中...' : '删除文件'}
                      </button>
                    ) : null}
                    <button type="button" onClick={() => void handleSave()} disabled={!selectedPath || saving || !dirty}>
                      {saving ? '保存中...' : '保存'}
                    </button>
                  </div>
                </div>

                {selectedPath ? (
                  <div className="monaco-panel">
                    {contentLoading ? (
                      <div className="empty-state">加载文件中...</div>
                    ) : (
                      <Editor
                        height="100%"
                        language={languageForPath(selectedPath)}
                        value={content}
                        onChange={(value) => {
                          setContent(value ?? '')
                          setDirty(true)
                        }}
                        theme="vs-dark"
                        options={{
                          minimap: { enabled: false },
                          fontSize: 13,
                          automaticLayout: true,
                          scrollBeyondLastLine: false,
                        }}
                      />
                    )}
                  </div>
                ) : (
                  <div className="empty-state">选择一个文件开始查看。</div>
                )}
              </div>
            </div>
          </div>
        ) : null}
      </section>

      <ConfirmDialog
        open={showDeleteConfirm && !!detail}
        title="删除会话私有 Skill"
        description={detail ? `确认删除会话私有 skill \`${detail.id}\` 吗？删除后只会影响当前 session。` : ''}
        confirmLabel="确认删除"
        loading={deleting}
        onCancel={() => setShowDeleteConfirm(false)}
        onConfirm={() => void handleDeleteConfirm()}
      />
      <ConfirmDialog
        open={showDeleteFileConfirm && !!detail && !!selectedPath}
        title="删除文件"
        description={selectedPath ? `确认删除文件 \`${selectedPath}\` 吗？` : ''}
        confirmLabel="确认删除"
        loading={deletingFile}
        onCancel={() => setShowDeleteFileConfirm(false)}
        onConfirm={() => void handleDeleteFileConfirm()}
      />
    </div>
  )
}
