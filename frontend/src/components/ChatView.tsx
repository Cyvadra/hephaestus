import { useEffect, useRef, useState, useCallback } from 'react'
import { createSession, getHistory, regenerate as apiRegenerate } from '../api/client'
import { streamMessage } from '../api/stream'
import type { ChatMessage } from '../api/types'
import { activePath, buildById, buildChildrenMap } from '../lib/tree'
import MessageBubble from './MessageBubble'
import Composer from './Composer'

interface Props {
  sessionId: number | null
  draftConcierge?: string | null
  onSessionCreated?: (id: number) => void
}

export default function ChatView({ sessionId, draftConcierge, onSessionCreated }: Props) {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [localLeafId, setLocalLeafId] = useState<number | null>(null)
  const [streaming, setStreaming] = useState(false)
  const [streamingText, setStreamingText] = useState('')
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
    setCommandResponse(null)
    setError(null)
    return () => controller.abort()
  }, [resolvedSessionId, loadHistory])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, streamingText])

  const byId = buildById(messages)
  const childrenMap = buildChildrenMap(messages)
  const path = activePath(localLeafId, byId)

  const handleSend = useCallback(async (text: string, leafOverride?: number) => {
    if (resolvedSessionId == null && text.trimStart().startsWith('/stop')) {
      return
    }

    const leafId = leafOverride !== undefined ? leafOverride : localLeafId
    setCommandResponse(null)
    setError(null)
    setStreaming(true)
    setStreamingText('')

    try {
      let targetSessionId = resolvedSessionId
      if (targetSessionId == null) {
        if (!draftConcierge) {
          throw new Error('请先选择顾问再开始新会话')
        }
        const created = await createSession(draftConcierge)
        targetSessionId = created.ID
        setResolvedSessionId(created.ID)
        onSessionCreated?.(created.ID)
      }

      const gen = streamMessage(targetSessionId, text, leafId ?? undefined)
      for await (const ev of gen) {
        if (ev.type === 'delta') {
          setStreamingText(t => t + ev.data)
        } else if (ev.type === 'done') {
          if (ev.data.command_response) {
            setCommandResponse(ev.data.command_response)
          }
          setStreamingText('')
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
    }
  }, [resolvedSessionId, draftConcierge, localLeafId, loadHistory, onSessionCreated])

  const handleRegenerate = useCallback(async () => {
    if (resolvedSessionId == null) return

    setError(null)
    setStreaming(true)
    try {
      const result = await apiRegenerate(resolvedSessionId)
      await loadHistory(resolvedSessionId)
      if (result.message) setLocalLeafId(result.message.ID)
    } catch (e) {
      setError(String(e))
    } finally {
      setStreaming(false)
    }
  }, [resolvedSessionId, loadHistory])

  const handleBranchSwitch = useCallback((newLeafId: number) => {
    setLocalLeafId(newLeafId)
  }, [])

  const lastAssistantIdx = path.map(m => m.Role).lastIndexOf('assistant')

  const isNewSession = resolvedSessionId == null && path.length === 0 && !streaming

  return (
    <div className={'chat-surface' + (isNewSession ? ' new-session' : '')}>
      <header className="chat-header">
        <div>
          <p className="chat-header-eyebrow">会话详情</p>
          <h2 className="chat-header-title">
            {resolvedSessionId == null ? `新会话 · ${draftConcierge ?? '未选择顾问'}` : '对话内容'}
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
          </div>
        ) : (
          path.map((msg, idx) => (
            <MessageBubble
              key={msg.ID}
              msg={msg}
              childrenMap={childrenMap}
              onBranchSwitch={handleBranchSwitch}
              onEditResend={(newText) => handleSend(newText, msg.ParentMessageID ?? undefined)}
              onRegenerate={idx === lastAssistantIdx && !streaming ? handleRegenerate : undefined}
            />
          ))
        )}
        {streaming && streamingText && (
          <div className="message-row assistant">
            <div className="streaming-bubble">
              {streamingText}
              <span>▍</span>
            </div>
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
        onStop={() => handleSend('/stop')}
      />
    </div>
  )
}
