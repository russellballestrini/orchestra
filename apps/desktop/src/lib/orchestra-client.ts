import type { APIErrorEnvelope, EventEnvelope, GlobalStats, Project, ProjectStats, SnapshotPayload, AgentConfig, DocItem, Issue, SessionDetail, SessionSummary } from '@/lib/orchestra-types'

/** Runtime connection configuration for the orchestrator backend. */
export type BackendConfig = {
  /** Base URL of the orchestrator HTTP server. */
  baseUrl: string
  /** Bearer token used for API authentication. */
  apiToken: string
  /** Optional map of MCP server names to their connection URIs. */
  mcpServers?: Record<string, string>
}

/** A tool exposed by an MCP (Model Context Protocol) server. */
export type MCPTool = {
  /** Tool name as registered with the MCP server. */
  name: string
  [key: string]: unknown
}

/** An MCP server registration managed by the orchestrator. */
export type MCPServer = {
  /** Server UUID, assigned by the backend. */
  id?: string
  /** Human-readable server name. */
  name: string
  /** Shell command used to launch the server process. */
  command: string
  [key: string]: unknown
}

/** Flattened issue record used in list views, merging Issue fields with runtime state. */
export type IssueListItem = Partial<Issue> & {
  id?: string
  issue_id?: string
  identifier?: string
  issue_identifier?: string
  state: string
  title?: string
  assigned_to_worker?: boolean
  due_at?: string
  error?: string
  last_message?: string
  session_id?: string
  [key: string]: unknown
}

/** A single historical event for an issue (e.g. state change, agent message). */
export type IssueHistoryEntry = {
  id?: string
  kind: string
  message?: string
  timestamp: string
  provider?: string
  input_tokens?: number
  output_tokens?: number
  [key: string]: unknown
}

/** A node in the project file tree returned by the backend. */
export type ProjectTreeNode = {
  name: string
  path: string
  is_dir: boolean
  children?: ProjectTreeNode[]
  [key: string]: unknown
}

/** A git commit record from a project repository. */
export type GitCommit = {
  hash?: string
  message: string
  author?: string
  date: string
  [key: string]: unknown
}

/** A single file entry from `git status` output. */
export type GitStatusEntry = {
  path: string
  status: string
  [key: string]: unknown
}

/** Result payload from a workspace migration plan or execution. */
export type WorkspaceMigrationResult = Record<string, unknown>

/** Result payload from a backend refresh operation. */
export type RefreshResult = Record<string, unknown>

/** Partial update fields for PATCH-ing an existing issue. */
export type IssueUpdatePayload = {
  state?: string
  assignee_id?: string
  provider?: string
  title?: string
  description?: string
  project_id?: string
  disabled_tools?: string[]
  [key: string]: unknown
}

/** Required fields for creating a new issue via the API. */
export type IssueCreatePayload = {
  title: string
  description: string
  state: string
  assignee_id: string
  project_id: string
  provider?: string
  disabled_tools?: string[]
}

/** Response from creating a GitHub pull request through the orchestrator. */
export type GitHubPRResult = {
  url: string
  number: number
  [key: string]: unknown
}

/** Health check response from the speech-to-text subsystem. */
export type STTHealth = {
  ready: boolean
  binary?: string
  model?: string
  language?: string
  reason?: string
}

/** Result of a speech-to-text transcription request. */
export type STTTranscriptionResult = {
  text: string
  elapsed_ms?: number
  language?: string
}

class APIError extends Error {
  code: string

  constructor(code: string, message: string) {
    super(message)
    this.code = code
  }
}

/**
 * Checks whether an error represents an unauthorized (401) response.
 * @param error - The caught error value to inspect.
 * @returns `true` if the error indicates an authentication failure.
 */
export function isUnauthorizedError(error: unknown): boolean {
  if (error instanceof APIError) {
    return error.code === 'unauthorized'
  }
  if (error instanceof Error) {
    return /^unauthorized:/.test(error.message.trim().toLowerCase())
  }
  if (typeof error === 'string') {
    return /^unauthorized:/.test(error.trim().toLowerCase())
  }
  return false
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function asString(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback
}

function asNumber(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

/**
 * Safely coerces an unknown value into a well-typed {@link SnapshotPayload}.
 * Missing or malformed fields are replaced with sensible defaults.
 * @param value - Raw parsed JSON (or any value) from the backend.
 * @returns A fully populated SnapshotPayload.
 */
export function normalizeSnapshotPayload(value: unknown): SnapshotPayload {
  const root = isRecord(value) ? value : {}
  const counts = isRecord(root.counts) ? root.counts : {}
  const totals = isRecord(root.codex_totals) ? root.codex_totals : {}
  const rateLimits = isRecord(root.rate_limits) ? root.rate_limits : null

  const running = Array.isArray(root.running)
    ? root.running
      .filter((entry): entry is Record<string, unknown> => isRecord(entry))
      .map((entry) => ({
        issue_id: asString(entry.issue_id),
        issue_identifier: asString(entry.issue_identifier),
        state: asString(entry.state),
        session_id: asString(entry.session_id, ''),
        turn_count: asNumber(entry.turn_count, 0),
        last_event: asString(entry.last_event, ''),
        last_message: asString(entry.last_message, ''),
        last_event_at: asString(entry.last_event_at, ''),
        started_at: asString(entry.started_at, ''),
      }))
    : []

  const retrying = Array.isArray(root.retrying)
    ? root.retrying
      .filter((entry): entry is Record<string, unknown> => isRecord(entry))
      .map((entry) => ({
        issue_id: asString(entry.issue_id),
        issue_identifier: asString(entry.issue_identifier),
        state: asString(entry.state),
        attempt: asNumber(entry.attempt, 0),
        due_at: asString(entry.due_at),
        error: asString(entry.error),
      }))
    : []

  return {
    generated_at: asString(root.generated_at, new Date().toISOString()),
    counts: {
      running: asNumber(counts.running, 0),
      retrying: asNumber(counts.retrying, 0),
    },
    running,
    retrying,
    codex_totals: {
      input_tokens: asNumber(totals.input_tokens, 0),
      output_tokens: asNumber(totals.output_tokens, 0),
      total_tokens: asNumber(totals.total_tokens, 0),
      seconds_running: asNumber(totals.seconds_running, 0),
    },
    rate_limits: rateLimits,
    mcp_servers: isRecord(root.mcp_servers) ? (root.mcp_servers as Record<string, string>) : undefined,
  }
}

/**
 * Safely coerces an unknown value into a well-typed {@link EventEnvelope}.
 * @param value - Raw parsed JSON from the SSE stream.
 * @param fallbackType - Event type to use when the value lacks a `type` field.
 * @returns A fully populated EventEnvelope.
 */
export function normalizeEventEnvelope(value: unknown, fallbackType = 'event'): EventEnvelope {
  const root = isRecord(value) ? value : {}
  return {
    type: asString(root.type, fallbackType),
    timestamp: asString(root.timestamp, new Date().toISOString()),
    data: isRecord(root.data) ? root.data : {},
  }
}

function buildHeaders(config: BackendConfig): HeadersInit {
  const headers: Record<string, string> = {
    Accept: 'application/json',
  }
  if (config.apiToken.trim() !== '') {
    headers.Authorization = `Bearer ${config.apiToken.trim()}`
  }
  return headers
}

async function requestJSON<T>(config: BackendConfig, path: string, init?: RequestInit, timeoutMs = 30000): Promise<T> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), timeoutMs)

  try {
    const response = await fetch(new URL(path, config.baseUrl).toString(), {
      ...init,
      signal: init?.signal ?? controller.signal,
      headers: {
        ...buildHeaders(config),
        ...(init?.headers ?? {}),
      },
    })

    if (!response.ok) {
      let parsed: APIErrorEnvelope | null = null
      try {
        parsed = (await response.json()) as APIErrorEnvelope
      } catch {
        parsed = null
      }
      if (parsed?.error?.code && parsed?.error?.message) {
        throw new APIError(parsed.error.code, parsed.error.message)
      }
      throw new APIError('request_failed', `${response.status} ${response.statusText}`)
    }

    // Handle cases where response might be empty (204 No Content) or other non-JSON but successful responses
    if (response.status === 204) {
      return {} as T
    }

    const text = await response.text()
    if (!text) {
      return {} as T
    }

    return JSON.parse(text) as T
  } finally {
    clearTimeout(timeout)
  }
}

