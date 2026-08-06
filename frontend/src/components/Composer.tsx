import { useState, useRef, type KeyboardEvent } from 'react'

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
          {disabled ? (
            <button onClick={onStop} className="composer-stop-btn">
              停止
            </button>
          ) : (
            <button onClick={submit} disabled={!text.trim()} className="composer-send-btn">
              发送
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
