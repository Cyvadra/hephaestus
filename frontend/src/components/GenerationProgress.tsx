import Markdown from './Markdown'
import type { StreamToolCall } from '../api/types'

export type StreamActivity =
  | { type: 'reasoning'; sequence: number; content: string }
  | { type: 'tool'; sequence: number; toolCall: StreamToolCall }

interface Props {
  content: string
  activities: StreamActivity[]
}

export default function GenerationProgress({ content, activities }: Props) {
  return (
    <div className="message-stack generation-progress">
      <details className="reasoning-panel" open>
        <summary className="reasoning-summary">
          <span className="reasoning-spinner" aria-hidden="true" />
          思考中
        </summary>
        <div className="reasoning-content" aria-live="polite">
          {activities.map(activity => activity.type === 'reasoning' ? (
            <div className="reasoning-text" key={activity.sequence}>{activity.content}</div>
          ) : (
            <StreamToolActivity key={activity.sequence} toolCall={activity.toolCall} />
          ))}
          {activities.length === 0 && <span className="reasoning-pending">正在分析问题…</span>}
        </div>
      </details>
      {content && (
        <div className="message-card assistant streaming-bubble" aria-live="polite">
          <div className="message-body">
            <Markdown>{content}</Markdown>
            <span className="streaming-cursor" aria-hidden="true">▍</span>
          </div>
        </div>
      )}
    </div>
  )
}

function StreamToolActivity({ toolCall }: { toolCall: StreamToolCall }) {
  return (
    <div className="tool-activity-list">
      <div className="tool-activity">
        <div className="tool-activity-header">
          <span className="tool-status-dot" data-status={toolCall.status} />
          <strong>{toolCall.name || '准备调用工具'}</strong>
          <span>{toolCall.status === 'complete' ? '已完成' : '调用中'}</span>
        </div>
        {toolCall.arguments && <pre>{toolCall.arguments}</pre>}
        {toolCall.result && <pre className="tool-result-content">{toolCall.result}</pre>}
      </div>
    </div>
  )
}