/**
 * Applies a partial update (PATCH) to an existing issue.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier (e.g. "ORK-42").
 * @param updates - Fields to update on the issue.
 * @returns The updated issue record.
 */
export async function updateIssue(
  config: BackendConfig,
  issueIdentifier: string,
  updates: IssueUpdatePayload,
): Promise<IssueListItem> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  return requestJSON<IssueListItem>(config, `/api/v1/issues/${encodeURIComponent(normalized)}`, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(updates),
  })
}

/**
 * Permanently deletes an issue from the orchestrator.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 */
export async function deleteIssue(config: BackendConfig, issueIdentifier: string): Promise<void> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  await requestJSON<void>(config, `/api/v1/issues/${encodeURIComponent(normalized)}`, {
    method: 'DELETE',
  })
}

/**
 * Stops the active agent session for an issue.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 * @param provider - Optional provider filter when multiple providers are active.
 */
export async function stopIssueSession(config: BackendConfig, issueIdentifier: string, provider?: string): Promise<void> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  let path = `/api/v1/issues/${encodeURIComponent(normalized)}/session`
  if (provider) {
    path += `?provider=${encodeURIComponent(provider)}`
  }
  await requestJSON<void>(config, path, {
    method: 'DELETE',
  })
}

/**
 * Fetches the current runtime snapshot from the orchestrator.
 * @param config - Backend connection configuration.
 * @returns The normalized runtime snapshot.
 */
export async function fetchState(config: BackendConfig): Promise<SnapshotPayload> {
  const payload = await requestJSON<unknown>(config, '/api/v1/state')
  return normalizeSnapshotPayload(payload)
}

/**
 * Fetches a filtered list of issues from the orchestrator.
 * @param config - Backend connection configuration.
 * @param states - Optional array of state filters (e.g. ["RUNNING", "TRACKED"]).
 * @param projectID - Optional project ID filter.
 * @param assigneeID - Optional assignee ID filter.
 * @returns Array of matching issue list items.
 */
export async function fetchIssues(config: BackendConfig, states?: string[], projectID?: string, assigneeID?: string): Promise<IssueListItem[]> {
  const params = new URLSearchParams()
  if (states && states.length > 0) params.set('states', states.join(','))
  if (projectID) params.set('project_id', projectID)
  if (assigneeID) params.set('assignee_id', assigneeID)
  const payload = await requestJSON<{ issues: IssueListItem[] }>(config, `/api/v1/issues?${params.toString()}`)
  return payload.issues || []
}

/**
 * Creates a new issue in the orchestrator.
 * @param config - Backend connection configuration.
 * @param payload - Issue creation fields (title, description, state, assignee, project).
 * @returns The newly created issue record.
 */
export async function createIssue(
  config: BackendConfig,
  payload: IssueCreatePayload,
): Promise<IssueListItem> {
  return requestJSON<IssueListItem>(config, '/api/v1/issues', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
}

/**
 * Searches issues by a free-text query string.
 * @param config - Backend connection configuration.
 * @param query - Search query text.
 * @returns Array of matching issue list items.
 */
export async function searchIssues(config: BackendConfig, query: string): Promise<IssueListItem[]> {
  const params = new URLSearchParams({ q: query })
  const payload = await requestJSON<{ issues: IssueListItem[] }>(config, `/api/v1/search?${params.toString()}`)
  return payload.issues || []
}

/**
 * Fetches the list of registered agent names.
 * @param config - Backend connection configuration.
 * @returns Array of agent name strings.
 */
export async function fetchAgents(config: BackendConfig): Promise<string[]> {
  const payload = await requestJSON<{ agents: string[] }>(config, '/api/v1/agents')
  return payload.agents || []
}

/**
 * Fetches the global agent configuration (commands, provider, max turns).
 * @param config - Backend connection configuration.
 * @returns The current agent configuration object.
 */
export async function fetchAgentConfig(config: BackendConfig): Promise<{ commands: Record<string, string>; agent_provider: string; max_turns: number }> {
  return requestJSON<{ commands: Record<string, string>; agent_provider: string; max_turns: number }>(config, '/api/v1/config/agents')
}

/**
 * Partially updates the global agent configuration.
 * @param config - Backend connection configuration.
 * @param updates - Fields to patch (e.g. max_turns).
 * @returns The updated agent configuration.
 */
export async function patchAgentConfig(config: BackendConfig, updates: { max_turns?: number }): Promise<{ commands: Record<string, string>; agent_provider: string; max_turns: number }> {
  return requestJSON<{ commands: Record<string, string>; agent_provider: string; max_turns: number }>(config, '/api/v1/config/agents', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(updates),
  })
}

