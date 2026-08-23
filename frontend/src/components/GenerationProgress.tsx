import { useEffect, useEffectEvent, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from '../i18n'
import Markdown from './Markdown'
import type { InteractionRequest, QuestionsInteractionRequest, StreamToolCall } from '../api/types'
import type { AuthorizationMode } from './Composer'
import { normalizeQuestionAnswers, type DraftQuestionAnswer } from '../lib/questionAnswers'

export type StreamActivity =
  | { type: 'reasoning'; sequence: number; content: string }
  | { type: 'tool'; sequence: number; toolCall: StreamToolCall }
  | { type: 'permission'; sequence: number; request: Extract<InteractionRequest, { kind: 'permission' }> }
  | { type: 'questions'; sequence: number; request: QuestionsInteractionRequest }

interface Props {
  content: string
  activities: StreamActivity[]
  onRespondToPermission?: (request: Extract<InteractionRequest, { kind: 'permission' }>, approved: boolean) => Promise<boolean>
  onRespondToQuestions?: (request: QuestionsInteractionRequest, answers: import('../api/types').QuestionAnswer[]) => Promise<boolean>
  authorizationMode?: AuthorizationMode
  onAutoApprovePermission?: (request: InteractionRequest) => Promise<boolean>
}

export default function GenerationProgress({ content, activities, onRespondToPermission, onRespondToQuestions, authorizationMode = 'askEachTime', onAutoApprovePermission }: Props) {
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
		  ) : activity.type === 'permission' ? (
      <PermissionActivity key={activity.sequence} request={activity.request} onRespond={onRespondToPermission} authorizationMode={authorizationMode} onAutoApprove={onAutoApprovePermission} />
		  ) : (
			<QuestionActivity key={activity.sequence} request={activity.request} onRespond={onRespondToQuestions} />
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

function PermissionActivity({ request, onRespond, authorizationMode, onAutoApprove }: { request: Extract<InteractionRequest, { kind: 'permission' }>; onRespond?: (request: Extract<InteractionRequest, { kind: 'permission' }>, approved: boolean) => Promise<boolean>; authorizationMode: AuthorizationMode; onAutoApprove?: (request: InteractionRequest) => Promise<boolean> }) {
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

function QuestionActivity({ request, onRespond }: { request: QuestionsInteractionRequest; onRespond?: (request: QuestionsInteractionRequest, answers: import('../api/types').QuestionAnswer[]) => Promise<boolean> }) {
  const { t } = useTranslation()
  const [drafts, setDrafts] = useState<Record<string, DraftQuestionAnswer>>({})
  const [responding, setResponding] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const answers = normalizeQuestionAnswers(request.questions, drafts)

  const setSelection = (questionId: string, optionId: string, multiSelect: boolean, checked: boolean) => {
    setDrafts(current => {
      const draft = current[questionId] ?? { selectedOptionIds: [], customText: '' }
      const selectedOptionIds = multiSelect
        ? checked ? [...draft.selectedOptionIds, optionId] : draft.selectedOptionIds.filter(id => id !== optionId)
        : checked ? [optionId] : []
      return { ...current, [questionId]: { ...draft, selectedOptionIds } }
    })
  }

  return <div className="tool-activity-list">
    <div className="tool-activity question-activity">
      <div className="tool-activity-header">
        <span className="tool-status-dot" data-status="calling" />
        <strong>{t('chat.questions.title')}</strong>
        <span>{t('chat.questions.awaiting')}</span>
      </div>
      <form onSubmit={async event => {
        event.preventDefault()
        if (!answers || responding || !onRespond) return
        setResponding(true)
        setError(null)
        const accepted = await onRespond(request, answers)
        if (!accepted) {
          setResponding(false)
          setError(t('chat.questions.submitFailed'))
        }
      }}>
        {request.questions.map(question => {
          const draft = drafts[question.id] ?? { selectedOptionIds: [], customText: '' }
          return <fieldset className="question-fieldset" key={question.id} disabled={responding}>
            <legend>{question.prompt}</legend>
            <div className="question-options">
              {question.options.map(option => <label className="question-option" key={option.id}>
                <input
                  type={question.multi_select ? 'checkbox' : 'radio'}
                  name={question.id}
                  checked={draft.selectedOptionIds.includes(option.id)}
                  onChange={event => setSelection(question.id, option.id, question.multi_select, event.currentTarget.checked)}
                />
                <span><strong>{option.title}</strong><small>{option.description}</small></span>
              </label>)}
            </div>
            <label className="question-custom-text">
              <span>{t('chat.questions.customAnswer')}</span>
              <textarea value={draft.customText} onChange={event => {
                const customText = event.currentTarget.value
                setDrafts(current => ({ ...current, [question.id]: { ...draft, customText } }))
              }} rows={2} />
            </label>
          </fieldset>
        })}
        {error && <p className="question-submit-error">{error}</p>}
        <div className="message-editor-actions permission-response-actions">
          <button type="submit" className="composer-send-btn" disabled={responding || answers == null}>{responding ? t('chat.questions.submitting') : t('chat.questions.submit')}</button>
        </div>
      </form>
    </div>
  </div>
}