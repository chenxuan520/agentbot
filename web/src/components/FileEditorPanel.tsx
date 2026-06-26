import Editor from '@monaco-editor/react'
import { useEffect, useMemo, useState } from 'react'

import { ApiClient } from '../api'
import { showSuccessToast } from '../toast'
import type { SessionRef, WorkspaceFileItem } from '../types'

interface FileEditorPanelProps {
  api: ApiClient
  sessionRef: SessionRef
  kind: 'memory' | 'hooks'
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

function pickDefaultPath(files: WorkspaceFileItem[]): string {
  return files.find((item) => item.exists)?.path ?? files[0]?.path ?? ''
}

function languageForPath(path: string): string {
  if (path.endsWith('.py')) {
    return 'python'
  }
  if (path.endsWith('.json')) {
    return 'json'
  }
  return 'markdown'
}

function buildFileTree(kind: 'memory' | 'hooks', files: WorkspaceFileItem[]): FileTreeNode {
  const root: FileTreeNode = {
    name: kind === 'memory' ? 'memory' : 'hooks',
    path: kind,
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
      currentPath = currentPath ? `${currentPath}/${part}` : part
      const isLeaf = index === parts.length - 1

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

export function FileEditorPanel({ api, sessionRef, kind }: FileEditorPanelProps) {
  const [files, setFiles] = useState<WorkspaceFileItem[]>([])
  const [selectedPath, setSelectedPath] = useState('')
  const [expandedDirectories, setExpandedDirectories] = useState<string[]>([])
  const [showHiddenFiles, setShowHiddenFiles] = useState(false)
  const [content, setContent] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [message, setMessage] = useState('')
  const visibleFiles = useMemo(() => (showHiddenFiles ? files : files.filter((item) => !isHiddenPath(item.path))), [files, showHiddenFiles])
  const tree = useMemo(() => buildFileTree(kind, visibleFiles), [visibleFiles, kind])
  const allDirectoryPaths = useMemo(() => collectDirectoryPaths(tree), [tree])

  useEffect(() => {
    let cancelled = false
    async function loadFiles() {
      setLoading(true)
      setMessage('')
      try {
        const items = await api.listFiles(sessionRef, kind)
        if (cancelled) {
          return
        }
        setFiles(items)
        setExpandedDirectories([])
        setSelectedPath(pickDefaultPath(items.filter((item) => !isHiddenPath(item.path))))
      } catch (error) {
        if (cancelled) {
          return
        }
        setFiles([])
        setSelectedPath('')
        setExpandedDirectories([])
        setMessage(error instanceof Error ? error.message : '加载文件失败')
      } finally {
        if (!cancelled) {
          setLoading(false)
        }
      }
    }
    void loadFiles()
    return () => {
      cancelled = true
    }
  }, [api, kind, sessionRef.conversationId, sessionRef.provider])

  useEffect(() => {
    const nextPath = pickDefaultPath(visibleFiles)
    if (selectedPath && visibleFiles.some((item) => item.path === selectedPath)) {
      return
    }
    if (nextPath !== selectedPath) {
      setSelectedPath(nextPath)
    }
  }, [selectedPath, visibleFiles])

  useEffect(() => {
    let cancelled = false
    async function loadContent() {
      if (!selectedPath) {
        setContent('')
        setDirty(false)
        return
      }
      setLoading(true)
      setMessage('')
      try {
        const result = await api.getFileContent(sessionRef, kind, selectedPath)
        if (cancelled) {
          return
        }
        setContent(result.content)
        setDirty(false)
      } catch (error) {
        if (cancelled) {
          return
        }
        setMessage(error instanceof Error ? error.message : '加载文件内容失败')
      } finally {
        if (!cancelled) {
          setLoading(false)
        }
      }
    }
    void loadContent()
    return () => {
      cancelled = true
    }
  }, [api, kind, selectedPath, sessionRef.conversationId, sessionRef.provider])

  async function handleSave() {
    if (!selectedPath) {
      return
    }
    setSaving(true)
    setMessage('')
    try {
      await api.updateFileContent(sessionRef, kind, selectedPath, content)
      setDirty(false)
      const items = await api.listFiles(sessionRef, kind)
      setFiles(items)
      showSuccessToast('已保存')
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '保存失败')
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

  function renderTreeNode(node: FileTreeNode, depth: number): JSX.Element {
    if (node.kind === 'directory') {
      const expanded = expandedDirectories.includes(node.path)
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
          {expanded ? node.children.map((child) => renderTreeNode(child, depth + 1)) : null}
        </div>
      )
    }

    const file = node.file
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
          <span className="tree-meta">{file?.exists ? `${file.size} B` : '未创建'}</span>
        </span>
      </button>
    )
  }

  return (
    <div className="file-editor-shell">
      <div className="file-list-panel">
        <div className="tree-panel-toolbar">
          <div className="panel-title">{kind === 'memory' ? 'Memory 目录树' : 'Hooks 目录树'}</div>
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
        <div className="file-list">
          {visibleFiles.length ? (
            <div className="file-tree">
              <button
                type="button"
                className={expandedDirectories.includes(tree.path) ? 'tree-directory-row tree-directory-button root expanded' : 'tree-directory-row tree-directory-button root'}
                onClick={() => toggleDirectory(tree.path)}
              >
                <span className="tree-expand-icon">{expandedDirectories.includes(tree.path) ? '▾' : '▸'}</span>
                <span className="tree-kind dir">DIR</span>
                <span className="tree-label">{tree.name}</span>
              </button>
              {expandedDirectories.includes(tree.path) ? tree.children.map((child) => renderTreeNode(child, 1)) : null}
            </div>
          ) : null}
          {!files.length ? <div className="empty-state compact">当前没有可编辑文件。</div> : null}
          {files.length > 0 && !visibleFiles.length ? <div className="empty-state compact">当前只剩隐藏文件。点击“显示隐藏文件”后可查看。</div> : null}
        </div>
      </div>

      <div className="editor-panel">
        <div className="editor-toolbar">
          <div>
            <div className="panel-title">{selectedPath || '未选择文件'}</div>
            {dirty ? <div className="muted small">有未保存改动</div> : null}
          </div>
          <button type="button" onClick={() => void handleSave()} disabled={!selectedPath || saving || !dirty}>
            {saving ? '保存中...' : '保存'}
          </button>
        </div>

        {message ? <div className="info-banner">{message}</div> : null}
        {selectedPath ? (
          <div className="monaco-panel">
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
          </div>
        ) : loading ? (
          <div className="empty-state">加载中...</div>
        ) : (
          <div className="empty-state">选择一个文件开始编辑。</div>
        )}
      </div>
    </div>
  )
}