/**
 * Triggers a full state refresh on the orchestrator backend.
 * @param config - Backend connection configuration.
 * @returns The refresh result payload.
 */
export async function postRefresh(config: BackendConfig): Promise<RefreshResult> {
  return requestJSON<RefreshResult>(config, '/api/v1/refresh', {
    method: 'POST',
  })
}

/**
 * Fetches a dry-run workspace migration plan between two paths.
 * @param config - Backend connection configuration.
 * @param from - Source workspace path.
 * @param to - Destination workspace path.
 * @returns The migration plan result.
 */
export async function fetchWorkspaceMigrationPlan(
  config: BackendConfig,
  from: string,
  to: string,
): Promise<WorkspaceMigrationResult> {
  const query = new URLSearchParams()
  if (from.trim() !== '') query.set('from', from.trim())
  if (to.trim() !== '') query.set('to', to.trim())
  const suffix = query.toString() ? `?${query.toString()}` : ''
  return requestJSON<WorkspaceMigrationResult>(config, `/api/v1/workspace/migration/plan${suffix}`)
}

/**
 * Executes a workspace migration between two paths.
 * @param config - Backend connection configuration.
 * @param from - Source workspace path.
 * @param to - Destination workspace path.
 * @returns The migration execution result.
 */
export async function applyWorkspaceMigration(
  config: BackendConfig,
  from: string,
  to: string,
): Promise<WorkspaceMigrationResult> {
  return requestJSON<WorkspaceMigrationResult>(config, '/api/v1/workspace/migrate', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      from: from.trim(),
      to: to.trim(),
      dry_run: false,
    }),
  })
}

/**
 * Fetches all registered projects from the orchestrator.
 * @param config - Backend connection configuration.
 * @returns Array of project records.
 */
export async function fetchProjects(config: BackendConfig): Promise<Project[]> {
  const data = await requestJSON<Project[]>(config, '/api/v1/projects')
  return data || []
}

/**
 * Fetches aggregate statistics for a specific project.
 * @param config - Backend connection configuration.
 * @param projectID - The project UUID.
 * @returns Project-level statistics (sessions, tokens, etc.).
 */
export async function fetchProjectStats(config: BackendConfig, projectID: string): Promise<ProjectStats> {
  return requestJSON<ProjectStats>(config, `/api/v1/projects/${encodeURIComponent(projectID)}`)
}

/**
 * Fetches platform-wide warehouse analytics (token totals, provider usage, recent sessions).
 * @param config - Backend connection configuration.
 * @returns Global statistics payload.
 */
export async function fetchWarehouseStats(config: BackendConfig): Promise<GlobalStats> {
  return requestJSON<GlobalStats>(config, '/api/v1/warehouse/stats')
}

/**
 * Registers a new project by its filesystem root path.
 * @param config - Backend connection configuration.
 * @param rootPath - Absolute filesystem path of the project root.
 * @returns The newly created project record.
 */
export async function createProject(config: BackendConfig, rootPath: string): Promise<Project> {
  return requestJSON<Project>(config, '/api/v1/projects', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ root_path: rootPath }),
  })
}

/**
 * Fetches full details for a single issue.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 * @returns The detailed issue record.
 */
export async function fetchIssueDetail(config: BackendConfig, issueIdentifier: string): Promise<IssueListItem> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  return requestJSON<IssueListItem>(config, `/api/v1/issues/${encodeURIComponent(normalized)}`)
}

/**
 * Fetches raw session log text for an issue.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 * @param provider - Optional provider filter.
 * @returns Raw log content as a string.
 */
export async function fetchIssueLogs(config: BackendConfig, issueIdentifier: string, provider?: string): Promise<string> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  const url = new URL(`/api/v1/issues/${encodeURIComponent(normalized)}/logs`, config.baseUrl)
  if (provider) url.searchParams.set('provider', provider)
  
  const response = await fetch(url.toString(), {
    headers: buildHeaders(config),
  })

  if (!response.ok) {
    throw new APIError('logs_not_found', 'failed to fetch issue logs')
  }

  return response.text()
}

/**
 * Fetches the event history for an issue.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 * @returns Array of historical event entries.
 */
export async function fetchIssueHistory(config: BackendConfig, issueIdentifier: string): Promise<IssueHistoryEntry[]> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  const data = await requestJSON<{ history: IssueHistoryEntry[] }>(config, `/api/v1/issues/${encodeURIComponent(normalized)}/history`)
  return data.history || []
}

/**
 * Fetches the workspace git diff for an issue.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 * @param provider - Optional provider filter.
 * @returns Unified diff output as a string.
 */
export async function fetchIssueDiff(config: BackendConfig, issueIdentifier: string, provider?: string): Promise<string> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  const url = new URL(`/api/v1/issues/${encodeURIComponent(normalized)}/diff`, config.baseUrl)
  if (provider) url.searchParams.set('provider', provider)

  const response = await fetch(url.toString(), {
    headers: buildHeaders(config),
  })

  if (!response.ok) {
    throw new APIError('diff_failed', 'failed to fetch workspace diff')
  }

  return response.text()
}

/**
 * Fetches the list of artifact paths produced by an issue's agent session.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 * @param provider - Optional provider filter.
 * @returns Array of relative artifact file paths.
 */
