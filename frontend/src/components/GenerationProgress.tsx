import { useEffect, useEffectEvent, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n'
import Markdown from './Markdown'
import type { InteractionRequest, StreamToolCall } from '../api/types'
import type { AuthorizationMode } from './Composer'

export type StreamActivity =
  | { type: 'reasoning'; sequence: number; content: string }
  | { type: 'tool'; sequence: number; toolCall: StreamToolCall }
	| { type: 'permission'; sequence: number; request: InteractionRequest }

interface Props {
  content: string
  activities: StreamActivity[]
	onRespondToPermission?: (request: InteractionRequest, approved: boolean) => Promise<boolean>
  authorizationMode?: AuthorizationMode
  onAutoApprovePermission?: (request: InteractionRequest) => Promise<boolean>
}

export default function GenerationProgress({ content, activities, onRespondToPermission, authorizationMode = 'askEachTime', onAutoApprovePermission }: Props) {
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
      <PermissionActivity key={activity.sequence} request={activity.request} onRespond={onRespondToPermission} authorizationMode={authorizationMode} onAutoApprove={onAutoApprovePermission} />
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

function PermissionActivity({ request, onRespond, authorizationMode, onAutoApprove }: { request: InteractionRequest; onRespond?: (request: InteractionRequest, approved: boolean) => Promise<boolean>; authorizationMode: AuthorizationMode; onAutoApprove?: (request: InteractionRequest) => Promise<boolean> }) {
  const { t } = useTranslation()
  const [secondsRemaining, setSecondsRemaining] = useState(20)
  const [responding, setResponding] = useState(false)
	const autoApproveRequestedRef = useRef(false)

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

  const autoRespond = useEffectEvent((approved: boolean) => {
	void respond(approved)
  })

  useEffect(() => {
    if (authorizationMode === 'allowAll') {
    if (!autoApproveRequestedRef.current && onAutoApprove) {
      autoApproveRequestedRef.current = true
      void onAutoApprove(request).then(accepted => {
        if (!accepted) autoApproveRequestedRef.current = false
      })
    }
      return
    }
    if (authorizationMode === 'timeoutDeny' && secondsRemaining === 5) autoRespond(false)
    if (authorizationMode === 'askEachTime' && secondsRemaining === 0) autoRespond(true)
  }, [authorizationMode, onAutoApprove, request, secondsRemaining])

  return <div className="tool-activity-list">
    <div className="tool-activity">
      <div className="tool-activity-header">
        <span className="tool-status-dot" data-status="calling" />
        <strong>{request.title}</strong>
        <span>{responding ? t('chat.permission.processing') : t(`chat.permission.${authorizationMode}`, { count: secondsRemaining })}</span>
      </div>
      <pre>{request.details}</pre>
      <div className="message-editor-actions permission-response-actions">
        <button type="button" className="message-action-btn" disabled={responding} onClick={async () => { if (responding || !onRespond) return; setResponding(true); const accepted = await onRespond(request, false); if (!accepted) setResponding(false) }}>{t('common.cancel')}</button>
        <button type="button" className="composer-send-btn permission-approve-pulse" disabled={responding} onClick={async () => { if (responding || !onRespond) return; setResponding(true); const accepted = await onRespond(request, true); if (!accepted) setResponding(false) }}>{t('chat.permission.authorize')}</button>
        <button type="button" className="message-action-btn" disabled={responding || !onAutoApprove} onClick={async () => { if (responding || !onAutoApprove) return; setResponding(true); const accepted = await onAutoApprove(request); if (!accepted) setResponding(false) }}>{t('chat.permission.allowAllAction')}</button>
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