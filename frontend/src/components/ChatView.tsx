import { useEffect, useLayoutEffect, useMemo, useRef, useState, useCallback, type Dispatch, type DragEvent, type SetStateAction } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ArrowDown, UploadCloud, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cancelActiveChatRun, cancelSteering, createSession, editAssistantMessage, forkSessionAtMessage, getActiveChatRun, getConfigurationCatalog, getHistory, getSteering, getSubagentRun, listConcierges, putSteering, respondToInteraction, respondToQuestions, sendCommand, setAutomaticApproval, updateSession } from '../api/client'
import { streamContinue, streamMessage, streamRegenerate, streamRun, type StreamEvent } from '../api/stream'
import type { ChatMessage, ChatRun, ConciergeItem, GenerationOptions, InteractionRequest, PermissionInteractionRequest, QuestionsInteractionRequest, ReasoningEffort, ReplayedMessage, SendMessageResponse, Session, SessionTarget, SteeringMode, StreamToolCall, SubagentRunDetail, UploadResult } from '../api/types'
import { activePath, buildById, buildChildrenMap } from '../lib/tree'
import MessageBubble from './MessageBubble'
import Composer from './Composer'
import GenerationProgress, { type StreamActivity } from './GenerationProgress'
import Markdown from './Markdown'
import { appendTerminalOutput, renderTerminalOutput } from '../lib/terminalOutput'
import { parseAttachmentPrefix, pendingAttachmentPrefix } from '../lib/attachments'
import { conciergeComposerOptions, composerReasoningEffort, rememberWebSearchPreference, sessionOptionOverrides, supportsWebSearch, webSearchSelectable, WEB_TOOL_GROUP, type AuthorizationMode } from '../lib/composerOptions'
import i18n from '../i18n'