export async function fetchArtifacts(config: BackendConfig, issueIdentifier: string, provider?: string): Promise<string[]> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  const url = new URL(`/api/v1/issues/${encodeURIComponent(normalized)}/artifacts`, config.baseUrl)
  if (provider) url.searchParams.set('provider', provider)

  const payload = await requestJSON<{ artifacts: string[] }>(config, url.pathname + url.search)
  return payload.artifacts || []
}

/**
 * Fetches the text content of a single artifact file.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 * @param relPath - Relative path of the artifact within the workspace.
 * @param provider - Optional provider filter.
 * @returns The artifact file content as a string.
 */
export async function fetchArtifactContent(config: BackendConfig, issueIdentifier: string, relPath: string, provider?: string): Promise<string> {
  const normalized = issueIdentifier.trim()
  if (normalized === '') {
    throw new APIError('invalid_request', 'issue identifier is required')
  }
  const url = new URL(`/api/v1/issues/${encodeURIComponent(normalized)}/artifacts/${encodeURIComponent(relPath)}`, config.baseUrl)
  if (provider) url.searchParams.set('provider', provider)

  const response = await fetch(url.toString(), {
    headers: buildHeaders(config),
  })

  if (!response.ok) {
    throw new APIError('fetch_failed', 'failed to fetch artifact content')
  }

  return response.text()
}

/**
 * Converts an unknown error into a user-friendly display string.
 * @param error - The caught error value.
 * @returns A human-readable error message.
 */
export function toDisplayError(error: unknown): string {
  if (error instanceof APIError) {
    return `${error.code}: ${error.message}`
  }
  if (error instanceof Error) {
    return error.message
  }
  return 'unexpected error'
}

/**
 * Fetches session summaries, optionally filtered by project.
 * @param config - Backend connection configuration.
 * @param projectId - Optional project ID to filter sessions.
 * @returns Array of session summary records.
 */
export async function fetchSessions(config: BackendConfig, projectId?: string): Promise<SessionSummary[]> {
  const url = projectId ? `/api/v1/sessions?project_id=${projectId}` : '/api/v1/sessions'
  const data = await requestJSON<SessionSummary[]>(config, url)
  return data || []
}

/**
 * Deletes a project registration from the orchestrator.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID to delete.
 */
export async function deleteProject(config: BackendConfig, projectId: string): Promise<void> {
  return requestJSON<void>(config, `/api/v1/projects/${projectId}`, {
    method: 'DELETE',
  })
}

/**
 * Triggers a refresh/rescan of a project's workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID to refresh.
 */
export async function refreshProject(config: BackendConfig, projectId: string): Promise<void> {
  return requestJSON<void>(config, `/api/v1/projects/${projectId}/refresh`, {
    method: 'POST',
  })
}

/**
 * Fetches the file tree for a project, optionally rooted at a sub-path.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param path - Optional sub-directory path to scope the tree.
 * @returns Array of tree nodes (files and directories).
 */
export async function fetchProjectTree(config: BackendConfig, projectId: string, path?: string): Promise<ProjectTreeNode[]> {
  const query = path ? `?path=${encodeURIComponent(path)}` : ''
  const data = await requestJSON<ProjectTreeNode[]>(config, `/api/v1/projects/${projectId}/tree${query}`)
  return data || []
}

/**
 * Fetches the text content of a single file within a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param path - Relative file path within the project.
 * @returns The file content as a string.
 */
export async function fetchProjectFileContent(config: BackendConfig, projectId: string, path: string): Promise<string> {
  const response = await fetch(`${config.baseUrl}/api/v1/projects/${encodeURIComponent(projectId)}/file?path=${encodeURIComponent(path)}`, {
    headers: buildHeaders(config),
  })
  if (!response.ok) {
    throw new Error(`failed to fetch project file content (${response.status} ${response.statusText})`)
  }
  return response.text()
}

/**
 * Fetches the git commit history for a project.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @returns Array of git commit records.
 */
export async function fetchProjectGitHistory(config: BackendConfig, projectId: string): Promise<GitCommit[]> {
  const data = await requestJSON<GitCommit[]>(config, `/api/v1/projects/${projectId}/git`)
  return data || []
}

/**
 * Fetches the current git status (modified/untracked files) for a project.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @returns Array of git status entries.
 */
export async function fetchProjectGitStatus(config: BackendConfig, projectId: string): Promise<GitStatusEntry[]> {
  const data = await requestJSON<GitStatusEntry[]>(config, `/api/v1/projects/${projectId}/git/status`)
  return data || []
}

/**
 * Fetches the git diff for a project, optionally at a specific commit hash.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param hash - Optional commit hash to diff against.
 * @returns Unified diff output as a string.
 */
export async function fetchProjectGitDiff(config: BackendConfig, projectId: string, hash?: string): Promise<string> {
  const query = hash ? `?hash=${encodeURIComponent(hash)}` : ''
  const response = await fetch(`${config.baseUrl}/api/v1/projects/${encodeURIComponent(projectId)}/git/diff${query}`, {
    headers: buildHeaders(config),
  })

  if (!response.ok) {
    throw new APIError('diff_failed', 'failed to fetch project git diff')
  }

  return response.text()
}

/**
 * Fetches full detail for a specific agent session.
 * @param config - Backend connection configuration.
 * @param sessionId - The session UUID.
 * @returns The full session detail record including events.
 */
export async function fetchSessionDetail(config: BackendConfig, sessionId: string): Promise<SessionDetail> {
  return requestJSON<SessionDetail>(config, `/api/v1/sessions/${sessionId}`)
}

/**
 * Creates a git commit in a project workspace with the given message.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param message - Commit message text.
 */
export async function gitCommit(config: BackendConfig, projectId: string, message: string): Promise<void> {
  await requestJSON<void>(config, `/api/v1/projects/${projectId}/git/commit`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message }),
  })
}

/**
 * Pushes commits to a remote git repository.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param remote - Remote name (defaults to "origin").
 * @param branch - Branch name (defaults to "main").
 */
