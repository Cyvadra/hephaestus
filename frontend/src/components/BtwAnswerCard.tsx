import { useTranslation } from 'react-i18next'
import type { BtwAnswer } from '../lib/btw'
import Markdown from './Markdown'

interface Props {
  answer: BtwAnswer
  onDismiss: () => void
}

/**
 * Renders an ephemeral /btw answer next to the conversation it was asked
 * about. The card is presentation-only: it is never part of the message
 * tree, so it cannot be reloaded or replayed.
 */
export default function BtwAnswerCard({ answer, onDismiss }: Props) {
  const { t } = useTranslation()
  const streaming = answer.status === 'streaming'
  return (
    <div className="btw-card">
      <div className="btw-head">
        <span className="btw-label">{t('chat.btw.label')}</span>
        <span className="btw-question">{answer.question}</span>
        <button
          type="button"
          className="btw-dismiss"
          onClick={onDismiss}
          title={t(streaming ? 'chat.btw.stop' : 'chat.btw.dismiss')}
          aria-label={t(streaming ? 'chat.btw.stop' : 'chat.btw.dismiss')}
        >
          {streaming ? '■' : '×'}
        </button>
      </div>
      <div className="btw-body" role="status" aria-live="polite">
        {answer.text
          ? <Markdown>{answer.text}</Markdown>
          : streaming
            ? <span className="btw-thinking">{t('chat.btw.thinking')}</span>
            : null}
      </div>
      {answer.error && <div className="btw-error">{t('chat.btw.error')}: {answer.error}</div>}
      <div className="btw-note">{t('chat.btw.ephemeral')}</div>
    </div>
  )
}
