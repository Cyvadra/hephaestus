import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { StreamToolCall } from '../api/types'

interface Props {
  content: string
  reasoning: string
  toolCalls: StreamToolCall[]
}

export default function GenerationProgress({ content, reasoning, toolCalls }: Props) {
  return (
    <div className="message-stack generation-progress">
      <details className="reasoning-panel" open>
        <summary className="reasoning-summary">
          <span className="reasoning-spinner" aria-hidden="true" />
          思考中
        </summary>
        <div className="reasoning-content" aria-live="polite">
          {reasoning && <div className="reasoning-text">{reasoning}</div>}
          {toolCalls.length > 0 && (
            <div className="tool-activity-list">
              {toolCalls.map(toolCall => (
                <div className="tool-activity" key={`${toolCall.call_index}:${toolCall.index}:${toolCall.id ?? ''}`}>
                  <div className="tool-activity-header">
                    <span className="tool-status-dot" data-status={toolCall.status} />
                    <strong>{toolCall.name || '准备调用工具'}</strong>
                    <span>{toolCall.status === 'complete' ? '已完成' : '调用中'}</span>
                  </div>
                  {toolCall.arguments && <pre>{toolCall.arguments}</pre>}
                  {toolCall.result && <pre className="tool-result-content">{toolCall.result}</pre>}
                </div>
              ))}
            </div>
          )}
          {!reasoning && toolCalls.length === 0 && <span className="reasoning-pending">正在分析问题…</span>}
        </div>
      </details>
      {content && (
        <div className="message-card assistant streaming-bubble" aria-live="polite">
          <div className="message-body">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{content}</ReactMarkdown>
            <span className="streaming-cursor" aria-hidden="true">▍</span>
          </div>
        </div>
      )}
    </div>
  )
}