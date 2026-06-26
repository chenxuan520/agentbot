import Editor from '@monaco-editor/react'
import { useEffect, useState } from 'react'

import { ApiClient } from '../api'
import { showSuccessToast } from '../toast'
import type { RepoSummary, RoleDetail, RoleSummary, SessionSettings, SkillSummary, SubagentSummary } from '../types'
import { ConfirmDialog } from './ConfirmDialog'

interface RolesLibraryPageProps {
  api: ApiClient
}

type RoleDetailTab = 'prompt' | 'settings'

function cloneSettings(settings: SessionSettings): SessionSettings {
  return JSON.parse(JSON.stringify(settings)) as SessionSettings
}

function formatJSONText(value: Record<string, unknown> | undefined): string {
  if (!value || Object.keys(value).length === 0) {
    return ''
  }
  return `${JSON.stringify(value, null, 2)}\n`
}

function formatRolePath(path: string): string {
  const normalized = path.replace(/\\/g, '/')
  const marker = '/templates/'
  const index = normalized.lastIndexOf(marker)
  if (index >= 0) {
    return normalized.slice(index + 1)
  }
  return normalized
}

function sortedUniqueIDs(ids: string[]): string[] {
  return [...new Set(ids.map((item) => item.trim()).filter(Boolean))].sort()
}

