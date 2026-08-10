import Markdown from './Markdown'
import type { InteractionRequest, StreamToolCall } from '../api/types'

export type StreamActivity =
  | { type: 'reasoning'; sequence: number; content: string }
  | { type: 'tool'; sequence: number; toolCall: StreamToolCall }
	| { type: 'permission'; sequence: number; request: InteractionRequest }

interface Props {
  content: string
  activities: StreamActivity[]
	onRespondToPermission?: (request: InteractionRequest, approved: boolean) => void
}

export default function GenerationProgress({ content, activities, onRespondToPermission }: Props) {
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
          ) : activity.type === 'tool' ? (
            <StreamToolActivity key={activity.sequence} toolCall={activity.toolCall} />
          ) : (
			<PermissionActivity key={activity.sequence} request={activity.request} onRespond={onRespondToPermission} />
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

function PermissionActivity({ request, onRespond }: { request: InteractionRequest; onRespond?: (request: InteractionRequest, approved: boolean) => void }) {
  return <div className="permission-activity">
    <strong>{request.title}</strong>
    <pre>{request.details}</pre>
    <div className="permission-actions">
      <button type="button" onClick={() => onRespond?.(request, true)}>确认</button>
      <button type="button" className="permission-deny" onClick={() => onRespond?.(request, false)}>取消</button>
    </div>
  </div>
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