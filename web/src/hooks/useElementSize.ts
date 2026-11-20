import { useEffect, useRef, useState } from 'react'

export interface ElementSize {
  width: number
  height: number
}

// Tracks an element's rendered size. Until the first measurement (and where
// ResizeObserver is missing, as in jsdom) it reports `initial`.
export function useElementSize<T extends Element>(initial: ElementSize) {
  const ref = useRef<T>(null)
  const [size, setSize] = useState(initial)

  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const update = () => {
      const { width, height } = el.getBoundingClientRect()
      if (width > 0 && height > 0) {
        setSize(prev => (prev.width === width && prev.height === height ? prev : { width, height }))
      }
    }
    update()
    const observer = new ResizeObserver(update)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  return [ref, size] as const
}
