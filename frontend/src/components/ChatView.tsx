import { useEffect, useLayoutEffect, useRef, useState, useCallback, type DragEvent } from 'react'
import { UploadCloud, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cancelActiveChatRun, createSession, editAssistantMessage, forkSessionAtMessage, getActiveChatRun, getConfigurationCatalog, getHistory, listConcierges, respondToInteraction, updateSession } from '../api/client'
import { streamContinue, streamMessage, streamRegenerate, streamRun, type StreamEvent } from '../api/stream'
import type { ChatMessage, ChatRun, ConciergeItem, GenerationOptions, InteractionRequest, ReasoningEffort, SendMessageResponse, Session, SessionTarget, StreamToolCall, UploadResult } from '../api/types'
import { activePath, buildById, buildChildrenMap } from '../lib/tree'
import MessageBubble from './MessageBubble'
import Composer from './Composer'
import GenerationProgress, { type StreamActivity } from './GenerationProgress'
import { appendTerminalOutput, renderTerminalOutput } from '../lib/terminalOutput'
import { pendingAttachmentPrefix } from '../lib/attachments'
import i18n from '../i18n'

const COMMAND_HELP_CACHE_KEY = 'hephaestus.commandHelp'
const COMMAND_HELP_CACHE_TTL_MS = 24 * 60 * 60 * 1000

interface Props {
  sessionId: number | null
	project: string | null
  draftConcierge?: ConciergeItem | null
  isChoosingConcierge?: boolean
  defaultConciergeId?: string | null
  configurationRefreshKey: number
  onChooseConcierge?: (concierge: ConciergeItem) => void
  onDefaultConciergeResolved?: (conciergeId: string) => void
  onSessionCreated?: (id: number) => void
  onSessionUpdated?: (session: Session) => void
  onSessionTarget?: (target: SessionTarget) => void
}

// notifyPermissionRequest surfaces a desktop notification for an
// ask_permission event when the user has already granted notification
// permission, and lazily asks for it otherwise so the first prompt in a
// session can request it (browsers require a user gesture to grant, so a
// fire-and-forget call here is best-effort, not guaranteed to prompt).
function notifyPermissionRequest(request: InteractionRequest) {
  if (typeof Notification === 'undefined') return
  if (Notification.permission === 'default') {
    void Notification.requestPermission()
    return
  }
  if (Notification.permission === 'granted') {
    new Notification(request.title, { body: i18n.t('chat.permission.notification') })
  }
}

function readCommandHelpCache(): string | null {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(COMMAND_HELP_CACHE_KEY) ?? 'null')
    if (
      typeof value === 'object' && value !== null &&
      typeof (value as { response?: unknown }).response === 'string' &&
      typeof (value as { expiresAt?: unknown }).expiresAt === 'number' &&
      (value as { expiresAt: number }).expiresAt > Date.now()
    ) {
      return (value as { response: string }).response
    }
  } catch {
    // A malformed cache should not prevent command entry.
  }
  localStorage.removeItem(COMMAND_HELP_CACHE_KEY)
  return null
}

function cacheCommandHelp(response: string) {
  localStorage.setItem(COMMAND_HELP_CACHE_KEY, JSON.stringify({
    response,
    expiresAt: Date.now() + COMMAND_HELP_CACHE_TTL_MS,
  }))
}

// consumeStream centralizes the event switch shared by send/regenerate/
// continue, so all three streaming paths handle every event type
// identically (in particular, ask_permission used to be silently dropped
// by regenerate and continue).
async function consumeStream(
  gen: AsyncGenerator<StreamEvent>,
  signal: AbortSignal,
  handlers: {
    setStreamingText: (updater: (text: string) => string) => void
    setStreamingActivities: (updater: (activities: StreamActivity[]) => StreamActivity[]) => void
    onSessionUpdated?: (session: Session) => void
    onSnapshot?: (run: ChatRun) => void
      onDone: (data: SendMessageResponse) => void | Promise<void>
    onError: (message: string) => void
    isCurrent?: () => boolean
  },
) {
  for await (const ev of gen) {
    if (handlers.isCurrent && !handlers.isCurrent()) continue
    if (ev.type === 'delta') {
      handlers.setStreamingText(t => t + ev.data)
    } else if (ev.type === 'reasoning') {
      handlers.setStreamingActivities(current => appendReasoningActivity(current, ev.sequence, ev.data))
    } else if (ev.type === 'tool_call' || ev.type === 'tool_output' || ev.type === 'tool_result') {
      handlers.setStreamingActivities(current => mergeToolActivity(current, ev.sequence, ev.data))
    } else if (ev.type === 'session_updated') {
      handlers.onSessionUpdated?.(ev.data)
    } else if (ev.type === 'ask_permission') {
      handlers.setStreamingActivities(current => [...current, { type: 'permission', sequence: ev.sequence, request: ev.data }])
      notifyPermissionRequest(ev.data)
    } else if (ev.type === 'snapshot') {
      handlers.onSnapshot?.(ev.data)
    } else if (ev.type === 'done') {
      if (ev.data.status === 'succeeded') await handlers.onDone(ev.data.response)
      else if (!signal.aborted) handlers.onError(ev.data.error || 'chat generation failed')
    } else if (ev.type === 'error') {
      if (!signal.aborted) handlers.onError(ev.data)
    }
  }
}

