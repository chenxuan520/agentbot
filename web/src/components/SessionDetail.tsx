import Editor from '@monaco-editor/react'
import { ChangeEvent, useEffect, useRef, useState } from 'react'

import { ApiClient } from '../api'
import { chatModeLabel, isSuspectedLeftGroup } from '../session-display'
import { showErrorToast, showSuccessToast } from '../toast'
import type {
  RemoteAgentRoute,
  RemoteAgentStatus,
  RepoSummary,
  ScheduleContentKind,
  ScheduleCreateInput,
  ScheduleJob,
  SessionAgentsFile,
  SessionAgentsMode,
  SessionDetail as SessionDetailType,
  SessionRef,
  SessionSettings,
  SessionSummary,
  SessionTranscriptMessage,
  SessionTranscriptPart,
  SessionTranscriptResponse,
  SessionTranscriptSessionOption,
  SkillSummary,
  SubagentSummary,
} from '../types'
import { ConfirmDialog } from './ConfirmDialog'
import { FileEditorPanel } from './FileEditorPanel'
import { SessionSkillsPanel } from './SessionSkillsPanel'

type SessionTab = 'settings' | 'schedule' | 'skills' | 'sessionSkills' | 'subagents' | 'repos' | 'memory' | 'hooks' | 'agents' | 'transcript'
type DeleteConfirmState = { kind: 'session' } | { kind: 'schedule'; job: ScheduleJob }
const sessionTabQueryParam = 'tab'
const defaultSessionTab: SessionTab = 'settings'

const sessionTabs: Array<[SessionTab, string]> = [
  ['settings', 'Settings'],
  ['agents', 'AGENTS'],
  ['memory', 'Memory'],
  ['hooks', 'Hooks'],
  ['schedule', 'Schedule'],
  ['skills', 'Skills'],
  ['sessionSkills', 'My Skills'],
  ['subagents', 'Subagents'],
  ['repos', 'Repos'],
  ['transcript', 'Transcript'],
]

const sessionTabSet = new Set<SessionTab>(sessionTabs.map(([tab]) => tab))

interface SessionDetailProps {
  api: ApiClient
  sessionRef: SessionRef
  scope: 'project' | 'session'
  summary?: SessionSummary
  onBack?: () => void
  onDisplayInfoResolved?: (ref: SessionRef, info: { displayName?: string; chatMode?: string }) => void
  onSessionChanged?: () => void
  onSessionDeleted?: () => void
}

interface ScheduleContentEditorState {
  job: ScheduleJob
  kind: ScheduleContentKind
}

interface TranscriptState {
  sessionId: string
  latestMessageId: string
  totalMessages: number
  availableSessions: SessionTranscriptSessionOption[]
  messages: SessionTranscriptMessage[]
}

function normalizeSessionTab(value?: string | null): SessionTab {
  const candidate = (value ?? '').trim()
  return sessionTabSet.has(candidate as SessionTab) ? (candidate as SessionTab) : defaultSessionTab
}

function readSessionTabFromQuery(): SessionTab {
  if (typeof window === 'undefined') {
    return defaultSessionTab
  }
  return normalizeSessionTab(new URLSearchParams(window.location.search).get(sessionTabQueryParam))
}

function writeSessionTabToQuery(tab: SessionTab) {
  if (typeof window === 'undefined') {
    return
  }
  const url = new URL(window.location.href)
  if (tab === defaultSessionTab) {
    url.searchParams.delete(sessionTabQueryParam)
  } else {
    url.searchParams.set(sessionTabQueryParam, tab)
  }
  const nextURL = `${url.pathname}${url.search}${url.hash}`
  const currentURL = `${window.location.pathname}${window.location.search}${window.location.hash}`
  if (nextURL !== currentURL) {
    window.history.replaceState({}, '', nextURL)
  }
}

function cloneSettings(settings: SessionSettings): SessionSettings {
  return JSON.parse(JSON.stringify(settings)) as SessionSettings
}

function formatJSONText(value: Record<string, unknown> | undefined): string {
  if (!value || Object.keys(value).length === 0) {
    return ''
  }
  return `${JSON.stringify(value, null, 2)}\n`
}

const remoteRouteLabels: Record<RemoteAgentRoute, string> = {
  disabled: '未启用',
  bot: 'Bot',
  local: '本地 agent',
}

// remoteStatusTone maps the live status to a visual tone: local (green, active),
// paused (amber, plugin online but forced to bot), offline/disabled (gray).
function remoteStatusTone(status: RemoteAgentStatus | null): string {
  if (!status) return 'loading'
  if (status.route === 'local') return 'local'
  if (status.connected) return 'paused'
  return status.enabled ? 'offline' : 'disabled'
}

function normalizeAgentsMode(mode?: SessionAgentsMode): SessionAgentsMode {
  return mode === 'custom' ? 'custom' : 'template'
}

async function copyToClipboard(text: string) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text)
    return
  }
  const area = document.createElement('textarea')
  area.value = text
  document.body.appendChild(area)
  area.select()
  try {
    if (!document.execCommand('copy')) {
      throw new Error('浏览器未允许复制')
    }
  } finally {
    document.body.removeChild(area)
  }
}

async function copyTextWithToast(text: string, successTitle: string, failureTitle: string) {
  try {
    await copyToClipboard(text)
    showSuccessToast(successTitle)
  } catch (error) {
    showErrorToast(error instanceof Error ? `${failureTitle}: ${error.message}` : failureTitle)
  }
}

function parseSchedulePayload(payload: string): Record<string, unknown> | null {
  try {
    return JSON.parse(payload) as Record<string, unknown>
  } catch {
    return null
  }
}

