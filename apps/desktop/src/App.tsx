import { useEffect, useMemo, useRef, useState } from 'react'
import { Activity, Database, FolderTree, ListTodo, Settings2 } from 'lucide-react'
import {
  IssueDetailView,
  CreateTaskDialog,
  CreateProjectDialog,
  SettingsCard,
} from '@/components/app-shell/panels'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { type TimelineItem } from '@/components/app-shell/types'
import {
  fetchAgentConfig,
  fetchAgents,
  fetchIssues,
  fetchProjectStats,
  fetchProjects,
  fetchState,
  fetchWarehouseStats,
  fetchUnsandboxStatus,
  isUnauthorizedError,
  normalizeEventEnvelope,
  normalizeSnapshotPayload,
  postRefresh,
  updateAgentConfig,
  patchAgentConfig,
  searchIssues,
  stopIssueSession,
  toDisplayError,
  updateIssue,
  createProject,
  createIssue,
  deleteProject,
  deleteIssue,
  fetchSessionDetail,
  fetchMCPTools,
  fetchProjectGitHubIssues,
  createProjectGitHubIssue,
  updateProjectGitHubIssue,
  type IssueCreatePayload,
  type IssueUpdatePayload,
  type IssueListItem,
} from '@/lib/orchestra-client'
import { startRuntimeSync } from '@/lib/runtime-sync'
import { appendTimelineEvent, applySnapshotUpdate } from '@/lib/runtime-store'
import type { GlobalStats, Project, ProjectStats, SessionDetail, SessionSummary, SnapshotPayload } from '@/lib/orchestra-types'
import { ProjectGrid } from '@/components/projects/ProjectGrid'
import { ProjectDetailView } from '@/components/projects/ProjectDetailView'
import { AnalyticsDashboard } from '@/components/analytics/AnalyticsDashboard'
import { SessionDetailView } from '@/components/analytics/SessionDetailView'
import { AgentsDashboard } from '@/components/agents/AgentsDashboard'
import { DocsDashboard } from '@/components/docs/DocsDashboard'
import { SandboxDashboard } from '@/components/sandbox/SandboxDashboard'
import { TerminalMultiplexer, type TerminalNode } from '@/components/terminal/TerminalMultiplexer'
import { AppShell } from '@app/layout/AppShell'
import {
  getCurrentSectionMeta,
  getSectionVisibility,
  isSectionID,
  sidebarItems,
  type SectionID,
} from '@app/routes/sections'
import { KanbanBoard } from '@widgets/kanban'
import type { IssueDetailResult, ToolSummary } from '@widgets/issue-detail/types'
import { Command } from 'cmdk'
import type { BackendConfig } from '@/lib/orchestra-client'
import { AppTooltipProvider } from '@/components/ui/tooltip-wrapper'
import { SectionErrorBoundary } from '@/components/ui/section-error-boundary'
import { EmbeddedAgentWidget } from '@/components/embedded-agent'
import { useBackendConfig, useNotifications, useIssueLookup, useWorkspaceMigration } from '@/hooks'

