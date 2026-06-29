export type AuthScope = 'project' | 'session'
export type SessionAgentsMode = 'template' | 'custom'

export interface SessionRef {
  provider: string
  conversationId: string
}

export interface MeResponse {
  scope: AuthScope
  provider?: string
  conversationId?: string
}

export interface SessionRuntimeSettings {
  replyMode: string
  historyTTLHours: number
  acceptGroupHumanMessagesWithoutMention: boolean
  acceptOtherBotMessages: boolean
  acceptInteractiveCardMessages: boolean
  remoteEnabled: boolean
}

export interface SessionSettings {
  version: number
  template: string
  agent: {
    backend: string
    agentsMode?: SessionAgentsMode
    opencodeConfig?: Record<string, unknown>
    opencodeHTTPTimeoutSeconds?: number
  }
  settings: SessionRuntimeSettings
  mounts: {
    skillIds: string[]
    subagentIds: string[]
    repoIds: string[]
  }
}

export interface SessionSummary extends SessionRef {
  displayName?: string
  chatMode?: string
  workspacePath: string
  template: string
  agentBackend: string
  activeSessionID: string
  replyMode: string
  skillIDs: string[]
  lastMessageAt: string
  updatedAt: string
}

export interface SessionDetail extends SessionRef {
  displayName?: string
  chatMode?: string
  workspacePath: string
  backend: string
  activeSessionId: string
  lastMessageAt: string
  settings: SessionSettings
  availableTemplates: string[]
  sessionToken: string
}

export type RemoteAgentRoute = 'disabled' | 'bot' | 'local'

export interface RemoteAgentStatus {
  enabled: boolean
  connected: boolean
  route: RemoteAgentRoute
  agentId: string
  sessionId: string
  title: string
}

export interface SessionTranscriptPart {
  type: string
  text: string
  reason: string
  tool: string
  toolStatus: string
  toolInputSummary: string
}

export interface SessionTranscriptMessage {
  id: string
  role: string
  createdAt: number
  parts: SessionTranscriptPart[]
}

export interface SessionTranscriptSessionOption {
  sessionId: string
  kind: string
  label: string
  topicKey?: string
}

export interface SessionTranscriptResponse {
  sessionId: string
  reset: boolean
  totalMessages: number
  latestMessageId: string
  contextTokens?: number
  contextInputTokens?: number
  availableSessions: SessionTranscriptSessionOption[]
  messages: SessionTranscriptMessage[]
}

export interface WorkspaceFileItem {
  path: string
  size: number
  updatedAt: string
  exists: boolean
}

export interface WorkspaceFileContent {
  path: string
  content: string
  exists: boolean
}

export interface SessionAgentsFile {
  path: string
  resolvedPath: string
  content: string
  mode: SessionAgentsMode
  readOnly: boolean
}

export interface RoleSummary {
  id: string
  path: string
  sessionCount: number
  updatedAt: string
}

export interface RoleDetail {
  id: string
  path: string
  settings: SessionSettings
  sessionCount: number
  updatedAt: string
  agentsFile: SessionAgentsFile
}

export interface SkillSummary {
  id: string
  title: string
  hasSkillFile: boolean
}

export interface SkillDetail extends SkillSummary {
  path: string
  updatedAt: string
  readOnly: boolean
}

export interface RepoSummary {
  id: string
  branch: string
  hasGit: boolean
}

export interface RepoBranches {
  id: string
  current: string
  branches: string[]
}

export interface SubagentSummary {
  id: string
  title: string
  description: string
  mode: string
  hasFile: boolean
}

export interface SubagentDetail extends SubagentSummary {
  path: string
  updatedAt: string
  readOnly: boolean
  content: string
}

export interface ScheduleJob {
  ID: string
  Provider: string
  ConversationID: string
  Route: string
  Payload: string
  RunAt: string
  Status: string
  CreatedAt: string
  UpdatedAt: string
  PromptTextResolved?: string
  NotifyTextResolved?: string
}

export interface ObservabilityEvent {
  time: string
  severity: string
  category: string
  provider?: string
  conversationId?: string
  summary: string
  detail?: string
}

export interface ObservabilityHealthItem {
  name: string
  ok: boolean
  error?: string
}

export interface ObservabilitySnapshot {
  startedAt: string
  now: string
  counters: Record<string, number>
  events: ObservabilityEvent[]
  health: {
    backend: ObservabilityHealthItem
    provider: ObservabilityHealthItem
  }
}

export type ScheduleContentKind = 'prompt' | 'notify'

export interface ScheduleCreateInput {
  runAt?: string
  cron?: string
  timezone?: string
  route: string
  payload: Record<string, unknown>
}

export interface ScheduleUpdateInput {
  kind: ScheduleContentKind
  content: string
}
