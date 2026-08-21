import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import { Check, Copy, Download, FileText, GitFork, Pencil, RefreshCw, StepForward } from 'lucide-react'
import { useTranslation } from 'react-i18next'
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
  onEditAssistant: (content: string) => Promise<void>
  editSaving?: boolean
  editDisabled?: boolean
  forkDisabled?: boolean
  readOnly?: boolean
  onFork?: () => void
  onRegenerate?: () => void
  onContinue?: () => void
}

export default function MessageBubble({ msg, branchMessage, processMessages, childrenMap, onBranchSwitch, onEditResend, onEditAssistant, editSaving = false, editDisabled = false, forkDisabled = false, readOnly = false, onFork, onRegenerate, onContinue }: Props) {
  const { t, i18n } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [userEditWidth, setUserEditWidth] = useState<number | null>(null)
  const [editText, setEditText] = useState(msg.Content)
  const [copied, setCopied] = useState(false)
  const [reasoningPinned, setReasoningPinned] = useState(false)
  const [reasoningHovered, setReasoningHovered] = useState(false)
  const [systemPinned, setSystemPinned] = useState(false)
  const [systemHovered, setSystemHovered] = useState(false)
  const isUser = msg.Role === 'user'
  const isAssistant = msg.Role === 'assistant'
	const attachments = msg.Attachments ?? []
  const messageTimestamp = formatMessageTimestamp(msg.Timestamp, i18n.language)
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
      await onEditAssistant(editText.trim())
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
      <div className="message-row user" data-user-message-id={msg.ID}>
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
                <button onClick={() => setEditing(false)} className="message-action-btn">{t('common.cancel')}</button>
                <button onClick={handleEditSubmit} className="composer-send-btn">{t('chat.message.resend')}</button>
              </div>
            </div>
          ) : (
            <>
              <div className="message-card user">
                {parsedUserContent?.attachments.map(attachment => (
                  <div className="message-attachment" key={attachment.path}>
                    <FileText aria-hidden="true" size={15} />
                    <span>{attachment.path}</span>
                    <small>{attachment.size}{attachment.contentIncluded ? t('chat.files.contentExtracted') : ''}</small>
                  </div>
                ))}
                <div className="message-body">{parsedUserContent?.body ?? msg.Content}</div>
              </div>
              <div className="message-actions user-message-actions">
                {messageTimestamp && <time className="message-timestamp" dateTime={msg.Timestamp}>{messageTimestamp}</time>}
                {sibs.length > 1 && (
                  <BranchSwitcher current={currentSibIdx} total={sibs.length} onPrev={handlePrevBranch} onNext={handleNextBranch} />
                )}
                <IconButton label={copied ? t('chat.message.copied') : t('chat.message.copy')} onClick={handleCopy}>
                  {copied ? <Check /> : <Copy />}
                </IconButton>
                {!readOnly && <button
                  onClick={startUserEdit}
                  className="message-action-btn message-icon-btn"
                  aria-label={t('chat.message.edit')}
                  title={t('chat.message.edit')}
                >
                  <Pencil />
                </button>}
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
                <span>{t('chat.message.body')}</span>
                <textarea
                  value={editText}
                  onChange={event => setEditText(event.target.value)}
                  onKeyDown={handleAssistantEditKeyDown}
                  className="message-editor-textarea"
                  rows={5}
                  autoFocus
                />
              </label>
              <div className="message-editor-actions">
                <button onClick={() => setEditing(false)} className="message-action-btn" disabled={editSaving}>{t('common.cancel')}</button>
                <button onClick={() => void handleAssistantEditSubmit()} className="composer-send-btn" disabled={!editText.trim() || editDisabled}>
                  {editSaving ? t('chat.message.saving') : t('common.save')}
                </button>
              </div>
            </div>
          ) : (
            <>
              {hasThinkingProcess && (
                <CollapsibleContextPanel
                  title={t('chat.reasoning.process')}
                  pinned={reasoningPinned}
                  hovered={reasoningHovered}
                  onPinnedChange={setReasoningPinned}
                  onHoveredChange={setReasoningHovered}
                >
                  <StoredThinkingProcess messages={thinkingMessages} />
                </CollapsibleContextPanel>
              )}
              {(msg.Content || attachments.length > 0) && (
                <div className="message-card assistant">
                  {msg.Status === 'incomplete' && <span className="message-status incomplete">{t('chat.message.incomplete')}</span>}
          {msg.Content && (
            <div className="message-body">
              <Markdown>{msg.Content}</Markdown>
            </div>
          )}
          {attachments.length > 0 && (
            <div className="assistant-attachments" aria-label={t('chat.files.sent')}>
              {attachments.map(attachment => <AssistantAttachment key={attachment.ID} attachment={attachment} />)}
            </div>
          )}
                </div>
              )}
              <div className="message-actions assistant-message-actions">
                {sibs.length > 1 && (
                  <BranchSwitcher current={currentSibIdx} total={sibs.length} onPrev={handlePrevBranch} onNext={handleNextBranch} />
                )}
                <IconButton label={copied ? t('chat.message.copied') : t('chat.message.copy')} onClick={handleCopy}>
                  {copied ? <Check /> : <Copy />}
                </IconButton>
                {canEdit && !readOnly && (
                  <button
                    onClick={() => { setEditText(msg.Content); setEditing(true) }}
                    className="message-action-btn message-icon-btn"
                    disabled={editDisabled}
                    aria-label={t('chat.message.edit')}
                    title={t('chat.message.edit')}
                  >
                    <Pencil />
                  </button>
                )}
                {onRegenerate && (
                  <button
                    onClick={onRegenerate}
                    className="message-action-btn message-icon-btn"
                    aria-label={t('chat.message.regenerate')}
                    title={t('chat.message.regenerate')}
                  >
                    <RefreshCw />
                  </button>
                )}
                {onContinue && (
                  <button
                    onClick={onContinue}
                    className="message-action-btn message-icon-btn"
                    aria-label={t('chat.message.continue')}
                    title={t('chat.message.continue')}
                  >
                    <StepForward />
                  </button>
                )}
                {onFork && (
                  <button
                    onClick={onFork}
                    className="message-action-btn message-icon-btn message-fork-btn"
                    disabled={forkDisabled}
                    aria-label={t('chat.message.fork')}
                    title={t('chat.message.fork')}
                  >
                    <GitFork />
                  </button>
                )}
                {messageTimestamp && <time className="message-timestamp" dateTime={msg.Timestamp}>{messageTimestamp}</time>}
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
          <summary>{t('chat.message.toolResult')}</summary>
          <pre>{msg.Content}</pre>
        </details>
      </div>
    )
  }

  if (msg.Role === 'system') {
    return (
      <div className="message-row assistant">
        <div className="message-stack">
          <CollapsibleContextPanel
            title={t('chat.message.systemMessage')}
            pinned={systemPinned}
            hovered={systemHovered}
            onPinnedChange={setSystemPinned}
            onHoveredChange={setSystemHovered}
          >
            <div className="reasoning-content system-message-content">
              <div className="reasoning-text">{msg.Content}</div>
            </div>
          </CollapsibleContextPanel>
        </div>
      </div>
    )
  }

  return (
    <div className="message-row assistant">
      <div className="message-card system">[{msg.Role}] {msg.Content}</div>
    </div>
  )
}