export async function gitPush(config: BackendConfig, projectId: string, remote = 'origin', branch = 'main'): Promise<void> {
  await requestJSON<void>(config, `/api/v1/projects/${projectId}/git/push`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ remote, branch }),
  })
}

/**
 * Pulls commits from a remote git repository.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param remote - Remote name (defaults to "origin").
 * @param branch - Branch name (defaults to "main").
 */
export async function gitPull(config: BackendConfig, projectId: string, remote = 'origin', branch = 'main'): Promise<void> {
  await requestJSON<void>(config, `/api/v1/projects/${projectId}/git/pull`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ remote, branch }),
  })
}

/**
 * Creates a GitHub pull request for an issue through the orchestrator.
 * @param config - Backend connection configuration.
 * @param issueIdentifier - Human-readable issue identifier.
 * @param payload - PR creation fields (title, body, head/base branches, optional owner/repo/token).
 * @returns The created PR result with URL and number.
 */
export async function createGitHubPR(
  config: BackendConfig,
  issueIdentifier: string,
  payload: { title: string; body: string; head: string; base: string; owner?: string; repo?: string; token?: string }
): Promise<GitHubPRResult> {
  return requestJSON<GitHubPRResult>(config, `/api/v1/issues/${encodeURIComponent(issueIdentifier)}/pr`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
}

/**
 * Replaces the global agent configuration (commands map and default provider).
 * @param config - Backend connection configuration.
 * @param payload - The full agent configuration to set.
 */
export async function updateAgentConfig(config: BackendConfig, payload: { commands: Record<string, string>, agent_provider: string }): Promise<void> {
  await requestJSON<void>(config, '/api/v1/config/agents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
}
/**
 * Fetches agent configuration files (CLAUDE.md, skills, etc.), optionally filtered by project.
 * @param config - Backend connection configuration.
 * @param projectID - Optional project ID to scope the query.
 * @returns Array of agent configuration records.
 */
export async function fetchAgentConfigs(config: BackendConfig, projectID?: string): Promise<AgentConfig[]> {
  const url = projectID ? `/api/v1/config/agents/items?project_id=${encodeURIComponent(projectID)}` : '/api/v1/config/agents/items'
  const data = await requestJSON<{ configs: AgentConfig[] }>(config, url)
  return data.configs || []
}

/**
 * Updates the content of a specific agent configuration file by its filesystem path.
 * @param config - Backend connection configuration.
 * @param path - Absolute path to the configuration file.
 * @param content - New file content to write.
 */
export async function updateAgentConfigByPath(config: BackendConfig, path: string, content: string): Promise<void> {
  await requestJSON<void>(config, '/api/v1/config/agents/items', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, content }),
  })
}

/**
 * Creates a new agent resource file (skill, hook, etc.) on disk.
 * @param config - Backend connection configuration.
 * @param payload - Resource creation fields (provider, type, name, scope, optional project ID).
 * @returns The filesystem path of the newly created resource.
 */
export async function createAgentResource(config: BackendConfig, payload: { provider: string, type: string, name: string, scope: string, project_id?: string }): Promise<{ path: string }> {
  return requestJSON<{ path: string }>(config, '/api/v1/config/agents/new', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
}

/**
 * Fetches the documentation tree structure.
 * @param config - Backend connection configuration.
 * @returns Array of doc tree items (files and folders).
 */
export async function fetchDocs(config: BackendConfig): Promise<DocItem[]> {
  const data = await requestJSON<{ docs: DocItem[] }>(config, '/api/v1/docs')
  return data.docs || []
}

/**
 * Fetches the rendered content of a documentation file.
 * @param config - Backend connection configuration.
 * @param path - Relative path of the document within the docs tree.
 * @returns The document content as a string.
 */
export async function fetchDocContent(config: BackendConfig, path: string): Promise<string> {
  const encodedPath = path.split('/').map(encodeURIComponent).join('/')
  const response = await fetch(new URL(`/api/v1/docs/${encodedPath}`, config.baseUrl).toString(), {
    headers: buildHeaders(config),
  })
  if (!response.ok) {
    throw new Error(`failed to fetch doc content: ${response.statusText}`)
  }
  return response.text()
}

/**
 * Fetches all tools exposed by registered MCP servers.
 * @param config - Backend connection configuration.
 * @returns Array of MCP tool records.
 */
export async function fetchMCPTools(config: BackendConfig): Promise<MCPTool[]> {
  const data = await requestJSON<{ tools: MCPTool[] }>(config, '/api/v1/mcp/tools')
  return data.tools || []
}

/**
 * Fetches all registered MCP servers.
 * @param config - Backend connection configuration.
 * @returns Array of MCP server records.
 */
export async function fetchMCPServers(config: BackendConfig): Promise<MCPServer[]> {
  const data = await requestJSON<{ servers: MCPServer[] }>(config, '/api/v1/mcp/servers')
  return data.servers || []
}

/**
 * Registers a new MCP server with the orchestrator.
 * @param config - Backend connection configuration.
 * @param name - Display name for the server.
 * @param command - Shell command to launch the server process.
 * @returns The created MCP server record.
 */
export async function createMCPServer(config: BackendConfig, name: string, command: string): Promise<MCPServer> {
  return requestJSON<MCPServer>(config, '/api/v1/mcp/servers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, command }),
  })
}

/**
 * Deletes an MCP server registration.
 * @param config - Backend connection configuration.
 * @param id - The MCP server UUID to delete.
 */
export async function deleteMCPServer(config: BackendConfig, id: string): Promise<void> {
  await requestJSON<void>(config, `/api/v1/mcp/servers/${id}`, {
    method: 'DELETE',
  })
}

/** A GitHub issue retrieved through the orchestrator's GitHub integration. */
export type GitHubIssue = {
  number: number
  title: string
  body: string
  state: string
  html_url: string
  labels: { name: string }[]
  created_at?: string
  updated_at?: string
  user?: { login: string; avatar_url: string }
}