/** Root application component that manages backend sync, navigation, and top-level UI state. */
export default function App() {
  const [theme, setTheme] = useState<'light' | 'dark'>(() => {
    if (typeof window === 'undefined') {
      return 'dark'
    }
    const stored = window.localStorage.getItem('orchestra-theme')
    return stored === 'light' ? 'light' : 'dark'
  })

  // Extracted hooks
  const {
    config, setConfig,
    loadingConfig, savingConfig, setSavingConfig,
    backendProfiles, setBackendProfiles,
    activeProfileId, setActiveProfileId,
    profilesPending, setProfilesPending,
    errorMessage, setErrorMessage,
  } = useBackendConfig()
  const {
    notifSound, setNotifSound,
    notifMuted, setNotifMuted,
    notifVolume, setNotifVolume,
    playNotification,
  } = useNotifications()

  const [snapshot, setSnapshot] = useState<SnapshotPayload | null>(null)
  const [timeline, setTimeline] = useState<TimelineItem[]>([])
  const [boardIssues, setBoardIssues] = useState<IssueListItem[]>([])
  const [githubBacklogIssues, setGithubBacklogIssues] = useState<IssueListItem[]>([])
  const [agentConfig, setAgentConfig] = useState<{ commands: Record<string, string>; agent_provider: string; max_turns: number } | null>(null)
  const [availableAgents, setAvailableAgents] = useState<string[]>([])
  const [allTools, setAllTools] = useState<ToolSummary[]>([])
  const [unsandboxConfigured, setUnsandboxConfigured] = useState(false)
  const [loadingState, setLoadingState] = useState(true)
  const [usePolling, setUsePolling] = useState(false)
  const syncControls = useRef<{ startPolling: () => void; stopPolling: () => void } | null>(null)
  const lastIssueFetchRef = useRef(0)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [statusMessage, setStatusMessage] = useState('')

  const setOperatorError = (prefix: string, err: unknown) => {
    const message = toDisplayError(err)
    setErrorMessage(`${prefix}: ${message}`)
    if (isUnauthorizedError(err) || message.startsWith('unauthorized:')) {
      setStatusMessage('Protected host detected. Add bearer token in Settings -> Backend Configuration.')
    }
  }

  const {
    issueLookupId, setIssueLookupId,
    issueLookupPending, setIssueLookupPending,
    issueLookupResult, setIssueLookupResult,
    issueLookupError, setIssueLookupError,
    executeIssueLookup,
  } = useIssueLookup(config, setStatusMessage)

  const {
    migrationFrom, setMigrationFrom,
    migrationTo, setMigrationTo,
    migrationPlan, setMigrationPlan: _setMigrationPlan,
    migrationPending,
    handleMigrationPlan,
    handleMigrationApply,
  } = useWorkspaceMigration(config, setStatusMessage, setOperatorError)

  const [sessionLookupResult, setSessionLookupResult] = useState<SessionDetail | null>(null)
  const [sessionLookupPending, setSessionLookupPending] = useState(false)
  const [sessionLookupError, setSessionLookupError] = useState('')
  const [refreshPending, setRefreshPending] = useState(false)
  const [inspectDialogOpen, setInspectDialogOpen] = useState(false)
  const [sessionInspectDialogOpen, setSessionInspectDialogOpen] = useState(false)
  const [createTaskDialogOpen, setCreateTaskDialogOpen] = useState(false)
  const [createTaskInitialState, setCreateTaskInitialState] = useState('Backlog')
  const [createProjectDialogOpen, setCreateProjectDialogOpen] = useState(false)
  const [activeSection, setActiveSection] = useState<SectionID>('ISSUES')
  const [settingsInitialTab, setSettingsInitialTab] = useState<'backend' | 'agents' | 'integrations' | 'shortcuts' | 'notifications' | undefined>(undefined)
  const [activePeriod, setActivePeriod] = useState<'Today' | 'Week' | 'Month'>('Week')
  const [paletteOpen, setPaletteOpen] = useState(false)

  const handleRefreshRef = useRef<(() => Promise<void>) | null>(null)

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === 'k' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        setPaletteOpen((open) => !open)
      }
      if (e.key === 'r' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        void handleRefreshRef.current?.()
      }
      if (e.key === '/' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        setSidebarCollapsed((v) => !v)
      }
      // Ctrl+1-8 tab switching (fallback for non-Electron)
      if (e.ctrlKey && !e.altKey && !e.shiftKey) {
        const num = parseInt(e.key, 10)
        if (!isNaN(num) && num >= 1 && num <= sidebarItems.length) {
          e.preventDefault()
          setActiveSection(sidebarItems[num - 1].id as SectionID)
        }
      }
    }
    document.addEventListener('keydown', down, true)
    return () => document.removeEventListener('keydown', down, true)
  }, [])

  // Ctrl+1-8 tab switching via Electron IPC (bypasses Chromium)
  useEffect(() => {
    const bridge = (window as unknown as Record<string, unknown>).orchestraDesktop as { onSwitchTab?: (cb: (tabNum: number) => void) => () => void } | undefined
    if (bridge?.onSwitchTab) {
      bridge.onSwitchTab((tabNum: number) => {
        if (tabNum >= 1 && tabNum <= sidebarItems.length) {
          setActiveSection(sidebarItems[tabNum - 1].id as SectionID)
        }
      })
    }
  }, [])

  // Projects & Warehouse State
  const [projects, setProjects] = useState<Project[]>([])
  const [projectStats, setProjectStats] = useState<Record<string, ProjectStats>>({})
  const [warehouseStats, setWarehouseStats] = useState<GlobalStats | null>(null)
  const [selectedProjectID, setSelectedProjectID] = useState<string | null>(null)
  const [dataLoading, setDataLoading] = useState(false)

  const [openTerminals, setOpenTerminals] = useState<TerminalNode[]>([
    { id: 'master-shell', title: 'System Shell' }
  ])

  // Sync open terminals with running sessions
  useEffect(() => {
    if (!snapshot?.running) return

    setOpenTerminals(prev => {
      // Always keep master-shell
      const _base = prev.some(p => p.id === 'master-shell') ? [] : [{ id: 'master-shell', title: 'System Shell' }]

      // Find sessions that are running but don't have a terminal window yet
      const _activeRunningIds = snapshot.running.map(r => `issue-${r.issue_identifier}`)
      const existingIds = prev.map(p => p.id)
      
      const newTerms = snapshot.running
        .filter(r => !existingIds.includes(`issue-${r.issue_identifier}`))
        .map(r => ({
          id: `issue-${r.issue_identifier}`,
          title: `Agent: ${r.issue_identifier}`,
          projectId: boardIssues.find((issue) => issue.issue_id === r.issue_id)?.project_id
        }))

      if (newTerms.length === 0) return prev
      
      return [...prev, ...newTerms]
    })
  }, [snapshot, boardIssues])

  const handleCloseTerminal = (id: string) => {
    setOpenTerminals(prev => prev.filter(t => t.id !== id))
  }

  const handleJumpToTerminal = (identifier: string) => {
    const termId = `issue-${identifier}`
    setOpenTerminals(prev => {
      if (prev.some(p => p.id === termId)) return prev
      return [...prev, { id: termId, title: `Agent: ${identifier}` }]
    })
    setActiveSection('CONSOLE')
  }

  const handleSectionChange = (section: string) => {
    if (!isSectionID(section)) {
      return
    }
    setActiveSection(section)
  }

  const handleCloneSession = (session: SessionSummary) => {
    setSelectedProjectID(session.project_id || null)
    setCreateTaskInitialState('Todo')
    setCreateTaskDialogOpen(true)
    setActiveSection('ISSUES')
  }

  const sidebarWidth = sidebarCollapsed ? 76 : 320
  const sectionVisibility = getSectionVisibility(activeSection)
  const currentSectionMeta = getCurrentSectionMeta(activeSection)

  const osOptions = useMemo(() => ({
    scrollbars: { autoHide: 'move' as const, theme: 'os-theme-custom' },
    overflow: { x: 'hidden' as const, y: 'scroll' as const }
  }), [])

  useEffect(() => {
    const root = document.documentElement
    if (theme === 'dark') {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }
    window.localStorage.setItem('orchestra-theme', theme)
  }, [theme])

  useEffect(() => {
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
    const handleChange = (e: MediaQueryListEvent) => {
      setTheme(e.matches ? 'dark' : 'light')
    }
    mediaQuery.addEventListener('change', handleChange)
    return () => mediaQuery.removeEventListener('change', handleChange)
  }, [])

  useEffect(() => {
    if (!config) return

    let mounted = true

    // Non-blocking metadata fetches
    fetchAgentConfig(config)
      .then(cfg => mounted && setAgentConfig(cfg))
      .catch(() => mounted && setAgentConfig(null))
    if (config) {
      fetchAgents(config)
        .then(agents => mounted && setAvailableAgents(agents))
        .catch(() => mounted && setAvailableAgents([]))

      fetchMCPTools(config)
        .then(tools => mounted && setAllTools(tools))
        .catch(() => mounted && setAllTools([]))

      fetchUnsandboxStatus(config)
        .then(s => mounted && setUnsandboxConfigured(s.configured === true && s.valid === true))
        .catch(() => mounted && setUnsandboxConfigured(false))
    }
    // Section-specific data loading with global loading state
    const loadRequiredData = async () => {
      // Always fetch projects if we have none yet
      // Always load projects - needed for task inspector project name resolution
      const needsProjects = true
      const needsWarehouse = activeSection === 'WAREHOUSE'

      if (!needsProjects && !needsWarehouse) return

      setDataLoading(true)
      try {
        if (needsProjects) {
          const projs = await fetchProjects(config)
          if (!mounted) return
          setProjects(projs)

          // Fetch stats for projects that don't have them yet
          const statsMap: Record<string, ProjectStats> = { ...projectStats }
          let statsUpdated = false
          for (const p of projs) {
            if (statsMap[p.id]) continue
            try {
              const s = await fetchProjectStats(config, p.id)
              statsMap[p.id] = s
              statsUpdated = true
            } catch (e) {
              console.error(`failed to fetch stats for project ${p.id}`, e)
            }
          }
          if (mounted && statsUpdated) setProjectStats(statsMap)
        }

        if (needsWarehouse) {
          const stats = await fetchWarehouseStats(config)
          if (mounted) setWarehouseStats(stats)
        }
      } catch (err) {
        if (mounted) setOperatorError('failed to fetch section data', err)
      } finally {
        if (mounted) setDataLoading(false)
      }
    }

    loadRequiredData()

    return () => {
      mounted = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [config, activeSection])

  // Fetch GitHub issues for connected projects → Kanban backlog
  useEffect(() => {
    if (!config || projects.length === 0) return
    let mounted = true
    const connected = projects.filter(p => p.github_token)
    if (connected.length === 0) { setGithubBacklogIssues([]); return }

    Promise.all(connected.map(async (p) => {
      try {
        const ghIssues = await fetchProjectGitHubIssues(config, p.id)
        return ghIssues.map(gh => ({
          id: `github-${gh.number}`, issue_id: `github-${gh.number}`,
          identifier: `GH-${gh.number}`, issue_identifier: `GH-${gh.number}`,
          title: gh.title, description: gh.body, state: 'Backlog',
          project_id: p.id, url: gh.html_url,
        } as IssueListItem))
      } catch { return [] as IssueListItem[] }
    })).then(results => {
      if (!mounted) return
      setGithubBacklogIssues(results.flat())
    })
    return () => { mounted = false }
  }, [config, projects])

  const allBoardIssues = useMemo(() => {
    const localTitles = new Set(boardIssues.map(i => i.title))
    const uniqueGh = githubBacklogIssues.filter(gh => !localTitles.has(gh.title))
    return [...boardIssues, ...uniqueGh]
  }, [boardIssues, githubBacklogIssues])
  const allBoardIssuesRef = useRef(allBoardIssues)
  allBoardIssuesRef.current = allBoardIssues

  const handleAgentConfigSave = async (nextAgentConfig: { commands: Record<string, string>; agent_provider: string; max_turns: number }) => {
    if (!config) return
    setSavingConfig(true)
    try {
      await updateAgentConfig(config, { commands: nextAgentConfig.commands, agent_provider: nextAgentConfig.agent_provider })
      await patchAgentConfig(config, { max_turns: nextAgentConfig.max_turns })
      setAgentConfig(nextAgentConfig)
      setStatusMessage('Agent configuration updated.')
    } catch (err) {
      setOperatorError('save agent config failed', err)
    } finally {
      setSavingConfig(false)
    }
  }

  useEffect(() => {
    if (!config) {
      return
    }

    const sync = startRuntimeSync(
      config,
      {
        onSnapshot: (next) => {
          setSnapshot((previous) => applySnapshotUpdate(previous, next))
          setLoadingState(false)
          setErrorMessage('')
          // Fetch board issues to populate the Kanban board persistence (throttled)
          const now = Date.now()
          if (now - lastIssueFetchRef.current > 5000) {
            lastIssueFetchRef.current = now
            fetchIssues(config).then(setBoardIssues).catch(() => {})
          }
        },
        onTimelineEvent: (eventType, envelope) => {
          setTimeline((previous) => appendTimelineEvent(previous, { type: envelope.type, at: envelope.timestamp, data: envelope.data }))
          if (eventType === 'RUN_SUCCEEDED') {
            const issueId = (envelope.data.issue_id as string) || ''
            const issueIdentifier = (envelope.data.issue_identifier as string) || ''
            if (issueId && issueIdentifier) {
              setBoardIssues((prev) => {
                const existing = prev.find((i) => i.issue_id === issueId)
                if (existing) {
                  return prev.map((i) => i.issue_id === issueId ? { ...i, state: 'Review' } : i)
                }
                return [
                  ...prev,
                  {
                    issue_id: issueId,
                    issue_identifier: issueIdentifier,
                    state: 'Review',
                  },
                ]
              })
              playNotification(issueIdentifier)
            }
          }
        },
        onStatus: (message) => {
          setStatusMessage(message)
        },
        onError: (message) => {
          setErrorMessage(message)
          if (isUnauthorizedError(message) || message.includes('unauthorized:')) {
            setStatusMessage('Protected host detected. Add bearer token in Settings -> Backend Configuration.')
          }
          setLoadingState(false)
        },
      },
      {
        fetchSnapshot: fetchState,
        normalizeSnapshot: normalizeSnapshotPayload,
        normalizeEnvelope: normalizeEventEnvelope,
        createEventSource: (url) => new EventSource(url),
        setIntervalFn: (cb, ms) => window.setInterval(cb, ms),
        clearIntervalFn: (id) => window.clearInterval(id),
        setTimeoutFn: (cb, ms) => window.setTimeout(cb, ms),
        clearTimeoutFn: (id) => window.clearTimeout(id),
      },
    )

    syncControls.current = { startPolling: sync.startPolling, stopPolling: sync.stopPolling }

    return () => {
      sync.stop()
      syncControls.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [config])

  // Listen for data mutations from the embedded agent and refresh the board
  useEffect(() => {
    if (!config) return
    const handler = () => {
      fetchIssues(config).then(setBoardIssues).catch(() => {})
    }
    window.addEventListener('orchestra-data-changed', handler)
    return () => window.removeEventListener('orchestra-data-changed', handler)
  }, [config])

  const _metrics = useMemo(() => {
    if (!snapshot) {
      return {
        running: '0',
        retrying: '0',
        totalTokens: '0',
      }
    }

    const _remaining = typeof snapshot.rate_limits?.remaining === 'number' ? String(snapshot.rate_limits.remaining) : ''
    const total = snapshot.codex_totals?.total_tokens ?? 0
    return {
      running: String(snapshot.counts.running ?? 0),
      retrying: String(snapshot.counts.retrying ?? 0),
      totalTokens: total > 10000 ? `${(total / 1000).toFixed(1)}k` : String(total),
    }
  }, [snapshot])

  const generatedAt = snapshot?.generated_at ? new Date(snapshot.generated_at).toLocaleString() : 'waiting for first snapshot'

  const handleRefresh = async () => {
    if (!config) {
      return
    }
    setRefreshPending(true)
    setStatusMessage('')
    setErrorMessage('')
    try {
      await postRefresh(config)
      setStatusMessage('Refresh queued successfully.')
    } catch (err) {
      setOperatorError('refresh failed', err)
    } finally {
      setRefreshPending(false)
    }
  }

  handleRefreshRef.current = handleRefresh

  const handleIssueUpdate = async (identifier: string, updates: IssueUpdatePayload) => {
    if (!config) return
    try {
      // If this is a GitHub backlog issue, promote to local task linked to the SAME GitHub issue
      if (identifier.startsWith('GH-')) {
        const ghIssue = allBoardIssuesRef.current.find(i =>
          i.identifier === identifier || i.issue_identifier === identifier
        )
        if (ghIssue) {
          const updatesRec = updates as Record<string, unknown>
          // Create local task linked to the existing GitHub issue
          const newIssue = await createIssue(config, {
            title: updatesRec.title as string || ghIssue.title || '',
            description: updatesRec.description as string || ghIssue.description || '',
            state: updatesRec.state as string || 'Backlog',
            assignee_id: updatesRec.assignee_id as string || '',
            project_id: ghIssue.project_id || '',
            provider: updatesRec.provider as string || '',
          })
          // Link to the EXISTING GitHub issue — don't create a new one
          const ghUrl = ghIssue.url || ''
          if (newIssue?.identifier && ghUrl) {
            await updateIssue(config, newIssue.identifier, { url: ghUrl } as IssueUpdatePayload)
          }
          setStatusMessage(`${identifier} linked to ${newIssue?.identifier || 'local task'}`)
          const updatedIssues = await fetchIssues(config)
          setBoardIssues(updatedIssues)
          await handleRefresh()
          if (newIssue?.identifier) {
            setIssueLookupId(newIssue.identifier)
            await executeIssueLookup(newIssue.identifier)
          }
          return
        }
      }

      await updateIssue(config, identifier, updates)

      // If title/description changed and project is GitHub-connected, sync to GitHub
      const updatesRec = updates as Record<string, unknown>
      if (updatesRec.title || updatesRec.description) {
        const issue = allBoardIssuesRef.current.find(i =>
          i.identifier === identifier || i.issue_identifier === identifier
        )
        if (issue?.project_id && issue.url?.includes('github.com')) {
          const project = projects.find(p => p.id === issue.project_id)
          if (project?.github_token) {
            // Extract GH issue number from URL (e.g. .../issues/22)
            const ghMatch = issue.url.match(/\/issues\/(\d+)$/)
            if (ghMatch) {
              const ghNumber = parseInt(ghMatch[1], 10)
              try {
                await updateProjectGitHubIssue(config, project.id, ghNumber, {
                  ...(updatesRec.title ? { title: updatesRec.title as string } : {}),
                  ...(updatesRec.description ? { body: updatesRec.description as string } : {}),
                })
              } catch {
                // GitHub sync failed silently — local update still succeeded
              }
            }
          }
        }
      }

      // If state changed, sync open/closed to GitHub
      if (updatesRec.state) {
        const issue = allBoardIssuesRef.current.find(i =>
          i.identifier === identifier || i.issue_identifier === identifier
        )
        if (issue?.project_id && issue.url?.includes('github.com')) {
          const proj = projects.find(p => p.id === issue.project_id)
          if (proj?.github_token) {
            const ghMatch = issue.url.match(/\/issues\/(\d+)$/)
            if (ghMatch) {
              const ghNumber = parseInt(ghMatch[1], 10)
              const ghState = updatesRec.state === 'Done' ? 'closed' : 'open'
              // Only sync if transitioning to/from Done
              const previousState = issue.state
              if (updatesRec.state === 'Done' || previousState === 'Done') {
                try {
                  await updateProjectGitHubIssue(config, proj.id, ghNumber, { state: ghState })
                } catch {
                  // GitHub sync failed silently — local update still succeeded
                }
              }
            }
          }
        }
      }

      // Instantly update the board issues for snappy drag-and-drop
      const updatedIssues = await fetchIssues(config)
      setBoardIssues(updatedIssues)

      // Refetch state immediately to show updates in Kanban/lists
      await handleRefresh()
      // Also refetch the specific issue to update the detail view if it's open
      await executeIssueLookup(identifier)
    } catch (err) {
      setErrorMessage(`update issue failed: ${toDisplayError(err)}`)
    }
  }

  const handleStopSession = async (identifier: string, provider?: string) => {
    if (!config) return
    try {
      await stopIssueSession(config, identifier, provider)
      // Move task back to Todo when stopped
      await updateIssue(config, identifier, { state: 'Todo' })
      setStatusMessage(`Session for ${identifier} stopped. Task moved to Todo.`)
      const updatedIssues = await fetchIssues(config)
      setBoardIssues(updatedIssues)
      await handleRefresh()
      await executeIssueLookup(identifier)
    } catch (err) {
      setErrorMessage(`stop session failed: ${toDisplayError(err)}`)
    }
  }

  const handleCreateIssue = (initialState: string) => {
    setCreateTaskInitialState(initialState)
    setCreateTaskDialogOpen(true)
  }

  const handleTaskSubmit = async (payload: IssueCreatePayload) => {
    if (!config) return
    try {
      const localIssue = await createIssue(config, payload)

      // If project is GitHub-connected, also create a GitHub issue and link it
      const project = projects.find(p => p.id === payload.project_id)
      if (project?.github_token && project.github_owner && project.github_repo) {
        try {
          const ghIssue = await createProjectGitHubIssue(config, project.id, {
            title: payload.title,
            body: payload.description || '',
          })
          // Link the local task to the GitHub issue via url field
          const issueId = localIssue?.identifier || localIssue?.issue_identifier || ''
          if (issueId && ghIssue.html_url) {
            await updateIssue(config, issueId, { url: ghIssue.html_url } as IssueUpdatePayload)
          }
        } catch {
          // GitHub create failed — local task still exists
        }
      }

      setStatusMessage(`Task "${payload.title}" created.`)

      // Instantly update the board issues so it appears without waiting for an SSE cycle
      const updatedIssues = await fetchIssues(config)
      setBoardIssues(updatedIssues)

      // Refetch GitHub issues for the project so the new linked issue shows up immediately
      if (project?.github_token) {
        try {
          const ghIssues = await fetchProjectGitHubIssues(config, project.id, 'open')
          void ghIssues // triggers githubBacklogIssues effect via setBoardIssues above
        } catch {
          // non-critical
        }
      }

      void handleRefresh()
    } catch (err) {
      setErrorMessage(`create task failed: ${toDisplayError(err)}`)
    }
  }

  const handleIssueDelete = async (identifier: string) => {
    if (!config) return

    try {
      // Close the linked GitHub issue before deleting locally
      const issueToClose = allBoardIssuesRef.current.find(i =>
        i.identifier === identifier || i.issue_identifier === identifier
      )
      if (issueToClose?.project_id && issueToClose.url?.includes('github.com')) {
        const proj = projects.find(p => p.id === issueToClose.project_id)
        if (proj?.github_token) {
          const ghMatch = issueToClose.url.match(/\/issues\/(\d+)$/)
          if (ghMatch) {
            const ghNumber = parseInt(ghMatch[1], 10)
            try {
              await updateProjectGitHubIssue(config, proj.id, ghNumber, { state: 'closed' })
            } catch {
              // GitHub close failed silently — proceed with local delete
            }
          }
        }
      }

      await deleteIssue(config, identifier)
      setStatusMessage(`Task ${identifier} deleted.`)

      // Remove from board immediately so UI reflects deletion even if follow-up refresh fails.
      setBoardIssues((prev) => prev.filter((issue) => {
        const candidate = typeof issue.identifier === 'string' ? issue.identifier : issue.issue_identifier
        return candidate !== identifier
      }))

      // Optimistically remove from snapshot
      setSnapshot(prev => {
        if (!prev) return prev
        return {
          ...prev,
          running: prev.running.filter(r => r.issue_identifier !== identifier),
          retrying: prev.retrying.filter(r => r.issue_identifier !== identifier)
        }
      })

      // Close issue-specific terminal if currently open.
      setOpenTerminals((prev) => prev.filter((terminal) => terminal.id !== `issue-${identifier}`))

      // Instantly update the board issues
      const updatedIssues = await fetchIssues(config)
      setBoardIssues(updatedIssues)

      void handleRefresh()
      if (issueLookupId === identifier) {
        setIssueLookupResult(null)
        setIssueLookupId('')
      }
    } catch (err) {
      setErrorMessage(`delete issue failed: ${toDisplayError(err)}`)
      throw err
    }
  }

  const handleInspectIssueFromList = async (issueIdentifier: string) => {
    setIssueLookupId(issueIdentifier)
    setIssueLookupError('')
    setIssueLookupPending(false)
    setInspectDialogOpen(true)

    // For GitHub backlog issues, populate directly from cached data instead of API
    if (issueIdentifier.startsWith('GH-')) {
      const ghIssue = allBoardIssuesRef.current.find(i =>
        i.identifier === issueIdentifier ||
        i.issue_identifier === issueIdentifier ||
        i.id === issueIdentifier
      )
      if (ghIssue) {
        setIssueLookupResult({
          ...ghIssue,
          project_name: projects.find(p => p.id === ghIssue.project_id)?.name || '',
        } as IssueDetailResult)
        return
      }
    }

    await executeIssueLookup(issueIdentifier)
  }

  const handleInspectSession = async (sessionId: string) => {
    if (!config) return
    setSessionInspectDialogOpen(true)
    setSessionLookupPending(true)
    setSessionLookupError('')
    try {
      const result = await fetchSessionDetail(config, sessionId)
      setSessionLookupResult(result)
    } catch (err) {
      setSessionLookupError(toDisplayError(err))
    } finally {
      setSessionLookupPending(false)
    }
  }

  const handleBackendConfigSave = async (nextConfig: BackendConfig) => {
    const desktopBridge = window.orchestraDesktop
    if (!desktopBridge || typeof desktopBridge.setBackendConfig !== 'function') {
      setErrorMessage('desktop bridge unavailable: cannot save backend config')
      return
    }

    try {
      new URL(nextConfig.baseUrl)
    } catch {
      setErrorMessage('backend config save failed: base URL must be a valid absolute URL')
      return
    }

    setSavingConfig(true)
    setErrorMessage('')
    try {
      const saved = await desktopBridge.setBackendConfig(nextConfig)
      setConfig(saved)
      if (typeof desktopBridge.getBackendProfiles === 'function') {
        const payload = await desktopBridge.getBackendProfiles()
        setBackendProfiles(payload.profiles)
        setActiveProfileId(payload.activeProfileId)
      }
      setStatusMessage('Backend configuration saved.')
    } catch (err) {
      setOperatorError('backend config save failed', err)
    } finally {
      setSavingConfig(false)
    }
  }

  const handleSetActiveProfile = async (profileId: string) => {
    const desktopBridge = window.orchestraDesktop
    if (!desktopBridge || typeof desktopBridge.setActiveBackendProfile !== 'function') {
      setErrorMessage('desktop bridge unavailable: cannot change active profile')
      return
    }

    setProfilesPending(true)
    setErrorMessage('')
    try {
      const nextConfig = await desktopBridge.setActiveBackendProfile(profileId)
      setConfig(nextConfig)
      if (typeof desktopBridge.getBackendProfiles === 'function') {
        const payload = await desktopBridge.getBackendProfiles()
        setBackendProfiles(payload.profiles)
        setActiveProfileId(payload.activeProfileId)
      }
      setStatusMessage('Active backend profile switched.')
    } catch (err) {
      setErrorMessage(`switch profile failed: ${toDisplayError(err)}`)
    } finally {
      setProfilesPending(false)
    }
  }

  const handleCreateProfile = async (name: string) => {
    const desktopBridge = window.orchestraDesktop
    if (!desktopBridge || typeof desktopBridge.saveBackendProfile !== 'function') {
      setErrorMessage('desktop bridge unavailable: cannot save profile')
      return
    }

    const fromConfig = config ?? { baseUrl: 'http://127.0.0.1:4010', apiToken: 'dev-token' }
    setProfilesPending(true)
    setErrorMessage('')
    try {
      const payload = await desktopBridge.saveBackendProfile({
        name: name.trim(),
        baseUrl: fromConfig.baseUrl,
        apiToken: fromConfig.apiToken,
        makeActive: true,
      })
      setBackendProfiles(payload.profiles)
      setActiveProfileId(payload.activeProfileId)
      const active = payload.profiles.find((profile) => profile.id === payload.activeProfileId)
      if (active) {
        setConfig({ baseUrl: active.baseUrl, apiToken: active.apiToken })
      }
      setStatusMessage('Backend profile created and activated.')
    } catch (err) {
      setErrorMessage(`create profile failed: ${toDisplayError(err)}`)
    } finally {
      setProfilesPending(false)
    }
  }

  const handleDeleteProfile = async (profileId: string) => {
    const desktopBridge = window.orchestraDesktop
    if (!desktopBridge || typeof desktopBridge.deleteBackendProfile !== 'function') {
      setErrorMessage('desktop bridge unavailable: cannot delete profile')
      return
    }

    setProfilesPending(true)
    setErrorMessage('')
    try {
      const payload = await desktopBridge.deleteBackendProfile(profileId)
      setBackendProfiles(payload.profiles)
      setActiveProfileId(payload.activeProfileId)
      const active = payload.profiles.find((profile) => profile.id === payload.activeProfileId)
      if (active) {
        setConfig({ baseUrl: active.baseUrl, apiToken: active.apiToken })
      }
      setStatusMessage('Backend profile deleted.')
    } catch (err) {
      setErrorMessage(`delete profile failed: ${toDisplayError(err)}`)
    } finally {
      setProfilesPending(false)
    }
  }

  const refreshProjectsAndStats = async () => {
    if (!config) return

    const projs = await fetchProjects(config)
    setProjects(projs)

    const statsMap: Record<string, ProjectStats> = { ...projectStats }
    let statsChanged = false
    for (const p of projs) {
      if (statsMap[p.id]) continue
      try {
        const s = await fetchProjectStats(config, p.id)
        statsMap[p.id] = s
        statsChanged = true
      } catch (e) {
        console.error(`failed to fetch stats for project ${p.id}`, e)
      }
    }
    if (statsChanged) {
      setProjectStats(statsMap)
    }
  }

  const handleAddProject = async (path: string) => {
    if (!path || !config) return

    try {
      await createProject(config, path)
      setStatusMessage(`Project at ${path} added successfully.`)
      await refreshProjectsAndStats()
    } catch (err) {
      setErrorMessage(`failed to add project: ${toDisplayError(err)}`)
    }
  }

  const handleDeleteProject = async (projectId: string) => {
    if (!config) {
      throw new Error('backend configuration unavailable')
    }
    try {
      await deleteProject(config, projectId)
      setStatusMessage('Project removed.')
      setProjects(prev => prev.filter(p => p.id !== projectId))
      setSelectedProjectID(null)
    } catch (err) {
      setErrorMessage(`failed to delete project: ${toDisplayError(err)}`)
      throw err
    }
  }

  const handleTogglePolling = () => {
    if (!syncControls.current) return
    if (usePolling) {
      syncControls.current.stopPolling()
      setStatusMessage('Switched to SSE live stream.')
    } else {
      syncControls.current.startPolling()
      setStatusMessage('Switched to high-frequency polling.')
    }
    setUsePolling(!usePolling)
  }

  const handleDownloadDiagnostics = () => {
    const data = {
      app: 'orchestra-desktop',
      timestamp: new Date().toISOString(),
      config: {
        baseUrl: config?.baseUrl,
        activeProfileId,
      },
      snapshot,
      timeline,
    }

    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `orchestra-diagnostics-${new Date().getTime()}.json`
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    URL.revokeObjectURL(url)
  }

  return (
    <AppTooltipProvider>
      <AppShell
        items={sidebarItems}
        activeSection={activeSection}
        onSectionChange={handleSectionChange}
        sidebarCollapsed={sidebarCollapsed}
        onToggleCollapsed={() => setSidebarCollapsed((prev) => !prev)}
        sidebarWidth={sidebarWidth}
        osOptions={osOptions}
        flushContent={sectionVisibility.showDocs}
        topBarProps={{
          sectionLabel: currentSectionMeta.label,
          sectionTitle: currentSectionMeta.title,
          theme,
          setTheme,
          activePeriod,
          setActivePeriod,
          refreshPending,
          configReady: Boolean(config),
          onOpenSettings: () => setActiveSection('SETTINGS'),
          onRefresh: handleRefresh,
          onSearch: (query) => (config ? searchIssues(config, query) : Promise.resolve([])),
          onResultClick: handleInspectIssueFromList,
          statusMessage,
          errorMessage,
          generatedAt,
          usePolling,
          onDownloadDiagnostics: handleDownloadDiagnostics,
          onTogglePolling: handleTogglePolling,
        }}
      >
        <div className="mt-4 flex flex-col flex-1 min-w-0 min-h-0 h-full">
              {sectionVisibility.showProjects ? (
                <SectionErrorBoundary name="Projects">
                <section className="flex-1 flex flex-col min-h-0">
                  {selectedProjectID && projects.find(p => p.id === selectedProjectID) ? (
                    <ProjectDetailView
                      project={projects.find(p => p.id === selectedProjectID)!}
                      stats={projectStats[selectedProjectID]}
                      config={config}
                      snapshot={snapshot}
                      boardIssues={boardIssues}
                      availableAgents={availableAgents}
                      loadingState={loadingState}
                      onBack={() => setSelectedProjectID(null)}
                      onInspectIssue={handleInspectIssueFromList}
                      onJumpToTerminal={handleJumpToTerminal}
                      onIssueUpdate={handleIssueUpdate}
                      onIssueDelete={handleIssueDelete}
                      onStopSession={handleStopSession}
                      onCreateIssue={handleCreateIssue}
                      onDeleteProject={handleDeleteProject}
                      onRefreshProjects={refreshProjectsAndStats}
                    />
                  ) : (
                    <ProjectGrid
                      projects={projects}
                      stats={projectStats}
                      loading={dataLoading}
                      onProjectClick={(id) => setSelectedProjectID(id)}
                      onAddProject={() => setCreateProjectDialogOpen(true)}
                      onDeleteProject={handleDeleteProject}
                    />
                  )}
                </section>
                </SectionErrorBoundary>
              ) : null}

              {sectionVisibility.showAgents ? (
                <SectionErrorBoundary name="Agents">
                <section className="flex-1 flex flex-col min-h-0">
                  <AgentsDashboard config={config} snapshot={snapshot} />
                </section>
                </SectionErrorBoundary>
              ) : null}

              {sectionVisibility.showWarehouse ? (
                <SectionErrorBoundary name="Analytics">
                <section className="flex-1 flex flex-col min-h-0">
                  <AnalyticsDashboard
                    stats={warehouseStats}
                    loading={dataLoading}
                    config={config}
                    onInspectSession={handleInspectSession}
                    onCloneSession={handleCloneSession}
                  />
                </section>
                </SectionErrorBoundary>
              ) : null}

              {sectionVisibility.showIssueBoard ? (
                <SectionErrorBoundary name="Kanban Board">
                <section className="flex-1 flex flex-col min-h-0">
                  <KanbanBoard
                    loadingState={loadingState}
                    snapshot={snapshot}
                    boardIssues={allBoardIssues}
                    projects={projects}
                    availableAgents={availableAgents}
                    onInspectIssue={handleInspectIssueFromList}
                    onJumpToTerminal={handleJumpToTerminal}
                    onIssueUpdate={handleIssueUpdate}
                    onIssueDelete={handleIssueDelete}
                    onStopSession={handleStopSession}
                    onCreateIssue={handleCreateIssue}
                  />
                </section>
                </SectionErrorBoundary>
              ) : null}

              {sectionVisibility.showDocs ? (
                <SectionErrorBoundary name="Documentation">
                <section className="flex-1 flex flex-col min-h-0 -mt-8 -mx-4 -mb-4">
                  <DocsDashboard config={config} theme={theme} />
                </section>
                </SectionErrorBoundary>
              ) : null}

              {sectionVisibility.showConsole && config ? (
                <SectionErrorBoundary name="Console">
                <section className="flex-1 flex flex-col min-h-0 border border-border rounded-xl overflow-hidden shadow-2xl">
                  <TerminalMultiplexer
                    activeTerminals={openTerminals}
                    baseUrl={config.baseUrl}
                    apiToken={config.apiToken}
                    onCloseTerminal={handleCloseTerminal}
                    onAddTerminal={() => {
                      const num = openTerminals.filter(t => t.id.startsWith('shell-')).length + 1
                      setOpenTerminals(prev => [...prev, { id: `shell-${Date.now()}`, title: `Shell ${num}` }])
                    }}
                    onAddAgentTerminal={(id, title, command) => {
                      setOpenTerminals(prev => [...prev, { id, title, initialCommand: command }])
                    }}
                    theme={theme}
                  />
                </section>
                </SectionErrorBoundary>
              ) : null}

              {sectionVisibility.showSandbox ? (
                <SectionErrorBoundary name="Sandbox">
                <section className="col-span-12 flex flex-col">
                  <SandboxDashboard config={config} onOpenSettings={() => { setSettingsInitialTab('integrations'); setActiveSection('SETTINGS') }} />
                </section>
                </SectionErrorBoundary>
              ) : null}

              {sectionVisibility.showSettings ? (
                <SectionErrorBoundary name="Settings">
                <section className="flex-1 flex flex-col min-h-0">
                  <SettingsCard
                    loadingConfig={loadingConfig}
                    savingConfig={savingConfig}
                    profilesPending={profilesPending}
                    config={config}
                    backendProfiles={backendProfiles}
                    activeProfileId={activeProfileId}
                    migrationPending={migrationPending}
                    migrationFrom={migrationFrom}
                    migrationTo={migrationTo}
                    migrationPlan={migrationPlan}
                    agentConfig={agentConfig}
                    onMigrationFromChange={setMigrationFrom}
                    onMigrationToChange={setMigrationTo}
                    onMigrationPlan={handleMigrationPlan}
                    onMigrationApply={handleMigrationApply}
                    onSaveBackendConfig={handleBackendConfigSave}
                    onSetActiveProfile={handleSetActiveProfile}
                    onCreateProfile={handleCreateProfile}
                    onDeleteProfile={handleDeleteProfile}
                    onSaveAgentConfig={handleAgentConfigSave}
                    notifSound={notifSound}
                    notifMuted={notifMuted}
                    notifVolume={notifVolume}
                    onNotifSoundChange={setNotifSound}
                    onNotifMutedChange={setNotifMuted}
                    onNotifVolumeChange={setNotifVolume}
                    initialTab={settingsInitialTab}
                  />
                </section>
                </SectionErrorBoundary>
              ) : null}
        </div>
        <EmbeddedAgentWidget
          config={config}
          onNavigate={(section, id) => {
            setActiveSection(section as SectionID)
            if (section === 'SETTINGS' && id) {
              setSettingsInitialTab(id as 'backend' | 'agents' | 'integrations' | 'shortcuts' | 'notifications')
            }
          }}
          onOpenSettings={() => { setSettingsInitialTab('integrations'); setActiveSection('SETTINGS') }}
          activeSection={activeSection}
        />
      </AppShell>

      <Dialog open={inspectDialogOpen} onOpenChange={setInspectDialogOpen}>
        <DialogContent className="!fixed !inset-0 !translate-x-0 !translate-y-0 !left-0 !top-0 !max-w-none w-full h-full overflow-hidden flex flex-col p-6 rounded-none border-none">
          <DialogHeader className="sr-only">
            <DialogTitle>Issue Inspector</DialogTitle>
            <DialogDescription>Task details</DialogDescription>
          </DialogHeader>
          <div className="flex-1 min-h-0">
            {issueLookupPending ? (
              <div className="space-y-4">
                <Skeleton className="h-8 w-[200px]" />
                <Skeleton className="h-[200px] w-full" />
              </div>
            ) : issueLookupError ? (
              <div className="rounded-md border border-red-300 bg-red-50 p-4 text-sm text-red-900 dark:border-red-900/70 dark:bg-red-950/35 dark:text-red-200">
                {issueLookupError}
              </div>
            ) : (issueLookupResult && typeof issueLookupResult === 'object') ? (
              <IssueDetailView
                result={{
                  ...issueLookupResult,
                  project_name: projects.find(p => p.id === (issueLookupResult as Record<string, unknown>).project_id)?.name || '',
                }}
                config={config}
                timeline={timeline}
                availableAgents={availableAgents}
                snapshot={snapshot}
                onUpdate={(updates) => handleIssueUpdate(issueLookupId, updates)}
                onStopSession={(p) => handleStopSession(issueLookupId, p)}
                theme={theme}
                />


            ) : (
              <p className="text-center text-sm text-muted-foreground py-10">No issue data available.</p>
            )}
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={sessionInspectDialogOpen} onOpenChange={setSessionInspectDialogOpen}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>Historical Session Analysis</DialogTitle>
            <DialogDescription>Review historical execution logs and token usage for this session.</DialogDescription>
          </DialogHeader>
          <div className="py-2">
            {sessionLookupPending ? (
              <div className="space-y-4">
                <Skeleton className="h-8 w-[200px]" />
                <Skeleton className="h-[200px] w-full" />
              </div>
            ) : sessionLookupError ? (
              <div className="rounded-md border border-red-300 bg-red-50 p-4 text-sm text-red-900 dark:border-red-900/70 dark:bg-red-950/35 dark:text-red-200">
                {sessionLookupError}
              </div>
            ) : sessionLookupResult ? (
              <SessionDetailView session={sessionLookupResult} />
            ) : (
              <p className="text-center text-sm text-muted-foreground">No session selected.</p>
            )}
          </div>
        </DialogContent>
      </Dialog>

      <CreateTaskDialog
        open={createTaskDialogOpen}
        onOpenChange={setCreateTaskDialogOpen}
        config={config}
        initialState={createTaskInitialState}
        availableAgents={availableAgents}
        allTools={allTools}
        projects={projects}
        initialProjectID={selectedProjectID || ''}
        hasUnsandbox={unsandboxConfigured}
        onSubmit={handleTaskSubmit}
      />

      <CreateProjectDialog
        open={createProjectDialogOpen}
        onOpenChange={setCreateProjectDialogOpen}
        onSubmit={handleAddProject}
      />

      <Command.Dialog
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        label="Global Command Palette"
        className="fixed top-1/2 left-1/2 w-full max-w-xl -translate-x-1/2 -translate-y-1/2 rounded-xl border border-border bg-card shadow-2xl z-[100] overflow-hidden"
      >
        <Command.Input
          autoFocus
          placeholder="Type a command or search..."
          className="w-full border-b border-border bg-transparent p-4 text-sm outline-none placeholder:text-muted-foreground"
        />
        <Command.List className="max-h-[300px] overflow-y-auto p-2">
          <Command.Empty className="p-4 text-center text-sm text-muted-foreground">No results found.</Command.Empty>

          <Command.Group heading="Navigation" className="px-2 py-1 text-xs font-semibold text-muted-foreground">
            <Command.Item
              onSelect={() => { setActiveSection('ISSUES'); setPaletteOpen(false) }}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground hover:bg-muted/50 data-[selected=true]:bg-muted/50"
            >
              <ListTodo className="h-4 w-4" /> Go to Tasks
            </Command.Item>
            <Command.Item
              onSelect={() => { setActiveSection('PROJECTS'); setPaletteOpen(false) }}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground hover:bg-muted/50 data-[selected=true]:bg-muted/50"
            >
              <FolderTree className="h-4 w-4" /> Go to Projects
            </Command.Item>
            <Command.Item
              onSelect={() => { setActiveSection('WAREHOUSE'); setPaletteOpen(false) }}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground hover:bg-muted/50 data-[selected=true]:bg-muted/50"
            >
              <Database className="h-4 w-4" /> Go to Analytics
            </Command.Item>
          </Command.Group>

          <Command.Group heading="Actions" className="px-2 py-1 mt-2 text-xs font-semibold text-muted-foreground border-t border-border/40">
            <Command.Item
              onSelect={() => { handleCreateIssue('Backlog'); setPaletteOpen(false) }}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground hover:bg-muted/50 data-[selected=true]:bg-muted/50"
            >
              <ListTodo className="h-4 w-4" /> Create New Task
            </Command.Item>
            <Command.Item
              onSelect={() => { setTheme(theme === 'dark' ? 'light' : 'dark'); setPaletteOpen(false) }}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground hover:bg-muted/50 data-[selected=true]:bg-muted/50"
            >
              <Settings2 className="h-4 w-4" /> Toggle Theme
            </Command.Item>
            <Command.Item
              onSelect={() => { handleTogglePolling(); setPaletteOpen(false) }}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground hover:bg-muted/50 data-[selected=true]:bg-muted/50"
            >
              <Activity className="h-4 w-4" /> Toggle Connection Mode (SSE/Polling)
            </Command.Item>
          </Command.Group>

          {projects.length > 0 && (
            <Command.Group heading="Projects" className="px-2 py-1 mt-2 text-xs font-semibold text-muted-foreground border-t border-border/40">
              {projects.map(p => (
                <Command.Item
                  key={p.id}
                  onSelect={() => {
                    setActiveSection('PROJECTS')
                    setSelectedProjectID(p.id)
                    setPaletteOpen(false)
                  }}
                  className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground hover:bg-muted/50 data-[selected=true]:bg-muted/50"
                >
                  <FolderTree className="h-4 w-4" /> {p.name}
                </Command.Item>
              ))}
            </Command.Group>
          )}
        </Command.List>
      </Command.Dialog>
    </AppTooltipProvider>
  )
}
