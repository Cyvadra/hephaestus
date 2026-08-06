import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { ChatMessage } from '../api/types'
import { siblings, descendToLeaf } from '../lib/tree'

interface Props {
  msg: ChatMessage
  childrenMap: Map<number | null, ChatMessage[]>
  onBranchSwitch: (leafId: number) => void
  onEditResend: (newText: string) => void
  onRegenerate?: () => void
}

export default function MessageBubble({ msg, childrenMap, onBranchSwitch, onEditResend, onRegenerate }: Props) {
  const [editing, setEditing] = useState(false)
  const [editText, setEditText] = useState(msg.Content)
  const [showReasoning, setShowReasoning] = useState(false)
  const isUser = msg.Role === 'user'
  const isAssistant = msg.Role === 'assistant'

  const sibs = siblings(msg, childrenMap)
  const currentSibIdx = sibs.findIndex(s => s.ID === msg.ID)

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
                className="composer-textarea"
                rows={3}
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
    return (
      <div className="message-row assistant">
        <div className="message-stack">
          {msg.ReasoningContent && (
            <button onClick={() => setShowReasoning(v => !v)} className="message-action-btn">
              {showReasoning ? '▾' : '▸'} Reasoning
            </button>
          )}
          {showReasoning && msg.ReasoningContent && (
            <div className="reasoning-block">{msg.ReasoningContent}</div>
          )}
          <div className="message-card assistant">
            <div className="message-body">
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.Content}</ReactMarkdown>
            </div>
          </div>
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

  return (
    <div className="message-row assistant">
      <div className="message-card system">[{msg.Role}] {msg.Content.slice(0, 200)}</div>
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
