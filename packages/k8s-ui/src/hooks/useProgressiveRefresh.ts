import { useEffect, useRef } from 'react'

const FAST_REFRESH_INTERVAL = 2000
const SLOW_REFRESH_INTERVAL = 15000
const FAST_REFRESH_DURATION = 120000

export function useProgressiveRefresh(
  enabled: boolean,
  refresh?: () => void | Promise<unknown>,
) {
  const refreshRef = useRef(refresh)
  refreshRef.current = refresh
  const hasRefresh = Boolean(refresh)

  useEffect(() => {
    if (!enabled || !hasRefresh) return
    const startedAt = Date.now()
    let cancelled = false
    let timeout: number | undefined
    const schedule = () => {
      const delay = Date.now() - startedAt < FAST_REFRESH_DURATION ? FAST_REFRESH_INTERVAL : SLOW_REFRESH_INTERVAL
      timeout = window.setTimeout(() => {
        void Promise.resolve()
          .then(() => refreshRef.current?.())
          .catch(() => undefined)
          .finally(() => {
            if (!cancelled) schedule()
          })
      }, delay)
    }
    schedule()
    return () => {
      cancelled = true
      if (timeout !== undefined) window.clearTimeout(timeout)
    }
  }, [enabled, hasRefresh])
}