function readSchedulePayloadString(payload: Record<string, unknown> | null, key: string): string {
  const value = payload?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function formatScheduleTime(value: string): string {
  if (!value) {
    return '-'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return date.toLocaleString()
}

function summarizeSchedulePayload(payload: string): string {
  const data = parseSchedulePayload(payload)
  if (!data) {
    return payload
  }

  const promptText = readSchedulePayloadString(data, 'promptText')
  if (promptText) {
    return `prompt: ${promptText}`
  }
  const promptFile = readSchedulePayloadString(data, 'promptFile')
  if (promptFile) {
    return `promptFile: ${promptFile}`
  }
  const notifyText = readSchedulePayloadString(data, 'notifyText')
  if (notifyText) {
    return `notify: ${notifyText}`
  }
  const replyMessageID = readSchedulePayloadString(data, 'replyMessageID')
  if (replyMessageID) {
    return `replyMessageID: ${replyMessageID}`
  }
  return payload
}

function describeScheduleMode(payload: string): string {
  const data = parseSchedulePayload(payload)
  const cron = readSchedulePayloadString(data, 'cron')
  if (!cron) {
    return '一次性'
  }
  const timezone = readSchedulePayloadString(data, 'timezone')
  return timezone ? `Cron: ${cron} · ${timezone}` : `Cron: ${cron}`
}

function describeNextScheduleTime(job: ScheduleJob): string {
  const data = parseSchedulePayload(job.Payload)
  const cron = readSchedulePayloadString(data, 'cron')
  if (job.Status === 'running') {
    return cron ? '执行中，完成后刷新' : '-'
  }
  return formatScheduleTime(job.RunAt)
}

function nextScheduleTimeLabel(job: ScheduleJob): string {
  const data = parseSchedulePayload(job.Payload)
  const cron = readSchedulePayloadString(data, 'cron')
  return cron ? '下次执行时间' : '执行时间'
}

function readResolvedScheduleContent(job: ScheduleJob): string {
  if (typeof job.PromptTextResolved === 'string' && job.PromptTextResolved !== '') {
    return job.PromptTextResolved
  }
  if (typeof job.NotifyTextResolved === 'string' && job.NotifyTextResolved !== '') {
    return job.NotifyTextResolved
  }
  return ''
}

function readResolvedScheduleContentKind(job: ScheduleJob): ScheduleContentKind | null {
  if (typeof job.PromptTextResolved === 'string' && job.PromptTextResolved !== '') {
    return 'prompt'
  }
  if (typeof job.NotifyTextResolved === 'string' && job.NotifyTextResolved !== '') {
    return 'notify'
  }
  return null
}

function readResolvedScheduleContentPath(job: ScheduleJob): string {
  const data = parseSchedulePayload(job.Payload)
  const promptFile = readSchedulePayloadString(data, 'promptFile')
  if (promptFile) {
    return promptFile
  }
  const notifyFile = readSchedulePayloadString(data, 'notifyFile')
  if (notifyFile) {
    return notifyFile
  }
  return ''
}

function createTranscriptState(): TranscriptState {
  return {
    sessionId: '',
    latestMessageId: '',
    totalMessages: 0,
    availableSessions: [],
    messages: [],
  }
}

function sortedUniqueIDs(ids: string[]): string[] {
  return [...new Set(ids.map((item) => item.trim()).filter(Boolean))].sort()
}

function transcriptScopedMessages(messages: SessionTranscriptMessage[]): SessionTranscriptMessage[] {
  const latestUserIndex = [...messages].map((item) => item.role.trim().toLowerCase()).lastIndexOf('user')
  if (latestUserIndex >= 0) {
    return messages.slice(latestUserIndex).slice(-50)
  }
  return messages.length > 50 ? messages.slice(-50) : messages
}

function mergeTranscriptState(current: TranscriptState, incoming: SessionTranscriptResponse): TranscriptState {
  if (incoming.reset) {
    const messages = transcriptScopedMessages(incoming.messages)
    return {
      sessionId: incoming.sessionId,
      latestMessageId: incoming.latestMessageId,
      totalMessages: incoming.totalMessages,
      availableSessions: incoming.availableSessions,
      messages,
    }
  }
  const byID = new Map(current.messages.map((item) => [item.id, item] as const))
  const order = current.messages.map((item) => item.id)
  for (const item of incoming.messages) {
    if (!byID.has(item.id)) {
      order.push(item.id)
    }
    byID.set(item.id, item)
  }
  const messages = transcriptScopedMessages(order.map((id) => byID.get(id)).filter((item): item is SessionTranscriptMessage => Boolean(item)))
  return {
    sessionId: incoming.sessionId,
    latestMessageId: incoming.latestMessageId || (messages.length ? messages[messages.length - 1].id : ''),
    totalMessages: incoming.totalMessages,
    availableSessions: incoming.availableSessions,
    messages,
  }
}

function formatTranscriptTime(value: number): string {
  if (!value) {
    return '-'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return String(value)
  }
  return date.toLocaleString()
}

function transcriptPartMeta(part: SessionTranscriptPart): string {
  const items = [part.type]
  if (part.tool) {
    items.push(part.tool)
  }
  if (part.toolStatus) {
    items.push(part.toolStatus)
  }
  if (part.toolInputSummary) {
    items.push(part.toolInputSummary)
  }
  return items.filter(Boolean).join(' · ')
}

function transcriptVisibleParts(parts: SessionTranscriptPart[]): SessionTranscriptPart[] {
  return parts.filter((part) => {
    const type = part.type.trim().toLowerCase()
    return type !== 'step-start' && type !== 'step-finish'
  })
}

function transcriptVisibleMessages(messages: SessionTranscriptMessage[]): SessionTranscriptMessage[] {
  const visible = messages
    .map((item) => ({
      ...item,
      parts: transcriptVisibleParts(item.parts),
    }))
    .filter((item) => item.parts.length > 0)
  return [...visible].reverse()
}

type ScheduleDraftMode = 'once' | 'cron'

interface ScheduleDraft {
  mode: ScheduleDraftMode
  route: string
  runAt: string
  cron: string
  timezone: string
  replyMessageID: string
  promptText: string
  notifyText: string
}

function defaultScheduleTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai'
}

function createScheduleDraft(): ScheduleDraft {
  return {
    mode: 'once',
    route: '',
    runAt: '',
    cron: '',
    timezone: defaultScheduleTimezone(),
    replyMessageID: '',
    promptText: '',
    notifyText: '',
  }
}

function toRFC3339(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    throw new Error('请输入合法的执行时间')
  }
  return date.toISOString()
}

function buildScheduleCreateInput(draft: ScheduleDraft): ScheduleCreateInput {
  const route = draft.route.trim()
  if (!route) {
    throw new Error('请填写 route')
  }

  const promptText = draft.promptText.trim()
  const notifyText = draft.notifyText.trim()
  if (!promptText && !notifyText) {
    throw new Error('请至少填写 promptText 或 notifyText')
  }
  if (promptText && notifyText) {
    throw new Error('promptText 和 notifyText 二选一即可')
  }

  const payload: Record<string, unknown> = {
    replyMessageID: draft.replyMessageID.trim(),
  }
  if (promptText) {
    payload.promptText = promptText
  }
  if (notifyText) {
    payload.notifyText = notifyText
  }

  if (draft.mode === 'once') {
    if (!draft.runAt.trim()) {
      throw new Error('请选择执行时间')
    }
    return {
      route,
      runAt: toRFC3339(draft.runAt),
      payload,
    }
  }

  const cron = draft.cron.trim()
  if (!cron) {
    throw new Error('请填写 cron 表达式')
  }
  const timezone = draft.timezone.trim()
  if (!timezone) {
    throw new Error('请填写 timezone')
  }
  return {
    route,
    cron,
    timezone,
    payload,
  }
}