/** A GitHub pull request retrieved through the orchestrator's GitHub integration. */
export type GitHubPR = {
  number: number
  title: string
  body: string
  state: string
  html_url: string
  diff_url: string
  head: { ref: string; label: string }
  base: { ref: string; label: string }
  user: { login: string; avatar_url: string }
  created_at: string
  merged_at: string | null
}

/** Current branch and list of all branches in a project repository. */
export type GitBranches = {
  current: string
  branches: string[]
}

/**
 * Fetches GitHub issues for a project via the orchestrator's GitHub integration.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param state - Issue state filter (defaults to "open").
 * @returns Array of GitHub issue records.
 */
export async function fetchProjectGitHubIssues(config: BackendConfig, projectId: string, state: string = 'open'): Promise<GitHubIssue[]> {
  return requestJSON<GitHubIssue[]>(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/issues?state=${state}`)
}

/**
 * Fetches the current branch and all branch names for a project.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @returns The current branch and list of all branches.
 */
export async function fetchProjectGitBranches(config: BackendConfig, projectId: string): Promise<GitBranches> {
  return requestJSON<GitBranches>(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/branches`)
}

/**
 * Fetches GitHub pull requests for a project.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @returns Array of GitHub PR records.
 */
export async function fetchProjectGitHubPulls(config: BackendConfig, projectId: string): Promise<GitHubPR[]> {
  return requestJSON<GitHubPR[]>(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/pulls`)
}

/**
 * Fetches the unified diff for a specific GitHub pull request.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param number - PR number.
 * @returns The PR diff as a string.
 */
export async function fetchProjectGitHubPullDiff(config: BackendConfig, projectId: string, number: number): Promise<string> {
  const response = await fetch(`${config.baseUrl}/api/v1/projects/${encodeURIComponent(projectId)}/github/pulls/${encodeURIComponent(String(number))}/diff`, {
    headers: buildHeaders(config),
  })
  return response.text()
}

/**
 * Creates a new GitHub issue for a project.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param payload - Issue creation fields (title, body, optional labels).
 * @returns The created GitHub issue record.
 */
export async function createProjectGitHubIssue(config: BackendConfig, projectId: string, payload: { title: string; body: string; labels?: string[] }): Promise<GitHubIssue> {
  return requestJSON<GitHubIssue>(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/issues`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
}

/**
 * Updates an existing GitHub issue for a project.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param number - GitHub issue number.
 * @param payload - Fields to update (title, body, state).
 * @returns The updated GitHub issue record.
 */
export async function updateProjectGitHubIssue(config: BackendConfig, projectId: string, number: number, payload: { title?: string; body?: string; state?: string }): Promise<GitHubIssue> {
  return requestJSON<GitHubIssue>(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/issues/${number}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
}

/**
 * Creates a new GitHub pull request for a project.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param payload - PR creation fields (title, body, head branch, base branch).
 * @returns The created PR URL and number.
 */
export async function createProjectGitHubPull(config: BackendConfig, projectId: string, payload: { title: string; body: string; head: string; base: string }): Promise<{ html_url: string; number: number }> {
  return requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/pulls`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
}

/**
 * Disconnects a project from its GitHub repository integration.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 */
export async function disconnectProjectGitHub(config: BackendConfig, projectId: string): Promise<void> {
  await requestJSON<void>(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/disconnect`, {
    method: 'POST',
  })
}

/** An MCP server configured for a specific provider (e.g. Claude, Codex). */
export type ProviderMCPServer = {
    name: string
    command: string
    args?: string[]
    url?: string
    env?: Record<string, string>
    type?: string  // "stdio" | "http"
    enabled: boolean
}

/**
 * Fetches MCP servers configured for a specific provider.
 * @param config - Backend connection configuration.
 * @param provider - Provider name (e.g. "CLAUDE").
 * @param projectId - Optional project ID to scope the query.
 * @returns Array of provider-specific MCP server records.
 */
export async function fetchProviderMCPServers(config: BackendConfig, provider: string, projectId?: string): Promise<ProviderMCPServer[]> {
    const params = projectId ? `?project_id=${encodeURIComponent(projectId)}` : ''
    return requestJSON<ProviderMCPServer[]>(config, `/api/v1/agents/${encodeURIComponent(provider)}/mcp${params}`)
}

/**
 * Adds an MCP server to a specific provider's configuration.
 * @param config - Backend connection configuration.
 * @param provider - Provider name.
 * @param server - Server definition (name, command, optional args).
 */
export async function addProviderMCPServer(config: BackendConfig, provider: string, server: { name: string; command: string; args?: string[] }): Promise<void> {
    await requestJSON(config, `/api/v1/agents/${encodeURIComponent(provider)}/mcp`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(server),
    })
}

/**
 * Removes an MCP server from a specific provider's configuration.
 * @param config - Backend connection configuration.
 * @param provider - Provider name.
 * @param name - Name of the MCP server to remove.
 */
export async function deleteProviderMCPServer(config: BackendConfig, provider: string, name: string): Promise<void> {
    await requestJSON(config, `/api/v1/agents/${encodeURIComponent(provider)}/mcp/${encodeURIComponent(name)}`, { method: 'DELETE' })
}

/** Permission and tool access configuration for a provider. */
export type ProviderPermissions = {
    approval_mode: string
    allow: string[]
    deny: string[]
    ask: string[]
    allowed_tools?: string[]
    enabled_plugins?: string[]
    sandbox?: string
}

/** Model selection and inference parameters for a provider. */
export type ProviderModelConfig = {
    model: string
    effort: string
    temperature: number | null
}

/** A lifecycle hook configured for a provider (e.g. pre-run, post-run scripts). */
export type ProviderHook = {
    event: string
    matcher?: string
    type: string
    command: string
    timeout?: number
}

/**
 * Fetches the permission configuration for a specific provider.
 * @param config - Backend connection configuration.
 * @param provider - Provider name.
 * @param projectId - Optional project ID for project-scoped permissions.
 * @returns The provider's permission settings.
 */
export async function fetchProviderPermissions(config: BackendConfig, provider: string, projectId?: string): Promise<ProviderPermissions> {
    const params = projectId ? `?project_id=${encodeURIComponent(projectId)}` : ''
    return requestJSON<ProviderPermissions>(config, `/api/v1/agents/${encodeURIComponent(provider)}/permissions${params}`)
}

