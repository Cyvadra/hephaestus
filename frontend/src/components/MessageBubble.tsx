import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { ChatMessage, ToolCall } from '../api/types'
import { siblings, descendToLeaf } from '../lib/tree'

interface Props {
  msg: ChatMessage
  branchMessage?: ChatMessage
  processMessages?: ChatMessage[]
  childrenMap: Map<number | null, ChatMessage[]>
  onBranchSwitch: (leafId: number) => void
  onEditResend: (newText: string) => void
  onRegenerate?: () => void
}

export default function MessageBubble({ msg, branchMessage, processMessages, childrenMap, onBranchSwitch, onEditResend, onRegenerate }: Props) {
  const [editing, setEditing] = useState(false)
  const [editText, setEditText] = useState(msg.Content)
  const isUser = msg.Role === 'user'
  const isAssistant = msg.Role === 'assistant'

  const branchAnchor = branchMessage ?? msg
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
      onEditResend(editText.trim())
      setEditing(false)
    }
  }

  if (isUser) {
    return (
      <div className="message-row user">
        <div className="message-stack user">
          {editing ? (
            <div className="message-editor">
              <textarea
                value={editText}
                onChange={e => setEditText(e.target.value)}
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
                <div className="message-body">{msg.Content}</div>
              </div>
              <div className="message-actions">
                {sibs.length > 1 && (
                  <BranchSwitcher current={currentSibIdx} total={sibs.length} onPrev={handlePrevBranch} onNext={handleNextBranch} />
                )}
                <button onClick={() => { setEditText(msg.Content); setEditing(true) }} className="message-action-btn">编辑</button>
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

    return (
      <div className="message-row assistant">
        <div className="message-stack">
          {hasThinkingProcess && (
            <details className="reasoning-panel">
              <summary className="reasoning-summary">思考过程</summary>
              <StoredThinkingProcess messages={thinkingMessages} />
            </details>
          )}
          {msg.Content && (
            <div className="message-card assistant">
              <div className="message-body">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.Content}</ReactMarkdown>
              </div>
            </div>
          )}
          <div className="message-actions">
            {sibs.length > 1 && (
              <BranchSwitcher current={currentSibIdx} total={sibs.length} onPrev={handlePrevBranch} onNext={handleNextBranch} />
            )}
            {onRegenerate && (
              <button onClick={onRegenerate} className="message-action-btn">重新生成</button>
            )}
          </div>
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
