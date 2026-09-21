import { useState, useEffect, useRef } from 'react'
import { DURATION_NORMAL, prefersReducedMotion } from '../utils/animation'

/**
 * Delays React unmount until exit animation completes.
 * - `shouldRender`: whether the component should be in the DOM
 * - `isOpen`: drives CSS transition classes (false → closed state, true → open state)
 *
 * Pass the EXIT duration (overlayExitMs('dialog') etc.) — that is how long the
 * closed state stays mounted. Under reduced motion the app stylesheet zeroes the
 * transition, so the hook disposes at logical close instead of holding an
 * invisible overlay (and its focus / scroll lock) for the exit window.
 */
export function useAnimatedUnmount(isVisible: boolean, duration = DURATION_NORMAL) {
  const [shouldRender, setShouldRender] = useState(isVisible)
  const [isOpen, setIsOpen] = useState(false)
  const rafRef = useRef(0)

  useEffect(() => {
    if (isVisible) {
      setShouldRender(true)
      // Double rAF: DOM paints closed state, then we flip to open → triggers CSS transition
      rafRef.current = requestAnimationFrame(() => {
        rafRef.current = requestAnimationFrame(() => setIsOpen(true))
      })
      return () => cancelAnimationFrame(rafRef.current)
    } else if (shouldRender) {
      setIsOpen(false)
      if (prefersReducedMotion()) {
        setShouldRender(false)
        return
      }
      // Start the disposal clock only once the closed state has painted (same
      // double rAF as the open path). A heavy overlay can take a slow frame to
      // commit its closed classes; a timer started at logical close would then
      // unmount it before the exit transition has had its full duration.
      let t = 0
      rafRef.current = requestAnimationFrame(() => {
        rafRef.current = requestAnimationFrame(() => {
          t = window.setTimeout(() => setShouldRender(false), duration)
        })
      })
      return () => { cancelAnimationFrame(rafRef.current); clearTimeout(t) }
    }
  }, [isVisible, duration]) // eslint-disable-line react-hooks/exhaustive-deps — shouldRender intentionally omitted to avoid re-triggering cleanup

  return { shouldRender, isOpen }
}
