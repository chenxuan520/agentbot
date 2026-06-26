import Editor from '@monaco-editor/react'
import { useEffect, useMemo, useState } from 'react'

import { ApiClient } from '../api'
import { showSuccessToast } from '../toast'
import type { WorkspaceFileItem } from '../types'

interface ScriptsPageProps {
  api: ApiClient
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

function pickDefaultScriptPath(files: WorkspaceFileItem[]): string {
  return files.find((item) => item.exists)?.path ?? files[0]?.path ?? ''
}

function languageForPath(path: string): string {
  if (path.endsWith('.py')) {
    return 'python'
  }
  if (path.endsWith('.sh')) {
    return 'shell'
  }
  if (path.endsWith('.json') || path.endsWith('.jsonc')) {
    return 'json'
  }
  if (path.endsWith('.yaml') || path.endsWith('.yml')) {
    return 'yaml'
  }
  if (path.endsWith('.ts')) {
    return 'typescript'
  }
  if (path.endsWith('.js')) {
    return 'javascript'
  }
  return 'markdown'
}

function buildFileTree(files: WorkspaceFileItem[]): FileTreeNode {
  const root: FileTreeNode = {
    name: 'scripts',
    path: 'scripts',
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

export function ScriptsPage({ api }: ScriptsPageProps) {
  const [files, setFiles] = useState<WorkspaceFileItem[]>([])
  const [selectedPath, setSelectedPath] = useState('')
  const [expandedDirectories, setExpandedDirectories] = useState<string[]>([])
  const [fileQuery, setFileQuery] = useState('')
  const [content, setContent] = useState('')
  const [loading, setLoading] = useState(true)
  const [contentLoading, setContentLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [message, setMessage] = useState('')

  const tree = useMemo(() => buildFileTree(files), [files])
  const normalizedFileQuery = fileQuery.trim().toLowerCase()
  const filteredTree = useMemo(() => filterTree(tree, normalizedFileQuery), [tree, normalizedFileQuery])
  const allDirectoryPaths = useMemo(() => collectDirectoryPaths(tree), [tree])

  async function loadScripts(preferredPath?: string) {
    setLoading(true)
    setMessage('')
    try {
      const nextFiles = await api.listScripts()
      setFiles(nextFiles)
      setExpandedDirectories([])
      setSelectedPath((current) => {
        if (preferredPath && nextFiles.some((item) => item.path === preferredPath)) {
          return preferredPath
        }
        if (current && nextFiles.some((item) => item.path === current)) {
          return current
        }
        return pickDefaultScriptPath(nextFiles)
      })
    } catch (error) {
      setFiles([])
      setSelectedPath('')
      setMessage(error instanceof Error ? error.message : '读取 scripts 失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadScripts()
  }, [api])

  useEffect(() => {
    const nextPath = pickDefaultScriptPath(files)
    if (selectedPath && files.some((item) => item.path === selectedPath)) {
      return
    }
    if (nextPath !== selectedPath) {
      setSelectedPath(nextPath)
    }
  }, [files, selectedPath])

  useEffect(() => {
    if (!selectedPath) {
      setContent('')
      setDirty(false)
      return
    }
    let cancelled = false
    async function loadContent() {
      setContentLoading(true)
      try {
        const result = await api.getScriptContent(selectedPath)
        if (cancelled) {
          return
        }
        setContent(result.content)
        setDirty(false)
      } catch (error) {
        if (!cancelled) {
          setMessage(error instanceof Error ? error.message : '读取脚本内容失败')
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
  }, [api, selectedPath])

  async function handleSave() {
    if (!selectedPath) {
      return
    }
    setSaving(true)
    setMessage('')
    try {
      await api.updateScriptContent(selectedPath, content)
      setDirty(false)
      await loadScripts(selectedPath)
      showSuccessToast(`已保存 ${selectedPath}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '保存脚本失败')
    } finally {
      setSaving(false)
    }
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

  const pathParts = selectedPath.split('/').filter(Boolean)
  const headerTitle = pathParts[pathParts.length - 1] ?? '未选择脚本'

  return (
    <div className="scripts-layout">
      <aside className="scripts-sidebar">
        <div className="sidebar-header">
          <div>
            <div className="eyebrow">Scripts</div>
            <h2>系统脚本</h2>
            <p className="muted small">编辑仓库根 `scripts/` 下的系统级脚本，并支持保存。</p>
          </div>
          <button type="button" className="toolbar-button subtle" onClick={() => void loadScripts()} disabled={loading}>
            {loading ? '刷新中...' : '刷新'}
          </button>
        </div>

        <div className="sidebar-summary muted small">{files.length} script files</div>
        {message ? <div className="info-banner">{message}</div> : null}

        <div className="settings-card scripts-list-shell">
          <div className="tree-panel-toolbar">
            <div>
              <div className="panel-title">脚本文件</div>
              <div className="muted small">支持编辑和保存 `.py` / `.sh` 等脚本文件。</div>
            </div>
            <div className="inline-actions tree-panel-actions">
              <button type="button" className="tree-action-button" onClick={expandAll} disabled={tree.children.length === 0}>
                全部展开
              </button>
              <button type="button" className="tree-action-button" onClick={collapseAll} disabled={expandedDirectories.length === 0}>
                全部折叠
              </button>
            </div>
          </div>
          <input value={fileQuery} onChange={(event) => setFileQuery(event.target.value)} placeholder="搜索文件名或路径" />
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
              <div className="empty-state compact">没有命中的脚本文件。</div>
            ) : loading ? (
              <div className="empty-state compact">加载脚本中...</div>
            ) : (
              <div className="empty-state compact">当前没有可编辑脚本。</div>
            )}
          </div>
        </div>
      </aside>

      <section className="scripts-main">
        {!selectedPath ? <div className="empty-state large">选择一个脚本查看或编辑。</div> : null}
        {selectedPath ? (
          <div className="detail-shell script-detail-shell">
            <div className="detail-header">
              <div>
                <div className="eyebrow">Script Detail</div>
                <h2>{headerTitle}</h2>
                <div className="muted mono session-id-line">scripts/{selectedPath}</div>
              </div>
              <button type="button" onClick={() => void handleSave()} disabled={saving || contentLoading || !dirty}>
                {saving ? '保存中...' : '保存'}
              </button>
            </div>

            {dirty ? <div className="muted small">有未保存改动。</div> : null}

            <div className="monaco-panel role-prompt-panel">
              {contentLoading ? (
                <div className="empty-state">加载脚本中...</div>
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
                    wordWrap: 'on',
                  }}
                />
              )}
            </div>
          </div>
        ) : null}
      </section>
    </div>
  )
}