function formatMessageTimestamp(timestamp: string, locale: string) {
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) return ''

  const isToday = date.toDateString() === new Date().toDateString()
  return new Intl.DateTimeFormat(locale, isToday
    ? { hour: '2-digit', minute: '2-digit', hour12: false }
    : { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false },
  ).format(date)
}

function formatAttachmentSize(size: number) {
  return size >= 1024 * 1024 ? `${(size / (1024 * 1024)).toFixed(1)} MB` : `${(size / 1024).toFixed(1)} KB`
}

function AssistantAttachment({ attachment }: { attachment: ChatMessage['Attachments'][number] }) {
  const { t } = useTranslation()
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
      title={t('chat.files.download', { name: attachment.Name })}
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

function CollapsibleContextPanel({ title, pinned, hovered, onPinnedChange, onHoveredChange, children }: {
  title: string
  pinned: boolean
  hovered: boolean
  onPinnedChange: React.Dispatch<React.SetStateAction<boolean>>
  onHoveredChange: React.Dispatch<React.SetStateAction<boolean>>
  children: React.ReactNode
}) {
  return (
    <div className="reasoning-panel">
      <details className="reasoning-details" open={pinned}>
        <summary
          className="reasoning-summary"
          onMouseEnter={() => onHoveredChange(true)}
          onMouseLeave={event => {
            const related = event.relatedTarget
            if (related instanceof Element && related.closest('.reasoning-preview')) return
            onHoveredChange(false)
          }}
          onClick={event => {
            event.preventDefault()
            onPinnedChange(current => !current)
          }}
        >
          {title}
        </summary>
        {pinned && children}
      </details>
      {!pinned && hovered && (
        <ContextPreview
          onClick={() => onPinnedChange(true)}
          onMouseEnter={() => onHoveredChange(true)}
          onMouseLeave={() => onHoveredChange(false)}
        >
          {children}
        </ContextPreview>
      )}
    </div>
  )
}

function ContextPreview({ children, onClick, onMouseEnter, onMouseLeave }: {
  children: React.ReactNode
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
      {children}
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
  const { t } = useTranslation()
  const name = toolCall.function?.name || t('chat.message.toolCall')
  const args = toolCall.function?.arguments
  const resultPreview = result && result.length > 12000
    ? `${result.slice(0, 12000)}\n\n${t('chat.message.truncatedResult')}`
    : result

  return (
    <div className="tool-activity">
      <div className="tool-activity-header">
        <span className="tool-status-dot" data-status="complete" />
        <strong>{name}</strong>
        <span>{t('chat.message.called')}</span>
      </div>
      {args && <pre>{args}</pre>}
      {resultPreview && (
        <details className="tool-output">
          <summary>{t('chat.message.viewResult')}</summary>
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
