import { useCallback } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'

/**
 * A CloudNativePG screen's own URL parameters (`q`, `cat`, `filter`, …),
 * written in place: an empty value removes the parameter, history is replaced
 * and its state (the return to the previous page) is kept.
 */
export function useCNPGScreenParams(): [URLSearchParams, (update: Record<string, string | null>) => void] {
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()
  const setParams = useCallback(
    (update: Record<string, string | null>) => {
      const params = new URLSearchParams(searchParams)
      for (const [k, v] of Object.entries(update)) {
        if (v === null || v === '') params.delete(k)
        else params.set(k, v)
      }
      setSearchParams(params, { replace: true, state: location.state })
    },
    [searchParams, setSearchParams, location.state],
  )
  return [searchParams, setParams]
}
