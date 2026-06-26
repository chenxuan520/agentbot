import Editor from '@monaco-editor/react'
import { ChangeEvent, useEffect, useMemo, useState } from 'react'

import { ApiClient } from '../api'
import { showSuccessToast } from '../toast'
import type { SkillDetail, SkillSummary, WorkspaceFileItem } from '../types'
import { ConfirmDialog } from './ConfirmDialog'

interface SkillsLibraryPageProps {
  api: ApiClient
  canManage?: boolean
}

interface FileTreeNode {
  name: string
  path: string
  kind: 'directory' | 'file'
  children: FileTreeNode[]
  file?: WorkspaceFileItem
}

function isHiddenPath(path: string): boolean {
  return path
    .split('/')
    .filter(Boolean)
    .some((part) => part.startsWith('.'))
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

function formatSkillPath(path: string): string {
  const normalized = path.replace(/\\/g, '/')
  const marker = '/agents/skills/'
  const index = normalized.lastIndexOf(marker)
  if (index >= 0) {
    return normalized.slice(index + 1)
  }
  return normalized
}

function pickDefaultSkillFilePath(files: WorkspaceFileItem[]): string {
  return files.find((item) => item.path === 'SKILL.md')?.path ?? files.find((item) => item.exists)?.path ?? files[0]?.path ?? ''
}

export function SkillsLibraryPage({ api, canManage = true }: SkillsLibraryPageProps) {
  const [items, setItems] = useState<SkillSummary[]>([])
  const [selectedSkillID, setSelectedSkillID] = useState('')
  const [detail, setDetail] = useState<SkillDetail | null>(null)
  const [newSkillID, setNewSkillID] = useState('')
  const [files, setFiles] = useState<WorkspaceFileItem[]>([])
  const [selectedPath, setSelectedPath] = useState('')
  const [expandedDirectories, setExpandedDirectories] = useState<string[]>([])
  const [showHiddenFiles, setShowHiddenFiles] = useState(false)
  const [fileQuery, setFileQuery] = useState('')
  const [content, setContent] = useState('')
  const [loading, setLoading] = useState(true)
  const [detailLoading, setDetailLoading] = useState(false)
  const [contentLoading, setContentLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [creating, setCreating] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [message, setMessage] = useState('')

  const visibleFiles = useMemo(() => (showHiddenFiles ? files : files.filter((item) => !isHiddenPath(item.path))), [files, showHiddenFiles])
  const tree = useMemo(() => buildFileTree(detail?.id ?? 'skill', visibleFiles), [detail?.id, visibleFiles])
  const normalizedFileQuery = fileQuery.trim().toLowerCase()
  const filteredTree = useMemo(() => filterTree(tree, normalizedFileQuery), [tree, normalizedFileQuery])
  const allDirectoryPaths = useMemo(() => collectDirectoryPaths(tree), [tree])

  async function loadSkills(preferredSkillID?: string) {
    setLoading(true)
    setMessage('')
    try {
      const nextItems = await api.listSkills()
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
      setMessage(error instanceof Error ? error.message : '读取 skills 失败')
      setItems([])
      setSelectedSkillID('')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadSkills()
  }, [])

  async function handleCreate() {
    const nextID = newSkillID.trim()
    if (!nextID) {
      setMessage('请先输入 skill id。')
      return
    }
    setCreating(true)
    setMessage('')
    try {
      const created = await api.createSkill(nextID, '')
      setNewSkillID('')
      await loadSkills(created.id)
      showSuccessToast(`已创建 skill: ${created.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '创建 skill 失败')
    } finally {
      setCreating(false)
    }
  }

  useEffect(() => {
    if (!selectedSkillID) {
      setDetail(null)
      setFiles([])
      setSelectedPath('')
      setExpandedDirectories([])
      setFileQuery('')
      setContent('')
      setDirty(false)
      return
    }
    let cancelled = false
    async function loadSkillDetail() {
      setDetailLoading(true)
      try {
        const [nextDetail, nextFiles] = await Promise.all([api.getSkillDetail(selectedSkillID), api.listSkillFiles(selectedSkillID)])
        if (cancelled) {
          return
        }
        setDetail(nextDetail)
        setFiles(nextFiles)
        setExpandedDirectories([])
        setFileQuery('')
        const nextVisibleFiles = nextFiles.filter((item) => !isHiddenPath(item.path))
        setSelectedPath((current) => {
          if (current && nextVisibleFiles.some((item) => item.path === current)) {
            return current
          }
          return pickDefaultSkillFilePath(nextVisibleFiles)
        })
        setDirty(false)
      } catch (error) {
        if (!cancelled) {
          setMessage(error instanceof Error ? error.message : '读取 skill 详情失败')
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
    void loadSkillDetail()
    return () => {
      cancelled = true
    }
  }, [api, selectedSkillID])

  useEffect(() => {
    const nextPath = pickDefaultSkillFilePath(visibleFiles)
    if (selectedPath && visibleFiles.some((item) => item.path === selectedPath)) {
      return
    }
    if (nextPath !== selectedPath) {
      setSelectedPath(nextPath)
    }
  }, [selectedPath, visibleFiles])

  useEffect(() => {
    if (!selectedSkillID || !selectedPath) {
      setContent('')
      setDirty(false)
      return
    }
    let cancelled = false
    async function loadFileContent() {
      setContentLoading(true)
      try {
        const result = await api.getSkillFileContent(selectedSkillID, selectedPath)
        if (cancelled) {
          return
        }
        setContent(result.content)
        setDirty(false)
      } catch (error) {
        if (!cancelled) {
          setMessage(error instanceof Error ? error.message : '读取 skill 文件失败')
          setContent('')
        }
      } finally {
        if (!cancelled) {
          setContentLoading(false)
        }
      }
    }
    void loadFileContent()
    return () => {
      cancelled = true
    }
  }, [api, selectedPath, selectedSkillID])

  async function handleUpload(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) {
      return
    }
    setUploading(true)
    setMessage('')
    try {
      const uploaded = await api.uploadSkill(file)
      await loadSkills(uploaded.id)
      showSuccessToast(`已上传 skill: ${uploaded.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '上传 skill 失败')
    } finally {
      setUploading(false)
    }
  }

  async function handleSave() {
    if (!detail || !selectedPath || detail.readOnly || !canManage) {
      return
    }
    setSaving(true)
    setMessage('')
    try {
      await api.updateSkillFileContent(detail.id, selectedPath, content)
      const nextDetail = await api.getSkillDetail(detail.id)
      const nextFiles = await api.listSkillFiles(detail.id)
      setDetail(nextDetail)
      setFiles(nextFiles)
      setDirty(false)
      showSuccessToast(`已保存 ${selectedPath}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '保存 skill 文件失败')
    } finally {
      setSaving(false)
    }
  }

  async function handleDeleteSkill() {
    if (!detail || detail.readOnly || !canManage) {
      return
    }
    setDeleting(true)
    setMessage('')
    try {
      const deletedID = detail.id
      await api.deleteSkill(deletedID)
      setDetail(null)
      setFiles([])
      setSelectedPath('')
      setContent('')
      setDirty(false)
      await loadSkills()
      showSuccessToast(`已删除 skill: ${deletedID}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '删除 skill 失败')
    } finally {
      setDeleting(false)
    }
  }

  async function handleDeleteConfirm() {
    await handleDeleteSkill()
    setShowDeleteConfirm(false)
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

  const readOnly = !canManage || detail?.readOnly === true

  return (
    <div className="skills-browser-layout">
      <aside className="skills-sidebar">
        <div className="sidebar-header">
          <div>
            <div className="eyebrow">Public Skills</div>
            <h2>公共 Skill 仓库</h2>
            <p className="muted small">浏览和编辑 `agents/skills/` 下的公共 skill 仓库。</p>
          </div>
          <div className="inline-actions">
            <button type="button" className="toolbar-button subtle" onClick={() => void loadSkills()} disabled={loading}>
              {loading ? '刷新中...' : '刷新'}
            </button>
            {canManage ? (
              <label className={uploading ? 'upload-button disabled' : 'upload-button'}>
                <input type="file" accept=".zip,application/zip" onChange={handleUpload} disabled={uploading} />
                {uploading ? '上传中...' : '上传 ZIP'}
              </label>
            ) : null}
          </div>
        </div>
        <div className="sidebar-summary muted small">{items.length} skills</div>
        {!canManage ? <div className="warning-banner">当前 token 为只读权限，可浏览公共 skill，但不能上传、编辑或删除。</div> : null}
        {message ? <div className="info-banner">{message}</div> : null}

          {canManage ? (
            <div className="settings-card role-create-card">
              <div>
                <div className="panel-title">新建 Skill</div>
                <p className="muted small">新建后会生成一个目录和默认 `SKILL.md`，再在右侧继续编辑。</p>
              </div>

              <label className="role-form-field">
                <span>Skill ID</span>
                <input value={newSkillID} onChange={(event) => setNewSkillID(event.target.value)} placeholder="例如 release-helper" />
              </label>

              <button type="button" onClick={() => void handleCreate()} disabled={creating || !newSkillID.trim()}>
                {creating ? '创建中...' : '创建 Skill'}
              </button>
            </div>
          ) : null}

          <div className="settings-card skills-list-shell">
            <div className="settings-card-header">
              <div>
                <h3>Skills</h3>
                <p className="muted small">按 `agents/skills/` 浏览公共 skill 仓库。</p>
              </div>
            </div>

            {loading ? <div className="empty-state compact">加载 skills 中...</div> : null}
            {!loading && items.length === 0 ? <div className="empty-state compact">当前还没有公共 skill。</div> : null}

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
                        <span className="meta-chip slim">{item.hasSkillFile ? 'SKILL.md' : 'files'}</span>
                      </div>
                      <div className="session-list-meta mono">{item.id}</div>
                    </button>
                  )
                })}
              </div>
            ) : null}
          </div>
      </aside>

      <section className="skills-main">
          {!selectedSkillID ? <div className="empty-state large">选择一个 skill 查看内容。</div> : null}
          {selectedSkillID && detailLoading ? <div className="empty-state large">读取 skill 详情中...</div> : null}
          {selectedSkillID && !detailLoading && detail ? (
            <div className="detail-shell skill-detail-shell">
              <div className="detail-header">
                <div>
                  <div className="eyebrow">Skill Detail</div>
                  <h2>{detail.title}</h2>
                  <div className="muted mono session-id-line">{formatSkillPath(detail.path)}</div>
                  <div className="detail-meta">
                    <span className="meta-chip">{detail.id}</span>
                    <span className="meta-chip">updated {detail.updatedAt ? new Date(detail.updatedAt).toLocaleString() : '-'}</span>
                    {readOnly ? <span className="meta-chip">read only</span> : null}
                  </div>
                </div>

                <div className="inline-actions">
                  {!readOnly ? (
                    <button type="button" className="danger-button" onClick={() => setShowDeleteConfirm(true)} disabled={deleting}>
                      {deleting ? '删除中...' : '删除 Skill'}
                    </button>
                  ) : null}
                </div>
              </div>

              <div className="file-editor-shell">
                  <div className="file-list-panel">
                    <div className="skill-files-toolbar">
                      <div className="tree-panel-toolbar">
                        <div className="panel-title">文件树</div>
                        <div className="inline-actions tree-panel-actions">
                          <label className="tree-toolbar-toggle" title="显示隐藏文件">
                            <span className="muted small">隐藏文件</span>
                            <span className={showHiddenFiles ? 'toggle-switch compact active' : 'toggle-switch compact'}>
                              <input type="checkbox" checked={showHiddenFiles} onChange={() => setShowHiddenFiles((current) => !current)} />
                              <span className="toggle-knob" />
                            </span>
                          </label>
                          <button type="button" className="tree-action-button" onClick={expandAll} disabled={tree.children.length === 0}>
                            全部展开
                          </button>
                          <button type="button" className="tree-action-button" onClick={collapseAll} disabled={expandedDirectories.length === 0}>
                            全部折叠
                          </button>
                        </div>
                      </div>
                      <input value={fileQuery} onChange={(event) => setFileQuery(event.target.value)} placeholder="搜索文件名或路径" />
                    </div>
                    <div className="file-list">
                      {filteredTree && visibleFiles.length > 0 ? (
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
                      ) : visibleFiles.length > 0 ? (
                        <div className="empty-state compact">没有命中的文件。</div>
                      ) : files.length > 0 ? (
                        <div className="empty-state compact">当前只剩隐藏文件。点击“显示隐藏文件”后可查看。</div>
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
                    {!readOnly ? (
                      <button type="button" onClick={() => void handleSave()} disabled={!selectedPath || saving || !dirty}>
                        {saving ? '保存中...' : '保存'}
                      </button>
                    ) : null}
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
                            readOnly,
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
        title="删除 Skill"
        description={detail ? `确认删除 skill \`${detail.id}\` 吗？` : ''}
        confirmLabel="确认删除"
        loading={deleting}
        onCancel={() => setShowDeleteConfirm(false)}
        onConfirm={() => void handleDeleteConfirm()}
      />
    </div>
  )
}