const WIDE_CHAT_HISTORY_STORAGE_KEY = 'hephaestus.wideChatHistory'
const HISTORY_NAV_TOP_THRESHOLD = 120
const HISTORY_NAV_BOTTOM_THRESHOLD = 900
const HISTORY_NAV_UNLOCK_DISTANCE = 48
const HISTORY_NAV_TARGET_OFFSET = 28
const STRUCTURED_MESSAGE_PREFIX = /^[<`[>#{(*-]/
// The optimistic bubble has no siblings; share one empty map rather than
// allocating a fresh one on every render.
const NO_CHILDREN: ReturnType<typeof buildChildrenMap> = new Map()

interface UserMessageNavigationItem {
  id: number
  summary: string
  index: number
}

interface PendingSteering {
  sessionId: number
  runId: number
  text: string
  mode: SteeringMode
}

function sameNavigationItem(left: UserMessageNavigationItem | null, right: UserMessageNavigationItem | null): boolean {
  return left?.id === right?.id && left?.summary === right?.summary && left?.index === right?.index
}

function summarizeUserMessage(content: string): string {
  const body = parseAttachmentPrefix(content)?.body ?? content
  const lines = body.split(/\r?\n/).map(line => line.trim()).filter(Boolean)
  if (lines.length === 0) return ''
  const line = STRUCTURED_MESSAGE_PREFIX.test(body.trimStart()) ? lines[lines.length - 1] : lines[0]
  return line.replace(/\s+/g, ' ')
}

function messageScrollTop(pane: HTMLElement, message: HTMLElement): number {
  return message.getBoundingClientRect().top - pane.getBoundingClientRect().top + pane.scrollTop
}

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
  onHeaderTitleChange?: (title: string) => void
}

// notifyPermissionRequest surfaces a desktop notification for an
// ask_permission event when the user has already granted notification
// permission, and lazily asks for it otherwise so the first prompt in a
// session can request it (browsers require a user gesture to grant, so a
// fire-and-forget call here is best-effort, not guaranteed to prompt).
function notifyPermissionRequest(request: PermissionInteractionRequest) {
  if (typeof Notification === 'undefined') return
  if (Notification.permission === 'default') {
    void Notification.requestPermission()
    return
  }
  if (Notification.permission === 'granted') {
    new Notification(request.title, { body: i18n.t('chat.permission.notification') })
  }
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
    } else if (ev.type === 'ask_permission' && ev.data.kind === 'permission') {
	  const request = ev.data as PermissionInteractionRequest
      handlers.setStreamingActivities(current => [...current, { type: 'permission', sequence: ev.sequence, request }])
      notifyPermissionRequest(request)
	} else if (ev.type === 'ask_questions' && ev.data.kind === 'questions') {
	  handlers.setStreamingActivities(current => [...current, { type: 'questions', sequence: ev.sequence, request: ev.data as QuestionsInteractionRequest }])
    } else if (ev.type === 'snapshot') {
      handlers.onSnapshot?.(ev.data)
    } else if (ev.type === 'done') {
      if (ev.data.status === 'succeeded') await handlers.onDone(ev.data.response)
      else if (!signal.aborted) handlers.onError(ev.data.error || 'chat generation failed')
      return
    } else if (ev.type === 'error') {
      if (!signal.aborted) handlers.onError(ev.data)
    }
  }
}

export default function ChatView({ sessionId, project, draftConcierge, isChoosingConcierge = false, defaultConciergeId, configurationRefreshKey, onChooseConcierge, onDefaultConciergeResolved, onSessionCreated, onSessionUpdated, onSessionTarget, onHeaderTitleChange }: Props) {
  const { t } = useTranslation()
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [localLeafId, setLocalLeafId] = useState<number | null>(null)
  const [streaming, setStreaming] = useState(false)
  const [streamingText, setStreamingText] = useState('')
  const [streamingActivities, setStreamingActivities] = useState<StreamActivity[]>([])
  const [steeringMode, setSteeringMode] = useState<SteeringMode>('normal')
  const [pendingSteering, setPendingSteering] = useState<PendingSteering | null>(null)
  const [optimisticUserMessage, setOptimisticUserMessage] = useState<ChatMessage | null>(null)
  const [regeneratingMessageId, setRegeneratingMessageId] = useState<number | null>(null)
  const [continuingMessageId, setContinuingMessageId] = useState<number | null>(null)
  const [editingMessageId, setEditingMessageId] = useState<number | null>(null)
  const [forking, setForking] = useState(false)
  const [commandResponse, setCommandResponse] = useState<string | null>(null)
  const [replayedMessages, setReplayedMessages] = useState<ReplayedMessage[]>([])
  const [commandHelp, setCommandHelp] = useState<string | null>(null)
  const [commandHelpLoading, setCommandHelpLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [uploadWarnings, setUploadWarnings] = useState<string[]>([])
  const [pendingFiles, setPendingFiles] = useState<File[]>([])
  const [dragDepth, setDragDepth] = useState(0)
  const [concierges, setConcierges] = useState<ConciergeItem[]>([])
  const [pluginDescriptions, setPluginDescriptions] = useState<Record<string, string>>({})
  const [resolvedSessionId, setResolvedSessionId] = useState<number | null>(sessionId)
  const [activeSession, setActiveSession] = useState<Session | null>(null)
  const [activeSubagentRun, setActiveSubagentRun] = useState<SubagentRunDetail | null>(null)
  const [headerTitleDraft, setHeaderTitleDraft] = useState('')
  const [generationOptions, setGenerationOptions] = useState<GenerationOptions>({ reasoningEffort: 'high', webSearch: false })
  const [authorizationMode, setAuthorizationMode] = useState<AuthorizationMode>('askEachTime')
  const [draftToolGroups, setDraftToolGroups] = useState<string[]>([])
  const [draftPlugins, setDraftPlugins] = useState<string[]>([])
  const [previousUserMessage, setPreviousUserMessage] = useState<UserMessageNavigationItem | null>(null)
  const [showBackToBottom, setShowBackToBottom] = useState(false)
  const [wideChatHistory, setWideChatHistory] = useState(() => localStorage.getItem(WIDE_CHAT_HISTORY_STORAGE_KEY) === 'true')
  const messagesPaneRef = useRef<HTMLDivElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const streamAbortRef = useRef<AbortController | null>(null)
  const streamSessionRef = useRef<number | null>(null)
  const currentSessionRef = useRef<number | null>(sessionId)
	const viewEpochRef = useRef(0)
  const shouldAutoScrollRef = useRef(true)
  // optionsOwnerRef names whose composer options are mounted (`session:<id>` or
  // `draft:<project>\0<concierge>`), so async loads cannot clobber the user's
  // choices and a draft cannot inherit the previous session's values.
  const optionsOwnerRef = useRef<string | null>(null)
  const createdSessionRef = useRef<number | null>(null)
  const cancelledTitleEditRef = useRef(false)
  const lockedUserMessageIdRef = useRef<number | null>(null)
  const highlightedMessageRef = useRef<number | null>(null)
  const [searchParams, setSearchParams] = useSearchParams()
  const selectedConcierge = useMemo(
    () => draftConcierge ?? concierges.find(concierge => concierge.name === defaultConciergeId) ?? concierges[0] ?? null,
    [concierges, defaultConciergeId, draftConcierge],
  )
  const newSessionConcierge = draftConcierge ?? selectedConcierge
  const sessionConcierge = useMemo(
    () => concierges.find(concierge => concierge.name === activeSession?.SourceConcierge),
    [activeSession?.SourceConcierge, concierges],
  )
  // The composer's 联网 control answers to one (Concierge, tool groups) pair:
  // the draft's Concierge and draft tool groups before the session exists, the
  // session's own afterwards.
  const [webConcierge, webToolGroups] = activeSession == null
    ? [newSessionConcierge, draftToolGroups]
    : [sessionConcierge, activeSession.Settings.tool_groups]
  // 联网 is offered whenever the web tool group is within reach: the Concierge
  // may provide it (for a draft, or for a session able to activate it) or the
  // session may already carry it. It is only live once that group is part of
  // the session's own tool groups, because EnableWebSearch merely mutes the
  // tools the group contributes.
  const webSearchAvailable = webSearchSelectable(webConcierge, webToolGroups)
  const webSearchActive = generationOptions.webSearch && supportsWebSearch(webToolGroups)

  const clearStreamingPresentation = useCallback(() => {
    setStreaming(false)
    setStreamingText('')
    setStreamingActivities([])
    setOptimisticUserMessage(null)
  }, [])

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
    setActiveSubagentRun(null)
    clearStreamingPresentation()
    setRegeneratingMessageId(null)
    setContinuingMessageId(null)
    optionsOwnerRef.current = null
    createdSessionRef.current = null
    // A pending ?highlight= scrolls to a specific message once messages load,
    // so the default jump-to-bottom must not fight it on this session's first render.
    shouldAutoScrollRef.current = new URLSearchParams(window.location.search).get('highlight') == null
    highlightedMessageRef.current = null
    lockedUserMessageIdRef.current = null
    setPreviousUserMessage(null)
    setShowBackToBottom(false)
  }, [sessionId, clearStreamingPresentation])

  const loadHistory = useCallback(async (targetSessionId: number, signal?: AbortSignal, epoch = viewEpochRef.current) => {
    const h = await getHistory(targetSessionId, signal)
    if (signal?.aborted || epoch !== viewEpochRef.current) return
    setActiveSession(h.session)
    if (h.session.ParentSubagentRunID != null) {
      const run = await getSubagentRun(h.session.ParentSubagentRunID, signal)
      if (signal?.aborted || epoch !== viewEpochRef.current) return
      setActiveSubagentRun(run)
    } else {
      setActiveSubagentRun(null)
    }
    setMessages(h.messages)
    setLocalLeafId(h.session.ActiveLeafMessageID)
    setAuthorizationMode(h.auto_approve ? 'allowAll' : 'askEachTime')
    const owner = `session:${targetSessionId}`
    if (optionsOwnerRef.current !== owner) {
      setGenerationOptions({
        reasoningEffort: composerReasoningEffort(h.session.ReasoningEffort || h.reasoning_effort),
        webSearch: h.session.EnableWebSearch ?? false,
      })
      optionsOwnerRef.current = owner
    }
  }, [])

  // Automatic approval is runtime state on the server, so a draft always starts
  // from the asking default and only carries "allow all" into createSession.
  useEffect(() => {
    if (resolvedSessionId == null) setAuthorizationMode('askEachTime')
  }, [resolvedSessionId])

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
    setReplayedMessages([])
    setError(null)
    setUploadWarnings([])
    setPendingSteering(null)
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
      const steering = await getSteering(resolvedSessionId).catch((cause: unknown) => {
        if (isCurrent()) setError(String(cause))
        return null
      })
      if (!isCurrent()) return
      setPendingSteering(steering?.run_id == null || steering.text == null || steering.mode == null
        ? null
        : { sessionId: resolvedSessionId, runId: steering.run_id, text: steering.text, mode: steering.mode })
      setStreaming(true)
      streamAbortRef.current = controller
      streamSessionRef.current = resolvedSessionId
      await consumeStream(streamRun(run.id, controller.signal), controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async () => {
          if (!isCurrent()) return
          clearStreamingPresentation()
          void loadHistory(resolvedSessionId, undefined, epoch).catch((cause: unknown) => {
            if (isCurrent()) setError(String(cause))
          })
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
  }, [clearStreamingPresentation, resolvedSessionId, loadHistory, onSessionUpdated])

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
      setPluginDescriptions(catalog.plugin_descriptions ?? {})
    }).catch(() => undefined)
  }, [configurationRefreshKey])

  // 历史加载 / 切换会话 / 编辑完成：整段内容被替换，直接瞬间跳到最新位置，
  // 避免从顶部做一次跨全高的平滑滚动（会给人“被硬控”的感觉）。
  useLayoutEffect(() => {
    if (searchParams.get('highlight') != null && highlightedMessageRef.current == null) return
    if (!shouldAutoScrollRef.current) return
    const pane = messagesPaneRef.current
    if (pane) pane.scrollTop = pane.scrollHeight
  }, [messages, searchParams])

  // Scroll to and briefly highlight a message deep-linked via ?highlight=,
  // e.g. from a chat history search result. Guarded by a ref (rather than
  // just clearing the param) so re-renders while the id is still in flight
  // don't re-highlight, and clearing the param doesn't need to race removal
  // of the highlight class.
  useEffect(() => {
    const raw = searchParams.get('highlight')
    const highlightId = raw ? Number(raw) : null
    if (highlightId == null || Number.isNaN(highlightId) || highlightedMessageRef.current === highlightId) return
    const el = document.getElementById(`message-${highlightId}`)
    if (!el) return
    highlightedMessageRef.current = highlightId
    el.scrollIntoView({ block: 'center', behavior: 'smooth' })
    el.classList.add('message-highlighted')
    window.setTimeout(() => el.classList.remove('message-highlighted'), 2000)
    setSearchParams(params => { params.delete('highlight'); return params }, { replace: true })
  }, [messages, searchParams, setSearchParams])

  // 流式输出过程中：增量内容很短，平滑跟随到底部更符合直觉。
  useEffect(() => {
    if (!shouldAutoScrollRef.current) return
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [streamingText, streamingActivities])

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

  const toggleSessionSetting = useCallback(async (kind: 'toolgroup' | 'plugin', key: 'tool_groups' | 'plugins', name: string, active: boolean) => {
    if (resolvedSessionId == null || streaming) return false
    try {
      await sendCommand(resolvedSessionId, `/${active ? 'activate' : 'deactivate'} ${kind} ${name}`)
      setActiveSession(current => {
        if (current == null) return current
        const names = active
          ? [...new Set([...current.Settings[key], name])]
          : current.Settings[key].filter(currentName => currentName !== name)
        return { ...current, Settings: { ...current.Settings, [key]: names } }
      })
      return true
    } catch (cause) {
      setError(String(cause))
      return false
    }
  }, [resolvedSessionId, streaming])

  // 联网 is gated by two things: the session's tool groups decide whether
  // web_search/web_fetch exist at all, and EnableWebSearch only mutes them.
  // Switching 联网 on therefore has to make sure the group is there: a draft
  // carries it into createSession, and a live session activates it with the
  // same command the tools menu issues. Resolves to whether the group is there.
  const enableWebToolGroup = useCallback(async () => {
    if (resolvedSessionId == null) {
      setDraftToolGroups(current => supportsWebSearch(current) ? current : [...current, WEB_TOOL_GROUP])
      return true
    }
    if (supportsWebSearch(activeSession?.Settings.tool_groups)) return true
    return toggleSessionSetting('toolgroup', 'tool_groups', WEB_TOOL_GROUP, true)
  }, [activeSession, resolvedSessionId, toggleSessionSetting])

  const persistGenerationOptions = useCallback((options: GenerationOptions) => {
    setGenerationOptions(options)
    if (resolvedSessionId == null) return
    void updateSession(resolvedSessionId, {
      reasoningEffort: options.reasoningEffort,
      enableWebSearch: options.webSearch,
    }).catch((cause: unknown) => setError(String(cause)))
  }, [resolvedSessionId])

  const handleReasoningEffortChange = useCallback((reasoningEffort: ReasoningEffort) => {
    persistGenerationOptions({ ...generationOptions, reasoningEffort })
  }, [generationOptions, persistGenerationOptions])

  const handleWebSearchToggle = useCallback(async (enabled: boolean) => {
    // Without the tool group the flag would record 联网 as on while it is not.
    if (enabled && !(await enableWebToolGroup())) return
    // 联网 is remembered per Concierge so sessions started from it later reuse
    // the choice instead of inheriting another Concierge's state.
    const concierge = activeSession?.SourceConcierge ?? newSessionConcierge?.name
    if (concierge != null) rememberWebSearchPreference(concierge, enabled)
    persistGenerationOptions({ ...generationOptions, webSearch: enabled })
  }, [activeSession, enableWebToolGroup, generationOptions, newSessionConcierge, persistGenerationOptions])

  const handleCommandHelpRequest = useCallback(async () => {
    if (commandHelp || commandHelpLoading || resolvedSessionId == null || streaming) return
    setCommandHelpLoading(true)
    try {
      const data = await sendCommand(resolvedSessionId, '/help')
      if (data.command_response) {
        setCommandHelp(data.command_response)
      }
    } catch (cause) {
      setError(String(cause))
    } finally {
      setCommandHelpLoading(false)
    }
  }, [commandHelp, commandHelpLoading, resolvedSessionId, streaming])

  const handleToolGroupToggle = useCallback((toolGroup: string, active: boolean) =>
    toggleSessionSetting('toolgroup', 'tool_groups', toolGroup, active), [toggleSessionSetting])

  const handlePluginToggle = useCallback((plugin: string, active: boolean) =>
    toggleSessionSetting('plugin', 'plugins', plugin, active), [toggleSessionSetting])

  // Accepts a file drag while idle, claiming the event; returns false for
  // drags the composer should ignore.
  const acceptFileDrag = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (streaming || !Array.from(event.dataTransfer.types).includes('Files')) return false
    event.preventDefault()
    return true
  }, [streaming])

  const handleDragEnter = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (acceptFileDrag(event)) setDragDepth(depth => depth + 1)
  }, [acceptFileDrag])
  const handleDragOver = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (acceptFileDrag(event)) event.dataTransfer.dropEffect = 'copy'
  }, [acceptFileDrag])
  const handleDragLeave = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (acceptFileDrag(event)) setDragDepth(depth => Math.max(0, depth - 1))
  }, [acceptFileDrag])
  const handleDrop = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (!acceptFileDrag(event)) return
    setDragDepth(0)
    handleFilesChange([...pendingFiles, ...Array.from(event.dataTransfer.files)])
  }, [acceptFileDrag, handleFilesChange, pendingFiles])

  // Derived once per message-tree change rather than on every streamed token.
  const childrenMap = useMemo(() => buildChildrenMap(messages), [messages])
  const path = useMemo(() => activePath(localLeafId, buildById(messages)), [messages, localLeafId])
  const displayMessages = useMemo(() => groupToolChains(path), [path])
  const lastAssistantIdx = useMemo(
    () => displayMessages.map(item => item.message.Role).lastIndexOf('assistant'),
    [displayMessages],
  )
  const userMessageNavigationItems = useMemo(() => {
    const items: UserMessageNavigationItem[] = displayMessages
      .filter(item => item.message.Role === 'user')
      .map((item, index) => ({
        id: item.message.ID,
        summary: summarizeUserMessage(item.message.Content),
        index: index + 1,
      }))
    if (optimisticUserMessage) {
      items.push({
        id: optimisticUserMessage.ID,
        summary: summarizeUserMessage(optimisticUserMessage.Content),
        index: items.length + 1,
      })
    }
    return items
  }, [displayMessages, optimisticUserMessage])
  const userMessageNavigationRef = useRef(userMessageNavigationItems)
  userMessageNavigationRef.current = userMessageNavigationItems

  const syncScrollNavigation = useCallback(() => {
    const pane = messagesPaneRef.current
    if (!pane) return
    const distanceFromBottom = pane.scrollHeight - pane.scrollTop - pane.clientHeight
    shouldAutoScrollRef.current = distanceFromBottom < 40
    const isNavigationVisible = pane.scrollTop >= HISTORY_NAV_TOP_THRESHOLD && distanceFromBottom >= HISTORY_NAV_BOTTOM_THRESHOLD
    setShowBackToBottom(isNavigationVisible)
    if (!isNavigationVisible) {
      setPreviousUserMessage(null)
      return
    }

    const items = userMessageNavigationRef.current
    const elements = new Map(Array.from(pane.querySelectorAll<HTMLElement>('[data-user-message-id]')).map(element => [
      Number(element.dataset.userMessageId),
      element,
    ]))
    let candidate: UserMessageNavigationItem | null = null
    for (const item of items) {
      const element = elements.get(item.id)
      if (element && messageScrollTop(pane, element) <= pane.scrollTop + 1) candidate = item
      else if (element) break
    }

    const lockedId = lockedUserMessageIdRef.current
    if (lockedId != null) {
      const lockedElement = elements.get(lockedId)
      const lockedItem = items.find(item => item.id === lockedId)
      const movedUpPastTarget = lockedElement && pane.scrollTop < messageScrollTop(pane, lockedElement) - HISTORY_NAV_UNLOCK_DISTANCE
      const movedDownToLaterMessage = lockedItem && candidate && candidate.index > lockedItem.index
      if (lockedElement && lockedItem && !movedUpPastTarget && !movedDownToLaterMessage) {
        setPreviousUserMessage(current => sameNavigationItem(current, lockedItem) ? current : lockedItem)
        return
      }
      lockedUserMessageIdRef.current = null
    }

    setPreviousUserMessage(current => sameNavigationItem(current, candidate) ? current : candidate)
  }, [])

  useLayoutEffect(() => {
    syncScrollNavigation()
  }, [messages, localLeafId, optimisticUserMessage, syncScrollNavigation])

  useEffect(() => {
    const pane = messagesPaneRef.current
    if (!pane) return
    const observer = new ResizeObserver(syncScrollNavigation)
    observer.observe(pane)
    return () => observer.disconnect()
  }, [syncScrollNavigation])

  const handlePreviousUserMessageClick = useCallback(() => {
    const pane = messagesPaneRef.current
    if (!pane || !previousUserMessage) return
    const target = pane.querySelector<HTMLElement>(`[data-user-message-id="${previousUserMessage.id}"]`)
    if (!target) return
    lockedUserMessageIdRef.current = previousUserMessage.id
    pane.scrollTo({ top: Math.max(0, messageScrollTop(pane, target) - HISTORY_NAV_TARGET_OFFSET), behavior: 'smooth' })
  }, [previousUserMessage])

  const handleBackToBottom = useCallback(() => {
    const pane = messagesPaneRef.current
    if (!pane) return
    lockedUserMessageIdRef.current = null
    pane.scrollTo({ top: pane.scrollHeight, behavior: 'smooth' })
  }, [])
  useEffect(() => {
    if (resolvedSessionId != null || newSessionConcierge == null) return
    setDraftToolGroups(newSessionConcierge.default_tool_groups)
    setDraftPlugins(newSessionConcierge.default_plugins)
  }, [newSessionConcierge, resolvedSessionId])

  // A draft starts from the selected Concierge's Identity defaults rather than
  // from the session the user was just looking at. Its options belong to one
  // (project, Concierge) pair, so edits made during the draft survive
  // re-renders while picking another Concierge re-applies its defaults.
  useEffect(() => {
    if (resolvedSessionId != null || newSessionConcierge == null) return
    const owner = `draft:${project ?? ''}\u0000${newSessionConcierge.name}`
    if (optionsOwnerRef.current === owner) return
    optionsOwnerRef.current = owner
    setGenerationOptions(conciergeComposerOptions(newSessionConcierge))
  }, [newSessionConcierge, project, resolvedSessionId])

  const stopActiveRun = useCallback(async () => {
    if (resolvedSessionId == null) return
    try {
      streamAbortRef.current?.abort()
      clearStreamingPresentation()
      setPendingSteering(null)
      await cancelActiveChatRun(resolvedSessionId)
    } catch (cause) {
      setError(String(cause))
    }
  }, [resolvedSessionId, clearStreamingPresentation])

  const handleSend = useCallback(async (text: string, files: File[] = [], leafOverride?: number | null) => {
    const isCommand = text.trimStart().startsWith('/')
    if (streaming && resolvedSessionId != null && text.trim() === '/stop') {
      await stopActiveRun()
      return
    }
    if (streaming && resolvedSessionId != null && !isCommand) {
      const targetSessionId = resolvedSessionId
      const epoch = viewEpochRef.current
      try {
        const response = await putSteering(targetSessionId, text, steeringMode)
        if (currentSessionRef.current !== targetSessionId || epoch !== viewEpochRef.current || response.run_id == null) return
        setPendingSteering({ sessionId: targetSessionId, runId: response.run_id, text: response.text ?? text, mode: response.mode ?? steeringMode })
        setPendingFiles([])
      } catch (cause) {
        if (currentSessionRef.current === targetSessionId && epoch === viewEpochRef.current) setError(String(cause))
      }
      return
    }
    if (resolvedSessionId == null && text.trimStart().startsWith('/stop')) {
      return
    }

    const leafId = leafOverride !== undefined ? leafOverride : localLeafId
    const previousLeafId = localLeafId
    if (leafOverride !== undefined) setLocalLeafId(leafId ?? null)
    setCommandResponse(null)
    setReplayedMessages([])
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
		let completed = false
    try {
      if (targetSessionId == null) {
        if (!selectedConcierge) {
          throw new Error(t('chat.concierge.selectBeforeStarting'))
        }
        if (project == null) throw new Error('No project selected')
        // "allow all" is runtime state on the server, so it is seeded with the
        // session instead of being patched in after the session exists.
        const created = await createSession(selectedConcierge.name, project, draftToolGroups, draftPlugins, authorizationMode === 'allowAll')
        targetSessionId = created.ID
        setActiveSession(created)
        optionsOwnerRef.current = `session:${created.ID}`
        createdSessionRef.current = created.ID
        setResolvedSessionId(created.ID)
        streamSessionRef.current = created.ID
        onSessionCreated?.(created.ID)
        // The backend seeds a new session with the Concierge's Identity
        // defaults, so only persist composer values the user changed away
        // from them (sessionOptionOverrides returns null when they match).
        const overrides = sessionOptionOverrides(created, generationOptions)
        if (overrides != null) setActiveSession(await updateSession(created.ID, overrides))
      }

      const gen = streamMessage(targetSessionId, text, leafId, files, generationOptions, controller.signal)
      await consumeStream(gen, controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async data => {
          if (data.command_response) setCommandResponse(data.command_response)
          setReplayedMessages(data.replayed_messages ?? [])
          if (data.session_target) {
			switchedSession = true
            onSessionTarget?.(data.session_target)
            return
          }
          const uploads = data.metadata?.uploads as UploadResult | undefined
          setUploadWarnings(uploads?.warnings ?? [])
          if (!isCurrent()) return
          clearStreamingPresentation()
          completed = true
          if (data.message) setLocalLeafId(data.message.ID)
          void loadHistory(targetSessionId!, undefined, epoch).catch((cause: unknown) => {
            if (isCurrent()) setError(String(cause))
          })
        },
        onError: setError,
			isCurrent,
      })
    } catch (cause) {
      if (leafOverride !== undefined) setLocalLeafId(previousLeafId)
      if (!controller.signal.aborted) setError(String(cause))
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null
      if (!completed && !switchedSession && targetSessionId != null && currentSessionRef.current === targetSessionId) await loadHistory(targetSessionId)
      if (currentSessionRef.current === targetSessionId) {
        clearStreamingPresentation()
        setPendingSteering(null)
      }
    }
  }, [clearStreamingPresentation, resolvedSessionId, selectedConcierge, project, localLeafId, loadHistory, onSessionCreated, onSessionUpdated, onSessionTarget, generationOptions, draftToolGroups, draftPlugins, steeringMode, stopActiveRun, streaming, authorizationMode, t])

  const runExistingSessionStream = useCallback(async (
    messageId: number,
    createStream: (signal: AbortSignal) => AsyncGenerator<StreamEvent>,
    setActiveMessageId: Dispatch<SetStateAction<number | null>>,
  ) => {
    if (resolvedSessionId == null) return

    setError(null)
    setStreaming(true)
    setStreamingText('')
    setStreamingActivities([])
    setActiveMessageId(messageId)
    shouldAutoScrollRef.current = true
    const controller = new AbortController()
    streamAbortRef.current = controller
    streamSessionRef.current = resolvedSessionId
    const epoch = viewEpochRef.current
    const isCurrent = () => !controller.signal.aborted && epoch === viewEpochRef.current && currentSessionRef.current === resolvedSessionId
    let completed = false
    try {
      await consumeStream(createStream(controller.signal), controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async data => {
          if (!isCurrent()) return
          clearStreamingPresentation()
          setActiveMessageId(null)
          completed = true
          if (data.message) setLocalLeafId(data.message.ID)
          void loadHistory(resolvedSessionId, undefined, epoch).catch((cause: unknown) => {
            if (isCurrent()) setError(String(cause))
          })
        },
        onError: setError,
        isCurrent,
      })
    } catch (cause) {
      if (!controller.signal.aborted) setError(String(cause))
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null
      if (currentSessionRef.current === resolvedSessionId) {
        if (!completed) await loadHistory(resolvedSessionId)
        setStreaming(false)
		setPendingSteering(null)
        setStreamingText('')
        setStreamingActivities([])
        setActiveMessageId(null)
      }
    }
  }, [clearStreamingPresentation, loadHistory, onSessionUpdated, resolvedSessionId])

  const handleRegenerate = useCallback(async (messageId: number) => {
    if (resolvedSessionId == null) return
    await runExistingSessionStream(
      messageId,
      signal => streamRegenerate(resolvedSessionId, generationOptions, signal),
      setRegeneratingMessageId,
    )
  }, [generationOptions, resolvedSessionId, runExistingSessionStream])

  const handleContinue = useCallback(async (messageId: number) => {
    if (resolvedSessionId == null) return
    await runExistingSessionStream(
      messageId,
      signal => streamContinue(resolvedSessionId, messageId, generationOptions, signal),
      setContinuingMessageId,
    )
  }, [generationOptions, resolvedSessionId, runExistingSessionStream])

  const handleEditAssistant = useCallback(async (messageId: number, content: string) => {
    if (resolvedSessionId == null || localLeafId == null) return

    setError(null)
    setEditingMessageId(messageId)
    try {
      const response = await editAssistantMessage(
        resolvedSessionId,
        messageId,
        localLeafId,
        content,
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

  const handleCancelSteering = useCallback(async () => {
    if (pendingSteering == null || resolvedSessionId !== pendingSteering.sessionId) return
    const { sessionId: targetSessionId, runId } = pendingSteering
    const epoch = viewEpochRef.current
    try {
      await cancelSteering(targetSessionId)
      if (currentSessionRef.current === targetSessionId && epoch === viewEpochRef.current) setPendingSteering(null)
    } catch (cause) {
      try {
        const current = await getSteering(targetSessionId)
        if (currentSessionRef.current === targetSessionId && epoch === viewEpochRef.current) {
          setPendingSteering(current?.run_id === runId && current.text != null && current.mode != null
            ? { sessionId: targetSessionId, runId, text: current.text, mode: current.mode }
            : null)
        }
      } catch {
        // Preserve the original failure when state refresh is unavailable.
      }
      if (currentSessionRef.current === targetSessionId && epoch === viewEpochRef.current) setError(String(cause))
    }
  }, [pendingSteering, resolvedSessionId])

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

  const handleAutomaticPermissionApproval = useCallback(async (_request: InteractionRequest): Promise<boolean> => {
    if (resolvedSessionId == null) return false
    try {
      await setAutomaticApproval(resolvedSessionId, true)
      setStreamingActivities(current => current.filter(activity => activity.type !== 'permission'))
      return true
    } catch (cause) {
      setError(String(cause))
      return false
    }
  }, [resolvedSessionId])

  const handleQuestionsResponse = useCallback(async (request: QuestionsInteractionRequest, answers: import('../api/types').QuestionAnswer[]): Promise<boolean> => {
    if (resolvedSessionId == null) return false
    try {
      await respondToQuestions(resolvedSessionId, request.id, answers)
      setStreamingActivities(current => current.filter(activity => activity.type !== 'questions' || activity.request.id !== request.id))
      return true
    } catch (cause) {
      setError(String(cause))
      return false
    }
  }, [resolvedSessionId])

  const handleAuthorizationModeChange = useCallback((mode: AuthorizationMode) => {
    setAuthorizationMode(mode)
    if (resolvedSessionId == null) return
    void setAutomaticApproval(resolvedSessionId, mode === 'allowAll').catch((cause: unknown) => setError(String(cause)))
  }, [resolvedSessionId])

  const handleChatHeaderDoubleClick = () => {
    setWideChatHistory(current => {
      const next = !current
      localStorage.setItem(WIDE_CHAT_HISTORY_STORAGE_KEY, String(next))
      return next
    })
  }

  const isNewSession = resolvedSessionId == null && path.length === 0 && !streaming
  const headerTitle = activeSession?.Title || (resolvedSessionId == null ? t('chat.session.new') : t('chat.session.unnamed', { id: resolvedSessionId }))
  const isSubagentSession = activeSession?.ParentSubagentRunID != null
  const conciergeName = activeSession?.SourceConcierge || selectedConcierge?.name
  const conciergeNickname = concierges.find(concierge => concierge.name === conciergeName)?.nickname || conciergeName || t('chat.concierge.notSelected')
  const toolGroups = activeSession == null ? (newSessionConcierge?.tool_groups ?? []) : [...new Set([
    ...(activeSession.Settings.tool_groups ?? []),
    ...(sessionConcierge?.tool_groups ?? []),
  ])].filter(toolGroup => toolGroup !== WEB_TOOL_GROUP).sort((left, right) => left.localeCompare(right))
  const activeToolGroups = (activeSession == null ? draftToolGroups : activeSession.Settings.tool_groups).filter(toolGroup => toolGroup !== WEB_TOOL_GROUP)
  const plugins = activeSession == null ? (newSessionConcierge?.plugins ?? []) : [...new Set([...(sessionConcierge?.plugins ?? []), ...(activeSession.Settings.plugins ?? [])])]
    .sort((left, right) => left.localeCompare(right))
  const activePlugins = activeSession == null ? draftPlugins : activeSession.Settings.plugins

  useEffect(() => {
    setHeaderTitleDraft(headerTitle)
  }, [headerTitle])

  useEffect(() => {
    onHeaderTitleChange?.(headerTitle)
  }, [headerTitle, onHeaderTitleChange])

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
      <header
        className="chat-header"
        onDoubleClick={event => {
          if (event.target instanceof Element && event.target.closest('.chat-history-message')) return
          handleChatHeaderDoubleClick()
        }}
      >
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
        {previousUserMessage && (
          <button
            type="button"
            className="chat-history-message"
            onClick={handlePreviousUserMessageClick}
            title={`${previousUserMessage.summary} [${previousUserMessage.index}/${userMessageNavigationItems.length}]`}
            aria-label={t('chat.navigation.previousUserMessage', { index: previousUserMessage.index, total: userMessageNavigationItems.length })}
          >
            <span>{previousUserMessage.summary}</span>
            <small>[{previousUserMessage.index}/{userMessageNavigationItems.length}]</small>
          </button>
        )}
      </header>
      <div className={'messages-region' + (wideChatHistory && !isNewSession ? ' wide-chat-history' : '')}>
        <div className="messages-pane" ref={messagesPaneRef} onScroll={syncScrollNavigation}>
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
          <GenerationProgress content={streamingText} activities={streamingActivities} onRespondToPermission={handlePermissionResponse} onRespondToQuestions={handleQuestionsResponse} authorizationMode={authorizationMode} onAutoApprovePermission={handleAutomaticPermissionApproval} />
            </div>
          ) : (
            <MessageBubble
              key={item.message.ID}
              msg={item.message}
              branchMessage={item.branchMessage}
              processMessages={item.processMessages}
              childrenMap={childrenMap}
              onBranchSwitch={setLocalLeafId}
              onEditResend={(newText) => handleSend(newText, [], item.message.ParentMessageID)}
              onEditAssistant={(content) => handleEditAssistant(item.message.ID, content)}
              editSaving={editingMessageId === item.message.ID}
              editDisabled={streaming || editingMessageId != null}
              forkDisabled={streaming || forking}
              readOnly={isSubagentSession}
              onFork={isSubagentSession || idx === lastAssistantIdx ? undefined : () => void handleForkAtMessage(item.message.ID)}
              onRegenerate={!isSubagentSession && idx === lastAssistantIdx && !streaming ? () => handleRegenerate(item.message.ID) : undefined}
              onContinue={!isSubagentSession && item.message.ID === localLeafId && idx === lastAssistantIdx && !streaming && item.message.Content.trim() && (!item.message.ToolCalls || item.message.ToolCalls.length === 0)
                ? () => handleContinue(item.message.ID)
                : undefined}
            />
          ))
        )}
        {optimisticUserMessage && (
          <div className="optimistic-message">
            <MessageBubble
              msg={optimisticUserMessage}
              childrenMap={NO_CHILDREN}
              onBranchSwitch={() => undefined}
              onEditResend={() => undefined}
              onEditAssistant={async () => undefined}
            />
          </div>
        )}
        {streaming && regeneratingMessageId == null && continuingMessageId == null && (
          <div className="message-row assistant">
      <GenerationProgress content={streamingText} activities={streamingActivities} onRespondToPermission={handlePermissionResponse} onRespondToQuestions={handleQuestionsResponse} authorizationMode={authorizationMode} onAutoApprovePermission={handleAutomaticPermissionApproval} />
          </div>
        )}
        {commandResponse && (
          <div className="command-block">{commandResponse}</div>
        )}
        {replayedMessages.map((message, index) => (
          <div className={`message-row ${message.role} replayed-message`} key={`${message.role}-${index}`}>
            <div className={`message-card ${message.role}`}>{message.content}</div>
          </div>
        ))}
        {error && (
          <div className="error-block">{error}</div>
        )}
        {uploadWarnings.length > 0 && (
          <div className="upload-warning-block">{uploadWarnings.map(warning => <div key={warning}>{warning}</div>)}</div>
        )}
        {isSubagentSession && activeSubagentRun && (activeSubagentRun.status === 'failed' || activeSubagentRun.status === 'interrupted') && (
          <div className="subagent-terminal-detail">
            <strong>{t('session.subagentOutcome')}</strong>
            {activeSubagentRun.error && <p>{activeSubagentRun.error}</p>}
            {activeSubagentRun.result && <div className="subagent-terminal-result"><Markdown>{activeSubagentRun.result}</Markdown></div>}
            {!activeSubagentRun.result && <small>{t('session.subagentNoResult')}</small>}
          </div>
        )}
          <div ref={bottomRef} />
        </div>
        {showBackToBottom && (
          <button
            type="button"
            className="chat-back-to-bottom"
            onClick={handleBackToBottom}
            title={t('chat.navigation.backToBottom')}
            aria-label={t('chat.navigation.backToBottom')}
          >
            <ArrowDown aria-hidden="true" size={17} />
          </button>
        )}
      </div>
      {!isSubagentSession && <Composer
        focusKey={resolvedSessionId == null ? `new:${isChoosingConcierge}` : String(resolvedSessionId)}
        onSend={(text, files) => handleSend(text, files)}
        commandHelp={commandHelp}
        commandHelpLoading={commandHelpLoading}
        onCommandHelpRequest={handleCommandHelpRequest}
        disabled={streaming}
        onStop={stopActiveRun}
    steeringMode={steeringMode}
    onSteeringModeChange={setSteeringMode}
    pendingSteering={pendingSteering?.text ?? null}
    onCancelSteering={handleCancelSteering}
        files={pendingFiles}
        onFilesChange={handleFilesChange}
        // The composer reads and writes the effective 联网 state: a session whose
        // web tool group is not active yet shows up as off, so switching it on
        // is exactly what adds the group.
        generationOptions={{ ...generationOptions, webSearch: webSearchActive }}
        webSearchAvailable={webSearchAvailable}
        onReasoningEffortChange={handleReasoningEffortChange}
        onWebSearchToggle={handleWebSearchToggle}
    authorizationMode={authorizationMode}
    onAuthorizationModeChange={handleAuthorizationModeChange}
        toolGroups={toolGroups}
        activeToolGroups={activeToolGroups}
        onToolGroupToggle={(toolGroup, active) => {
          if (resolvedSessionId == null) {
            setDraftToolGroups(current => active ? [...new Set([...current, toolGroup])] : current.filter(item => item !== toolGroup))
            return
          }
          void handleToolGroupToggle(toolGroup, active)
        }}
        plugins={plugins}
        pluginDescriptions={pluginDescriptions}
        activePlugins={activePlugins}
        onPluginToggle={(plugin, active) => {
          if (resolvedSessionId == null) {
            setDraftPlugins(current => active ? [...new Set([...current, plugin])] : current.filter(item => item !== plugin))
            return
          }
          void handlePluginToggle(plugin, active)
        }}
      />}
    </div>
  )
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
