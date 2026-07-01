import { useState, useCallback, useRef, useEffect } from 'react'

const MIN_SPIN_DURATION = 400 // ms
const SUCCESS_DISPLAY_DURATION = 1200 // ms

type RefreshPhase = 'idle' | 'spinning' | 'success'

/**
 * Hook that provides a three-phase refresh animation:
 *   idle → spinning → success (checkmark) → idle
 *
 * @param refetchFn - The actual refetch function to call
 * @returns [wrappedRefetch, phase] - A wrapped function and current animation phase
 */
export function useRefreshAnimation(refetchFn: () => void | Promise<unknown>): [() => void, boolean, RefreshPhase] {
  const [phase, setPhase] = useState<RefreshPhase>('idle')
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // Guards the async `finally`/timer callbacks from setting state after the
  // host unmounts — this hook now lives on routed views that mount/unmount.
  const mountedRef = useRef(true)
  useEffect(() => {
    // Re-arm on (re)mount — Strict Mode's mount→cleanup→remount would otherwise
    // leave this stuck false and freeze the spinner on the next refresh.
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      if (timeoutRef.current) clearTimeout(timeoutRef.current)
    }
  }, [])

  const wrappedRefetch = useCallback(() => {
    if (timeoutRef.current) {
      clearTimeout(timeoutRef.current)
    }

    setPhase('spinning')

    const result = refetchFn()
    const startTime = Date.now()

    const showSuccess = () => {
      const elapsed = Date.now() - startTime
      const remaining = MIN_SPIN_DURATION - elapsed

      const transitionToSuccess = () => {
        if (!mountedRef.current) return
        setPhase('success')
        timeoutRef.current = setTimeout(() => {
          if (mountedRef.current) setPhase('idle')
        }, SUCCESS_DISPLAY_DURATION)
      }

      if (remaining > 0) {
        timeoutRef.current = setTimeout(transitionToSuccess, remaining)
      } else {
        transitionToSuccess()
      }
    }

    if (result instanceof Promise) {
      result.finally(showSuccess)
    } else {
      showSuccess()
    }
  }, [refetchFn])

  // isAnimating = backward compat (true when not idle)
  const isAnimating = phase !== 'idle'

  return [wrappedRefetch, isAnimating, phase]
}
