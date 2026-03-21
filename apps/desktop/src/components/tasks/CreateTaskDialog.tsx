import { useEffect, useRef, useState } from 'react'
import { Loader2, Mic, Square } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
} from '@/components/ui/dialog'
import { getWhisperClient, type WhisperStatus } from '@/lib/whisper-client'
import { validateTaskTitle, validateTaskDescription } from '@/lib/validation'
import {
  type BackendConfig,
  type IssueCreatePayload,
  type MCPTool,
} from '@/lib/orchestra-client'
import type { Project } from '@/lib/orchestra-types'
import { AgentSelector, ProjectSelector } from '@/components/app-shell/shared/controls'

export function CreateTaskDialog({
  open,
  onOpenChange,
  config: _config,
  initialState,
  availableAgents,
  allTools: _allTools = [],
  projects = [],
  initialProjectID = '',
  hasUnsandbox = false,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  config: BackendConfig | null
  initialState: string
  availableAgents: string[]
  allTools?: MCPTool[]
  projects?: Project[]
  initialProjectID?: string
  hasUnsandbox?: boolean
  onSubmit: (payload: IssueCreatePayload) => Promise<void>
}) {
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [state, setState] = useState(initialState)
  const [assignee, setAssignee] = useState('Unassigned')
  const [provider, setProvider] = useState('')
  const [harness, setHarness] = useState<'local' | 'unsandbox'>('local')
  const [disabledTools, setDisabledTools] = useState<string[]>([])
  const [projectID, setProjectID] = useState(initialProjectID || (projects.length > 0 ? projects[0].id : ''))
  const [pending, setPending] = useState(false)
  const [titleError, setTitleError] = useState('')
  const [descError, setDescError] = useState('')
  const [submitError, setSubmitError] = useState('')
  const [recording, setRecording] = useState(false)
  const [whisperStatus, setWhisperStatus] = useState<WhisperStatus>({ state: 'idle' })
  const transcriptionCancelledRef = useRef(false)
  const activeFieldRef = useRef<'title' | 'description'>('description')

  useEffect(() => {
    if (open) {
      setState(initialState)
      setProjectID(initialProjectID || (projects.length > 0 ? projects[0].id : ''))
      setTitle('')
      setDescription('')
      setAssignee('Unassigned')
      setProvider(availableAgents.length > 0 ? availableAgents[0] : '')
      setHarness('local')
      setDisabledTools([])
      setSubmitError('')
    }
  }, [open, initialState, initialProjectID, availableAgents, projects])

  useEffect(() => {
    if (!open) {
      const client = getWhisperClient()
      if (client.recording) {
        void client.stopRecording()
      }
      transcriptionCancelledRef.current = true
      setRecording(false)
      setWhisperStatus({ state: 'idle' })
    } else {
      transcriptionCancelledRef.current = false
    }
  }, [open])


  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const tErr = validateTaskTitle(title)
    const dErr = validateTaskDescription(description)
    setTitleError(tErr)
    setDescError(dErr)
    if (tErr || dErr) return
    setPending(true)
    setSubmitError('')
    try {
      await onSubmit({
        title,
        description,
        state,
        assignee_id: assignee,
        project_id: projectID,
        provider: harness === 'unsandbox' ? 'UNSANDBOX' : provider,
        disabled_tools: disabledTools
      })
      onOpenChange(false)
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Task creation failed'
      setSubmitError(message)
    } finally {
      setPending(false)
    }
  }

  const startRecording = async () => {
    if (recording || whisperStatus.state !== 'idle') return
    try {
      const client = getWhisperClient(setWhisperStatus)
      await client.startRecording()
      setRecording(true)
      setSubmitError('')
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Microphone access failed'
      setSubmitError(message)
    }
  }

  const stopRecording = async () => {
    if (!recording) return
    setRecording(false)
    try {
      const client = getWhisperClient(setWhisperStatus)
      const pcm = await client.stopRecording()
      if (pcm.length === 0 || transcriptionCancelledRef.current) return
      const text = await client.transcribe(pcm)
      if (!transcriptionCancelledRef.current && text.trim()) {
        if (activeFieldRef.current === 'title') {
          setTitle((prev) => (prev.trim() ? `${prev.trimEnd()} ${text.trim()}` : text.trim()))
        } else {
          setDescription((prev) => (prev.trim() ? `${prev.trimEnd()}\n${text.trim()}` : text.trim()))
        }
      }
    } catch (error) {
      if (!transcriptionCancelledRef.current) {
        const message = error instanceof Error ? error.message : 'Transcription failed'
        setSubmitError(message)
      }
    } finally {
      if (!transcriptionCancelledRef.current) {
        setWhisperStatus({ state: 'idle' })
      }
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-5xl w-[90vw] bg-card border-border/30 shadow-[0_32px_64px_-12px_rgba(0,0,0,0.6)] p-0 overflow-hidden min-h-[55vh] max-h-[85vh] flex flex-col rounded-2xl">
        <form onSubmit={handleSubmit} className="flex flex-col h-full flex-1">
          <div className="flex-1 flex flex-col overflow-hidden px-6 pt-6 pb-4 space-y-4">
            <input
              autoFocus
              className="w-full bg-transparent border-none outline-none text-xl font-bold placeholder:text-muted-foreground/20 focus:ring-0 focus:outline-none p-0 selection:bg-primary/30"
              placeholder="What needs to be done?"
              value={title}
              onChange={(e) => { setTitle(e.target.value); setTitleError('') }}
              onFocus={() => { activeFieldRef.current = 'title' }}
              required
            />
            {titleError && <p className="text-xs text-red-400 -mt-2">{titleError}</p>}
            <div
              className="flex-1 min-h-0 cursor-text"
              onClick={(e) => {
                const ta = (e.currentTarget as HTMLElement).querySelector('textarea')
                if (ta && e.target === e.currentTarget) ta.focus()
              }}
            >
              <textarea
                className="w-full h-full bg-transparent border-none outline-none text-sm text-foreground/70 placeholder:text-muted-foreground/15 focus:ring-0 focus:outline-none p-0 resize-none min-h-0 selection:bg-primary/20 leading-relaxed"
                placeholder="Describe the task for the agent..."
                value={description}
                onChange={(e) => { setDescription(e.target.value); setDescError('') }}
                onFocus={() => { activeFieldRef.current = 'description' }}
              />
            </div>
            {descError && <p className="text-xs text-red-400 -mt-2">{descError}</p>}
          </div>

          {submitError && (
            <div className="mx-6 mb-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
              {submitError}
            </div>
          )}

          <div className="px-4 py-3 flex items-center justify-between bg-muted/10">
            <div className="flex items-center gap-1">
              <ProjectSelector
                value={projectID}
                projects={projects}
                onChange={setProjectID}
              />
              <div className="w-px h-4 bg-border/20 mx-1" />
              {hasUnsandbox && (
                <>
                  <div className="flex items-center rounded-md border border-border/20 bg-muted/20 p-0.5 gap-0.5">
                    <button
                      type="button"
                      onClick={() => setHarness('local')}
                      className={`h-5 px-2 rounded text-[10px] font-bold uppercase tracking-widest transition-colors ${harness === 'local' ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground/50 hover:text-foreground'}`}
                    >
                      Local
                    </button>
                    <button
                      type="button"
                      onClick={() => setHarness('unsandbox')}
                      className={`h-5 px-2 rounded text-[10px] font-bold uppercase tracking-widest transition-colors ${harness === 'unsandbox' ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground/50 hover:text-foreground'}`}
                    >
                      Unsandbox
                    </button>
                  </div>
                  <div className="w-px h-4 bg-border/20 mx-1" />
                </>
              )}
              {harness === 'local' && (
                <AgentSelector
                  value={assignee}
                  agents={availableAgents}
                  onChange={(val) => {
                    setAssignee(val)
                    const agentName = val.replace('agent-', '')
                    if (availableAgents.includes(agentName)) {
                      setProvider(agentName)
                    } else if (val === '') {
                      setProvider(availableAgents.length > 0 ? availableAgents[0] : '')
                    }
                  }}
                />
              )}
            </div>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onPointerDown={(event) => {
                  event.preventDefault()
                  void startRecording()
                }}
                onPointerUp={(event) => {
                  event.preventDefault()
                  void stopRecording()
                }}
                onPointerLeave={() => {
                  void stopRecording()
                }}
                onPointerCancel={() => {
                  void stopRecording()
                }}
                onKeyDown={(event) => {
                  if (event.key === ' ' || event.key === 'Enter') {
                    event.preventDefault()
                    void startRecording()
                  }
                }}
                onKeyUp={(event) => {
                  if (event.key === ' ' || event.key === 'Enter') {
                    event.preventDefault()
                    void stopRecording()
                  }
                }}
                disabled={whisperStatus.state !== 'idle' && !recording}
                className={`h-7 px-3 text-[10px] font-bold uppercase tracking-widest touch-none ${
                  recording
                    ? 'text-red-400'
                    : whisperStatus.state !== 'idle'
                      ? 'text-amber-400'
                      : 'text-muted-foreground/50 hover:text-foreground'
                }`}
              >
                {recording ? (
                  <><Square className="h-3 w-3 mr-1" /> Release to Stop</>
                ) : whisperStatus.state === 'loading' ? (
                  <><Loader2 className="h-3 w-3 mr-1 animate-spin-smooth" /> Loading {whisperStatus.progress}%</>
                ) : whisperStatus.state === 'transcribing' ? (
                  <><Loader2 className="h-3 w-3 mr-1 animate-spin-smooth" /> Transcribing...</>
                ) : (
                  <><Mic className="h-3 w-3 mr-1" /> Hold to Talk</>
                )}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => onOpenChange(false)}
                disabled={pending}
                className="text-muted-foreground/40 hover:text-foreground h-7 px-3 text-[10px] font-bold uppercase tracking-widest"
              >
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={pending || !title.trim() || !projectID}
                className="h-7 px-4 bg-primary text-primary-foreground hover:bg-primary/90 shadow-lg shadow-primary/20 font-bold uppercase tracking-widest text-[10px] disabled:opacity-30"
              >
                {pending ? <Loader2 className="h-3 w-3 animate-spin-smooth" /> : 'Create'}
              </Button>
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