export function SessionDetail({ api, sessionRef, scope, summary, onBack, onDisplayInfoResolved, onSessionChanged, onSessionDeleted }: SessionDetailProps) {
  const [detail, setDetail] = useState<SessionDetailType | null>(null)
  const [jobs, setJobs] = useState<ScheduleJob[]>([])
  const [skills, setSkills] = useState<SkillSummary[]>([])
  const [subagents, setSubagents] = useState<SubagentSummary[]>([])
  const [repos, setRepos] = useState<RepoSummary[]>([])
  const [settingsDraft, setSettingsDraft] = useState<SessionSettings | null>(null)
  const [opencodeConfigText, setOpencodeConfigText] = useState('')
  const [remoteStatus, setRemoteStatus] = useState<RemoteAgentStatus | null>(null)
  const [agentsFile, setAgentsFile] = useState<SessionAgentsFile | null>(null)
  const [agentsModeDraft, setAgentsModeDraft] = useState<SessionAgentsMode>('template')
  const [agentsContentDraft, setAgentsContentDraft] = useState('')
  const [activeTab, setActiveTab] = useState<SessionTab>(() => readSessionTabFromQuery())
  const [detailLoading, setDetailLoading] = useState(true)
  const [jobsLoading, setJobsLoading] = useState(false)
  const [skillsLoading, setSkillsLoading] = useState(false)
  const [subagentsLoading, setSubagentsLoading] = useState(false)
  const [reposLoading, setReposLoading] = useState(false)
  const [agentsLoading, setAgentsLoading] = useState(false)
  const [savingSettings, setSavingSettings] = useState(false)
  const [savingSchedule, setSavingSchedule] = useState(false)
  const [savingSkills, setSavingSkills] = useState(false)
  const [savingSubagents, setSavingSubagents] = useState(false)
  const [savingRepos, setSavingRepos] = useState(false)
  const [savingAgents, setSavingAgents] = useState(false)
  const [cancelingScheduleJobID, setCancelingScheduleJobID] = useState('')
  const [savingScheduleContentJobID, setSavingScheduleContentJobID] = useState('')
  const [deleteConfirm, setDeleteConfirm] = useState<DeleteConfirmState | null>(null)
  const [scheduleEditor, setScheduleEditor] = useState<ScheduleContentEditorState | null>(null)
  const [deletingSession, setDeletingSession] = useState(false)
  const [exportingSessionData, setExportingSessionData] = useState(false)
  const [importingSessionData, setImportingSessionData] = useState(false)
  const [rotatingToken, setRotatingToken] = useState(false)
  const [transcript, setTranscript] = useState<TranscriptState>(createTranscriptState)
  const [transcriptLoading, setTranscriptLoading] = useState(false)
  const [transcriptRefreshing, setTranscriptRefreshing] = useState(false)
  const [transcriptError, setTranscriptError] = useState('')
  const [scheduleDraft, setScheduleDraft] = useState<ScheduleDraft>(createScheduleDraft)
  const [scheduleContentDrafts, setScheduleContentDrafts] = useState<Record<string, string>>({})
  const [skillQuery, setSkillQuery] = useState('')
  const [subagentQuery, setSubagentQuery] = useState('')
  const [repoQuery, setRepoQuery] = useState('')
  const [message, setMessage] = useState('')
  const transcriptSessionRef = useRef('')
  const transcriptLatestMessageRef = useRef('')
  const transcriptRequestRef = useRef(0)
  const tabsRef = useRef<HTMLDivElement | null>(null)
  const tabButtonRefs = useRef<Partial<Record<SessionTab, HTMLButtonElement | null>>>({})
  const [tabIndicator, setTabIndicator] = useState<{ left: number; width: number } | null>(null)
  const scheduleJobs = Array.isArray(jobs) ? jobs : []
  const selectedSkillIDs = settingsDraft?.mounts.skillIds ?? []
  const selectedSubagentIDs = settingsDraft?.mounts.subagentIds ?? []
  const selectedRepoIDs = settingsDraft?.mounts.repoIds ?? []

  useEffect(() => {
    function syncTabIndicator() {
      const tabsElement = tabsRef.current
      const activeButton = tabButtonRefs.current[activeTab]
      if (!tabsElement || !activeButton) {
        setTabIndicator(null)
        return
      }
      setTabIndicator({ left: activeButton.offsetLeft, width: activeButton.offsetWidth })
    }

    syncTabIndicator()
    const frame = window.requestAnimationFrame(() => {
      syncTabIndicator()
      tabButtonRefs.current[activeTab]?.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'nearest' })
    })
    window.addEventListener('resize', syncTabIndicator)
    return () => {
      window.cancelAnimationFrame(frame)
      window.removeEventListener('resize', syncTabIndicator)
    }
  }, [activeTab])

  useEffect(() => {
    writeSessionTabToQuery(activeTab)
  }, [activeTab])

  async function loadDetail() {
    setDetailLoading(true)
    setMessage('')
    try {
      const nextDetail = await api.getSessionDetail(sessionRef)
      setDetail(nextDetail)
      setSettingsDraft(cloneSettings(nextDetail.settings))
      setOpencodeConfigText(formatJSONText(nextDetail.settings.agent.opencodeConfig))
      onDisplayInfoResolved?.(
        { provider: nextDetail.provider, conversationId: nextDetail.conversationId },
        { displayName: nextDetail.displayName, chatMode: nextDetail.chatMode },
      )
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取 session 详情失败')
    } finally {
      setDetailLoading(false)
    }
  }

  async function loadSkills() {
    if (skills.length > 0) {
      return
    }
    setSkillsLoading(true)
    try {
      setSkills(await api.listSkills())
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取 skill 列表失败')
    } finally {
      setSkillsLoading(false)
    }
  }

  async function loadSubagents() {
    if (subagents.length > 0) {
      return
    }
    setSubagentsLoading(true)
    try {
      setSubagents(await api.listSubagents())
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取 subagent 列表失败')
    } finally {
      setSubagentsLoading(false)
    }
  }

  async function loadRepos() {
    if (repos.length > 0) {
      return
    }
    setReposLoading(true)
    try {
      setRepos(await api.listRepos())
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '读取 repo 列表失败')
    } finally {
      setReposLoading(false)
    }
  }

  async function loadSchedule() {
    setJobsLoading(true)
    try {
      const nextJobs = await api.listSessionSchedule(sessionRef)
      setJobs(Array.isArray(nextJobs) ? nextJobs : [])
      const nextDrafts: Record<string, string> = {}
      for (const job of Array.isArray(nextJobs) ? nextJobs : []) {
        const content = readResolvedScheduleContent(job)
        if (content) {
          nextDrafts[job.ID] = content
        }
      }
      setScheduleContentDrafts(nextDrafts)
    } catch (error) {
      setJobs([])
      setScheduleContentDrafts({})
      setMessage(error instanceof Error ? error.message : '读取定时任务失败')
    } finally {
      setJobsLoading(false)
    }
  }

  useEffect(() => {
    setDetail(null)
    setSettingsDraft(null)
    setJobs([])
    setActiveTab(readSessionTabFromQuery())
    setAgentsFile(null)
    setAgentsModeDraft('template')
    setAgentsContentDraft('')
    setOpencodeConfigText('')
    setScheduleDraft(createScheduleDraft())
    setScheduleContentDrafts({})
    setTranscript(createTranscriptState())
    setTranscriptError('')
    transcriptRequestRef.current += 1
    void loadDetail()
    void loadSchedule()
  }, [sessionRef.conversationId, sessionRef.provider])

  // Poll the live remote-agent connection status so the panel reflects plugin
  // connect/disconnect without a manual refresh.
  useEffect(() => {
    let cancelled = false
    let timer: number | undefined
    async function poll() {
      try {
        const status = await api.getRemoteStatus(sessionRef)
        if (!cancelled) {
          setRemoteStatus(status)
        }
      } catch {
        if (!cancelled) {
          setRemoteStatus(null)
        }
      } finally {
        if (!cancelled) {
          timer = window.setTimeout(poll, 4000)
        }
      }
    }
    setRemoteStatus(null)
    void poll()
    return () => {
      cancelled = true
      if (timer) {
        window.clearTimeout(timer)
      }
    }
  }, [api, sessionRef.conversationId, sessionRef.provider])

  useEffect(() => {
    transcriptSessionRef.current = transcript.sessionId
    transcriptLatestMessageRef.current = transcript.latestMessageId
  }, [transcript.latestMessageId, transcript.sessionId])

  async function loadTranscript(options?: { reset?: boolean; loading?: boolean; sessionId?: string }) {
    const reset = options?.reset ?? false
    const loading = options?.loading ?? false
    const requestedSessionID = (options?.sessionId ?? transcriptSessionRef.current).trim()
    const requestID = transcriptRequestRef.current + 1
    transcriptRequestRef.current = requestID
    if (loading) {
      setTranscriptLoading(true)
    } else {
      setTranscriptRefreshing(true)
    }
    try {
      const nextTranscript = await api.getSessionTranscript(
        sessionRef,
        requestedSessionID,
        reset ? '' : transcriptLatestMessageRef.current,
      )
      if (transcriptRequestRef.current !== requestID) {
        return
      }
      setTranscript((current) => mergeTranscriptState(reset ? createTranscriptState() : current, nextTranscript))
      setTranscriptError('')
    } catch (error) {
      if (transcriptRequestRef.current !== requestID) {
        return
      }
      setTranscriptError(error instanceof Error ? error.message : '读取 transcript 失败')
    } finally {
      if (transcriptRequestRef.current !== requestID) {
        return
      }
      if (loading) {
        setTranscriptLoading(false)
      } else {
        setTranscriptRefreshing(false)
      }
    }
  }

  useEffect(() => {
    void loadSkills()
    void loadSubagents()
    void loadRepos()
  }, [api])

  useEffect(() => {
    if (activeTab !== 'agents' || agentsFile) {
      return
    }
    let cancelled = false
    async function loadAgents() {
      setAgentsLoading(true)
      try {
        const result = await api.getSessionAgents(sessionRef)
        if (!cancelled) {
          setAgentsFile(result)
          setAgentsModeDraft(normalizeAgentsMode(result.mode))
          setAgentsContentDraft(result.content)
        }
      } catch (error) {
        if (!cancelled) {
          setMessage(error instanceof Error ? error.message : '读取 AGENTS.md 失败')
        }
      } finally {
        if (!cancelled) {
          setAgentsLoading(false)
        }
      }
    }
    void loadAgents()
    return () => {
      cancelled = true
    }
  }, [activeTab, agentsFile, api, sessionRef])

  useEffect(() => {
    if (activeTab !== 'transcript') {
      transcriptRequestRef.current += 1
      return
    }
    void loadTranscript({ loading: true })
  }, [activeTab, api, sessionRef])

  async function handleSettingsSave() {
    if (!settingsDraft) {
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
        showErrorToast(error instanceof Error ? `高级 OpenCode 配置无法解析: ${error.message}` : '高级 OpenCode 配置无法解析')
        return
      }
    }

    const parsedSettings = cloneSettings(settingsDraft)
    parsedSettings.agent.backend = (parsedSettings.agent.backend || 'opencode').trim() || 'opencode'
    if (parsedOpencodeConfig) {
      parsedSettings.agent.opencodeConfig = parsedOpencodeConfig
    } else {
      delete parsedSettings.agent.opencodeConfig
    }

    setSavingSettings(true)
    setMessage('')
    try {
      const updated = await api.updateSessionSettings(sessionRef, parsedSettings)
      setDetail(updated)
      setSettingsDraft(cloneSettings(updated.settings))
      setOpencodeConfigText(formatJSONText(updated.settings.agent.opencodeConfig))
      setAgentsFile(null)
      setAgentsModeDraft('template')
      setAgentsContentDraft('')
      await onSessionChanged?.()
      showSuccessToast('Settings 已保存并重建')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存 settings 失败')
    } finally {
      setSavingSettings(false)
    }
  }

  async function handleSkillsSave() {
    if (!settingsDraft) {
      return
    }
    setSavingSkills(true)
    setMessage('')
    try {
      const updated = await api.updateSessionSettings(sessionRef, settingsDraft)
      setDetail(updated)
      setSettingsDraft(cloneSettings(updated.settings))
      setAgentsFile(null)
      setAgentsModeDraft('template')
      setAgentsContentDraft('')
      await onSessionChanged?.()
      showSuccessToast('Skills 挂载已保存并重建')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存 skills 失败')
    } finally {
      setSavingSkills(false)
    }
  }

  async function handleSubagentsSave() {
    if (!settingsDraft) {
      return
    }
    setSavingSubagents(true)
    setMessage('')
    try {
      const updated = await api.updateSessionSettings(sessionRef, settingsDraft)
      setDetail(updated)
      setSettingsDraft(cloneSettings(updated.settings))
      setAgentsFile(null)
      setAgentsModeDraft('template')
      setAgentsContentDraft('')
      await onSessionChanged?.()
      showSuccessToast('Subagents 挂载已保存并重建')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存 subagents 失败')
    } finally {
      setSavingSubagents(false)
    }
  }

  async function handleReposSave() {
    if (!settingsDraft) {
      return
    }
    setSavingRepos(true)
    setMessage('')
    try {
      const updated = await api.updateSessionSettings(sessionRef, settingsDraft)
      setDetail(updated)
      setSettingsDraft(cloneSettings(updated.settings))
      setAgentsFile(null)
      setAgentsModeDraft('template')
      setAgentsContentDraft('')
      await onSessionChanged?.()
      showSuccessToast('Repos 挂载已保存并重建')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存 repos 失败')
    } finally {
      setSavingRepos(false)
    }
  }

  async function handleAgentsSave() {
    if (!agentsFile) {
      return
    }
    setSavingAgents(true)
    setMessage('')
    try {
      const nextMode = normalizeAgentsMode(agentsModeDraft)
      const updatedAgents = await api.updateSessionAgents(sessionRef, nextMode, nextMode === 'custom' ? agentsContentDraft : undefined)
      const updatedDetail = await api.getSessionDetail(sessionRef)
      setDetail(updatedDetail)
      setSettingsDraft(cloneSettings(updatedDetail.settings))
      setAgentsFile(updatedAgents)
      setAgentsModeDraft(normalizeAgentsMode(updatedAgents.mode))
      setAgentsContentDraft(updatedAgents.content)
      await onSessionChanged?.()
      showSuccessToast(nextMode === 'custom' ? 'Custom AGENTS 已保存；当前 active session 已清空' : '已恢复跟随 template；当前 active session 已清空')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存 AGENTS 失败')
    } finally {
      setSavingAgents(false)
    }
  }

  async function handleRotateToken() {
    setRotatingToken(true)
    setMessage('')
    try {
      const sessionToken = await api.rotateSessionToken(sessionRef)
      setDetail((current) => (current ? { ...current, sessionToken } : current))
      showSuccessToast('Session token 已轮换')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '轮换 token 失败')
    } finally {
      setRotatingToken(false)
    }
  }

  async function handleSessionDataExport() {
    setExportingSessionData(true)
    setMessage('')
    try {
      const blob = await api.exportSessionData(sessionRef)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `${sessionRef.conversationId}-session-data.zip`
      document.body.appendChild(anchor)
      anchor.click()
      document.body.removeChild(anchor)
      URL.revokeObjectURL(url)
      showSuccessToast('已导出当前 session 的 memory、hooks 和私有 skills')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '导出 session 数据失败')
    } finally {
      setExportingSessionData(false)
    }
  }

  async function handleSessionDataImport(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) {
      return
    }
    setImportingSessionData(true)
    setMessage('')
    try {
      await api.importSessionData(sessionRef, file)
      setAgentsFile(null)
      showSuccessToast('已导入当前 session 的 memory、hooks 和私有 skills')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '导入 session 数据失败')
    } finally {
      setImportingSessionData(false)
    }
  }

  async function handleSessionDelete() {
    if (scope !== 'project') {
      return
    }
    setDeletingSession(true)
    setMessage('')
    try {
      await api.deleteSession(sessionRef)
      await onSessionDeleted?.()
      showSuccessToast(`已删除 session: ${sessionRef.conversationId}`)
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '删除 session 失败')
    } finally {
      setDeletingSession(false)
    }
  }

  async function handleScheduleCreate() {
    setSavingSchedule(true)
    setMessage('')
    try {
      const job = await api.createSessionSchedule(sessionRef, buildScheduleCreateInput(scheduleDraft))
      setScheduleDraft(createScheduleDraft())
      await loadSchedule()
      showSuccessToast(`已创建定时任务: ${job.ID}`)
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '创建定时任务失败')
    } finally {
      setSavingSchedule(false)
    }
  }

  async function handleScheduleDelete(job: ScheduleJob) {
    setCancelingScheduleJobID(job.ID)
    setMessage('')
    try {
      await api.cancelSessionSchedule(job.ID)
      await loadSchedule()
      showSuccessToast(`已删除定时任务: ${job.ID}`)
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '删除定时任务失败')
    } finally {
      setCancelingScheduleJobID('')
    }
  }

  function requestSessionDelete() {
    if (scope !== 'project') {
      return
    }
    setDeleteConfirm({ kind: 'session' })
  }

  function requestScheduleDelete(job: ScheduleJob) {
    setDeleteConfirm({ kind: 'schedule', job })
  }

  async function handleDeleteConfirm() {
    const current = deleteConfirm
    if (!current) {
      return
    }
    if (current.kind === 'session') {
      await handleSessionDelete()
    } else {
      await handleScheduleDelete(current.job)
    }
    setDeleteConfirm(null)
  }

  async function handleScheduleContentSave(job: ScheduleJob) {
    const kind = readResolvedScheduleContentKind(job)
    if (!kind) {
      return
    }
    const content = scheduleContentDrafts[job.ID] ?? readResolvedScheduleContent(job)
    setSavingScheduleContentJobID(job.ID)
    setMessage('')
    try {
      await api.updateSessionSchedule(job.ID, { kind, content })
      await loadSchedule()
      showSuccessToast(kind === 'prompt' ? `已更新定时任务提示词: ${job.ID}` : `已更新定时任务提醒文本: ${job.ID}`)
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '更新定时任务内容失败')
    } finally {
      setSavingScheduleContentJobID('')
    }
  }

  function openScheduleEditor(job: ScheduleJob) {
    const kind = readResolvedScheduleContentKind(job)
    if (!kind) {
      return
    }
    setScheduleEditor({ job, kind })
  }

  function closeScheduleEditor() {
    if (savingScheduleContentJobID) {
      return
    }
    setScheduleEditor(null)
  }

  function toggleSkill(skillID: string) {
    setSettingsDraft((current) => {
      if (!current) {
        return current
      }
      const nextSkillIDs = current.mounts.skillIds.includes(skillID)
        ? current.mounts.skillIds.filter((item) => item !== skillID)
        : [...current.mounts.skillIds, skillID].sort()
      return {
        ...current,
        mounts: {
          ...current.mounts,
          skillIds: nextSkillIDs,
        },
      }
    })
  }

  function toggleSubagent(subagentID: string) {
    setSettingsDraft((current) => {
      if (!current) {
        return current
      }
      const nextSubagentIDs = current.mounts.subagentIds.includes(subagentID)
        ? current.mounts.subagentIds.filter((item) => item !== subagentID)
        : [...current.mounts.subagentIds, subagentID].sort()
      return {
        ...current,
        mounts: {
          ...current.mounts,
          subagentIds: nextSubagentIDs,
        },
      }
    })
  }

  function toggleRepo(repoID: string) {
    setSettingsDraft((current) => {
      if (!current) {
        return current
      }
      const currentIDs = current.mounts.repoIds ?? []
      const nextRepoIDs = currentIDs.includes(repoID)
        ? currentIDs.filter((item) => item !== repoID)
        : sortedUniqueIDs([...currentIDs, repoID])
      return {
        ...current,
        mounts: {
          ...current.mounts,
          repoIds: nextRepoIDs,
        },
      }
    })
  }

  function setRepoIDs(nextRepoIDs: string[]) {
    setSettingsDraft((current) => {
      if (!current) {
        return current
      }
      return {
        ...current,
        mounts: {
          ...current.mounts,
          repoIds: sortedUniqueIDs(nextRepoIDs),
        },
      }
    })
  }

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

  const currentDetail = detail && detail.provider === sessionRef.provider && detail.conversationId === sessionRef.conversationId ? detail : null
  const headerDisplayName = currentDetail?.displayName || summary?.displayName || sessionRef.conversationId
  const headerChatMode = currentDetail?.chatMode || summary?.chatMode || ''
  const suspectedLeftGroup = isSuspectedLeftGroup(currentDetail ? { displayName: currentDetail.displayName, chatMode: currentDetail.chatMode } : undefined)
  const headerBackend = currentDetail?.backend || summary?.agentBackend || ''
  const headerTemplate = currentDetail?.settings.template || summary?.template || ''
  const visibleTranscriptMessages = transcriptVisibleMessages(transcript.messages)
  const deleteConfirmLoading =
    deleteConfirm?.kind === 'session'
      ? deletingSession
      : deleteConfirm?.kind === 'schedule'
        ? cancelingScheduleJobID === deleteConfirm.job.ID
        : false
  const agentsDirty =
    !!agentsFile &&
    (agentsModeDraft !== normalizeAgentsMode(agentsFile.mode) ||
      (agentsModeDraft === 'custom' && agentsContentDraft !== agentsFile.content))

  return (
    <div className="detail-shell">
      <div className="detail-header">
        <div>
          <div className="eyebrow">{scope === 'project' ? 'Session Detail' : 'My Session'}</div>
          <h2>{headerDisplayName}</h2>
          <button
            type="button"
            className="workspace-copy-inline muted mono session-id-line"
            onClick={() => void copyTextWithToast(sessionRef.conversationId, 'Conversation ID 已复制', '复制 Conversation ID 失败')}
            title="点击复制 Conversation ID"
          >
            {sessionRef.conversationId}
          </button>
          <div className="detail-meta">
            <span className="meta-chip detail-meta-tag">{sessionRef.provider}</span>
            {chatModeLabel(headerChatMode) ? <span className="meta-chip detail-meta-tag">{chatModeLabel(headerChatMode)}</span> : null}
            {suspectedLeftGroup ? (
              <span className="session-left-tag" title="飞书能读到群名但拿不到会话类型，通常说明 bot 已被移出该群">
                已退群
              </span>
            ) : null}
            {headerBackend ? <span className="meta-chip detail-meta-tag">{headerBackend}</span> : null}
            {headerTemplate ? <span className="meta-chip detail-meta-tag">template: {headerTemplate}</span> : null}
          </div>
        </div>
        <div className="inline-actions detail-header-actions">
          {scope === 'project' ? (
            <button type="button" className="danger-button" onClick={requestSessionDelete} disabled={deletingSession}>
              {deletingSession ? '删除中...' : '删除 Session'}
            </button>
          ) : null}
          {onBack ? (
            <button type="button" className="mobile-back-button" onClick={onBack}>
              返回列表
            </button>
          ) : null}
        </div>
      </div>

      {message ? <div className="info-banner">{message}</div> : null}

        <div ref={tabsRef} className="tabs-row session-detail-tabs">
          <span
            aria-hidden="true"
            className={`tab-active-pill${tabIndicator ? ' visible' : ''}`}
            style={tabIndicator ? { width: `${tabIndicator.width}px`, transform: `translateX(${tabIndicator.left}px)` } : undefined}
          />
          {sessionTabs.map(([tab, label]) => (
            <button
              key={tab}
              ref={(element) => {
                tabButtonRefs.current[tab] = element
              }}
              type="button"
              className={activeTab === tab ? 'tab-button active' : 'tab-button'}
              onClick={() => setActiveTab(tab)}
            >
              {label}
            </button>
          ))}
        </div>

      {detailLoading || !detail || !settingsDraft ? (
        <div className="empty-state">加载 session 详情中...</div>
      ) : (
        <>
          {activeTab === 'settings' ? (
            <div className="tab-panel">
              <div className="token-strip">
                <div className="token-strip-main">
                  <div className="token-strip-label">Session Token</div>
                  <input readOnly value={detail.sessionToken} className="token-inline-input mono" title={detail.sessionToken} />
                </div>
                <div className="inline-actions token-strip-actions">
                  <button type="button" onClick={() => void copyTextWithToast(detail.sessionToken, 'Session Token 已复制', '复制 Session Token 失败')}>
                    复制
                  </button>
                  <button type="button" onClick={() => void handleRotateToken()} disabled={rotatingToken}>
                    {rotatingToken ? '更新中...' : '更新 Token'}
                  </button>
                </div>
              </div>

              <div className="settings-grid">
                <label>
                  <span>Template</span>
                  <select
                    value={settingsDraft.template}
                    onChange={(event) =>
                      setSettingsDraft((current) =>
                        current ? { ...current, template: event.target.value } : current,
                      )
                    }
                  >
                    {detail.availableTemplates.map((item) => (
                      <option key={item} value={item}>
                        {item}
                      </option>
                    ))}
                  </select>
                </label>

                <label>
                  <span>Reply Mode</span>
                  <select
                    value={settingsDraft.settings.replyMode}
                    onChange={(event) =>
                      setSettingsDraft((current) =>
                        current
                          ? { ...current, settings: { ...current.settings, replyMode: event.target.value } }
                          : current,
                      )
                    }
                  >
                    <option value="direct">direct</option>
                    <option value="topic">topic</option>
                    <option value="thread">thread</option>
                    <option value="topic-session">topic-session</option>
                  </select>
                </label>

                <label>
                  <span>History TTL Hours</span>
                  <input
                    type="number"
                    min={1}
                    value={settingsDraft.settings.historyTTLHours}
                    onChange={(event) =>
                      setSettingsDraft((current) =>
                        current
                          ? {
                              ...current,
                              settings: {
                                ...current.settings,
                                historyTTLHours: Number(event.target.value) || 1,
                              },
                            }
                          : current,
                      )
                    }
                  />
                </label>

                <label>
                  <span>OpenCode HTTP Timeout Seconds</span>
                  <input
                    type="number"
                    min={0}
                    value={settingsDraft.agent.opencodeHTTPTimeoutSeconds ?? 0}
                    onChange={(event) => {
                      const nextValue = event.target.value.trim()
                      setSettingsDraft((current) =>
                        current
                          ? {
                              ...current,
                              agent: {
                                ...current.agent,
                                opencodeHTTPTimeoutSeconds: nextValue === '' ? 0 : Math.max(0, Number(nextValue) || 0),
                              },
                            }
                          : current,
                      )
                    }}
                  />
                  <div className="muted small">0 = use default timeout (300 seconds)</div>
                </label>
              </div>

              <div className="checkbox-list">
                <label className="checkbox-row">
                  <input
                    type="checkbox"
                    checked={settingsDraft.settings.acceptGroupHumanMessagesWithoutMention}
                    onChange={(event) =>
                      setSettingsDraft((current) =>
                        current
                          ? {
                              ...current,
                              settings: {
                                ...current.settings,
                                acceptGroupHumanMessagesWithoutMention: event.target.checked,
                              },
                            }
                          : current,
                      )
                    }
                  />
                  <span>接受群里未 @ 的人类消息</span>
                </label>
                <label className="checkbox-row">
                  <input
                    type="checkbox"
                    checked={settingsDraft.settings.acceptOtherBotMessages}
                    onChange={(event) =>
                      setSettingsDraft((current) =>
                        current
                          ? {
                              ...current,
                              settings: { ...current.settings, acceptOtherBotMessages: event.target.checked },
                            }
                          : current,
                      )
                    }
                  />
                  <span>接受其他 bot 消息（轮询补偿）</span>
                </label>
                <label className="checkbox-row">
                  <input
                    type="checkbox"
                    checked={settingsDraft.settings.acceptInteractiveCardMessages}
                    onChange={(event) =>
                      setSettingsDraft((current) =>
                        current
                          ? {
                              ...current,
                              settings: {
                                ...current.settings,
                                acceptInteractiveCardMessages: event.target.checked,
                              },
                            }
                          : current,
                      )
                    }
                  />
                  <span>接受 interactive 卡片消息</span>
                </label>
                <label className="checkbox-row">
                  <input
                    type="checkbox"
                    checked={settingsDraft.settings.remoteEnabled}
                    onChange={(event) =>
                      setSettingsDraft((current) =>
                        current
                          ? {
                              ...current,
                              settings: { ...current.settings, remoteEnabled: event.target.checked },
                            }
                          : current,
                      )
                    }
                  />
                  <span>允许本地 agent 接入（remote-agent）</span>
                </label>
              </div>

              <div className="settings-card role-settings-card">
                <div>
                  <div className="panel-title">高级 OpenCode 配置</div>
                  <div className="muted small">给当前 session 写 `opencodeConfig` patch。留空表示不写。</div>
                </div>
                <textarea
                  className="role-advanced-textarea mono"
                  value={opencodeConfigText}
                  onChange={(event) => setOpencodeConfigText(event.target.value)}
                  placeholder={'例如：\n{\n  "model": "gpt-5"\n}'}
                  rows={8}
                />
              </div>

              <div className={`remote-status remote-status--${remoteStatusTone(remoteStatus)}`}>
                <div className="remote-status-head">
                  <span className="remote-status-dot" />
                  <span className="remote-status-name">本地 agent 接入</span>
                  <span className="remote-status-badge">
                    {remoteStatus ? remoteRouteLabels[remoteStatus.route] : '检测中…'}
                  </span>
                </div>
                <div className="remote-status-body">
                  {!remoteStatus ? (
                    <span className="muted small">正在检测连接状态…</span>
                  ) : remoteStatus.connected ? (
                    <div className="remote-status-meta">
                      <span className="remote-status-line">
                        {remoteStatus.route === 'local'
                          ? '插件已连接，消息正转发给本地 agent。'
                          : '插件已连接，但已手动切回 bot（在飞书发 /connect-local 可切回本地）。'}
                      </span>
                      <div className="remote-status-fields">
                        {remoteStatus.agentId ? (
                          <span className="remote-status-field">
                            <b>agent</b>
                            {remoteStatus.agentId}
                          </span>
                        ) : null}
                        {remoteStatus.sessionId ? (
                          <span className="remote-status-field">
                            <b>session</b>
                            {remoteStatus.sessionId}
                          </span>
                        ) : null}
                        {remoteStatus.title ? (
                          <span className="remote-status-field">
                            <b>title</b>
                            {remoteStatus.title}
                          </span>
                        ) : null}
                      </div>
                    </div>
                  ) : (
                    <span className="muted small">
                      {remoteStatus.enabled
                        ? '暂无本地 agent 插件连接，消息由 bot 处理。'
                        : '未开启。打开上方开关并 rebuild 后，本地 agent 插件即可用该会话的 session token 接入。'}
                    </span>
                  )}
                </div>
              </div>

              <div className="detail-submeta">
                {detail.activeSessionId ? (
                  <button
                    type="button"
                    className="workspace-copy-inline"
                    onClick={() => void copyTextWithToast(detail.activeSessionId, 'Active Session ID 已复制', '复制 Active Session ID 失败')}
                    title="点击复制 Active Session ID"
                  >
                    <span>Active Session ID:</span> <span className="mono">{detail.activeSessionId}</span>
                  </button>
                ) : (
                  <div>Active Session ID: <span className="mono">-</span></div>
                )}
                <button
                  type="button"
                  className="workspace-copy-inline"
                  onClick={() => void copyTextWithToast(detail.workspacePath, '路径已复制', '复制 Workspace 路径失败')}
                  title="点击复制 Workspace 路径"
                >
                  <span>Workspace:</span> <span className="mono">{detail.workspacePath}</span>
                </button>
                <div>Last Message At: {detail.lastMessageAt ? new Date(detail.lastMessageAt).toLocaleString() : '-'}</div>
              </div>

              <div className="settings-card session-data-card">
                <div className="settings-card-header">
                  <div>
                    <h3>Session Data</h3>
                    <p className="muted">导出和导入当前 session 的特有数据包：`memory`、`hooks`、私有 `My Skills`。不包含公共 skill。</p>
                  </div>
                  <div className="inline-actions">
                    <button type="button" className="toolbar-button subtle" onClick={() => void handleSessionDataExport()} disabled={exportingSessionData}>
                      {exportingSessionData ? '导出中...' : '导出 ZIP'}
                    </button>
                    <label className={importingSessionData ? 'upload-button disabled' : 'upload-button'}>
                      <input type="file" accept=".zip,application/zip" onChange={handleSessionDataImport} disabled={importingSessionData} />
                      {importingSessionData ? '导入中...' : '导入 ZIP'}
                    </label>
                  </div>
                </div>
              </div>

              <div className="tab-actions">
                <button type="button" onClick={() => void handleSettingsSave()} disabled={savingSettings}>
                  {savingSettings ? '保存并重建中...' : '保存并立即生效'}
                </button>
              </div>
            </div>
          ) : null}

          {activeTab === 'schedule' ? (
            <div className="tab-panel schedule-tab-shell">
              <div className="settings-card">
                <div className="settings-card-header">
                  <div>
                    <h3>当前定时任务</h3>
                    <p className="muted">按当前 session 的 `provider / conversationId` 展示仍在生效的任务（`pending` / `running`）。</p>
                  </div>
                  <div className="inline-actions">
                    <button type="button" className="toolbar-button subtle" onClick={() => void loadSchedule()} disabled={jobsLoading}>
                      {jobsLoading ? '刷新中...' : '刷新'}
                    </button>
                  </div>
                </div>
                {jobsLoading ? <div className="info-banner">正在加载定时任务...</div> : null}
                {!jobsLoading && !scheduleJobs.length ? <div className="empty-state compact">当前 session 没有生效中的定时任务。</div> : null}
                {scheduleJobs.length ? (
                  <div className="schedule-list">
                    {scheduleJobs.map((job) => {
                      const contentKind = readResolvedScheduleContentKind(job)
                      const originalContent = readResolvedScheduleContent(job)
                      const contentPath = readResolvedScheduleContentPath(job)
                      const draftContent = scheduleContentDrafts[job.ID] ?? originalContent
                      const contentDirty = draftContent !== originalContent
                      return (
                      <div key={job.ID} className="schedule-item">
                        <div className="schedule-item-header">
                          <div>
                            <div className="panel-title">{job.Route}</div>
                            <div className="muted mono">job: {job.ID}</div>
                          </div>
                          <div className="inline-actions schedule-item-actions">
                            <span className="meta-chip">{job.Status}</span>
                            <button
                              type="button"
                              className="danger-button"
                              onClick={() => requestScheduleDelete(job)}
                              disabled={cancelingScheduleJobID === job.ID}
                            >
                              {cancelingScheduleJobID === job.ID ? '删除中...' : '删除'}
                            </button>
                          </div>
                        </div>
                        <div className="detail-submeta schedule-meta">
                          <div>调度方式: {describeScheduleMode(job.Payload)}</div>
                          <div>
                            {nextScheduleTimeLabel(job)}: <span className="schedule-time-value">{describeNextScheduleTime(job)}</span>
                          </div>
                          <div>Created At: {formatScheduleTime(job.CreatedAt)}</div>
                          <div>Updated At: {formatScheduleTime(job.UpdatedAt)}</div>
                        </div>
                        {!contentKind ? <div className="schedule-payload mono">{summarizeSchedulePayload(job.Payload)}</div> : null}
                        {contentKind ? (
                          <div className="schedule-editor-box">
                            <label className="schedule-editor-field">
                              <span>{contentKind === 'prompt' ? '提示词' : '提醒文本'}</span>
                              {contentPath ? <div className="detail-submeta mono">file: {contentPath}</div> : null}
                              <textarea
                                rows={contentKind === 'prompt' ? 4 : 3}
                                value={draftContent}
                                onChange={(event) =>
                                  setScheduleContentDrafts((current) => ({
                                    ...current,
                                    [job.ID]: event.target.value,
                                  }))
                                }
                              />
                            </label>
                            <div className="inline-actions schedule-editor-actions">
                              <button
                                type="button"
                                className="toolbar-button subtle"
                                onClick={() => openScheduleEditor(job)}
                                disabled={savingScheduleContentJobID === job.ID}
                              >
                                放大编辑
                              </button>
                              <button
                                type="button"
                                className="toolbar-button subtle"
                                onClick={() =>
                                  setScheduleContentDrafts((current) => ({
                                    ...current,
                                    [job.ID]: originalContent,
                                  }))
                                }
                                disabled={savingScheduleContentJobID === job.ID || !contentDirty}
                              >
                                重置
                              </button>
                              <button
                                type="button"
                                onClick={() => void handleScheduleContentSave(job)}
                                disabled={savingScheduleContentJobID === job.ID || !draftContent.trim() || !contentDirty}
                              >
                                {savingScheduleContentJobID === job.ID ? '保存中...' : '保存内容'}
                              </button>
                            </div>
                          </div>
                        ) : null}
                      </div>
                    )})}
                  </div>
                ) : null}
              </div>

              <div className="settings-card">
                <div className="settings-card-header">
                  <div>
                    <h3>创建定时任务</h3>
                    <p className="muted">支持一次性任务和 cron 周期任务。当前表单先覆盖常用的 `promptText` / `notifyText` 场景。</p>
                  </div>
                  <div className="inline-actions">
                    <button type="button" onClick={() => setScheduleDraft(createScheduleDraft())} disabled={savingSchedule}>
                      重置
                    </button>
                    <button type="button" onClick={() => void handleScheduleCreate()} disabled={savingSchedule}>
                      {savingSchedule ? '创建中...' : '创建任务'}
                    </button>
                  </div>
                </div>

                <div className="schedule-form-grid">
                  <label>
                    <span>类型</span>
                    <select
                      value={scheduleDraft.mode}
                      onChange={(event) =>
                        setScheduleDraft((current) => ({
                          ...current,
                          mode: event.target.value === 'cron' ? 'cron' : 'once',
                        }))
                      }
                    >
                      <option value="once">一次性</option>
                      <option value="cron">Cron</option>
                    </select>
                  </label>

                  <label>
                    <span>Route</span>
                    <input
                      value={scheduleDraft.route}
                      onChange={(event) => setScheduleDraft((current) => ({ ...current, route: event.target.value }))}
                      placeholder="reminder.follow_up"
                    />
                  </label>

                  {scheduleDraft.mode === 'once' ? (
                    <label>
                      <span>Run At</span>
                      <input
                        type="datetime-local"
                        value={scheduleDraft.runAt}
                        onChange={(event) => setScheduleDraft((current) => ({ ...current, runAt: event.target.value }))}
                      />
                    </label>
                  ) : (
                    <>
                      <label>
                        <span>Cron</span>
                        <input
                          value={scheduleDraft.cron}
                          onChange={(event) => setScheduleDraft((current) => ({ ...current, cron: event.target.value }))}
                          placeholder="0 8 * * *"
                        />
                      </label>

                      <label>
                        <span>Timezone</span>
                        <input
                          value={scheduleDraft.timezone}
                          onChange={(event) => setScheduleDraft((current) => ({ ...current, timezone: event.target.value }))}
                          placeholder="Asia/Shanghai"
                        />
                      </label>
                    </>
                  )}

                  <label className="field-span-3">
                    <span>Reply Message ID</span>
                    <input
                      value={scheduleDraft.replyMessageID}
                      onChange={(event) => setScheduleDraft((current) => ({ ...current, replyMessageID: event.target.value }))}
                      placeholder="留空表示直接发群消息；回复原 topic/thread 时填原 message_id"
                    />
                    <div className="muted small">所有会发消息的定时任务都必须显式带这个字段；留空会作为空串发给后端。</div>
                  </label>

                  <label className="field-span-3">
                    <span>Prompt Text</span>
                    <textarea
                      rows={4}
                      value={scheduleDraft.promptText}
                      onChange={(event) => setScheduleDraft((current) => ({ ...current, promptText: event.target.value }))}
                      placeholder="到点后继续跑 agent"
                    />
                  </label>

                  <label className="field-span-3">
                    <span>Notify Text</span>
                    <textarea
                      rows={3}
                      value={scheduleDraft.notifyText}
                      onChange={(event) => setScheduleDraft((current) => ({ ...current, notifyText: event.target.value }))}
                      placeholder="如果只想发一条提醒，填这里；和 Prompt Text 二选一"
                    />
                  </label>
                </div>
              </div>
            </div>
          ) : null}

          {activeTab === 'skills' ? (
            <div className="tab-panel skill-tab-shell">
              <div className="skill-tab-toolbar">
                <input
                  value={skillQuery}
                  onChange={(event) => setSkillQuery(event.target.value)}
                  placeholder="搜索 skill id 或标题"
                />
                <button type="button" onClick={() => void handleSkillsSave()} disabled={savingSkills}>
                  {savingSkills ? '保存中...' : '保存'}
                </button>
              </div>

              {skillsLoading ? <div className="info-banner">正在加载公共 skill 列表...</div> : null}

              <div className="current-skills-box">
                <div className="panel-title">当前挂载</div>
                {selectedSkillIDs.length ? (
                  <div className="skill-tag-list">
                    {selectedSkillIDs.map((item) => (
                      <span key={item} className="skill-tag">
                        {item}
                      </span>
                    ))}
                  </div>
                ) : (
                  <div className="muted">当前没有挂载 skill。</div>
                )}
              </div>

              <div className="skill-picker-list">
                {filteredSkills.map((item) => (
                  <label key={item.id} className="skill-picker-item">
                    <input type="checkbox" checked={selectedSkillIDs.includes(item.id)} onChange={() => toggleSkill(item.id)} />
                    <div>
                      <div className="skill-picker-title">{item.title}</div>
                      <div className="muted mono">{item.id}</div>
                    </div>
                  </label>
                ))}
                {!filteredSkills.length ? <div className="empty-state compact">没有命中的 skill。</div> : null}
              </div>
            </div>
          ) : null}

          {activeTab === 'sessionSkills' ? <SessionSkillsPanel api={api} sessionRef={sessionRef} /> : null}

          {activeTab === 'transcript' ? (
            <div className="tab-panel transcript-tab-shell">
              <div className="settings-card transcript-panel-shell">
                <div className="settings-card-header transcript-header">
                  <div>
                    <h3>Live Transcript</h3>
                    <p className="muted">默认加载当前可用 session；在 `topic-session` 模式下可切换其他 topic session。优先看最近一条用户消息之后的内容，但总展示不超过 50 条。进入标签时加载一次，后续手动刷新。</p>
                  </div>
                </div>

                <div className="transcript-toolbar">
                  {transcript.availableSessions.length ? (
                    <label className="transcript-session-picker">
                      <span className="muted small">Session</span>
                      <select
                        value={transcript.sessionId || ''}
                        onChange={(event) => void loadTranscript({ loading: true, reset: true, sessionId: event.target.value })}
                        disabled={transcriptRefreshing || transcriptLoading}
                      >
                        {transcript.availableSessions.map((item) => (
                          <option key={`${item.sessionId}-${item.topicKey || item.kind}`} value={item.sessionId}>
                            {item.label}
                          </option>
                        ))}
                      </select>
                    </label>
                  ) : (
                    <div />
                  )}
                  <div className="inline-actions transcript-toolbar-actions">
                    <button type="button" className="toolbar-button subtle" onClick={() => void loadTranscript()} disabled={transcriptRefreshing || transcriptLoading}>
                      {transcriptRefreshing ? '刷新中...' : '刷新'}
                    </button>
                    <button type="button" className="toolbar-button subtle" onClick={() => setTranscript(createTranscriptState())} disabled={transcriptRefreshing || transcriptLoading}>
                      重置视图
                    </button>
                  </div>
                </div>

                <div className="detail-submeta transcript-meta-grid">
                  <div>main_active_session_id: <span className="mono">{detail.activeSessionId || '-'}</span></div>
                  <div>loaded_session_id: <span className="mono">{transcript.sessionId || '-'}</span></div>
                  <div>latest_message_id: <span className="mono">{transcript.latestMessageId || '-'}</span></div>
                  <div>message_count: visible <span className="mono">{visibleTranscriptMessages.length}</span> / loaded <span className="mono">{transcript.messages.length}</span> / total <span className="mono">{transcript.totalMessages}</span></div>
                </div>

                {transcriptError ? <div className="info-banner">{transcriptError}</div> : null}
                {transcriptLoading ? <div className="empty-state compact">正在加载 transcript...</div> : null}
                {!transcriptLoading && !transcript.availableSessions.length ? <div className="empty-state compact">当前没有可展示的 transcript session。</div> : null}
                {!transcriptLoading && transcript.availableSessions.length > 0 && !visibleTranscriptMessages.length ? <div className="empty-state compact">当前 transcript 还没有可展示的消息。</div> : null}

                {visibleTranscriptMessages.length ? (
                  <div className="transcript-list">
                    {visibleTranscriptMessages.map((item) => (
                      <div key={item.id} className="transcript-item">
                        <div className="transcript-item-header">
                          <div className="transcript-role">{item.role || 'unknown'}</div>
                          <div className="muted mono transcript-message-id">{item.id}</div>
                          <div className="muted mono">{formatTranscriptTime(item.createdAt)}</div>
                        </div>
                          <div className="transcript-parts">
                            {item.parts.map((part, index) => (
                            <div key={`${item.id}-${index}`} className={`transcript-part${!part.text && !part.reason ? ' transcript-part-compact' : ''}`}>
                              <div className="transcript-part-meta mono">{transcriptPartMeta(part)}</div>
                              {part.text ? <pre className="transcript-part-body">{part.text}</pre> : null}
                              {!part.text && part.reason ? <pre className="transcript-part-body">{part.reason}</pre> : null}
                            </div>
                          ))}
                        </div>
                      </div>
                    ))}
                  </div>
                ) : null}
              </div>
            </div>
          ) : null}

          {activeTab === 'subagents' ? (
            <div className="tab-panel skill-tab-shell">
              <div className="skill-tab-toolbar">
                <input
                  value={subagentQuery}
                  onChange={(event) => setSubagentQuery(event.target.value)}
                  placeholder="搜索 subagent id / 标题 / 描述"
                />
                <button type="button" onClick={() => void handleSubagentsSave()} disabled={savingSubagents}>
                  {savingSubagents ? '保存中...' : '保存'}
                </button>
              </div>

              {subagentsLoading ? <div className="info-banner">正在加载 subagent 列表...</div> : null}

              <div className="current-skills-box">
                <div className="panel-title">当前挂载</div>
                {selectedSubagentIDs.length ? (
                  <div className="skill-tag-list">
                    {selectedSubagentIDs.map((item) => (
                      <span key={item} className="skill-tag">
                        {item}
                      </span>
                    ))}
                  </div>
                ) : (
                  <div className="muted">当前没有挂载 subagent。</div>
                )}
              </div>

              <div className="skill-picker-list">
                {filteredSubagents.map((item) => (
                  <label key={item.id} className="skill-picker-item">
                    <input type="checkbox" checked={selectedSubagentIDs.includes(item.id)} onChange={() => toggleSubagent(item.id)} />
                    <div>
                      <div className="skill-picker-title">{item.title}</div>
                      <div className="muted mono">{item.id}{item.mode ? ` · ${item.mode}` : ''}</div>
                      {item.description ? <div className="muted small">{item.description}</div> : null}
                    </div>
                  </label>
                ))}
                {!filteredSubagents.length ? <div className="empty-state compact">没有命中的 subagent。</div> : null}
              </div>
            </div>
          ) : null}

          {activeTab === 'repos' ? (
            <div className="tab-panel skill-tab-shell">
              <div className="skill-tab-toolbar">
                <div className="repo-toolbar-main">
                  <input
                    value={repoQuery}
                    onChange={(event) => setRepoQuery(event.target.value)}
                    placeholder="搜索 repo id 或分支"
                  />
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
                <button type="button" onClick={() => void handleReposSave()} disabled={savingRepos}>
                  {savingRepos ? '保存中...' : '保存'}
                </button>
              </div>

              <div className="muted small repo-tab-hint">
                把 git 仓库 clone 到服务器的 `agents/repos/&lt;id&gt;`，这里勾选后会以软链挂到 workspace 根目录。多个 session 勾同一个 repo 会共享同一份工作区（注意并发改动会互相影响）。
              </div>

              {reposLoading ? <div className="info-banner">正在加载 repo 列表...</div> : null}

              <div className="current-skills-box">
                <div className="panel-title">当前挂载</div>
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
                  <div className="muted">当前没有挂载 repo。</div>
                )}
              </div>

              <div className="skill-picker-list">
                {filteredRepos.map((item) => (
                  <label key={item.id} className="skill-picker-item">
                    <input type="checkbox" checked={selectedRepoIDs.includes(item.id)} onChange={() => toggleRepo(item.id)} />
                    <div>
                      <div className="skill-picker-title">{item.id}</div>
                      <div className="muted mono">{item.branch ? `branch: ${item.branch}` : item.hasGit ? 'git repo' : '非 git 目录'}</div>
                    </div>
                  </label>
                ))}
                {!filteredRepos.length ? <div className="empty-state compact">agents/repos 下还没有可挂载的 repo。</div> : null}
              </div>
            </div>
          ) : null}

          {activeTab === 'memory' ? <FileEditorPanel api={api} sessionRef={sessionRef} kind="memory" /> : null}
          {activeTab === 'hooks' ? <FileEditorPanel api={api} sessionRef={sessionRef} kind="hooks" /> : null}
          {activeTab === 'agents' ? (
            <div className="tab-panel agents-panel-shell">
              <div className="settings-card">
                <div className="settings-card-header">
                  <div>
                    <h3>AGENTS.md</h3>
                    <p className="muted">
                      {agentsModeDraft === 'custom'
                        ? '当前 session 使用自定义 AGENTS.md。保存后会清空当前 active session，下一条普通消息按新指令创建新 session。'
                        : '当前 session 跟随 template 的 AGENTS.md。切到 custom 后就会和 template 脱钩。'}
                    </p>
                  </div>
                  {agentsFile ? (
                    <div className="inline-actions">
                      <button type="button" onClick={() => void handleAgentsSave()} disabled={savingAgents || !agentsDirty}>
                        {savingAgents ? '保存中...' : '保存 AGENTS'}
                      </button>
                    </div>
                  ) : null}
                </div>
                {agentsFile ? (
                  <div className="settings-grid">
                    <label>
                      <span>Role Mode</span>
                      <select value={agentsModeDraft} onChange={(event) => setAgentsModeDraft(normalizeAgentsMode(event.target.value as SessionAgentsMode))}>
                        <option value="template">template</option>
                        <option value="custom">custom</option>
                      </select>
                    </label>
                  </div>
                ) : null}
                {agentsFile ? <div className="detail-submeta mono">source: {agentsFile.resolvedPath}</div> : null}
              </div>
              {agentsLoading ? (
                <div className="empty-state">加载 AGENTS.md 中...</div>
              ) : agentsFile ? (
                <div className="monaco-panel agents-monaco-panel">
                  <Editor
                    height="100%"
                    language="markdown"
                    value={agentsContentDraft}
                    onChange={(value) => setAgentsContentDraft(value ?? '')}
                    theme="vs-dark"
                    options={{
                      readOnly: agentsModeDraft !== 'custom',
                      minimap: { enabled: false },
                      fontSize: 13,
                      automaticLayout: true,
                      scrollBeyondLastLine: false,
                    }}
                  />
                </div>
              ) : (
                <div className="empty-state">当前没有可展示的 AGENTS.md。</div>
              )}
            </div>
          ) : null}
        </>
      )}
      <ConfirmDialog
        open={deleteConfirm !== null}
        title={deleteConfirm?.kind === 'session' ? '删除 Session' : '删除定时任务'}
        description={
          deleteConfirm?.kind === 'session'
            ? `确认删除 session \`${sessionRef.conversationId}\` 吗？删除后下次访问会按默认流程重建。`
            : deleteConfirm?.kind === 'schedule'
              ? `确认删除定时任务 \`${deleteConfirm.job.ID}\` 吗？删除后会将其标记为 cancelled。`
              : ''
        }
        confirmLabel="确认删除"
        loading={deleteConfirmLoading}
        onCancel={() => setDeleteConfirm(null)}
        onConfirm={() => void handleDeleteConfirm()}
      />
      {scheduleEditor ? (
        <div className="modal-backdrop" role="presentation" onClick={closeScheduleEditor}>
          <div
            className="modal-card schedule-editor-modal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="schedule-editor-title"
            onClick={(event) => event.stopPropagation()}
          >
            <div className="modal-copy">
              <div className="eyebrow">Schedule Editor</div>
              <h3 id="schedule-editor-title">{scheduleEditor.kind === 'prompt' ? '编辑提示词' : '编辑提醒文本'}</h3>
              <p className="muted mono">job: {scheduleEditor.job.ID}</p>
            </div>
            <label className="schedule-editor-field">
              <span>{scheduleEditor.kind === 'prompt' ? '提示词内容' : '提醒文本内容'}</span>
              {readResolvedScheduleContentPath(scheduleEditor.job) ? (
                <div className="schedule-editor-source mono">file: {readResolvedScheduleContentPath(scheduleEditor.job)}</div>
              ) : null}
              <textarea
                className="schedule-editor-modal-textarea"
                rows={scheduleEditor.kind === 'prompt' ? 20 : 12}
                value={scheduleContentDrafts[scheduleEditor.job.ID] ?? readResolvedScheduleContent(scheduleEditor.job)}
                onChange={(event) =>
                  setScheduleContentDrafts((current) => ({
                    ...current,
                    [scheduleEditor.job.ID]: event.target.value,
                  }))
                }
              />
            </label>
            <div className="modal-actions">
              <button type="button" className="toolbar-button subtle" onClick={closeScheduleEditor} disabled={savingScheduleContentJobID === scheduleEditor.job.ID}>
                关闭
              </button>
              <button
                type="button"
                className="toolbar-button subtle"
                onClick={() =>
                  setScheduleContentDrafts((current) => ({
                    ...current,
                    [scheduleEditor.job.ID]: readResolvedScheduleContent(scheduleEditor.job),
                  }))
                }
                disabled={savingScheduleContentJobID === scheduleEditor.job.ID}
              >
                重置
              </button>
              <button
                type="button"
                onClick={async () => {
                  await handleScheduleContentSave(scheduleEditor.job)
                  setScheduleEditor(null)
                }}
                disabled={savingScheduleContentJobID === scheduleEditor.job.ID || !(scheduleContentDrafts[scheduleEditor.job.ID] ?? readResolvedScheduleContent(scheduleEditor.job)).trim()}
              >
                {savingScheduleContentJobID === scheduleEditor.job.ID ? '保存中...' : '保存内容'}
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  )
}