/**
 * Updates the permission configuration for a specific provider.
 * @param config - Backend connection configuration.
 * @param provider - Provider name.
 * @param perms - The full permissions object to set.
 */
export async function updateProviderPermissions(config: BackendConfig, provider: string, perms: ProviderPermissions): Promise<void> {
    await requestJSON(config, `/api/v1/agents/${encodeURIComponent(provider)}/permissions`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(perms),
    })
}

/**
 * Fetches the model configuration for a specific provider.
 * @param config - Backend connection configuration.
 * @param provider - Provider name.
 * @returns The provider's model settings (model name, effort, temperature).
 */
export async function fetchProviderModel(config: BackendConfig, provider: string): Promise<ProviderModelConfig> {
    return requestJSON<ProviderModelConfig>(config, `/api/v1/agents/${encodeURIComponent(provider)}/model`)
}

/**
 * Updates the model configuration for a specific provider.
 * @param config - Backend connection configuration.
 * @param provider - Provider name.
 * @param model - The model configuration to set.
 */
export async function updateProviderModel(config: BackendConfig, provider: string, model: ProviderModelConfig): Promise<void> {
    await requestJSON(config, `/api/v1/agents/${encodeURIComponent(provider)}/model`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(model),
    })
}

/**
 * Fetches lifecycle hooks configured for a specific provider.
 * @param config - Backend connection configuration.
 * @param provider - Provider name.
 * @returns Array of hook definitions.
 */
export async function fetchProviderHooks(config: BackendConfig, provider: string): Promise<ProviderHook[]> {
    return requestJSON<ProviderHook[]>(config, `/api/v1/agents/${encodeURIComponent(provider)}/hooks`)
}

/**
 * Replaces all lifecycle hooks for a specific provider.
 * @param config - Backend connection configuration.
 * @param provider - Provider name.
 * @param hooks - The full array of hooks to set.
 */
export async function updateProviderHooks(config: BackendConfig, provider: string, hooks: ProviderHook[]): Promise<void> {
    await requestJSON(config, `/api/v1/agents/${encodeURIComponent(provider)}/hooks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(hooks),
    })
}

/**
 * Checks the health status of the speech-to-text subsystem.
 * @param config - Backend connection configuration.
 * @returns STT health status including readiness and model info.
 */
export async function fetchSTTHealth(config: BackendConfig): Promise<STTHealth> {
  return requestJSON<STTHealth>(config, '/api/v1/stt/health')
}

/**
 * Sends an audio recording to the backend for speech-to-text transcription.
 * @param config - Backend connection configuration.
 * @param audio - Audio blob to transcribe (typically WebM format).
 * @param language - Optional language hint for the transcription model.
 * @returns Transcription result with text and timing info.
 */
export async function transcribeAudio(config: BackendConfig, audio: Blob, language?: string): Promise<STTTranscriptionResult> {
  const form = new FormData()
  form.append('audio', audio, 'recording.webm')
  if (language && language.trim() !== '') {
    form.append('language', language.trim())
  }

  return requestJSON<STTTranscriptionResult>(config, '/api/v1/stt/transcribe', {
    method: 'POST',
    body: form,
  })
}

/**
 * Checks out a git branch in a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param branch - Branch name to check out.
 */
export async function gitCheckout(config: BackendConfig, projectId: string, branch: string): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/checkout`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ branch }) })
}

/**
 * Creates a new git branch in a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param name - Name of the new branch.
 */
export async function gitCreateBranch(config: BackendConfig, projectId: string, name: string): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/branches`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) })
}

/**
 * Deletes a git branch from a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param branch - Branch name to delete.
 */
export async function gitDeleteBranch(config: BackendConfig, projectId: string, branch: string): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/branches/${encodeURIComponent(branch)}`, { method: 'DELETE' })
}

/**
 * Stages files for the next git commit in a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param files - Array of file paths to stage.
 */
export async function gitStage(config: BackendConfig, projectId: string, files: string[]): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/stage`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ files }) })
}

/**
 * Unstages files from the git index in a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param files - Array of file paths to unstage.
 */
export async function gitUnstage(config: BackendConfig, projectId: string, files: string[]): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/unstage`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ files }) })
}

/**
 * Merges a branch into the current branch in a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param branch - Branch name to merge.
 */
export async function gitMerge(config: BackendConfig, projectId: string, branch: string): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/merge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ branch }),
  })
}

/**
 * Stashes uncommitted changes in a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 */
export async function gitStash(config: BackendConfig, projectId: string): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/stash`, { method: 'POST' })
}

/**
 * Pops the most recent stash entry in a project workspace.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 */
export async function gitStashPop(config: BackendConfig, projectId: string): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/git/stash/pop`, { method: 'POST' })
}

/**
 * Fetches reviews for a GitHub pull request.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param prNumber - The PR number.
 * @returns Array of review objects.
 */
export async function fetchPRReviews(config: BackendConfig, projectId: string, prNumber: number): Promise<unknown[]> {
  return requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/pulls/${prNumber}/reviews`)
}

/**
 * Submits a review on a GitHub pull request.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param prNumber - The PR number.
 * @param body - Review body text.
 * @param event - Review event type (e.g. "APPROVE", "REQUEST_CHANGES", "COMMENT").
 */
export async function submitPRReview(config: BackendConfig, projectId: string, prNumber: number, body: string, event: string): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/pulls/${prNumber}/reviews`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ body, event }) })
}

/**
 * Merges a GitHub pull request.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param prNumber - The PR number.
 * @param method - Merge method (e.g. "merge", "squash", "rebase").
 */
