import { useEffect, useLayoutEffect, useRef, useState, useCallback } from 'react'
import { createSession, editAssistantMessage, getHistory, listConcierges, respondToInteraction } from '../api/client'
import { streamContinue, streamMessage, streamRegenerate, type StreamEvent } from '../api/stream'
import type { ChatMessage, ConciergeItem, InteractionRequest, SendMessageResponse, Session, StreamToolCall, UploadResult } from '../api/types'
import { activePath, buildById, buildChildrenMap } from '../lib/tree'
import MessageBubble from './MessageBubble'
import Composer from './Composer'
import GenerationProgress, { type StreamActivity } from './GenerationProgress'
import { appendTerminalOutput, renderTerminalOutput } from '../lib/terminalOutput'
import { pendingAttachmentPrefix } from '../lib/attachments'

interface Props {
  sessionId: number | null
	project: string | null
  draftConcierge?: ConciergeItem | null
  isChoosingConcierge?: boolean
  defaultConciergeId?: string | null
  onChooseConcierge?: (concierge: ConciergeItem) => void
  onDefaultConciergeResolved?: (conciergeId: string) => void
  onSessionCreated?: (id: number) => void
  onSessionUpdated?: (session: Session) => void
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
    new Notification(request.title, { body: '20 秒后将自动允许，请返回 Hephaestus 确认。' })
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
    onDone: (data: SendMessageResponse) => void | Promise<void>
    onError: (message: string) => void
  },
) {
  for await (const ev of gen) {
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
    } else if (ev.type === 'done') {
      await handlers.onDone(ev.data)
    } else if (ev.type === 'error') {
      if (!signal.aborted) handlers.onError(ev.data)
    }
  }
}

