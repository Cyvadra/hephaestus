import { useState, useRef, type KeyboardEvent } from 'react'
import { ArrowUp, X } from 'lucide-react'

interface Props {
  onSend: (text: string, files: File[]) => void
  onStop: () => void
  disabled: boolean
}

export default function Composer({ onSend, onStop, disabled }: Props) {
  const [text, setText] = useState('')
  const [files, setFiles] = useState<File[]>([])
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const isCommand = text.trimStart().startsWith('/')

  const submit = () => {
    const t = text.trim()
    if (!t || disabled) return
    onSend(t, files)
    setText('')
    setFiles([])
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  const addFiles = (incoming: FileList | null) => {
    if (!incoming) return
    const next = [...files, ...Array.from(incoming)]
    if (next.length > 5 || next.some(file => file.size > 50 * 1024 * 1024) || next.reduce((total, file) => total + file.size, 0) > 250 * 1024 * 1024) return
    setFiles(next)
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
        {files.length > 0 && (
          <div className="composer-files" aria-live="polite">
            {files.map((file, index) => (
              <div className="composer-file" key={`${file.name}-${file.lastModified}-${index}`}>
                <span>{file.name} ({formatSize(file.size)})</span>
                <button type="button" onClick={() => setFiles(current => current.filter((_, currentIndex) => currentIndex !== index))} title="移除文件" aria-label={`移除 ${file.name}`}>
                  <X aria-hidden="true" size={14} />
                </button>
              </div>
            ))}
          </div>
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
              <>
                <input ref={fileInputRef} type="file" multiple hidden onChange={event => addFiles(event.target.files)} />
                <div className="composer-upload-tooltip">
                  <button type="button" onClick={() => fileInputRef.current?.click()} disabled={isCommand} className="composer-upload-btn" aria-label="上传文件（最多 5 个，单文件最大 50 MB，总计 250 MB）" aria-describedby="upload-file-limits">
                    <svg aria-hidden="true" width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
                      <path d="M5.5498 9.75V5H6.9502V9.75C6.9502 10.3299 7.4201 10.7998 8 10.7998C8.5799 10.7998 9.0498 10.3299 9.0498 9.75V4.5C9.0498 2.9536 7.7964 1.7002 6.25 1.7002C4.7036 1.7002 3.4502 2.9536 3.4502 4.5V9.75C3.4502 12.2629 5.4871 14.2998 8 14.2998C10.5129 14.2998 12.5498 12.2629 12.5498 9.75V4H13.9502V9.75C13.9502 13.0361 11.2861 15.7002 8 15.7002C4.71391 15.7002 2.0498 13.0361 2.0498 9.75V4.5C2.04981 2.1804 3.9304 0.299806 6.25 0.299805C8.5696 0.299805 10.4502 2.1804 10.4502 4.5V9.75C10.4502 11.1031 9.3531 12.2002 8 12.2002C6.6469 12.2002 5.5498 11.1031 5.5498 9.75Z" fill="currentColor" />
                    </svg>
                  </button>
                  <span id="upload-file-limits" role="tooltip">上传文件：最多 5 个，单文件最大 50 MB，总计 250 MB</span>
                </div>
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
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function formatSize(size: number) {
  return size >= 1024 * 1024 ? `${(size / (1024 * 1024)).toFixed(1)} MB` : `${(size / 1024).toFixed(1)} KB`
}
