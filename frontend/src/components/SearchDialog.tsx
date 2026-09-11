import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Search, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { searchMessages, searchSessions } from '../api/client'
import type { MessageSearchResult, Session } from '../api/types'
import { useDebouncedValue } from '../lib/useDebouncedValue'

const FAST_DEBOUNCE_MS = 180
const FULL_TEXT_DEBOUNCE_MS = 600

interface Props {
  project: string
  onClose: () => void
  onSelectSession: (id: number) => void
  onSelectMessage: (sessionId: number, messageId: number) => void
}

export default function SearchDialog({ project, onClose, onSelectSession, onSelectMessage }: Props) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const debouncedQuery = useDebouncedValue(query, FAST_DEBOUNCE_MS)
  const fullTextQuery = useDebouncedValue(query, FULL_TEXT_DEBOUNCE_MS)

  const [sessionResults, setSessionResults] = useState<Session[]>([])
  const [sessionLoading, setSessionLoading] = useState(false)
  const [messageResults, setMessageResults] = useState<MessageSearchResult[]>([])
  const [messageLoading, setMessageLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const sessionControllerRef = useRef<AbortController | null>(null)
  const messageControllerRef = useRef<AbortController | null>(null)

  const runMessageSearch = useCallback((raw: string) => {
    messageControllerRef.current?.abort()
    const trimmed = raw.trim()
    if (!trimmed) {
      setMessageResults([])
      setMessageLoading(false)
      return
    }
    const controller = new AbortController()
    messageControllerRef.current = controller
    setMessageLoading(true)
    searchMessages(project, trimmed, 20, 0, controller.signal)
      .then(results => { if (messageControllerRef.current === controller) setMessageResults(results) })
      .catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause)) })
      .finally(() => { if (messageControllerRef.current === controller) setMessageLoading(false) })
  }, [project])

  useEffect(() => {
    sessionControllerRef.current?.abort()
    const trimmed = debouncedQuery.trim()
    if (!trimmed) {
      setSessionResults([])
      setSessionLoading(false)
      return
    }
    const controller = new AbortController()
    sessionControllerRef.current = controller
    setSessionLoading(true)
    searchSessions(project, trimmed, controller.signal)
      .then(results => { if (sessionControllerRef.current === controller) setSessionResults(results) })
      .catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause)) })
      .finally(() => { if (sessionControllerRef.current === controller) setSessionLoading(false) })
    return () => controller.abort()
  }, [debouncedQuery, project])

  useEffect(() => {
    runMessageSearch(fullTextQuery)
    return () => messageControllerRef.current?.abort()
  }, [fullTextQuery, runMessageSearch])

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  const trimmedQuery = query.trim()

  return createPortal(
    <div className="session-dialog-backdrop search-dialog-backdrop" role="presentation" onMouseDown={onClose}>
      <div className="search-dialog" role="dialog" aria-modal="true" aria-label={t('search.title')} onMouseDown={event => event.stopPropagation()}>
        <div className="search-dialog-input">
          <Search aria-hidden="true" size={16} />
          <input
            autoFocus
            value={query}
            onChange={event => setQuery(event.target.value)}
            placeholder={t('search.placeholder')}
            aria-label={t('search.title')}
          />
          <button type="button" className="search-dialog-close" aria-label={t('common.close')} onClick={onClose}>
            <X aria-hidden="true" size={16} />
          </button>
        </div>
        {error && <div className="sidebar-error" role="alert">{error}</div>}
        {trimmedQuery === '' ? (
          <div className="search-dialog-empty">{t('search.prompt')}</div>
        ) : (
          <div className="search-dialog-results">
            <div className="search-dialog-section">
              <div className="search-dialog-section-title">{t('search.conversations')}</div>
              {sessionResults.length === 0
                ? <div className="search-dialog-empty">{sessionLoading ? t('common.loading') : t('search.noResults')}</div>
                : sessionResults.map(session => (
                  <button key={session.ID} type="button" className="search-result-item" onClick={() => onSelectSession(session.ID)}>
                    <span className="search-result-title">{session.Title || t('session.unnamed', { id: session.ID })}</span>
                    {session.Summary && <span className="search-result-snippet">{session.Summary}</span>}
                  </button>
                ))}
            </div>

            <div className="search-dialog-section">
              <div className="search-dialog-section-title">
                <span>{t('search.messages')}</span>
                <button type="button" className="search-dialog-fulltext-btn" onClick={() => runMessageSearch(query)} disabled={messageLoading}>
                  {t('search.searchFullText')}
                </button>
              </div>
              {messageResults.length === 0
                ? <div className="search-dialog-empty">{messageLoading ? t('common.loading') : t('search.noResults')}</div>
                : messageResults.map(result => (
                  <button key={result.message_id} type="button" className="search-result-item" onClick={() => onSelectMessage(result.session_id, result.message_id)}>
                    <span className="search-result-title">{result.session_title || t('session.unnamed', { id: result.session_id })}</span>
                    <span className="search-result-snippet">{result.snippet}</span>
                  </button>
                ))}
            </div>
          </div>
        )}
      </div>
    </div>
  , document.body)
}