export default function ChatView({ sessionId, project, draftConcierge, isChoosingConcierge = false, defaultConciergeId, onChooseConcierge, onDefaultConciergeResolved, onSessionCreated, onSessionUpdated }: Props) {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [localLeafId, setLocalLeafId] = useState<number | null>(null)
  const [streaming, setStreaming] = useState(false)
  const [streamingText, setStreamingText] = useState('')
  const [streamingActivities, setStreamingActivities] = useState<StreamActivity[]>([])
  const [optimisticUserMessage, setOptimisticUserMessage] = useState<ChatMessage | null>(null)
  const [regeneratingMessageId, setRegeneratingMessageId] = useState<number | null>(null)
  const [continuingMessageId, setContinuingMessageId] = useState<number | null>(null)
  const [editingMessageId, setEditingMessageId] = useState<number | null>(null)
  const [commandResponse, setCommandResponse] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [uploadWarnings, setUploadWarnings] = useState<string[]>([])
  const [concierges, setConcierges] = useState<ConciergeItem[]>([])
  const [resolvedSessionId, setResolvedSessionId] = useState<number | null>(sessionId)
  const messagesPaneRef = useRef<HTMLDivElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const streamAbortRef = useRef<AbortController | null>(null)
  const shouldAutoScrollRef = useRef(true)

  useEffect(() => {
    setResolvedSessionId(sessionId)
    shouldAutoScrollRef.current = true
  }, [sessionId])

  const loadHistory = useCallback(async (targetSessionId: number, signal?: AbortSignal) => {
    const h = await getHistory(targetSessionId, signal)
    if (signal?.aborted) return
    setMessages(h.messages)
    setLocalLeafId(h.session.ActiveLeafMessageID)
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
    if (!isChoosingConcierge) return
    void listConcierges().then(items => {
      setConcierges(items)
      if (!items.some(concierge => concierge.name === defaultConciergeId)) {
        const fallback = items[0]
        if (fallback) onDefaultConciergeResolved?.(fallback.name)
      }
    }).catch((cause: unknown) => setError(String(cause)))
  }, [isChoosingConcierge, defaultConciergeId, onDefaultConciergeResolved])

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

  const byId = buildById(messages)
  const childrenMap = buildChildrenMap(messages)
  const path = activePath(localLeafId, byId)
  const displayMessages = groupToolChains(path)
  const selectedConcierge = draftConcierge ?? concierges.find(concierge => concierge.name === defaultConciergeId) ?? concierges[0] ?? null

  const handleSend = useCallback(async (text: string, files: File[] = [], leafOverride?: number) => {
    if (resolvedSessionId == null && text.trimStart().startsWith('/stop')) {
      return
    }

    const leafId = leafOverride !== undefined ? leafOverride : localLeafId
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
    })

    const controller = new AbortController()
    streamAbortRef.current = controller
    let targetSessionId = resolvedSessionId
    try {
      if (targetSessionId == null) {
        if (!selectedConcierge) {
          throw new Error('请先选择顾问再开始新会话')
        }
        if (project == null) throw new Error('No project selected')
        const created = await createSession(selectedConcierge.name, project)
        targetSessionId = created.ID
        setResolvedSessionId(created.ID)
        onSessionCreated?.(created.ID)
      }

      const gen = streamMessage(targetSessionId, text, leafId ?? undefined, files, controller.signal)
      await consumeStream(gen, controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async data => {
          if (data.command_response) setCommandResponse(data.command_response)
          const uploads = data.metadata?.uploads as UploadResult | undefined
          setUploadWarnings(uploads?.warnings ?? [])
          await loadHistory(targetSessionId!)
          if (data.message) setLocalLeafId(data.message.ID)
        },
        onError: setError,
      })
    } catch (cause) {
      if (!controller.signal.aborted) setError(String(cause))
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null
      if (targetSessionId != null) await loadHistory(targetSessionId)
      setStreaming(false)
      setStreamingText('')
      setStreamingActivities([])
      setOptimisticUserMessage(null)
    }
  }, [resolvedSessionId, selectedConcierge, project, localLeafId, loadHistory, onSessionCreated, onSessionUpdated])

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
    try {
      const gen = streamRegenerate(resolvedSessionId, controller.signal)
      await consumeStream(gen, controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async data => {
          await loadHistory(resolvedSessionId)
          if (data.message) setLocalLeafId(data.message.ID)
        },
        onError: setError,
      })
    } catch (cause) {
      if (!controller.signal.aborted) setError(String(cause))
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null
      await loadHistory(resolvedSessionId)
      setStreaming(false)
      setStreamingText('')
      setStreamingActivities([])
      setRegeneratingMessageId(null)
    }
  }, [resolvedSessionId, loadHistory, onSessionUpdated])

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
    try {
      const gen = streamContinue(resolvedSessionId, messageId, controller.signal)
      await consumeStream(gen, controller.signal, {
        setStreamingText,
        setStreamingActivities,
        onSessionUpdated,
        onDone: async data => {
          await loadHistory(resolvedSessionId)
          if (data.message) setLocalLeafId(data.message.ID)
        },
        onError: setError,
      })
    } catch (cause) {
      if (!controller.signal.aborted) setError(String(cause))
    } finally {
      if (streamAbortRef.current === controller) streamAbortRef.current = null
      await loadHistory(resolvedSessionId)
      setStreaming(false)
      setStreamingText('')
      setStreamingActivities([])
      setContinuingMessageId(null)
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
      await fetch(`/api/v1/sessions/${resolvedSessionId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text: '/stop' }),
      })
    } catch (cause) {
      setError(String(cause))
    }
  }, [resolvedSessionId])

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

  return (
    <div className={'chat-surface' + (isNewSession ? ' new-session' : '')}>
      <header className="chat-header">
        <div>
          <h2 className="chat-header-title">
            {resolvedSessionId == null ? `新会话${selectedConcierge ? ` · ${selectedConcierge.name}` : ''}` : '对话内容'}
          </h2>
        </div>
        <div className="chat-badge">
          <span className="chat-dot" />
          实时
        </div>
      </header>
      <div className="messages-pane" ref={messagesPaneRef} onScroll={handleMessagesScroll}>
        {isNewSession ? (
          <div className="empty-state-card">
            <h2>{isChoosingConcierge ? '选择 Concierge' : '开始新的对话'}</h2>
            {isChoosingConcierge ? (
              <div className="concierge-card-grid">
                {concierges.map(concierge => (
                  <button
                    className={'concierge-card' + (concierge.name === selectedConcierge?.name ? ' selected' : '')}
                    key={concierge.name}
                    onClick={() => onChooseConcierge?.(concierge)}
                    aria-pressed={concierge.name === selectedConcierge?.name}
                  >
                    <strong>{concierge.name}</strong>
                    <p>{concierge.identity}</p>
                    <CardTags label="工具组" values={concierge.tool_groups} />
                    <CardTags label="印象" values={concierge.impressions} />
                  </button>
                ))}
              </div>
            ) : selectedConcierge && (
              <div className="concierge-details">
                <div className="concierge-detail">
                  <span>顾问</span>
                  <strong>{selectedConcierge.name}</strong>
                </div>
                <div className="concierge-detail">
                  <span>身份</span>
                  <p>{selectedConcierge.identity}</p>
                </div>
                <DetailList label="印象" values={selectedConcierge.impressions} />
                <DetailList label="工具组" values={selectedConcierge.tool_groups} />
                <DetailList label="插件" values={selectedConcierge.plugins} />
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
              onEditResend={(newText) => handleSend(newText, [], item.message.ParentMessageID ?? undefined)}
              onEditAssistant={(content, reasoningContent) => handleEditAssistant(item.message.ID, content, reasoningContent)}
              editSaving={editingMessageId === item.message.ID}
              editDisabled={streaming || editingMessageId != null}
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
        disabled={streaming}
        onStop={handleStop}
      />
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
  const index = current.findIndex(activity =>
    activity.type === 'tool' && activity.toolCall.call_index === incoming.call_index && (
      activity.toolCall.index === incoming.index || Boolean(incoming.id && activity.toolCall.id === incoming.id)
    ),
  )
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
  const configuredValues = Array.isArray(values) ? values : []

  return (
    <div className="concierge-detail">
      <span>{label}</span>
      {configuredValues.length > 0 ? (
        <div className="concierge-tag-list">
          {configuredValues.map(value => <span className="concierge-tag" key={value}>{value}</span>)}
        </div>
      ) : (
        <p>未配置</p>
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
