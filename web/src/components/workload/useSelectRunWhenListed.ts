import { useEffect, useRef, useState } from 'react'

const POLL_MS = 1000
const MAX_POLLS = 15

/**
 * Selects a run once the runs list includes it. A just-created run reaches
 * Radar's cache a moment after the API server accepts it, and the run view
 * replaces a selection it can't find with its default run, so selecting it
 * right away would not stick. Refetches the list every second, for up to 15
 * seconds, until the run appears.
 */
export function useSelectRunWhenListed(
  listedKeys: string[] | undefined,
  refetch: () => unknown,
  select: (key: string) => void,
): (key: string) => void {
  const [pending, setPending] = useState('')
  const listed = Boolean(pending) && (listedKeys ?? []).includes(pending)
  const latest = useRef({ refetch, select })
  latest.current = { refetch, select }

  useEffect(() => {
    if (!pending) return
    if (listed) {
      latest.current.select(pending)
      setPending('')
      return
    }
    let polls = 0
    const timer = setInterval(() => {
      if (++polls > MAX_POLLS) {
        setPending('')
        return
      }
      void latest.current.refetch()
    }, POLL_MS)
    return () => clearInterval(timer)
  }, [pending, listed])

  return setPending
}