export async function mergePR(config: BackendConfig, projectId: string, prNumber: number, method: string): Promise<void> {
  await requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/pulls/${prNumber}/merge`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ method }) })
}

/**
 * Fetches comments on a GitHub pull request.
 * @param config - Backend connection configuration.
 * @param projectId - The project UUID.
 * @param prNumber - The PR number.
 * @returns Array of comment objects.
 */
export async function fetchPRComments(config: BackendConfig, projectId: string, prNumber: number): Promise<unknown[]> {
  return requestJSON(config, `/api/v1/projects/${encodeURIComponent(projectId)}/github/pulls/${prNumber}/comments`)
}

// --- Unsandbox Configuration ---

/** Configuration state for the Unsandbox remote execution integration. */
export type UnsandboxConfig = {
  configured: boolean
  public_key: string
  has_secret: boolean
}

/** Runtime status of the Unsandbox integration (validity, errors). */
export type UnsandboxStatus = {
  configured: boolean
  valid?: boolean
  error?: string
  key_info?: Record<string, unknown>
}

/**
 * Fetches the current Unsandbox API key configuration.
 * @param config - Backend connection configuration.
 * @returns The Unsandbox configuration state.
 */
export async function fetchUnsandboxConfig(config: BackendConfig): Promise<UnsandboxConfig> {
  return requestJSON<UnsandboxConfig>(config, '/api/v1/config/unsandbox')
}

/**
 * Saves Unsandbox API keys to the backend configuration.
 * @param config - Backend connection configuration.
 * @param publicKey - Unsandbox public API key.
 * @param secretKey - Unsandbox secret API key.
 * @returns The updated Unsandbox configuration.
 */
export async function saveUnsandboxConfig(config: BackendConfig, publicKey: string, secretKey: string): Promise<UnsandboxConfig> {
  return requestJSON<UnsandboxConfig>(config, '/api/v1/config/unsandbox', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ public_key: publicKey, secret_key: secretKey }),
  })
}

/**
 * Deletes the stored Unsandbox API keys from the backend.
 * @param config - Backend connection configuration.
 */
export async function deleteUnsandboxConfig(config: BackendConfig): Promise<void> {
  await requestJSON<void>(config, '/api/v1/config/unsandbox', {
    method: 'DELETE',
  })
}

/**
 * Fetches the current Unsandbox integration status (whether keys are valid).
 * @param config - Backend connection configuration.
 * @returns The Unsandbox runtime status.
 */
export async function fetchUnsandboxStatus(config: BackendConfig): Promise<UnsandboxStatus> {
  return requestJSON<UnsandboxStatus>(config, '/api/v1/unsandbox/status')
}

/** SSH forwarding configuration for unsandbox containers. */
export type SSHConfig = {
  forward_enabled: boolean
  key_path: string
  available_keys: string[]
}

/**
 * Fetches the current SSH forwarding configuration and available keys.
 * @param config - Backend connection configuration.
 * @returns The SSH config state.
 */
export async function fetchSSHConfig(config: BackendConfig): Promise<SSHConfig> {
  return requestJSON<SSHConfig>(config, '/api/v1/config/ssh')
}

/**
 * Saves the SSH forwarding configuration.
 * @param config - Backend connection configuration.
 * @param forwardEnabled - Whether to inject the SSH key into containers.
 * @param keyPath - Absolute path to the private key, or empty for auto-detect.
 * @returns Updated SSH config state.
 */
export async function saveSSHConfig(
  config: BackendConfig,
  forwardEnabled: boolean,
  keyPath: string,
): Promise<SSHConfig> {
  return requestJSON<SSHConfig>(config, '/api/v1/config/ssh', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ forward_enabled: forwardEnabled, key_path: keyPath }),
  })
}

/** Result of executing code in the Unsandbox remote environment. */
export type UnsandboxExecuteResult = {
  status: string
  output: string
  error: string
  job_id: string
}

/**
 * Executes code in the Unsandbox remote sandbox environment.
 * @param config - Backend connection configuration.
 * @param language - Programming language (e.g. "python", "bash").
 * @param code - Source code to execute.
 * @param network - Network access level (defaults to "semitrusted").
 * @returns Execution result with output, errors, and job ID.
 */
export async function executeUnsandbox(
  config: BackendConfig,
  language: string,
  code: string,
  network?: string,
): Promise<UnsandboxExecuteResult> {
  return requestJSON<UnsandboxExecuteResult>(config, '/api/v1/unsandbox/execute', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ language, code, network: network || 'semitrusted' }),
  }, 150000)
}

/** An active or completed Unsandbox execution session. */
export type UnsandboxSession = {
  id: string
  language: string
  status: string
  created_at?: string
  [key: string]: unknown
}

/**
 * Fetches all Unsandbox execution sessions.
 * @param config - Backend connection configuration.
 * @returns Object containing array of session records.
 */
export async function fetchUnsandboxSessions(config: BackendConfig): Promise<{ sessions: UnsandboxSession[] }> {
  return requestJSON<{ sessions: UnsandboxSession[] }>(config, '/api/v1/unsandbox/sessions')
}

/** A service running within the Unsandbox environment. */
export type UnsandboxService = {
  id: string
  status: string
  [key: string]: unknown
}

/**
 * Fetches all services running in the Unsandbox environment.
 * @param config - Backend connection configuration.
 * @returns Object containing array of service records.
 */
export async function fetchUnsandboxServices(config: BackendConfig): Promise<{ services: UnsandboxService[] }> {
  return requestJSON<{ services: UnsandboxService[] }>(config, '/api/v1/unsandbox/services')
}

/**
 * Fetches configured agent provider API keys from the orchestrator.
 * @param config - Backend connection configuration.
 * @returns Object containing provider configuration status and keys.
 */
export async function fetchAgentProviderKeys(config: BackendConfig): Promise<{
  providers: Record<string, { configured: boolean; api_key?: string }>
}> {
  return requestJSON(config, '/api/v1/config/agent-providers')
}

/**
 * Saves an API key for a specific agent provider.
 * @param config - Backend connection configuration.
 * @param providerId - The provider identifier (e.g. 'openai', 'claude').
 * @param apiKey - The API key to save.
 */
export async function saveAgentProviderKey(
  config: BackendConfig,
  providerId: string,
  apiKey: string,
): Promise<void> {
  await requestJSON(config, '/api/v1/config/agent-providers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider: providerId, api_key: apiKey }),
  })
}
