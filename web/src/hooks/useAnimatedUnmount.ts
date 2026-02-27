import { useState, useEffect } from 'react'
import { DURATION_NORMAL } from '../utils/animation'

/**
 * Delays React unmount until exit animation completes.
 * - `shouldRender`: whether the component should be in the DOM
 * - `isOpen`: drives CSS transition classes (false → closed state, true → open state)
 */
export function useAnimatedUnmount(isVisible: boolean, duration = DURATION_NORMAL) {
  const [shouldRender, setShouldRender] = useState(isVisible)
  const [isOpen, setIsOpen] = useState(false)

  useEffect(() => {
    if (isVisible) {
      setShouldRender(true)
      // Double rAF: DOM paints closed state, then we flip to open → triggers CSS transition
      requestAnimationFrame(() => requestAnimationFrame(() => setIsOpen(true)))
    } else if (shouldRender) {
      setIsOpen(false)
      const t = setTimeout(() => setShouldRender(false), duration)
      return () => clearTimeout(t)
    }
  }, [isVisible, duration]) // eslint-disable-line react-hooks/exhaustive-deps

  return { shouldRender, isOpen }
}
