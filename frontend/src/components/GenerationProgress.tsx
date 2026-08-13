import { useEffect, useEffectEvent, useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n'
import Markdown from './Markdown'
import type { InteractionRequest, StreamToolCall } from '../api/types'

export type StreamActivity =
  | { type: 'reasoning'; sequence: number; content: string }
  | { type: 'tool'; sequence: number; toolCall: StreamToolCall }
	| { type: 'permission'; sequence: number; request: InteractionRequest }

interface Props {
  content: string
  activities: StreamActivity[]
	onRespondToPermission?: (request: InteractionRequest, approved: boolean) => Promise<boolean>
}

export default function GenerationProgress({ content, activities, onRespondToPermission }: Props) {
  const { t } = useTranslation()
  return (
    <div className="message-stack generation-progress">
      <details className="reasoning-panel" open>
        <summary className="reasoning-summary">
          <span className="reasoning-spinner" aria-hidden="true" />
          {t('chat.reasoning.inProgress')}
        </summary>
        <div className="reasoning-content" aria-live="polite">
          {activities.map(activity => activity.type === 'reasoning' ? (
            <div className="reasoning-text" key={activity.sequence}>{activity.content}</div>
          ) : activity.type === 'tool' ? (
            <StreamToolActivity key={activity.sequence} toolCall={activity.toolCall} />
          ) : (
			<PermissionActivity key={activity.sequence} request={activity.request} onRespond={onRespondToPermission} />
          ))}
          {activities.length === 0 && <span className="reasoning-pending">{t('chat.reasoning.analyzing')}</span>}
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

function PermissionActivity({ request, onRespond }: { request: InteractionRequest; onRespond?: (request: InteractionRequest, approved: boolean) => Promise<boolean> }) {
  const { t } = useTranslation()
  const [secondsRemaining, setSecondsRemaining] = useState(20)
  const [responding, setResponding] = useState(false)

  useEffect(() => {
    const originalTitle = document.title
    const deadline = Date.now() + 20_000
    const countdownTimer = window.setInterval(() => {
      const nextSeconds = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
      setSecondsRemaining(nextSeconds)
    }, 250)
    const titleTimer = window.setInterval(() => {
      document.title = document.title === originalTitle ? i18n.t('chat.permission.authorize') : originalTitle
    }, 1000)
    return () => {
      window.clearInterval(countdownTimer)
      window.clearInterval(titleTimer)
      document.title = originalTitle
    }
  }, [])

  const respond = useEffectEvent(async (approved: boolean) => {
    if (responding || !onRespond) return
    setResponding(true)
    const accepted = await onRespond(request, approved)
    if (!accepted) setResponding(false)
  })

  const autoRespond = useEffectEvent(() => {
    void respond(true)
  })

  useEffect(() => {
    if (secondsRemaining === 0) autoRespond()
  }, [secondsRemaining])

  return <div className="tool-activity-list">
    <div className="tool-activity">
      <div className="tool-activity-header">
        <span className="tool-status-dot" data-status="calling" />
        <strong>{request.title}</strong>
        <span>{responding ? t('chat.permission.processing') : t('chat.permission.autoApprove', { count: secondsRemaining })}</span>
      </div>
      <pre>{request.details}</pre>
      <div className="message-editor-actions permission-response-actions">
        <button type="button" className="message-action-btn" disabled={responding} onClick={async () => { if (responding || !onRespond) return; setResponding(true); const accepted = await onRespond(request, false); if (!accepted) setResponding(false) }}>{t('common.cancel')}</button>
        <button type="button" className="composer-send-btn permission-approve-pulse" disabled={responding} onClick={async () => { if (responding || !onRespond) return; setResponding(true); const accepted = await onRespond(request, true); if (!accepted) setResponding(false) }}>{t('chat.permission.authorize')}</button>
      </div>
    </div>
  </div>
}

function StreamToolActivity({ toolCall }: { toolCall: StreamToolCall }) {
  const { t } = useTranslation()
  return (
    <div className="tool-activity-list">
      <div className="tool-activity">
        <div className="tool-activity-header">
          <span className="tool-status-dot" data-status={toolCall.status} />
          <strong>{toolCall.name || t('chat.permission.awaitingTool')}</strong>
          <span>{toolCall.status === 'complete' ? t('chat.permission.complete') : toolCall.status === 'error' ? t('chat.permission.failed') : t('chat.permission.calling')}</span>
        </div>
        {toolCall.arguments && <pre>{toolCall.arguments}</pre>}
        {toolCall.result && <pre className="tool-result-content">{toolCall.result}</pre>}
      </div>
    </div>
  )
}