export function RolesLibraryPage({ api }: RolesLibraryPageProps) {
  const [items, setItems] = useState<RoleSummary[]>([])
  const [selectedRoleID, setSelectedRoleID] = useState('')
  const [detail, setDetail] = useState<RoleDetail | null>(null)
  const [settingsDraft, setSettingsDraft] = useState<SessionSettings | null>(null)
  const [opencodeConfigText, setOpencodeConfigText] = useState('')
  const [agentsContent, setAgentsContent] = useState('')
  const [skills, setSkills] = useState<SkillSummary[]>([])
  const [subagents, setSubagents] = useState<SubagentSummary[]>([])
  const [repos, setRepos] = useState<RepoSummary[]>([])
  const [copyFrom, setCopyFrom] = useState('')
  const [newRoleID, setNewRoleID] = useState('')
  const [loading, setLoading] = useState(true)
  const [detailLoading, setDetailLoading] = useState(false)
  const [skillsLoading, setSkillsLoading] = useState(false)
  const [subagentsLoading, setSubagentsLoading] = useState(false)
  const [reposLoading, setReposLoading] = useState(false)
  const [reposError, setReposError] = useState('')
  const [creating, setCreating] = useState(false)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [activeDetailTab, setActiveDetailTab] = useState<RoleDetailTab>('prompt')
  const [skillQuery, setSkillQuery] = useState('')
  const [subagentQuery, setSubagentQuery] = useState('')
  const [repoQuery, setRepoQuery] = useState('')
  const [message, setMessage] = useState('')

  async function loadRoles(preferredRoleID?: string) {
    setLoading(true)
    try {
      const nextItems = await api.listRoles()
      setItems(nextItems)
      setCopyFrom((current) => {
        if (current && nextItems.some((item) => item.id === current)) {
          return current
        }
        return nextItems[0]?.id ?? ''
      })
      setSelectedRoleID((current) => {
        if (preferredRoleID && nextItems.some((item) => item.id === preferredRoleID)) {
          return preferredRoleID
        }
        if (current && nextItems.some((item) => item.id === current)) {
          return current
        }
        return nextItems[0]?.id ?? ''
      })
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取 roles 失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadRoles()
  }, [])

  useEffect(() => {
    setActiveDetailTab('prompt')
  }, [selectedRoleID])

  useEffect(() => {
    let cancelled = false
    async function loadSkills() {
      setSkillsLoading(true)
      try {
        const nextSkills = await api.listSkills()
        if (!cancelled) {
          setSkills(nextSkills)
        }
      } catch {
        if (!cancelled) {
          setSkills([])
        }
      } finally {
        if (!cancelled) {
          setSkillsLoading(false)
        }
      }
    }
    void loadSkills()
    return () => {
      cancelled = true
    }
  }, [api])

  useEffect(() => {
    let cancelled = false
    async function loadSubagents() {
      setSubagentsLoading(true)
      try {
        const nextSubagents = await api.listSubagents()
        if (!cancelled) {
          setSubagents(nextSubagents)
        }
      } catch {
        if (!cancelled) {
          setSubagents([])
        }
      } finally {
        if (!cancelled) {
          setSubagentsLoading(false)
        }
      }
    }
    void loadSubagents()
    return () => {
      cancelled = true
    }
  }, [api])

  useEffect(() => {
    let cancelled = false
    async function loadRepos() {
      setReposLoading(true)
      setReposError('')
      try {
        const nextRepos = await api.listRepos()
        if (!cancelled) {
          setRepos(nextRepos)
        }
      } catch (error) {
        if (!cancelled) {
          setRepos([])
          setReposError(error instanceof Error ? error.message : '读取 repos 失败')
        }
      } finally {
        if (!cancelled) {
          setReposLoading(false)
        }
      }
    }
    void loadRepos()
    return () => {
      cancelled = true
    }
  }, [api])

  useEffect(() => {
    if (!selectedRoleID) {
      setDetail(null)
      setSettingsDraft(null)
      setOpencodeConfigText('')
      setAgentsContent('')
      setDirty(false)
      return
    }
    let cancelled = false
    async function loadDetail() {
      setDetailLoading(true)
      try {
        const nextDetail = await api.getRoleDetail(selectedRoleID)
        if (cancelled) {
          return
        }
        setDetail(nextDetail)
        setSettingsDraft(cloneSettings(nextDetail.settings))
        setOpencodeConfigText(formatJSONText(nextDetail.settings.agent.opencodeConfig))
        setAgentsContent(nextDetail.agentsFile.content)
        setDirty(false)
      } catch (error) {
        if (!cancelled) {
          setMessage(error instanceof Error ? error.message : '读取 role 详情失败')
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
  }, [api, selectedRoleID])

  async function handleCreateRole() {
    const roleID = newRoleID.trim()
    if (!roleID) {
      setMessage('请先输入 role 名称。')
      return
    }
    if (!copyFrom) {
      setMessage('当前没有可复制的基础 role。')
      return
    }
    setCreating(true)
    setMessage('')
    try {
      const created = await api.createRole(roleID, copyFrom)
      setNewRoleID('')
      await loadRoles(created.id)
      showSuccessToast(`已创建 role: ${created.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '创建 role 失败')
    } finally {
      setCreating(false)
    }
  }

  async function handleSaveRole() {
    if (!detail || !settingsDraft) {
      return
    }

    let parsedOpencodeConfig: Record<string, unknown> | undefined
    const trimmedOpencodeConfig = opencodeConfigText.trim()
    if (trimmedOpencodeConfig) {
      try {
        const parsed = JSON.parse(trimmedOpencodeConfig) as Record<string, unknown>
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          throw new Error('必须是 JSON 对象')
        }
        parsedOpencodeConfig = parsed
      } catch (error) {
        setMessage(error instanceof Error ? `高级 OpenCode 配置无法解析: ${error.message}` : '高级 OpenCode 配置无法解析')
        return
      }
    }

    const parsedSettings = cloneSettings(settingsDraft)
    parsedSettings.template = detail.id
    parsedSettings.agent.backend = (parsedSettings.agent.backend || 'opencode').trim() || 'opencode'
    if (parsedOpencodeConfig) {
      parsedSettings.agent.opencodeConfig = parsedOpencodeConfig
    } else {
      delete parsedSettings.agent.opencodeConfig
    }
    parsedSettings.mounts.skillIds = [...new Set(parsedSettings.mounts.skillIds.map((item) => item.trim()).filter(Boolean))].sort()
    parsedSettings.mounts.subagentIds = [...new Set(parsedSettings.mounts.subagentIds.map((item) => item.trim()).filter(Boolean))].sort()
    parsedSettings.mounts.repoIds = [...new Set((parsedSettings.mounts.repoIds ?? []).map((item) => item.trim()).filter(Boolean))].sort()

    setSaving(true)
    setMessage('')
    try {
      const updated = await api.updateRole(detail.id, parsedSettings, agentsContent)
      setDetail(updated)
      setSettingsDraft(cloneSettings(updated.settings))
      setOpencodeConfigText(formatJSONText(updated.settings.agent.opencodeConfig))
      setAgentsContent(updated.agentsFile.content)
      setDirty(false)
      await loadRoles(updated.id)
      showSuccessToast(`已保存 role: ${updated.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '保存 role 失败')
    } finally {
      setSaving(false)
    }
  }

  async function handleDeleteRole() {
    if (!detail) {
      return
    }
    setDeleting(true)
    setMessage('')
    try {
      await api.deleteRole(detail.id)
      setDetail(null)
      setSelectedRoleID('')
      setSettingsDraft(null)
      setOpencodeConfigText('')
      setAgentsContent('')
      setDirty(false)
      await loadRoles()
      showSuccessToast(`已删除 role: ${detail.id}`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '删除 role 失败')
    } finally {
      setDeleting(false)
    }
  }

  async function handleDeleteConfirm() {
    await handleDeleteRole()
    setShowDeleteConfirm(false)
  }

  function updateSettings(mutator: (current: SessionSettings) => SessionSettings) {
    setSettingsDraft((current) => {
      if (!current) {
        return current
      }
      setDirty(true)
      return mutator(current)
    })
  }

  function toggleSkill(skillID: string) {
    updateSettings((current) => {
      const hasSkill = current.mounts.skillIds.includes(skillID)
      return {
        ...current,
        mounts: {
          ...current.mounts,
          skillIds: hasSkill
            ? current.mounts.skillIds.filter((item) => item !== skillID)
            : [...current.mounts.skillIds, skillID].sort(),
        },
      }
    })
  }

  function toggleSubagent(subagentID: string) {
    updateSettings((current) => {
      const hasSubagent = current.mounts.subagentIds.includes(subagentID)
      return {
        ...current,
        mounts: {
          ...current.mounts,
          subagentIds: hasSubagent
            ? current.mounts.subagentIds.filter((item) => item !== subagentID)
            : [...current.mounts.subagentIds, subagentID].sort(),
        },
      }
    })
  }

  function toggleRepo(repoID: string) {
    updateSettings((current) => {
      const currentIDs = current.mounts.repoIds ?? []
      const hasRepo = currentIDs.includes(repoID)
      return {
        ...current,
        mounts: {
          ...current.mounts,
          repoIds: hasRepo ? currentIDs.filter((item) => item !== repoID) : sortedUniqueIDs([...currentIDs, repoID]),
        },
      }
    })
  }

  function setRepoIDs(nextRepoIDs: string[]) {
    updateSettings((current) => ({
      ...current,
      mounts: {
        ...current.mounts,
        repoIds: sortedUniqueIDs(nextRepoIDs),
      },
    }))
  }

  const deleteDisabled = !detail || detail.id === 'default' || detail.sessionCount > 0 || deleting
  const filteredSkills = skills.filter((item) => {
    const query = skillQuery.trim().toLowerCase()
    if (!query) {
      return true
    }
    return item.id.toLowerCase().includes(query) || item.title.toLowerCase().includes(query)
  })
  const filteredSubagents = subagents.filter((item) => {
    const query = subagentQuery.trim().toLowerCase()
    if (!query) {
      return true
    }
    return item.id.toLowerCase().includes(query) || item.title.toLowerCase().includes(query) || item.description.toLowerCase().includes(query)
  })
  const selectedRepoIDs = settingsDraft?.mounts.repoIds ?? []
  const filteredRepos = repos.filter((item) => {
    const query = repoQuery.trim().toLowerCase()
    if (!query) {
      return true
    }
    return item.id.toLowerCase().includes(query) || item.branch.toLowerCase().includes(query)
  })
  const allRepoIDs = repos.map((item) => item.id)
  const filteredRepoIDs = filteredRepos.map((item) => item.id)
  const allReposSelected = allRepoIDs.length > 0 && allRepoIDs.every((item) => selectedRepoIDs.includes(item))
  const filteredReposSelected = filteredRepoIDs.length > 0 && filteredRepoIDs.every((item) => selectedRepoIDs.includes(item))

  return (
    <div className="roles-layout">
      <aside className="roles-sidebar">
        <div className="sidebar-header">
          <div>
            <div className="eyebrow">Roles</div>
            <h2>Role 管理</h2>
            <p className="muted small">基于现有 template 创建 role，并在右侧直接编辑默认提示词和配置。</p>
          </div>
          <button type="button" className="toolbar-button subtle" onClick={() => void loadRoles()} disabled={loading}>
            {loading ? '刷新中...' : '刷新'}
          </button>
        </div>
        <div className="sidebar-summary muted small">{items.length} roles</div>
        {message ? <div className="info-banner">{message}</div> : null}

          <div className="settings-card role-create-card">
            <div>
              <div className="panel-title">新建 Role</div>
              <p className="muted small">基于现有 template 创建新 role，再修改提示词和默认 settings。</p>
            </div>

            <label className="role-form-field">
              <span>Role Name</span>
              <input value={newRoleID} onChange={(event) => setNewRoleID(event.target.value)} placeholder="例如 ops-oncall" />
            </label>

            <label className="role-form-field">
              <span>Copy From</span>
              <select value={copyFrom} onChange={(event) => setCopyFrom(event.target.value)}>
                {items.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.id}
                  </option>
                ))}
              </select>
            </label>

            <button type="button" onClick={() => void handleCreateRole()} disabled={creating || !newRoleID.trim() || !copyFrom}>
              {creating ? '创建中...' : '创建 Role'}
            </button>
          </div>

          <div className="settings-card roles-list-card">
            <div className="settings-card-header">
              <div>
                <h3>现有 Roles</h3>
                <p className="muted small">选中后在右侧直接编辑模板提示词和默认配置。</p>
              </div>
            </div>

            {loading ? <div className="empty-state compact">加载 roles 中...</div> : null}
            {!loading && !items.length ? <div className="empty-state compact">当前没有可管理的 role。</div> : null}

            {!loading && items.length ? (
              <div className="roles-list">
                {items.map((item) => {
                  const active = item.id === selectedRoleID
                  return (
                    <button
                      key={item.id}
                      type="button"
                      className={active ? 'session-list-item active' : 'session-list-item'}
                      onClick={() => setSelectedRoleID(item.id)}
                    >
                      <div className="session-list-head">
                        <div className="session-list-title">{item.id}</div>
                        <span className="meta-chip slim">{item.sessionCount} in use</span>
                      </div>
                      <div className="session-list-meta">updated {item.updatedAt ? new Date(item.updatedAt).toLocaleString() : '-'}</div>
                    </button>
                  )
                })}
              </div>
            ) : null}
          </div>
      </aside>

      <section className="roles-main">
          {!selectedRoleID ? <div className="empty-state large">选择一个 role 查看和编辑模板内容。</div> : null}
          {selectedRoleID && detailLoading ? <div className="empty-state large">读取 role 详情中...</div> : null}
          {selectedRoleID && !detailLoading && detail ? (
            <div className="detail-shell role-detail-shell">
              <div className="detail-header role-detail-header">
                <div>
                  <div className="eyebrow">Role Detail</div>
                  <h2>{detail.id}</h2>
                  <div className="muted mono session-id-line">{formatRolePath(detail.path)}</div>
                  <div className="detail-meta">
                    <span className="meta-chip">{detail.sessionCount} session(s)</span>
                    <span className="meta-chip">updated {detail.updatedAt ? new Date(detail.updatedAt).toLocaleString() : '-'}</span>
                  </div>
                </div>

                <div className="role-detail-actions">
                  <button type="button" onClick={() => void handleSaveRole()} disabled={saving || !dirty}>
                    {saving ? '保存中...' : '保存'}
                  </button>
                  <button type="button" className="danger-button" onClick={() => setShowDeleteConfirm(true)} disabled={deleteDisabled}>
                    {deleting ? '删除中...' : '删除'}
                  </button>
                </div>
              </div>

              {dirty ? <div className="muted small">有未保存改动。</div> : null}

              {detail.id === 'default' ? <div className="warning-banner">`default` 是保底 role，当前不允许删除。</div> : null}
              {detail.id !== 'default' && detail.sessionCount > 0 ? (
                <div className="warning-banner">当前还有 {detail.sessionCount} 个 session 正在使用这个 role，需先切走后再删除。</div>
              ) : null}

              <div className="tabs-row role-tabs-row">
                {([
                  ['prompt', 'Prompt'],
                  ['settings', 'Settings'],
                ] as Array<[RoleDetailTab, string]>).map(([tab, label]) => (
                  <button key={tab} type="button" className={activeDetailTab === tab ? 'tab-button active' : 'tab-button'} onClick={() => setActiveDetailTab(tab)}>
                    {label}
                  </button>
                ))}
              </div>

              {activeDetailTab === 'prompt' ? (
                <div className="role-editor-card role-editor-single">
                  <div className="editor-toolbar">
                    <div>
                      <div className="panel-title">AGENTS.md</div>
                      <div className="muted small">这里直接编辑 role 的系统提示词、规则和使用说明。</div>
                    </div>
                  </div>
                  <div className="monaco-panel role-prompt-panel">
                    <Editor
                      height="100%"
                      language="markdown"
                      value={agentsContent}
                      onChange={(value) => {
                        setAgentsContent(value ?? '')
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
                  </div>
                </div>
              ) : null}

              {activeDetailTab === 'settings' && settingsDraft ? (
                <div className="tab-panel role-settings-sections">
                  <div className="settings-card role-settings-card">
                    <div className="panel-title">基础设置</div>
                    <div className="role-settings-grid">
                      <label className="role-form-field">
                        <span>Template</span>
                        <input value={settingsDraft.template} readOnly />
                      </label>

                      <label className="role-form-field">
                        <span>Backend</span>
                        <input value={settingsDraft.agent.backend || 'opencode'} readOnly />
                      </label>

                      <label className="role-form-field">
                        <span>Reply Mode</span>
                        <select
                          value={settingsDraft.settings.replyMode}
                          onChange={(event) =>
                            updateSettings((current) => ({
                              ...current,
                              settings: { ...current.settings, replyMode: event.target.value },
                            }))
                          }
                        >
                          <option value="direct">direct</option>
                          <option value="topic">topic</option>
                          <option value="thread">thread</option>
                          <option value="topic-session">topic-session</option>
                        </select>
                      </label>

                      <label className="role-form-field">
                        <span>History TTL Hours</span>
                        <input
                          type="number"
                          min={1}
                          value={settingsDraft.settings.historyTTLHours}
                          onChange={(event) =>
                            updateSettings((current) => ({
                              ...current,
                              settings: {
                                ...current.settings,
                                historyTTLHours: Math.max(1, Number(event.target.value) || 1),
                              },
                            }))
                          }
                        />
                      </label>

                      <label className="role-form-field">
                        <span>OpenCode HTTP Timeout Seconds</span>
                        <input
                          type="number"
                          min={0}
                          value={settingsDraft.agent.opencodeHTTPTimeoutSeconds ?? 0}
                          onChange={(event) => {
                            const nextValue = event.target.value.trim()
                            updateSettings((current) => ({
                              ...current,
                              agent: {
                                ...current.agent,
                                opencodeHTTPTimeoutSeconds: nextValue === '' ? 0 : Math.max(0, Number(nextValue) || 0),
                              },
                            }))
                          }}
                        />
                        <div className="muted small">0 = 跟随默认超时（300 秒）</div>
                      </label>
                    </div>
                  </div>

                  <div className="settings-card role-settings-card">
                    <div className="panel-title">消息开关</div>
                    <div className="toggle-stack">
                      <label className="toggle-card">
                        <div className="toggle-copy">
                          <div className="toggle-title">接受群里未 @ 的人类消息</div>
                          <div className="muted small">打开后，这个 role 默认会处理群里普通人类消息。</div>
                        </div>
                        <span className={settingsDraft.settings.acceptGroupHumanMessagesWithoutMention ? 'toggle-switch active' : 'toggle-switch'}>
                          <input
                            type="checkbox"
                            checked={settingsDraft.settings.acceptGroupHumanMessagesWithoutMention}
                            onChange={(event) =>
                              updateSettings((current) => ({
                                ...current,
                                settings: {
                                  ...current.settings,
                                  acceptGroupHumanMessagesWithoutMention: event.target.checked,
                                },
                              }))
                            }
                          />
                          <span className="toggle-knob" />
                        </span>
                      </label>

                      <label className="toggle-card">
                        <div className="toggle-copy">
                          <div className="toggle-title">接受其他 bot 消息</div>
                          <div className="muted small">打开后会轮询补偿其他 bot 发的消息。</div>
                        </div>
                        <span className={settingsDraft.settings.acceptOtherBotMessages ? 'toggle-switch active' : 'toggle-switch'}>
                          <input
                            type="checkbox"
                            checked={settingsDraft.settings.acceptOtherBotMessages}
                            onChange={(event) =>
                              updateSettings((current) => ({
                                ...current,
                                settings: {
                                  ...current.settings,
                                  acceptOtherBotMessages: event.target.checked,
                                },
                              }))
                            }
                          />
                          <span className="toggle-knob" />
                        </span>
                      </label>

                      <label className="toggle-card">
                        <div className="toggle-copy">
                          <div className="toggle-title">接受 interactive 卡片消息</div>
                          <div className="muted small">打开后会把 interactive 卡片交给 hook 和主链路处理。</div>
                        </div>
                        <span className={settingsDraft.settings.acceptInteractiveCardMessages ? 'toggle-switch active' : 'toggle-switch'}>
                          <input
                            type="checkbox"
                            checked={settingsDraft.settings.acceptInteractiveCardMessages}
                            onChange={(event) =>
                              updateSettings((current) => ({
                                ...current,
                                settings: {
                                  ...current.settings,
                                  acceptInteractiveCardMessages: event.target.checked,
                                },
                              }))
                            }
                          />
                          <span className="toggle-knob" />
                        </span>
                      </label>
                      <label className="toggle-row">
                        <div className="toggle-copy">
                          <div className="toggle-title">允许本地 agent 接入（remote-agent）</div>
                          <div className="muted small">打开后，本地 agent 插件可用该会话的 session token 接管会话。</div>
                        </div>
                        <span className={settingsDraft.settings.remoteEnabled ? 'toggle-switch active' : 'toggle-switch'}>
                          <input
                            type="checkbox"
                            checked={settingsDraft.settings.remoteEnabled}
                            onChange={(event) =>
                              updateSettings((current) => ({
                                ...current,
                                settings: {
                                  ...current.settings,
                                  remoteEnabled: event.target.checked,
                                },
                              }))
                            }
                          />
                          <span className="toggle-knob" />
                        </span>
                      </label>
                    </div>
                  </div>

                  <div className="settings-card role-settings-card">
                    <div className="settings-card-header">
                      <div>
                        <h3>默认挂载 Skills</h3>
                        <p className="muted small">这个 role 被使用时，会默认把这些 skill 挂到 workspace。</p>
                      </div>
                    </div>

                    {settingsDraft.mounts.skillIds.length ? (
                      <div className="skill-tag-list">
                        {settingsDraft.mounts.skillIds.map((item) => (
                          <span key={item} className="skill-tag">
                            {item}
                          </span>
                        ))}
                      </div>
                    ) : (
                      <div className="muted small">当前没有默认挂载 skill。</div>
                    )}

                    <div className="role-skill-toolbar">
                      <input value={skillQuery} onChange={(event) => setSkillQuery(event.target.value)} placeholder="搜索 skill id 或标题" />
                    </div>

                    {skillsLoading ? <div className="info-banner">正在加载 skills...</div> : null}

                    <div className="skill-picker-list role-skills-list">
                      {filteredSkills.map((item) => (
                        <label key={item.id} className="skill-picker-item">
                          <input type="checkbox" checked={settingsDraft.mounts.skillIds.includes(item.id)} onChange={() => toggleSkill(item.id)} />
                          <div>
                            <div className="skill-picker-title">{item.title}</div>
                            <div className="muted mono">{item.id}</div>
                          </div>
                        </label>
                      ))}
                      {!skillsLoading && !filteredSkills.length ? <div className="empty-state compact">没有命中的 skill。</div> : null}
                    </div>
                  </div>

                  <div className="settings-card role-settings-card">
                    <div className="settings-card-header">
                      <div>
                        <h3>默认挂载 Subagents</h3>
                        <p className="muted small">这个 role 被使用时，会默认把这些 subagent 挂到 workspace 的 `.agents/agents/`。</p>
                      </div>
                    </div>

                    {settingsDraft.mounts.subagentIds.length ? (
                      <div className="skill-tag-list">
                        {settingsDraft.mounts.subagentIds.map((item) => (
                          <span key={item} className="skill-tag">
                            {item}
                          </span>
                        ))}
                      </div>
                    ) : (
                      <div className="muted small">当前没有默认挂载 subagent。</div>
                    )}

                    <div className="role-skill-toolbar">
                      <input value={subagentQuery} onChange={(event) => setSubagentQuery(event.target.value)} placeholder="搜索 subagent id / 标题 / 描述" />
                    </div>

                    {subagentsLoading ? <div className="info-banner">正在加载 subagents...</div> : null}

                    <div className="skill-picker-list role-skills-list">
                      {filteredSubagents.map((item) => (
                        <label key={item.id} className="skill-picker-item">
                          <input type="checkbox" checked={settingsDraft.mounts.subagentIds.includes(item.id)} onChange={() => toggleSubagent(item.id)} />
                          <div>
                            <div className="skill-picker-title">{item.title}</div>
                            <div className="muted mono">{item.id}{item.mode ? ` · ${item.mode}` : ''}</div>
                            {item.description ? <div className="muted small">{item.description}</div> : null}
                          </div>
                        </label>
                      ))}
                      {!subagentsLoading && !filteredSubagents.length ? <div className="empty-state compact">没有命中的 subagent。</div> : null}
                    </div>
                  </div>

                  <div className="settings-card role-settings-card">
                    <div className="settings-card-header">
                      <div>
                        <h3>默认挂载 Repos</h3>
                        <p className="muted small">这个 role 被使用时，会默认把这些共享 git 仓库以软链挂到新建 session 的 workspace 根目录。</p>
                      </div>
                    </div>

                    {selectedRepoIDs.length ? (
                      <div className="skill-tag-list">
                        {selectedRepoIDs.map((item) => {
                          const branch = repos.find((repo) => repo.id === item)?.branch
                          return (
                            <span key={item} className="skill-tag repo-tag">
                              <span className="repo-tag-id mono">{item}</span>
                              {branch ? <span className="repo-tag-branch mono">{branch}</span> : null}
                            </span>
                          )
                        })}
                      </div>
                    ) : (
                      <div className="muted small">当前没有默认挂载 repo。</div>
                    )}

                    <div className="role-skill-toolbar">
                      <input value={repoQuery} onChange={(event) => setRepoQuery(event.target.value)} placeholder="搜索 repo id 或分支" />
                      <div className="inline-actions repo-bulk-actions">
                        <button type="button" className="toolbar-button subtle" onClick={() => setRepoIDs(allRepoIDs)} disabled={!allRepoIDs.length || allReposSelected}>
                          全选全部
                        </button>
                        <button
                          type="button"
                          className="toolbar-button subtle"
                          onClick={() => setRepoIDs([...selectedRepoIDs, ...filteredRepoIDs])}
                          disabled={!filteredRepoIDs.length || filteredReposSelected}
                        >
                          全选当前列表
                        </button>
                        <button type="button" className="toolbar-button subtle" onClick={() => setRepoIDs([])} disabled={!selectedRepoIDs.length}>
                          清空
                        </button>
                      </div>
                    </div>

                    {reposLoading ? <div className="info-banner">正在加载 repos...</div> : null}
                    {reposError ? <div className="error-banner">{reposError}</div> : null}

                    <div className="skill-picker-list role-skills-list">
                      {filteredRepos.map((item) => (
                        <label key={item.id} className="skill-picker-item">
                          <input type="checkbox" checked={selectedRepoIDs.includes(item.id)} onChange={() => toggleRepo(item.id)} />
                          <div>
                            <div className="skill-picker-title mono">{item.id}</div>
                            <div className="muted mono">{item.branch ? `branch: ${item.branch}` : item.hasGit ? 'git repo' : '非 git 目录'}</div>
                          </div>
                        </label>
                      ))}
                      {!reposLoading && !filteredRepos.length ? <div className="empty-state compact">agents/repos 下还没有可挂载的 repo。</div> : null}
                    </div>
                  </div>

                  <div className="settings-card role-settings-card">
                    <div>
                      <div className="panel-title">高级 OpenCode 配置</div>
                      <div className="muted small">只在需要 role 级 `opencodeConfig` patch 时填写；留空表示不写。</div>
                    </div>
                    <textarea
                      className="role-advanced-textarea mono"
                      value={opencodeConfigText}
                      onChange={(event) => {
                        setOpencodeConfigText(event.target.value)
                        setDirty(true)
                      }}
                      placeholder={'例如：\n{\n  "model": "gpt-5"\n}'}
                      rows={8}
                    />
                  </div>
                </div>
              ) : null}
            </div>
          ) : null}
      </section>
      <ConfirmDialog
        open={showDeleteConfirm && !!detail}
        title="删除 Role"
        description={detail ? `确认删除 role \`${detail.id}\` 吗？` : ''}
        confirmLabel="确认删除"
        loading={deleting}
        onCancel={() => setShowDeleteConfirm(false)}
        onConfirm={() => void handleDeleteConfirm()}
      />
    </div>
  )
}
