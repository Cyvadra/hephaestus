import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import { Check, Copy, Download, FileText, GitFork, Pencil, RefreshCw, StepForward } from 'lucide-react'
import Markdown from './Markdown'
import type { ChatMessage, ToolCall } from '../api/types'
import { attachmentDownloadURL } from '../api/client'
import { siblings, descendToLeaf } from '../lib/tree'
import { parseAttachmentPrefix } from '../lib/attachments'

interface Props {
  msg: ChatMessage
  branchMessage?: ChatMessage
  processMessages?: ChatMessage[]
  childrenMap: Map<number | null, ChatMessage[]>
  onBranchSwitch: (leafId: number) => void
  onEditResend: (newText: string) => void
  onEditAssistant: (content: string, reasoningContent: string) => Promise<void>
  editSaving?: boolean
  editDisabled?: boolean
  forkDisabled?: boolean
  onFork?: () => void
  onRegenerate?: () => void
  onContinue?: () => void
}

export default function MessageBubble({ msg, branchMessage, processMessages, childrenMap, onBranchSwitch, onEditResend, onEditAssistant, editSaving = false, editDisabled = false, forkDisabled = false, onFork, onRegenerate, onContinue }: Props) {
  const [editing, setEditing] = useState(false)
  const [userEditWidth, setUserEditWidth] = useState<number | null>(null)
  const [editText, setEditText] = useState(msg.Content)
  const [editReasoning, setEditReasoning] = useState(msg.ReasoningContent)
  const [copied, setCopied] = useState(false)
  const [reasoningPinned, setReasoningPinned] = useState(false)
  const [reasoningHovered, setReasoningHovered] = useState(false)
  const isUser = msg.Role === 'user'
  const isAssistant = msg.Role === 'assistant'
	const attachments = msg.Attachments ?? []
  const parsedUserContent = isUser ? parseAttachmentPrefix(msg.Content) : null
  const attachmentPrefix = parsedUserContent ? msg.Content.slice(0, msg.Content.length - parsedUserContent.body.length) : ''

  const messageSiblings = siblings(msg, childrenMap)
  const branchAnchor = messageSiblings.length > 1 ? msg : (branchMessage ?? msg)
  const sibs = siblings(branchAnchor, childrenMap)
  const currentSibIdx = sibs.findIndex(s => s.ID === branchAnchor.ID)

  const handlePrevBranch = () => {
    if (currentSibIdx <= 0) return
    const sib = sibs[currentSibIdx - 1]
    onBranchSwitch(descendToLeaf(sib.ID, childrenMap))
  }

  const handleNextBranch = () => {
    if (currentSibIdx >= sibs.length - 1) return
    const sib = sibs[currentSibIdx + 1]
    onBranchSwitch(descendToLeaf(sib.ID, childrenMap))
  }

  const handleEditSubmit = () => {
    if (editText.trim()) {
      onEditResend(attachmentPrefix + editText.trim())
      setEditing(false)
    }
  }

  const handleAssistantEditSubmit = async () => {
    if (!editText.trim() || editDisabled) return
    try {
      await onEditAssistant(editText.trim(), editReasoning)
      setEditing(false)
    } catch {
      // ChatView surfaces the request error without discarding the draft.
    }
  }

  const handleUserEditKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
      event.preventDefault()
      handleEditSubmit()
    }
  }

  const handleAssistantEditKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
      event.preventDefault()
      void handleAssistantEditSubmit()
    }
  }

  const handleCopy = async () => {
    await navigator.clipboard.writeText(msg.Content)
    setCopied(true)
    window.setTimeout(() => setCopied(false), 1000)
  }

  const startUserEdit = (event: React.MouseEvent<HTMLButtonElement>) => {
    const card = event.currentTarget.closest('.message-stack')?.querySelector('.message-card')
    setUserEditWidth(card instanceof HTMLElement ? card.getBoundingClientRect().width + 80 : null)
    setEditText(parsedUserContent?.body ?? msg.Content)
    setEditing(true)
  }

  if (isUser) {
    return (
      <div className="message-row user">
        <div className={'message-stack user' + (editing ? ' editing' : '')} style={editing && userEditWidth != null ? { width: `min(100%, ${userEditWidth}px)` } : undefined}>
          {editing ? (
            <div className="message-editor">
              <textarea
                value={editText}
                onChange={e => setEditText(e.target.value)}
                onKeyDown={handleUserEditKeyDown}
                className="message-editor-textarea"
                rows={3}
                autoFocus
              />
              <div className="message-editor-actions">
                <button onClick={() => setEditing(false)} className="message-action-btn">取消</button>
                <button onClick={handleEditSubmit} className="composer-send-btn">重发</button>
              </div>
            </div>
          ) : (
            <>
              <div className="message-card user">
                {parsedUserContent?.attachments.map(attachment => (
                  <div className="message-attachment" key={attachment.path}>
                    <FileText aria-hidden="true" size={15} />
                    <span>{attachment.path}</span>
                    <small>{attachment.size}{attachment.contentIncluded ? ' · 已提取内容' : ''}</small>
                  </div>
                ))}
                <div className="message-body">{parsedUserContent?.body ?? msg.Content}</div>
              </div>
              <div className="message-actions user-message-actions">
                {sibs.length > 1 && (
                  <BranchSwitcher current={currentSibIdx} total={sibs.length} onPrev={handlePrevBranch} onNext={handleNextBranch} />
                )}
                <IconButton label={copied ? '已复制' : '复制'} onClick={handleCopy}>
                  {copied ? <Check /> : <Copy />}
                </IconButton>
                <button
                  onClick={startUserEdit}
                  className="message-action-btn message-icon-btn"
                  aria-label="编辑"
                  title="编辑"
                >
                  <Pencil />
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    )
  }

  if (isAssistant) {
    const thinkingMessages = processMessages ?? [msg]
    const hasThinkingProcess = thinkingMessages.some(message =>
      Boolean(message.ReasoningContent) || (Array.isArray(message.ToolCalls) && message.ToolCalls.length > 0),
    )
    const canEdit = !Array.isArray(msg.ToolCalls) || msg.ToolCalls.length === 0

    return (
      <div className="message-row assistant">
        <div className="message-stack">
          {editing ? (
            <div className="message-editor assistant-message-editor">
              <label className="message-editor-field">
                <span>正文</span>
                <textarea
                  value={editText}
                  onChange={event => setEditText(event.target.value)}
                  onKeyDown={handleAssistantEditKeyDown}
                  className="message-editor-textarea"
                  rows={5}
                  autoFocus
                />
              </label>
              <label className="message-editor-field">
                <span>思考过程</span>
                <textarea
                  value={editReasoning}
                  onChange={event => setEditReasoning(event.target.value)}
                  onKeyDown={handleAssistantEditKeyDown}
                  className="message-editor-textarea reasoning-editor-textarea"
                  rows={4}
                />
              </label>
              <div className="message-editor-actions">
                <button onClick={() => setEditing(false)} className="message-action-btn" disabled={editSaving}>取消</button>
                <button onClick={() => void handleAssistantEditSubmit()} className="composer-send-btn" disabled={!editText.trim() || editDisabled}>
                  {editSaving ? '保存中' : '保存'}
                </button>
              </div>
            </div>
          ) : (
            <>
              {hasThinkingProcess && (
                <div className="reasoning-panel">
                  <details className="reasoning-details" open={reasoningPinned}>
                    <summary
                      className="reasoning-summary"
                      onMouseEnter={() => setReasoningHovered(true)}
                      onMouseLeave={event => {
                        const related = event.relatedTarget
                        if (related instanceof Element && related.closest('.reasoning-preview')) return
                        setReasoningHovered(false)
                      }}
                      onClick={event => {
                        event.preventDefault()
                        setReasoningPinned(pinned => !pinned)
                      }}
                    >
                      思考过程
                    </summary>
                    {reasoningPinned && <StoredThinkingProcess messages={thinkingMessages} />}
                  </details>
                  {!reasoningPinned && reasoningHovered && (
                    <ReasoningPreview
                      messages={thinkingMessages}
                      onClick={() => setReasoningPinned(true)}
                      onMouseEnter={() => setReasoningHovered(true)}
                      onMouseLeave={() => setReasoningHovered(false)}
                    />
                  )}
                </div>
              )}
              {(msg.Content || attachments.length > 0) && (
                <div className="message-card assistant">
                  {msg.Status === 'incomplete' && <span className="message-status incomplete">异常终止</span>}
          {msg.Content && (
            <div className="message-body">
              <Markdown>{msg.Content}</Markdown>
            </div>
          )}
          {attachments.length > 0 && (
            <div className="assistant-attachments" aria-label="已发送文件">
              {attachments.map(attachment => <AssistantAttachment key={attachment.ID} attachment={attachment} />)}
            </div>
          )}
                </div>
              )}
              <div className="message-actions assistant-message-actions">
                {sibs.length > 1 && (
                  <BranchSwitcher current={currentSibIdx} total={sibs.length} onPrev={handlePrevBranch} onNext={handleNextBranch} />
                )}
                <IconButton label={copied ? '已复制' : '复制'} onClick={handleCopy}>
                  {copied ? <Check /> : <Copy />}
                </IconButton>
                {canEdit && (
                  <button
                    onClick={() => { setEditText(msg.Content); setEditReasoning(msg.ReasoningContent); setEditing(true) }}
                    className="message-action-btn message-icon-btn"
                    disabled={editDisabled}
                    aria-label="编辑"
                    title="编辑"
                  >
                    <Pencil />
                  </button>
                )}
                {onRegenerate && (
                  <button
                    onClick={onRegenerate}
                    className="message-action-btn message-icon-btn"
                    aria-label="重新生成"
                    title="重新生成"
                  >
                    <RefreshCw />
                  </button>
                )}
                {onContinue && (
                  <button
                    onClick={onContinue}
                    className="message-action-btn message-icon-btn"
                    aria-label="继续生成"
                    title="继续生成"
                  >
                    <StepForward />
                  </button>
                )}
                {onFork && (
                  <button
                    onClick={onFork}
                    className="message-action-btn message-icon-btn message-fork-btn"
                    disabled={forkDisabled}
                    aria-label="创建会话分支"
                    title="创建会话分支"
                  >
                    <GitFork />
                  </button>
                )}
              </div>
            </>
          )}
        </div>
      </div>
    )
  }

  if (msg.Role === 'tool') {
    return (
      <div className="message-row assistant">
        <details className="stored-tool-result">
          <summary>工具执行结果</summary>
          <pre>{msg.Content}</pre>
        </details>
      </div>
    )
  }

  return (
    <div className="message-row assistant">
      <div className="message-card system">[{msg.Role}] {msg.Content.slice(0, 200)}</div>
    </div>
  )
}

function formatAttachmentSize(size: number) {
  return size >= 1024 * 1024 ? `${(size / (1024 * 1024)).toFixed(1)} MB` : `${(size / 1024).toFixed(1)} KB`
}

function AssistantAttachment({ attachment }: { attachment: ChatMessage['Attachments'][number] }) {
  const nameRef = useRef<HTMLSpanElement>(null)
  const [scroll, setScroll] = useState({ distance: 0, duration: 0 })

  useEffect(() => {
    const name = nameRef.current
    if (!name) return
    const updateScroll = () => {
      const distance = Math.max(0, name.scrollWidth - name.clientWidth)
      const duration = distance > 0 ? Math.max(.5, distance / 100) : 0
      setScroll(current => current.distance === distance && current.duration === duration ? current : { distance, duration })
    }
    const observer = new ResizeObserver(updateScroll)
    observer.observe(name)
    updateScroll()
    return () => observer.disconnect()
  }, [attachment.Name])

  const style = {
    '--attachment-name-scroll-distance': `${scroll.distance}px`,
    '--attachment-name-scroll-duration': `${scroll.duration}s`,
  } as CSSProperties

  return (
    <a
      className={'assistant-attachment' + (scroll.distance > 0 ? ' overflowing' : '')}
      download={attachment.Name}
      href={attachmentDownloadURL(attachment.SessionID, attachment.ID)}
      title={`下载 ${attachment.Name}`}
    >
      <FileText aria-hidden="true" size={16} />
      <span ref={nameRef} style={style}><span>{attachment.Name}</span></span>
      <small>{formatAttachmentSize(attachment.Size)}</small>
      <Download aria-hidden="true" size={15} />
    </a>
  )
}

function IconButton({ label, onClick, children }: { label: string; onClick: () => void | Promise<void>; children: React.ReactNode }) {
  return (
    <button
      onClick={() => void onClick()}
      className="message-action-btn message-icon-btn"
      aria-label={label}
      title={label}
    >
      {children}
    </button>
  )
}

function ReasoningPreview({ messages, onClick, onMouseEnter, onMouseLeave }: {
  messages: ChatMessage[]
  onClick: () => void
  onMouseEnter: () => void
  onMouseLeave: () => void
}) {
  const previewRef = useRef<HTMLDivElement>(null)
  const [isOverflowing, setIsOverflowing] = useState(false)

  useLayoutEffect(() => {
    const preview = previewRef.current
    if (!preview) return

    const updateOverflow = () => setIsOverflowing(preview.scrollHeight > preview.clientHeight)
    updateOverflow()
    const observer = new ResizeObserver(updateOverflow)
    observer.observe(preview)
    return () => observer.disconnect()
  }, [])

  return (
    <div
      ref={previewRef}
      className={`reasoning-preview${isOverflowing ? ' is-overflowing' : ''}`}
      onClick={onClick}
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
    >
      <StoredThinkingProcess messages={messages} />
    </div>
  )
}

function StoredThinkingProcess({ messages }: { messages: ChatMessage[] }) {
  const toolResults = new Map(
    messages
      .filter(message => message.Role === 'tool' && message.ToolCallID)
      .map(message => [message.ToolCallID, message.Content]),
  )

  return (
    <div className="reasoning-content">
      {messages.map(message => {
        if (message.Role !== 'assistant') return null
        const toolCalls = Array.isArray(message.ToolCalls) ? message.ToolCalls : []
        return (
          <div className="reasoning-step" key={message.ID}>
            {message.ReasoningContent && <div className="reasoning-text">{message.ReasoningContent}</div>}
            {toolCalls.length > 0 && (
              <div className="tool-activity-list">
                {toolCalls.map((toolCall, index) => (
                  <StoredToolCall
                    key={toolCall.id ?? index}
                    toolCall={toolCall}
                    result={toolCall.id ? toolResults.get(toolCall.id) : undefined}
                  />
                ))}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

function StoredToolCall({ toolCall, result }: { toolCall: ToolCall; result?: string }) {
  const name = toolCall.function?.name || '工具调用'
  const args = toolCall.function?.arguments
  const resultPreview = result && result.length > 12000
    ? `${result.slice(0, 12000)}\n\n[结果过长，已截断]`
    : result

  return (
    <div className="tool-activity">
      <div className="tool-activity-header">
        <span className="tool-status-dot" data-status="complete" />
        <strong>{name}</strong>
        <span>已调用</span>
      </div>
      {args && <pre>{args}</pre>}
      {resultPreview && (
        <details className="tool-output">
          <summary>查看执行结果</summary>
          <pre className="tool-result-content">{resultPreview}</pre>
        </details>
      )}
    </div>
  )
}

function BranchSwitcher({ current, total, onPrev, onNext }: { current: number; total: number; onPrev: () => void; onNext: () => void }) {
  return (
    <span className="message-action-btn branch-switcher">
      <button onClick={onPrev} disabled={current === 0}>‹</button>
      <span>{current + 1}/{total}</span>
      <button onClick={onNext} disabled={current === total - 1}>›</button>
    </span>
  )
}
