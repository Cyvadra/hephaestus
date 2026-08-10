import { useCallback, useEffect, useRef, useState, type RefObject } from 'react'

export const HOVER_MENU_CLOSE_DELAY_MS = 250

type MenuState = 'closed' | 'hover' | 'pinned'

export function useHoverMenu(rootRef: RefObject<HTMLElement | null>, closeDelay = HOVER_MENU_CLOSE_DELAY_MS) {
  const [state, setState] = useState<MenuState>('closed')
  const closeTimerRef = useRef<number | null>(null)

  const cancelClose = useCallback(() => {
    if (closeTimerRef.current != null) {
      window.clearTimeout(closeTimerRef.current)
      closeTimerRef.current = null
    }
  }, [])

  const close = useCallback(() => {
    cancelClose()
    setState('closed')
  }, [cancelClose])

  const openOnHover = useCallback(() => {
    cancelClose()
    setState(current => current === 'closed' ? 'hover' : current)
  }, [cancelClose])

  const scheduleClose = useCallback(() => {
    if (state !== 'hover') return
    cancelClose()
    closeTimerRef.current = window.setTimeout(() => {
      closeTimerRef.current = null
      setState('closed')
    }, closeDelay)
  }, [state, closeDelay, cancelClose])

  const pinOpen = useCallback(() => {
    cancelClose()
    setState('pinned')
  }, [cancelClose])

  const togglePinned = useCallback(() => {
    cancelClose()
    setState(current => current === 'pinned' ? 'closed' : 'pinned')
  }, [cancelClose])

  useEffect(() => {
    if (state === 'closed') return
    const closeOnOutsidePointer = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) close()
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') close()
    }
    document.addEventListener('mousedown', closeOnOutsidePointer)
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      document.removeEventListener('mousedown', closeOnOutsidePointer)
      document.removeEventListener('keydown', closeOnEscape)
      cancelClose()
    }
  }, [state, rootRef, close, cancelClose])

  return { open: state !== 'closed', cancelClose, close, openOnHover, scheduleClose, pinOpen, togglePinned }
}