export default function ChatView({ sessionId, project, draftConcierge, isChoosingConcierge = false, defaultConciergeId, configurationRefreshKey, onChooseConcierge, onDefaultConciergeResolved, onSessionCreated, onSessionUpdated, onSessionTarget }: Props) {
  const { t } = useTranslation()
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [localLeafId, setLocalLeafId] = useState<number | null>(null)
  const [streaming, setStreaming] = useState(false)
  const [streamingText, setStreamingText] = useState('')
  const [streamingActivities, setStreamingActivities] = useState<StreamActivity[]>([])
  const [optimisticUserMessage, setOptimisticUserMessage] = useState<ChatMessage | null>(null)
  const [regeneratingMessageId, setRegeneratingMessageId] = useState<number | null>(null)
  const [continuingMessageId, setContinuingMessageId] = useState<number | null>(null)
  const [editingMessageId, setEditingMessageId] = useState<number | null>(null)
  const [forking, setForking] = useState(false)
  const [commandResponse, setCommandResponse] = useState<string | null>(null)
  const [commandHelp, setCommandHelp] = useState<string | null>(readCommandHelpCache)
  const [commandHelpLoading, setCommandHelpLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [uploadWarnings, setUploadWarnings] = useState<string[]>([])
  const [pendingFiles, setPendingFiles] = useState<File[]>([])
  const [dragDepth, setDragDepth] = useState(0)
  const [concierges, setConcierges] = useState<ConciergeItem[]>([])
  const [availablePlugins, setAvailablePlugins] = useState<string[]>([])
  const [pluginDescriptions, setPluginDescriptions] = useState<Record<string, string>>({})
  const [resolvedSessionId, setResolvedSessionId] = useState<number | null>(sessionId)
  const [activeSession, setActiveSession] = useState<Session | null>(null)
  const [headerTitleDraft, setHeaderTitleDraft] = useState('')
  const [generationOptions, setGenerationOptions] = useState<GenerationOptions>({ reasoningEffort: 'high', webSearch: false })
  const messagesPaneRef = useRef<HTMLDivElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const streamAbortRef = useRef<AbortController | null>(null)
  const streamSessionRef = useRef<number | null>(null)
  const currentSessionRef = useRef<number | null>(sessionId)
	const viewEpochRef = useRef(0)
  const shouldAutoScrollRef = useRef(true)
  const initializedOptionsSessionRef = useRef<number | null>(null)
  const createdSessionRef = useRef<number | null>(null)
  const cancelledTitleEditRef = useRef(false)

  useLayoutEffect(() => {
    const isPromotingDraftSession = sessionId != null && streamSessionRef.current === sessionId
    if (isPromotingDraftSession) {
      currentSessionRef.current = sessionId
      setResolvedSessionId(sessionId)
      return
    }

		viewEpochRef.current++
    currentSessionRef.current = sessionId
    if (streamSessionRef.current != null && streamSessionRef.current !== sessionId) {
      streamAbortRef.current?.abort()
      streamAbortRef.current = null
      streamSessionRef.current = null
    }
    setResolvedSessionId(sessionId)
    setActiveSession(null)
    setStreaming(false)
    setStreamingText('')
    setStreamingActivities([])
    setOptimisticUserMessage(null)
    setRegeneratingMessageId(null)
    setContinuingMessageId(null)
    initializedOptionsSessionRef.current = null
    createdSessionRef.current = null
    shouldAutoScrollRef.current = true
  }, [sessionId])

  const loadHistory = useCallback(async (targetSessionId: number, signal?: AbortSignal, epoch = viewEpochRef.current) => {
    const h = await getHistory(targetSessionId, signal)
    if (signal?.aborted || epoch !== viewEpochRef.current) return
    setActiveSession(h.session)
    setMessages(h.messages)
    setLocalLeafId(h.session.ActiveLeafMessageID)
    if (initializedOptionsSessionRef.current !== targetSessionId) {
      setGenerationOptions({
        reasoningEffort: composerReasoningEffort(h.session.ReasoningEffort || h.reasoning_effort),
        webSearch: h.session.EnableWebSearch ?? false,
      })
      initializedOptionsSessionRef.current = targetSessionId
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    if (resolvedSessionId == null) {
      setMessages([])
      setLocalLeafId(null)
    } else {
      void loadHistory(resolvedSessionId, controller.signal).catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(String(cause))
      })
    }
    setRegeneratingMessageId(null)
    setCommandResponse(null)
    setError(null)
    setUploadWarnings([])
    return () => controller.abort()
  }, [resolvedSessionId, loadHistory])

  useEffect(() => {
    if (resolvedSessionId == null) return
    if (streamAbortRef.current != null && streamSessionRef.current === resolvedSessionId) return
    let disposed = false
    const controller = new AbortController()
    const epoch = viewEpochRef.current
    const isCurrent = () => !disposed && !controller.signal.aborted && epoch === viewEpochRef.current && currentSessionRef.current === resolvedSessionId
    void getActiveChatRun(resolvedSessionId).then(async run => {
    if (!isCurrent()) return
      setStreaming(true)
      streamAbortRef.current = controller
      streamSessionRef.current = resolvedSessionId
      await consumeStream(streamRun(run.id, controller.signal), controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async () => {
			if (isCurrent()) await loadHistory(resolvedSessionId, undefined, epoch)
        },
        onError: setError,
			isCurrent,
      })
    }).catch((cause: unknown) => {
      if (!disposed && !controller.signal.aborted && !(cause instanceof Error && cause.message === 'no active chat run')) setError(String(cause))
    }).finally(() => {
			if (isCurrent()) {
        setStreaming(false)
        setStreamingText('')
        setStreamingActivities([])
      }
    })
    return () => {
      disposed = true
      controller.abort()
      if (streamAbortRef.current === controller) streamAbortRef.current = null
    }
  }, [resolvedSessionId, loadHistory, onSessionUpdated])

  useEffect(() => {
    void listConcierges(project ?? undefined).then(items => {
      setConcierges(items)
      if (isChoosingConcierge && !items.some(concierge => concierge.name === defaultConciergeId)) {
        const fallback = items[0]
        if (fallback) onDefaultConciergeResolved?.(fallback.name)
      }
    }).catch((cause: unknown) => setError(String(cause)))
  }, [project, isChoosingConcierge, defaultConciergeId, configurationRefreshKey, onDefaultConciergeResolved])

  useEffect(() => {
    void getConfigurationCatalog().then(catalog => {
      setAvailablePlugins(catalog.plugins)
      setPluginDescriptions(catalog.plugin_descriptions ?? {})
    }).catch(() => undefined)
  }, [configurationRefreshKey])

  // 历史加载 / 切换会话 / 编辑完成：整段内容被替换，直接瞬间跳到最新位置，
  // 避免从顶部做一次跨全高的平滑滚动（会给人“被硬控”的感觉）。
  useLayoutEffect(() => {
    if (!shouldAutoScrollRef.current) return
    const pane = messagesPaneRef.current
    if (pane) pane.scrollTop = pane.scrollHeight
  }, [messages])

  // 流式输出过程中：增量内容很短，平滑跟随到底部更符合直觉。
  useEffect(() => {
    if (!shouldAutoScrollRef.current) return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [streamingText, streamingActivities])

  const handleMessagesScroll = () => {
    const pane = messagesPaneRef.current
    if (!pane) return
    shouldAutoScrollRef.current = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 40
  }

  const handleHeaderTitleSubmit = useCallback(async () => {
    if (resolvedSessionId == null) return
    const currentTitle = activeSession?.Title || `Session #${resolvedSessionId}`
    const title = headerTitleDraft.trim()
    if (!title || title === currentTitle) {
      setHeaderTitleDraft(currentTitle)
      return
    }
    try {
      const updated = await updateSession(resolvedSessionId, { title })
      setActiveSession(updated)
      setHeaderTitleDraft(updated.Title)
      onSessionUpdated?.(updated)
    } catch (cause) {
      setHeaderTitleDraft(currentTitle)
      setError(String(cause))
    }
  }, [activeSession, headerTitleDraft, onSessionUpdated, resolvedSessionId])

  const handleFilesChange = useCallback((next: File[]) => {
    if (next.length > 5 || next.some(file => file.size > 50 * 1024 * 1024) || next.reduce((total, file) => total + file.size, 0) > 250 * 1024 * 1024) return
    setPendingFiles(next)
  }, [])

  const handleGenerationOptionsChange = useCallback((options: GenerationOptions) => {
    setGenerationOptions(options)
    if (resolvedSessionId == null) return
    void updateSession(resolvedSessionId, {
      reasoningEffort: options.reasoningEffort,
      enableWebSearch: options.webSearch,
    }).catch((cause: unknown) => setError(String(cause)))
  }, [resolvedSessionId])

  const handleCommandHelpRequest = useCallback(async () => {
    if (commandHelp || commandHelpLoading || resolvedSessionId == null || streaming) return
    setCommandHelpLoading(true)
    try {
      const response = await fetch(`/api/v1/sessions/${resolvedSessionId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text: '/help' }),
      })
      if (!response.ok) {
        const body = await response.json().catch(() => ({ error: response.statusText }))
        throw new Error(body.error ?? response.statusText)
      }
      const data = await response.json() as SendMessageResponse
      if (data.command_response) {
        cacheCommandHelp(data.command_response)
        setCommandHelp(data.command_response)
      }
    } catch (cause) {
      setError(String(cause))
    } finally {
      setCommandHelpLoading(false)
    }
  }, [commandHelp, commandHelpLoading, resolvedSessionId, streaming])

  const handleToolGroupToggle = useCallback(async (toolGroup: string, active: boolean) => {
    if (resolvedSessionId == null || streaming) return
    try {
      const response = await fetch(`/api/v1/sessions/${resolvedSessionId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text: `/${active ? 'activate' : 'deactivate'} toolgroup ${toolGroup}` }),
      })
      if (!response.ok) {
        const body = await response.json().catch(() => ({ error: response.statusText }))
        throw new Error(body.error ?? response.statusText)
      }
      setActiveSession(current => {
        if (current == null) return current
        const toolGroups = active
          ? [...new Set([...current.Settings.tool_groups, toolGroup])]
          : current.Settings.tool_groups.filter(currentToolGroup => currentToolGroup !== toolGroup)
        return { ...current, Settings: { ...current.Settings, tool_groups: toolGroups } }
      })
    } catch (cause) {
      setError(String(cause))
    }
  }, [resolvedSessionId, streaming])

  const handlePluginToggle = useCallback(async (plugin: string, active: boolean) => {
    if (resolvedSessionId == null || streaming) return
    try {
      const response = await fetch(`/api/v1/sessions/${resolvedSessionId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text: `/${active ? 'activate' : 'deactivate'} plugin ${plugin}` }),
      })
      if (!response.ok) {
        const body = await response.json().catch(() => ({ error: response.statusText }))
        throw new Error(body.error ?? response.statusText)
      }
      setActiveSession(current => {
        if (current == null) return current
        const plugins = active
          ? [...new Set([...current.Settings.plugins, plugin])]
          : current.Settings.plugins.filter(currentPlugin => currentPlugin !== plugin)
        return { ...current, Settings: { ...current.Settings, plugins } }
      })
    } catch (cause) {
      setError(String(cause))
    }
  }, [resolvedSessionId, streaming])

  const handleDragEnter = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (streaming || !Array.from(event.dataTransfer.types).includes('Files')) return
    event.preventDefault()
    setDragDepth(depth => depth + 1)
  }, [streaming])

  const handleDragOver = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (streaming || !Array.from(event.dataTransfer.types).includes('Files')) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }, [streaming])

  const handleDragLeave = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (streaming || !Array.from(event.dataTransfer.types).includes('Files')) return
    event.preventDefault()
    setDragDepth(depth => Math.max(0, depth - 1))
  }, [streaming])

  const handleDrop = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (streaming || !Array.from(event.dataTransfer.types).includes('Files')) return
    event.preventDefault()
    setDragDepth(0)
    handleFilesChange([...pendingFiles, ...Array.from(event.dataTransfer.files)])
  }, [handleFilesChange, pendingFiles, streaming])

  const byId = buildById(messages)
  const childrenMap = buildChildrenMap(messages)
  const path = activePath(localLeafId, byId)
  const displayMessages = groupToolChains(path)
  const selectedConcierge = draftConcierge ?? concierges.find(concierge => concierge.name === defaultConciergeId) ?? concierges[0] ?? null

  const handleSend = useCallback(async (text: string, files: File[] = [], leafOverride?: number | null) => {
    if (resolvedSessionId == null && text.trimStart().startsWith('/stop')) {
      return
    }

    const leafId = leafOverride !== undefined ? leafOverride : localLeafId
    const previousLeafId = localLeafId
    if (leafOverride !== undefined) setLocalLeafId(leafId ?? null)
    setCommandResponse(null)
    setError(null)
    setStreaming(true)
    setStreamingText('')
    setStreamingActivities([])
    shouldAutoScrollRef.current = true
    setOptimisticUserMessage({
      ID: -Date.now(),
      SessionID: resolvedSessionId ?? 0,
      ParentMessageID: leafId ?? null,
      Timestamp: new Date().toISOString(),
      Role: 'user',
      Content: pendingAttachmentPrefix(files) + text,
      Status: 'complete',
      ReasoningContent: '',
      ToolCalls: null,
      ToolCallID: '',
	  Attachments: [],
    })

    const controller = new AbortController()
    streamAbortRef.current = controller
    let targetSessionId = resolvedSessionId
    streamSessionRef.current = targetSessionId
		const epoch = viewEpochRef.current
		const isCurrent = () => !controller.signal.aborted && epoch === viewEpochRef.current && currentSessionRef.current === targetSessionId
    let switchedSession = false
    try {
      if (targetSessionId == null) {
        if (!selectedConcierge) {
          throw new Error(t('chat.concierge.selectBeforeStarting'))
        }
        if (project == null) throw new Error('No project selected')
        const created = await createSession(selectedConcierge.name, project)
        targetSessionId = created.ID
        setActiveSession(created)
        initializedOptionsSessionRef.current = created.ID
        createdSessionRef.current = created.ID
        setResolvedSessionId(created.ID)
        streamSessionRef.current = created.ID
        onSessionCreated?.(created.ID)
        const updated = await updateSession(created.ID, {
          reasoningEffort: generationOptions.reasoningEffort,
          enableWebSearch: generationOptions.webSearch,
        })
        setActiveSession(updated)
      }

      const gen = streamMessage(targetSessionId, text, leafId, files, generationOptions, controller.signal)
      await consumeStream(gen, controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async data => {
          if (data.command_response) setCommandResponse(data.command_response)
          if (data.session_target) {
			switchedSession = true
            onSessionTarget?.(data.session_target)
            return
          }
          const uploads = data.metadata?.uploads as UploadResult | undefined
          setUploadWarnings(uploads?.warnings ?? [])
          if (!isCurrent()) return
          await loadHistory(targetSessionId!, undefined, epoch)
          if (data.message) setLocalLeafId(data.message.ID)
        },
        onError: setError,
			isCurrent,
      })
    } catch (cause) {
      if (leafOverride !== undefined) setLocalLeafId(previousLeafId)
      if (!controller.signal.aborted) setError(String(cause))
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null
      if (!switchedSession && targetSessionId != null && currentSessionRef.current === targetSessionId) await loadHistory(targetSessionId)
      if (currentSessionRef.current === targetSessionId) {
        setStreaming(false)
        setStreamingText('')
        setStreamingActivities([])
        setOptimisticUserMessage(null)
      }
    }
  }, [resolvedSessionId, selectedConcierge, project, localLeafId, loadHistory, onSessionCreated, onSessionUpdated, onSessionTarget, generationOptions, t])

  const handleRegenerate = useCallback(async (messageId: number) => {
    if (resolvedSessionId == null) return

    setError(null)
    setStreaming(true)
    setStreamingText('')
    setStreamingActivities([])
    setRegeneratingMessageId(messageId)
    shouldAutoScrollRef.current = true
    const controller = new AbortController()
    streamAbortRef.current = controller
    streamSessionRef.current = resolvedSessionId
		const epoch = viewEpochRef.current
		const isCurrent = () => !controller.signal.aborted && epoch === viewEpochRef.current && currentSessionRef.current === resolvedSessionId
    try {
      const gen = streamRegenerate(resolvedSessionId, generationOptions, controller.signal)
      await consumeStream(gen, controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async data => {
          if (!isCurrent()) return
          await loadHistory(resolvedSessionId, undefined, epoch)
          if (data.message) setLocalLeafId(data.message.ID)
        },
        onError: setError,
			isCurrent,
      })
    } catch (cause) {
      if (!controller.signal.aborted) setError(String(cause))
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null
      if (currentSessionRef.current === resolvedSessionId) {
        await loadHistory(resolvedSessionId)
        setStreaming(false)
        setStreamingText('')
        setStreamingActivities([])
        setRegeneratingMessageId(null)
      }
    }
  }, [resolvedSessionId, loadHistory, onSessionUpdated, generationOptions])

  const handleContinue = useCallback(async (messageId: number) => {
    if (resolvedSessionId == null) return

    setError(null)
    setStreaming(true)
    setStreamingText('')
    setStreamingActivities([])
    setContinuingMessageId(messageId)
    shouldAutoScrollRef.current = true
    const controller = new AbortController()
    streamAbortRef.current = controller
    streamSessionRef.current = resolvedSessionId
		const epoch = viewEpochRef.current
		const isCurrent = () => !controller.signal.aborted && epoch === viewEpochRef.current && currentSessionRef.current === resolvedSessionId
    try {
      const gen = streamContinue(resolvedSessionId, messageId, controller.signal)
      await consumeStream(gen, controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async data => {
          if (!isCurrent()) return
          await loadHistory(resolvedSessionId, undefined, epoch)
          if (data.message) setLocalLeafId(data.message.ID)
        },
        onError: setError,
			isCurrent,
      })
    } catch (cause) {
      if (!controller.signal.aborted) setError(String(cause))
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null
      if (currentSessionRef.current === resolvedSessionId) {
        await loadHistory(resolvedSessionId)
        setStreaming(false)
        setStreamingText('')
        setStreamingActivities([])
        setContinuingMessageId(null)
      }
    }
  }, [resolvedSessionId, loadHistory, onSessionUpdated])

  const handleBranchSwitch = useCallback((newLeafId: number) => {
    setLocalLeafId(newLeafId)
  }, [])

  const handleEditAssistant = useCallback(async (messageId: number, content: string, reasoningContent: string) => {
    if (resolvedSessionId == null || localLeafId == null) return

    setError(null)
    setEditingMessageId(messageId)
    try {
      const response = await editAssistantMessage(
        resolvedSessionId,
        messageId,
        localLeafId,
        content,
        reasoningContent,
      )
      await loadHistory(resolvedSessionId)
      if (response.message) setLocalLeafId(response.message.ID)
    } catch (cause) {
      setError(String(cause))
      throw cause
    } finally {
      setEditingMessageId(null)
    }
  }, [resolvedSessionId, localLeafId, loadHistory])

  const handleStop = useCallback(async () => {
    if (resolvedSessionId == null) return
    try {
      await cancelActiveChatRun(resolvedSessionId)
    } catch (cause) {
      setError(String(cause))
    }
  }, [resolvedSessionId])

  const handleForkAtMessage = useCallback(async (messageId: number) => {
    if (resolvedSessionId == null || streaming || forking) return
    setForking(true)
    setError(null)
    try {
      const fork = await forkSessionAtMessage(resolvedSessionId, messageId)
      onSessionCreated?.(fork.ID)
    } catch (cause) {
      setError(String(cause))
    } finally {
      setForking(false)
    }
  }, [forking, onSessionCreated, resolvedSessionId, streaming])

  const handlePermissionResponse = useCallback(async (_request: import('../api/types').InteractionRequest, approved: boolean): Promise<boolean> => {
  if (resolvedSessionId == null) return false
  try {
    await respondToInteraction(resolvedSessionId, approved)
    setStreamingActivities(current => current.filter(activity => activity.type !== 'permission'))
    return true
  } catch (cause) {
    setError(String(cause))
    return false
  }
  }, [resolvedSessionId])

  const lastAssistantIdx = displayMessages.map(item => item.message.Role).lastIndexOf('assistant')

  const isNewSession = resolvedSessionId == null && path.length === 0 && !streaming
  const headerTitle = activeSession?.Title || (resolvedSessionId == null ? t('chat.session.new') : t('chat.session.unnamed', { id: resolvedSessionId }))
  const conciergeName = activeSession?.SourceConcierge || selectedConcierge?.name
  const conciergeNickname = concierges.find(concierge => concierge.name === conciergeName)?.nickname || conciergeName || t('chat.concierge.notSelected')
  const sessionConcierge = concierges.find(concierge => concierge.name === activeSession?.SourceConcierge)
  const toolGroups = activeSession == null ? [] : [...new Set([
    ...(activeSession.Settings.tool_groups ?? []),
    ...(sessionConcierge?.tool_groups ?? []),
  ])].filter(toolGroup => toolGroup !== 'web').sort((left, right) => left.localeCompare(right))
  const activeToolGroups = (activeSession?.Settings.tool_groups ?? []).filter(toolGroup => toolGroup !== 'web')
  const plugins = [...new Set([...availablePlugins, ...(activeSession?.Settings.plugins ?? [])])]
    .sort((left, right) => left.localeCompare(right))
  const activePlugins = activeSession?.Settings.plugins ?? []

  useEffect(() => {
    setHeaderTitleDraft(headerTitle)
  }, [headerTitle])

  return (
    <div
      className={'chat-surface' + (isNewSession ? ' new-session' : '')}
      onDragEnter={handleDragEnter}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {dragDepth > 0 && (
        <div className="file-drop-overlay" role="status" aria-live="polite">
          <UploadCloud aria-hidden="true" size={32} strokeWidth={1.8} />
          <strong>{t('chat.dropFiles.title')}</strong>
          <span>{t('chat.dropFiles.limits')}</span>
        </div>
      )}
      <header className="chat-header">
        <div className="chat-header-content">
          {resolvedSessionId == null ? <h2 className="chat-header-title">{headerTitle}</h2> : (
            <label className="chat-header-title-editor">
              <input
                value={headerTitleDraft}
                maxLength={64}
                aria-label={t('chat.session.title')}
                onChange={event => setHeaderTitleDraft(event.target.value)}
                onKeyDown={event => {
                  if (event.key === 'Enter') {
                    event.preventDefault()
                    event.currentTarget.blur()
                  } else if (event.key === 'Escape') {
                    event.preventDefault()
                    cancelledTitleEditRef.current = true
                    setHeaderTitleDraft(headerTitle)
                    event.currentTarget.blur()
                  }
                }}
                onBlur={() => {
                  if (cancelledTitleEditRef.current) {
                    cancelledTitleEditRef.current = false
                    return
                  }
                  void handleHeaderTitleSubmit()
                }}
              />
              <span aria-hidden="true">{headerTitleDraft || ' '}</span>
            </label>
          )}
          <div className="chat-header-identity">
            <Zap aria-hidden="true" size={12} strokeWidth={1.8} fill="currentColor" />
            <span>{conciergeNickname}</span>
          </div>
        </div>
      </header>
      <div className="messages-pane" ref={messagesPaneRef} onScroll={handleMessagesScroll}>
        {isNewSession ? (
          <div className="empty-state-card">
            <h2>{isChoosingConcierge ? t('chat.concierge.select') : t('chat.concierge.start')}</h2>
            {isChoosingConcierge ? (
              <div className="concierge-card-grid">
                {concierges.map(concierge => (
                  <button
                    className={'concierge-card' + (concierge.name === selectedConcierge?.name ? ' selected' : '')}
                    key={concierge.name}
                    onClick={() => onChooseConcierge?.(concierge)}
                    aria-pressed={concierge.name === selectedConcierge?.name}
                  >
                    <strong>{concierge.identity}</strong>
                    <p>{concierge.description}</p>
                    <CardTags label={t('chat.concierge.toolGroups')} values={concierge.tool_groups} />
                    <CardTags label={t('chat.concierge.impressions')} values={concierge.impressions} />
                  </button>
                ))}
              </div>
            ) : selectedConcierge && (
              <div className="concierge-details">
                <div className="concierge-detail">
                  <span>{t('chat.concierge.advisor')}</span>
                  <strong>{selectedConcierge.name}</strong>
                </div>
                <div className="concierge-detail">
                  <span>{t('chat.concierge.identity')}</span>
                  <p>{selectedConcierge.identity}</p>
                </div>
                <DetailList label={t('chat.concierge.impressions')} values={selectedConcierge.impressions} />
                <DetailList label={t('chat.concierge.toolGroups')} values={selectedConcierge.tool_groups} />
                <DetailList label={t('chat.concierge.plugins')} values={selectedConcierge.plugins} />
              </div>
            )}
          </div>
        ) : (
          displayMessages.map((item, idx) => regeneratingMessageId === item.message.ID || continuingMessageId === item.message.ID ? (
            <div className="message-row assistant" key={item.message.ID}>
  			<GenerationProgress content={streamingText} activities={streamingActivities} onRespondToPermission={handlePermissionResponse} />
            </div>
          ) : (
            <MessageBubble
              key={item.message.ID}
              msg={item.message}
              branchMessage={item.branchMessage}
              processMessages={item.processMessages}
              childrenMap={childrenMap}
              onBranchSwitch={handleBranchSwitch}
              onEditResend={(newText) => handleSend(newText, [], item.message.ParentMessageID)}
              onEditAssistant={(content, reasoningContent) => handleEditAssistant(item.message.ID, content, reasoningContent)}
              editSaving={editingMessageId === item.message.ID}
              editDisabled={streaming || editingMessageId != null}
              forkDisabled={streaming || forking}
              onFork={idx === lastAssistantIdx ? undefined : () => void handleForkAtMessage(item.message.ID)}
              onRegenerate={idx === lastAssistantIdx && !streaming ? () => handleRegenerate(item.message.ID) : undefined}
              onContinue={idx === lastAssistantIdx && !streaming && item.message.Status === 'incomplete' && item.message.Content.trim()
                ? () => handleContinue(item.message.ID)
                : undefined}
            />
          ))
        )}
        {optimisticUserMessage && (
          <div className="optimistic-message">
            <MessageBubble
              msg={optimisticUserMessage}
              childrenMap={new Map()}
              onBranchSwitch={() => undefined}
              onEditResend={() => undefined}
              onEditAssistant={async () => undefined}
            />
          </div>
        )}
        {streaming && regeneratingMessageId == null && (
          <div className="message-row assistant">
			<GenerationProgress content={streamingText} activities={streamingActivities} onRespondToPermission={handlePermissionResponse} />
          </div>
        )}
        {commandResponse && (
          <div className="command-block">{commandResponse}</div>
        )}
        {error && (
          <div className="error-block">{error}</div>
        )}
        {uploadWarnings.length > 0 && (
          <div className="upload-warning-block">{uploadWarnings.map(warning => <div key={warning}>{warning}</div>)}</div>
        )}
        <div ref={bottomRef} />
      </div>
      <Composer
        onSend={(text, files) => handleSend(text, files)}
        commandHelp={commandHelp}
        commandHelpLoading={commandHelpLoading}
        onCommandHelpRequest={handleCommandHelpRequest}
        disabled={streaming}
        onStop={handleStop}
        files={pendingFiles}
        onFilesChange={handleFilesChange}
        generationOptions={generationOptions}
        onGenerationOptionsChange={handleGenerationOptionsChange}
        toolGroups={toolGroups}
        activeToolGroups={activeToolGroups}
        onToolGroupToggle={(toolGroup, active) => { void handleToolGroupToggle(toolGroup, active) }}
        plugins={resolvedSessionId == null ? [] : plugins}
        pluginDescriptions={pluginDescriptions}
        activePlugins={activePlugins}
        onPluginToggle={(plugin, active) => { void handlePluginToggle(plugin, active) }}
      />
    </div>
  )
}

function composerReasoningEffort(effort: string): ReasoningEffort {
  if (effort === 'high' || effort === 'max') return effort
  return 'none'
}

function appendReasoningActivity(current: StreamActivity[], sequence: number, content: string): StreamActivity[] {
  const previous = current.at(-1)
  if (previous?.type === 'reasoning') {
    return [...current.slice(0, -1), { ...previous, content: previous.content + content }]
  }
  return [...current, { type: 'reasoning', sequence, content }]
}

function mergeToolActivity(current: StreamActivity[], sequence: number, incoming: StreamToolCall): StreamActivity[] {
  const maxDisplayedToolOutput = 1024 * 1024
  let index = current.findIndex(activity =>
    activity.type === 'tool' && activity.toolCall.call_index === incoming.call_index && Boolean(incoming.id && activity.toolCall.id === incoming.id),
  )
  if (index === -1) {
    index = current.findIndex(activity =>
      activity.type === 'tool' && activity.toolCall.call_index === incoming.call_index && activity.toolCall.index === incoming.index,
    )
  }
  if (index === -1) {
    // Providers may stream a tool's name and id after its initial argument
    // fragments. Until then, associate the fragment with the latest pending
    // call in this LLM response rather than rendering one card per chunk.
    for (let currentIndex = current.length - 1; currentIndex >= 0; currentIndex--) {
      const activity = current[currentIndex]
      if (activity.type === 'tool' && activity.toolCall.call_index === incoming.call_index && activity.toolCall.status === 'calling') {
        index = currentIndex
        break
      }
    }
  }
  if (index === -1) return [...current, { type: 'tool', sequence, toolCall: incoming }]

  const existing = current[index]
  if (existing.type !== 'tool') return current
  let result = existing.toolCall.result
  let outputCursor = existing.toolCall.output_cursor ?? result?.length ?? 0
  let outputPendingControl = existing.toolCall.output_pending_control
  let outputCarriageReturn = existing.toolCall.output_carriage_return
  if (incoming.result) {
    const rendered = incoming.status === 'calling'
      ? appendTerminalOutput({
          text: result ?? '',
          cursor: outputCursor,
          pendingControl: outputPendingControl,
          carriageReturn: outputCarriageReturn,
        }, incoming.result)
      : renderTerminalOutput(incoming.result)
    result = rendered.text
    outputCursor = rendered.cursor
	outputPendingControl = rendered.pendingControl
	outputCarriageReturn = rendered.carriageReturn
    if (result.length > maxDisplayedToolOutput) {
      const omitted = result.length - maxDisplayedToolOutput
      result = `[earlier output omitted]\n${result.slice(-maxDisplayedToolOutput)}`
      outputCursor = Math.max(0, outputCursor - omitted) + '[earlier output omitted]\n'.length
    }
  }
  const updated = {
    ...existing.toolCall,
    ...incoming,
    id: incoming.id || existing.toolCall.id,
    name: incoming.name || existing.toolCall.name,
    arguments: incoming.arguments
      ? `${existing.toolCall.arguments ?? ''}${incoming.arguments}`
      : existing.toolCall.arguments,
    result,
    output_cursor: outputCursor,
  output_pending_control: outputPendingControl,
  output_carriage_return: outputCarriageReturn,
  }
  return current.map((activity, currentIndex) => currentIndex === index
    ? { ...existing, toolCall: updated }
    : activity,
  )
}

interface DisplayMessage {
  message: ChatMessage
  branchMessage?: ChatMessage
  processMessages?: ChatMessage[]
}

function groupToolChains(path: ChatMessage[]): DisplayMessage[] {
  const grouped: DisplayMessage[] = []

  for (let index = 0; index < path.length;) {
    const message = path[index]
    if (message.Role !== 'assistant') {
      grouped.push({ message })
      index++
      continue
    }

    const continuation = path[index + 1]
    if (message.Status === 'incomplete' && continuation?.Role === 'assistant' && continuation.ParentMessageID === message.ID) {
      grouped.push({
        message: {
          ...continuation,
          Content: message.Content + continuation.Content,
        },
        branchMessage: message,
        processMessages: [message, continuation],
      })
      index += 2
      continue
    }

    let end = index + 1
    while (end < path.length && path[end].Role !== 'user') end++
    const replyChain = path.slice(index, end)
    const hasTools = replyChain.some(item => item.Role === 'tool')
    const finalAssistant = replyChain.findLast(item => item.Role === 'assistant')

    if (hasTools && finalAssistant) {
      grouped.push({
        message: finalAssistant,
        branchMessage: message,
        processMessages: replyChain,
      })
    } else {
      replyChain.forEach(item => grouped.push({ message: item }))
    }
    index = end
  }

  return grouped
}

function DetailList({ label, values }: { label: string; values?: string[] }) {
  const { t } = useTranslation()
  const configuredValues = Array.isArray(values) ? values : []

  return (
    <div className="concierge-detail">
      <span>{label}</span>
      {configuredValues.length > 0 ? (
        <div className="concierge-tag-list">
          {configuredValues.map(value => <span className="concierge-tag" key={value}>{value}</span>)}
        </div>
      ) : (
        <p>{t('chat.concierge.unconfigured')}</p>
      )}
    </div>
  )
}

function CardTags({ label, values }: { label: string; values: string[] }) {
  if (values.length === 0) return null

  return (
    <div className="concierge-card-tags">
      <span>{label}</span>
      <div className="concierge-tag-list">
        {values.map(value => <span className="concierge-tag" key={value}>{value}</span>)}
      </div>
    </div>
  )
}
