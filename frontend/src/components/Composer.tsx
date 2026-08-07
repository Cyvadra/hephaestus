import { useState, useRef, type KeyboardEvent } from 'react'
import { ArrowUp } from 'lucide-react'

interface Props {
  onSend: (text: string) => void
  onStop: () => void
  disabled: boolean
}

export default function Composer({ onSend, onStop, disabled }: Props) {
  const [text, setText] = useState('')
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const isCommand = text.trimStart().startsWith('/')

  const submit = () => {
    const t = text.trim()
    if (!t || disabled) return
    onSend(t)
    setText('')
  }

  const handleKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit()
    }
  }

  return (
    <div className="composer-panel">
      <div className="composer-card">
        {isCommand && (
          <div className="composer-hint">斜杠命令：回复不会写入普通历史记录。</div>
        )}
        <div className="composer-input-row">
          <textarea
            ref={textareaRef}
            value={text}
            onChange={e => setText(e.target.value)}
            onKeyDown={handleKey}
            disabled={disabled && !text.startsWith('/')}
            placeholder={disabled ? '生成中…' : '请输入你的问题…'}
            rows={3}
            className="composer-textarea"
          />
          <div className="composer-action-row">
            {disabled ? (
              <button type="button" onClick={onStop} className="composer-stop-btn">
                停止
              </button>
            ) : (
              <button
                type="button"
                onClick={submit}
                disabled={!text.trim()}
                className="composer-send-btn composer-send-icon-btn"
                aria-label="发送"
                title="发送"
              >
                <ArrowUp aria-hidden="true" size={18} strokeWidth={2.5} />
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
