import type {
  MeResponse,
  ObservabilitySnapshot,
  RoleDetail,
  RoleSummary,
  ScheduleCreateInput,
  ScheduleJob,
  RepoSummary,
  RepoBranches,
  ScheduleUpdateInput,
  SkillDetail,
  SessionAgentsMode,
  SessionAgentsFile,
  RemoteAgentStatus,
  SessionDetail,
  SessionRef,
  SessionSettings,
  SessionModelsResponse,
  SessionSummary,
  SessionTranscriptResponse,
  SkillSummary,
  SubagentDetail,
  SubagentSummary,
  WorkspaceFileContent,
  WorkspaceFileItem,
} from './types'

const apiBaseURL = (import.meta.env.VITE_API_BASE_URL as string | undefined)?.trim() ?? ''

export class ApiClient {
  constructor(private readonly token: string) {}

  async getMe(): Promise<MeResponse> {
    return this.request('/api/v1/admin/me')
  }

  async getObservability(): Promise<ObservabilitySnapshot> {
    return this.request('/api/v1/admin/observability')
  }

  async clearObservability(): Promise<ObservabilitySnapshot> {
    return this.request('/api/v1/admin/observability', { method: 'DELETE' })
  }

  async listSessions(): Promise<SessionSummary[]> {
    const response = await this.request<{ items: SessionSummary[] }>('/api/v1/admin/sessions')
    return response.items
  }

  async resolveSessionDisplayNames(sessions: SessionRef[]): Promise<Array<SessionRef & { displayName?: string; chatMode?: string }>> {
    const response = await this.request<{ items: Array<SessionRef & { displayName?: string; chatMode?: string }> }>('/api/v1/admin/sessions/display-names', {
      method: 'POST',
      body: JSON.stringify({ sessions }),
    })
    return response.items
  }

