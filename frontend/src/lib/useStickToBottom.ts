import { useCallback, useEffect, useRef, type RefObject } from 'react'

// Scrolling down to within this distance of the bottom re-engages following.
const RESTICK_DISTANCE = 24

/**
 * Keeps a scroll pane pinned to its bottom while its content grows, until the
 * user shows intent to read elsewhere. Following is instant (no smooth
 * animation), so it never fights the user's own scrolling, and only an upward
 * user gesture releases it: programmatic jumps and content growth cannot.
 */
export function useStickToBottom(paneRef: RefObject<HTMLElement | null>, contentRef: RefObject<HTMLElement | null>) {
  const stuckRef = useRef(true)
  const lastScrollTopRef = useRef(0)

  const scrollToBottom = useCallback(() => {
    const pane = paneRef.current
    if (!pane) return
    pane.scrollTop = pane.scrollHeight
    lastScrollTopRef.current = pane.scrollTop
  }, [paneRef])

  const setStuck = useCallback((stuck: boolean) => {
    stuckRef.current = stuck
    if (stuck) scrollToBottom()
  }, [scrollToBottom])

  const handleScroll = useCallback(() => {
    const pane = paneRef.current
    if (!pane) return
    const top = pane.scrollTop
    const distance = pane.scrollHeight - top - pane.clientHeight
    // A shrink that clamps scrollTop lands exactly on the bottom, so it is
    // told apart from the user scrolling up by the remaining distance.
    if (distance <= 1) stuckRef.current = true
    else if (top < lastScrollTopRef.current - 1) stuckRef.current = false
    else if (top > lastScrollTopRef.current && distance <= RESTICK_DISTANCE) stuckRef.current = true
    lastScrollTopRef.current = top
  }, [paneRef])

  useEffect(() => {
    const pane = paneRef.current
    const content = contentRef.current
    if (!pane || !content) return
    // Wheel and touch release before the scroll lands, so the next content
    // resize cannot yank a small upward gesture back to the bottom.
    const release = () => {
      if (pane.scrollHeight > pane.clientHeight && pane.scrollTop > 0) stuckRef.current = false
    }
    // A nested block (code, tool output) that can still scroll up takes the wheel itself.
    const scrollsNestedBlock = (target: EventTarget | null) => {
      for (let element = target instanceof Element ? target : null; element && element !== pane; element = element.parentElement) {
        if (element.scrollTop > 0) return true
      }
      return false
    }
    const handleWheel = (event: WheelEvent) => {
      if (event.deltaY < 0 && !scrollsNestedBlock(event.target)) release()
    }
    let touchY: number | null = null
    const handleTouchStart = (event: TouchEvent) => {
      touchY = event.touches[0]?.clientY ?? null
    }
    const handleTouchMove = (event: TouchEvent) => {
      const y = event.touches[0]?.clientY ?? null
      if (touchY != null && y != null && y > touchY) release()
      touchY = y
    }
    const observer = new ResizeObserver(() => {
      if (stuckRef.current) scrollToBottom()
    })
    observer.observe(content)
    observer.observe(pane)
    pane.addEventListener('wheel', handleWheel, { passive: true })
    pane.addEventListener('touchstart', handleTouchStart, { passive: true })
    pane.addEventListener('touchmove', handleTouchMove, { passive: true })
    return () => {
      observer.disconnect()
      pane.removeEventListener('wheel', handleWheel)
      pane.removeEventListener('touchstart', handleTouchStart)
      pane.removeEventListener('touchmove', handleTouchMove)
    }
  }, [contentRef, paneRef, scrollToBottom])

  return { stuckRef, setStuck, scrollToBottom, handleScroll }
}
