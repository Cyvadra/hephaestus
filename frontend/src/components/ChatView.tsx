import { useEffect, useRef, useState, useCallback } from 'react'
import { createSession, getHistory } from '../api/client'
import { streamMessage, streamRegenerate } from '../api/stream'
import type { ChatMessage, ConciergeItem, StreamToolCall } from '../api/types'
import { activePath, buildById, buildChildrenMap } from '../lib/tree'
import MessageBubble from './MessageBubble'
import Composer from './Composer'
import GenerationProgress from './GenerationProgress'

interface Props {
  sessionId: number | null
  draftConcierge?: ConciergeItem | null
  onSessionCreated?: (id: number) => void
}

export default function ChatView({ sessionId, draftConcierge, onSessionCreated }: Props) {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [localLeafId, setLocalLeafId] = useState<number | null>(null)
  const [streaming, setStreaming] = useState(false)
  const [streamingText, setStreamingText] = useState('')
  const [streamingReasoning, setStreamingReasoning] = useState('')
  const [streamingToolCalls, setStreamingToolCalls] = useState<StreamToolCall[]>([])
  const [regeneratingMessageId, setRegeneratingMessageId] = useState<number | null>(null)
  const [commandResponse, setCommandResponse] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [resolvedSessionId, setResolvedSessionId] = useState<number | null>(sessionId)
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    setResolvedSessionId(sessionId)
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
    return () => controller.abort()
  }, [resolvedSessionId, loadHistory])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, streamingText, streamingReasoning, streamingToolCalls])

  const byId = buildById(messages)
  const childrenMap = buildChildrenMap(messages)
  const path = activePath(localLeafId, byId)
  const displayMessages = groupToolChains(path)

  const handleSend = useCallback(async (text: string, leafOverride?: number) => {
    if (resolvedSessionId == null && text.trimStart().startsWith('/stop')) {
      return
    }

    const leafId = leafOverride !== undefined ? leafOverride : localLeafId
    setCommandResponse(null)
    setError(null)
    setStreaming(true)
    setStreamingText('')
    setStreamingReasoning('')
    setStreamingToolCalls([])

    try {
      let targetSessionId = resolvedSessionId
      if (targetSessionId == null) {
        if (!draftConcierge) {
          throw new Error('请先选择顾问再开始新会话')
        }
        const created = await createSession(draftConcierge.name)
        targetSessionId = created.ID
        setResolvedSessionId(created.ID)
        onSessionCreated?.(created.ID)
      }

      const gen = streamMessage(targetSessionId, text, leafId ?? undefined)
      for await (const ev of gen) {
        if (ev.type === 'delta') {
          setStreamingText(t => t + ev.data)
        } else if (ev.type === 'reasoning') {
          setStreamingReasoning(t => t + ev.data)
        } else if (ev.type === 'tool_call' || ev.type === 'tool_result') {
          setStreamingToolCalls(current => mergeToolCall(current, ev.data))
        } else if (ev.type === 'done') {
          if (ev.data.command_response) {
            setCommandResponse(ev.data.command_response)
          }
          await loadHistory(targetSessionId)
          if (ev.data.message) setLocalLeafId(ev.data.message.ID)
        } else if (ev.type === 'error') {
          setError(ev.data)
        }
      }
    } catch (e) {
      setError(String(e))
    } finally {
      setStreaming(false)
      setStreamingText('')
      setStreamingReasoning('')
      setStreamingToolCalls([])
    }
  }, [resolvedSessionId, draftConcierge, localLeafId, loadHistory, onSessionCreated])

  const handleRegenerate = useCallback(async (messageId: number) => {
    if (resolvedSessionId == null) return

    setError(null)
    setStreaming(true)
    setStreamingText('')
    setStreamingReasoning('')
    setStreamingToolCalls([])
    setRegeneratingMessageId(messageId)
    try {
      const gen = streamRegenerate(resolvedSessionId)
      for await (const ev of gen) {
        if (ev.type === 'delta') {
          setStreamingText(t => t + ev.data)
        } else if (ev.type === 'reasoning') {
          setStreamingReasoning(t => t + ev.data)
        } else if (ev.type === 'tool_call' || ev.type === 'tool_result') {
          setStreamingToolCalls(current => mergeToolCall(current, ev.data))
        } else if (ev.type === 'done') {
          await loadHistory(resolvedSessionId)
          if (ev.data.message) setLocalLeafId(ev.data.message.ID)
        } else if (ev.type === 'error') {
          setError(ev.data)
        }
      }
    } catch (e) {
      setError(String(e))
    } finally {
      setStreaming(false)
      setStreamingText('')
      setStreamingReasoning('')
      setStreamingToolCalls([])
      setRegeneratingMessageId(null)
    }
  }, [resolvedSessionId, loadHistory])

  const handleBranchSwitch = useCallback((newLeafId: number) => {
    setLocalLeafId(newLeafId)
  }, [])

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

  const lastAssistantIdx = displayMessages.map(item => item.message.Role).lastIndexOf('assistant')

  const isNewSession = resolvedSessionId == null && path.length === 0 && !streaming

  return (
    <div className={'chat-surface' + (isNewSession ? ' new-session' : '')}>
      <header className="chat-header">
        <div>
          <p className="chat-header-eyebrow">会话详情</p>
          <h2 className="chat-header-title">
            {resolvedSessionId == null ? `新会话 · ${draftConcierge?.name ?? '未选择顾问'}` : '对话内容'}
          </h2>
        </div>
        <div className="chat-badge">
          <span className="chat-dot" />
          实时
        </div>
      </header>
      <div className="messages-pane">
        {isNewSession ? (
          <div className="empty-state-card">
            <h2>开始新的对话</h2>
            {draftConcierge && (
              <div className="concierge-details">
                <div className="concierge-detail">
                  <span>顾问</span>
                  <strong>{draftConcierge.name}</strong>
                </div>
                <div className="concierge-detail">
                  <span>身份</span>
                  <p>{draftConcierge.identity}</p>
                </div>
                <DetailList label="印象" values={draftConcierge.impressions} />
                <DetailList label="工具组" values={draftConcierge.tool_groups} />
                <DetailList label="插件" values={draftConcierge.plugins} />
              </div>
            )}
          </div>
        ) : (
          displayMessages.map((item, idx) => regeneratingMessageId === item.message.ID ? (
            <div className="message-row assistant" key={item.message.ID}>
              <GenerationProgress content={streamingText} reasoning={streamingReasoning} toolCalls={streamingToolCalls} />
            </div>
          ) : (
            <MessageBubble
              key={item.message.ID}
              msg={item.message}
              branchMessage={item.branchMessage}
              processMessages={item.processMessages}
              childrenMap={childrenMap}
              onBranchSwitch={handleBranchSwitch}
              onEditResend={(newText) => handleSend(newText, item.message.ParentMessageID ?? undefined)}
              onRegenerate={idx === lastAssistantIdx && !streaming ? () => handleRegenerate(item.message.ID) : undefined}
            />
          ))
        )}
        {streaming && regeneratingMessageId == null && (
          <div className="message-row assistant">
            <GenerationProgress content={streamingText} reasoning={streamingReasoning} toolCalls={streamingToolCalls} />
          </div>
        )}
        {commandResponse && (
          <div className="command-block">{commandResponse}</div>
        )}
        {error && (
          <div className="error-block">{error}</div>
        )}
        <div ref={bottomRef} />
      </div>
      <Composer
        onSend={(text) => handleSend(text)}
        disabled={streaming}
        onStop={handleStop}
      />
    </div>
  )
}

function mergeToolCall(current: StreamToolCall[], incoming: StreamToolCall): StreamToolCall[] {
  const index = current.findIndex(toolCall =>
    toolCall.call_index === incoming.call_index && (
      toolCall.index === incoming.index || Boolean(incoming.id && toolCall.id === incoming.id)
    ),
  )
  if (index === -1) return [...current, incoming]

  const existing = current[index]
  const updated = {
    ...existing,
    ...incoming,
    id: incoming.id || existing.id,
    name: incoming.name || existing.name,
    arguments: incoming.arguments
      ? `${existing.arguments ?? ''}${incoming.arguments}`
      : existing.arguments,
    result: incoming.result || existing.result,
  }
  return current.map((toolCall, currentIndex) => currentIndex === index ? updated : toolCall)
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