  async getSessionDetail(ref: SessionRef): Promise<SessionDetail> {
    return this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}`)
  }

  async getRemoteStatus(ref: SessionRef): Promise<RemoteAgentStatus> {
    return this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/remote-status`)
  }

  async getSessionTranscript(ref: SessionRef, sessionId = '', afterMessageId = ''): Promise<SessionTranscriptResponse> {
    const query = new URLSearchParams()
    if (sessionId.trim()) {
      query.set('sessionId', sessionId.trim())
    }
    if (afterMessageId.trim()) {
      query.set('afterMessageId', afterMessageId.trim())
    }
    const suffix = query.toString() ? `?${query.toString()}` : ''
    return this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/transcript${suffix}`)
  }

  async getSessionModels(ref: SessionRef): Promise<SessionModelsResponse> {
    return this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/models`)
  }

  async getSessionAgents(ref: SessionRef): Promise<SessionAgentsFile> {
    return this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/agents`)
  }

  async updateSessionAgents(ref: SessionRef, mode: SessionAgentsMode, content?: string): Promise<SessionAgentsFile> {
    return this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/agents`, {
      method: 'PUT',
      body: JSON.stringify({ mode, content }),
    })
  }

  async updateSessionSettings(ref: SessionRef, settings: SessionSettings): Promise<SessionDetail> {
    await this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/settings`, {
      method: 'PUT',
      body: JSON.stringify({ settings }),
    })
    return this.getSessionDetail(ref)
  }

  async rotateSessionToken(ref: SessionRef): Promise<string> {
    const response = await this.request<{ sessionToken: string }>(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/token/rotate`,
      { method: 'POST' },
    )
    return response.sessionToken
  }

  async listFiles(ref: SessionRef, kind: 'memory' | 'hooks'): Promise<WorkspaceFileItem[]> {
    const response = await this.request<{ items: WorkspaceFileItem[] }>(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/files/${kind}`,
    )
    return response.items
  }

  async getFileContent(ref: SessionRef, kind: 'memory' | 'hooks', path: string): Promise<WorkspaceFileContent> {
    return this.request(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/files/${kind}/content?path=${encodeURIComponent(path)}`,
    )
  }

  async updateFileContent(ref: SessionRef, kind: 'memory' | 'hooks', path: string, content: string): Promise<void> {
    await this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/files/${kind}/content`, {
      method: 'PUT',
      body: JSON.stringify({ path, content }),
    })
  }

  async listSkills(): Promise<SkillSummary[]> {
    const response = await this.request<{ items: SkillSummary[] }>('/api/v1/admin/skills')
    return response.items
  }

  async listRepos(): Promise<RepoSummary[]> {
    const response = await this.request<{ items: RepoSummary[] }>('/api/v1/admin/repos')
    return response.items
  }

  async cloneRepo(url: string, id?: string): Promise<RepoSummary> {
    return this.request('/api/v1/admin/repos', {
      method: 'POST',
      body: JSON.stringify({ url, id: id ?? '' }),
    })
  }

  async listRepoBranches(id: string): Promise<RepoBranches> {
    return this.request(`/api/v1/admin/repos/${encodeURIComponent(id)}/branches`)
  }

  async pullRepo(id: string): Promise<{ id: string; branch: string; output: string }> {
    return this.request(`/api/v1/admin/repos/${encodeURIComponent(id)}/pull`, {
      method: 'POST',
    })
  }

  async checkoutRepoBranch(id: string, branch: string): Promise<RepoSummary> {
    return this.request(`/api/v1/admin/repos/${encodeURIComponent(id)}/checkout`, {
      method: 'POST',
      body: JSON.stringify({ branch }),
    })
  }

  async createSkill(id: string, content: string): Promise<SkillDetail> {
    return this.request('/api/v1/admin/skills', {
      method: 'POST',
      body: JSON.stringify({ id, content }),
    })
  }

  async listSubagents(): Promise<SubagentSummary[]> {
    const response = await this.request<{ items: SubagentSummary[] }>('/api/v1/admin/subagents')
    return response.items
  }

  async getSubagentDetail(subagentID: string): Promise<SubagentDetail> {
    return this.request(`/api/v1/admin/subagents/${encodeURIComponent(subagentID)}`)
  }

  async createSubagent(id: string, content: string): Promise<SubagentDetail> {
    return this.request('/api/v1/admin/subagents', {
      method: 'POST',
      body: JSON.stringify({ id, content }),
    })
  }

  async updateSubagent(subagentID: string, content: string): Promise<SubagentDetail> {
    return this.request(`/api/v1/admin/subagents/${encodeURIComponent(subagentID)}`, {
      method: 'PUT',
      body: JSON.stringify({ content }),
    })
  }

  async deleteSubagent(subagentID: string): Promise<void> {
    await this.request(`/api/v1/admin/subagents/${encodeURIComponent(subagentID)}`, { method: 'DELETE' })
  }

  async getSkillDetail(skillID: string): Promise<SkillDetail> {
    return this.request(`/api/v1/admin/skills/${encodeURIComponent(skillID)}`)
  }

  async listSkillFiles(skillID: string): Promise<WorkspaceFileItem[]> {
    const response = await this.request<{ items: WorkspaceFileItem[] }>(`/api/v1/admin/skills/${encodeURIComponent(skillID)}/files`)
    return response.items
  }

  async getSkillFileContent(skillID: string, path: string): Promise<WorkspaceFileContent> {
    return this.request(`/api/v1/admin/skills/${encodeURIComponent(skillID)}/files/content?path=${encodeURIComponent(path)}`)
  }

  async updateSkillFileContent(skillID: string, path: string, content: string): Promise<void> {
    await this.request(`/api/v1/admin/skills/${encodeURIComponent(skillID)}/files/content`, {
      method: 'PUT',
      body: JSON.stringify({ path, content }),
    })
  }

  async deleteSkill(skillID: string): Promise<void> {
    await this.request(`/api/v1/admin/skills/${encodeURIComponent(skillID)}`, { method: 'DELETE' })
  }

  async listRoles(): Promise<RoleSummary[]> {
    const response = await this.request<{ items: RoleSummary[] }>('/api/v1/admin/roles')
    return response.items
  }

  async getRoleDetail(roleID: string): Promise<RoleDetail> {
    return this.request(`/api/v1/admin/roles/${encodeURIComponent(roleID)}`)
  }

  async createRole(name: string, copyFrom: string): Promise<RoleDetail> {
    return this.request('/api/v1/admin/roles', {
      method: 'POST',
      body: JSON.stringify({ name, copyFrom }),
    })
  }

  async updateRole(roleID: string, settings: SessionSettings, agentsContent: string): Promise<RoleDetail> {
    return this.request(`/api/v1/admin/roles/${encodeURIComponent(roleID)}`, {
      method: 'PUT',
      body: JSON.stringify({ settings, agentsContent }),
    })
  }

  async deleteRole(roleID: string): Promise<void> {
    await this.request(`/api/v1/admin/roles/${encodeURIComponent(roleID)}`, { method: 'DELETE' })
  }

  async deleteSession(ref: SessionRef): Promise<void> {
    await this.request(`/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}`, {
      method: 'DELETE',
    })
  }

  async listScripts(): Promise<WorkspaceFileItem[]> {
    const response = await this.request<{ items: WorkspaceFileItem[] }>('/api/v1/admin/scripts')
    return response.items
  }

  async getScriptContent(path: string): Promise<WorkspaceFileContent> {
    return this.request(`/api/v1/admin/scripts/content?path=${encodeURIComponent(path)}`)
  }

  async updateScriptContent(path: string, content: string): Promise<void> {
    await this.request('/api/v1/admin/scripts/content', {
      method: 'PUT',
      body: JSON.stringify({ path, content }),
    })
  }

  async listSessionSkills(ref: SessionRef): Promise<SkillSummary[]> {
    const response = await this.request<{ items: SkillSummary[] }>(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-skills`,
    )
    return response.items
  }

  async createSessionSkill(ref: SessionRef, id: string, content: string): Promise<SkillDetail> {
    return this.request(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-skills`,
      {
        method: 'POST',
        body: JSON.stringify({ id, content }),
      },
    )
  }

  async uploadSessionSkill(ref: SessionRef, file: File): Promise<SkillDetail> {
    const form = new FormData()
    form.append('file', file)
    return this.request(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-skills/upload`,
      {
        method: 'POST',
        body: form,
      },
    )
  }

  async getSessionSkillDetail(ref: SessionRef, skillID: string): Promise<SkillDetail> {
    return this.request(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-skills/${encodeURIComponent(skillID)}`,
    )
  }

  async listSessionSkillFiles(ref: SessionRef, skillID: string): Promise<WorkspaceFileItem[]> {
    const response = await this.request<{ items: WorkspaceFileItem[] }>(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-skills/${encodeURIComponent(skillID)}/files`,
    )
    return response.items
  }

  async getSessionSkillFileContent(ref: SessionRef, skillID: string, path: string): Promise<WorkspaceFileContent> {
    return this.request(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-skills/${encodeURIComponent(skillID)}/files/content?path=${encodeURIComponent(path)}`,
    )
  }

  async updateSessionSkillFileContent(ref: SessionRef, skillID: string, path: string, content: string): Promise<void> {
    await this.request(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-skills/${encodeURIComponent(skillID)}/files/content`,
      {
        method: 'PUT',
        body: JSON.stringify({ path, content }),
      },
    )
  }

  async deleteSessionSkill(ref: SessionRef, skillID: string): Promise<void> {
    await this.request(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-skills/${encodeURIComponent(skillID)}`,
      { method: 'DELETE' },
    )
  }

  async exportSessionData(ref: SessionRef): Promise<Blob> {
    return this.requestBlob(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-data/export`,
    )
  }

  async importSessionData(ref: SessionRef, file: File): Promise<void> {
    const form = new FormData()
    form.append('file', file)
    await this.request(
      `/api/v1/admin/sessions/${encodeURIComponent(ref.provider)}/${encodeURIComponent(ref.conversationId)}/session-data/import`,
      {
        method: 'POST',
        body: form,
      },
    )
  }

  async listSessionSchedule(ref: SessionRef): Promise<ScheduleJob[]> {
    const query = new URLSearchParams({
      provider: ref.provider,
      conversationId: ref.conversationId,
    })
    const response = await this.request<ScheduleJob[] | null>(`/api/v1/schedule?${query.toString()}`)
    return Array.isArray(response) ? response : []
  }

  async createSessionSchedule(ref: SessionRef, input: ScheduleCreateInput): Promise<ScheduleJob> {
    return this.request('/api/v1/schedule', {
      method: 'POST',
      body: JSON.stringify({
        provider: ref.provider,
        conversationId: ref.conversationId,
        ...input,
      }),
    })
  }

  async cancelSessionSchedule(jobID: string): Promise<void> {
    await this.request('/api/v1/schedule/cancel', {
      method: 'POST',
      body: JSON.stringify({ jobId: jobID }),
    })
  }

  async updateSessionSchedule(jobID: string, input: ScheduleUpdateInput): Promise<ScheduleJob> {
    return this.request('/api/v1/schedule', {
      method: 'PUT',
      body: JSON.stringify({ jobId: jobID, ...input }),
    })
  }

  async uploadSkill(file: File): Promise<SkillSummary> {
    const form = new FormData()
    form.append('file', file)
    return this.request('/api/v1/admin/skills/upload', { method: 'POST', body: form })
  }

  private async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers)
    headers.set('Authorization', `Bearer ${this.token}`)
    if (!(init.body instanceof FormData) && !headers.has('Content-Type') && init.body !== undefined) {
      headers.set('Content-Type', 'application/json')
    }
    const response = await fetch(`${apiBaseURL}${path}`, { ...init, headers })
    if (!response.ok) {
      let message = response.statusText
      try {
        const body = (await response.json()) as { error?: string }
        if (body.error) {
          message = body.error
        }
      } catch {
        // keep status text
      }
      throw new Error(message)
    }
    return (await response.json()) as T
  }

  private async requestBlob(path: string, init: RequestInit = {}): Promise<Blob> {
    const headers = new Headers(init.headers)
    headers.set('Authorization', `Bearer ${this.token}`)
    const response = await fetch(`${apiBaseURL}${path}`, { ...init, headers })
    if (!response.ok) {
      let message = response.statusText
      try {
        const body = (await response.json()) as { error?: string }
        if (body.error) {
          message = body.error
        }
      } catch {
        // keep status text
      }
      throw new Error(message)
    }
    return response.blob()
  }
}
