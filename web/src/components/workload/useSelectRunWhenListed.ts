import { useCallback, useEffect, useRef, useState } from 'react'

const POLL_MS = 1000
const MAX_POLLS = 15

/**
 * Selects a run once the runs list includes it. A just-created run reaches
 * Radar's cache a moment after the API server accepts it, and the run view
 * replaces a selection it can't find with its default run, so selecting it
 * right away would not stick. Refetches the list every second until the run
 * appears. When the list fails (the caller may create Workflows but not list
 * them) or 15 seconds pass, hands the run to `unlisted` instead, so the user
 * still gets to it. `scope` names the list's owner: a run waiting for one
 * template is dropped when the view moves to another.
 */
export function useSelectRunWhenListed(
  scope: string,
  listedKeys: string[] | undefined,
  listFailed: boolean,
  refetch: () => unknown,
  select: (key: string) => void,
  unlisted: (key: string) => void,
): (key: string) => void {
  const [pending, setPending] = useState<{ key: string; scope: string } | null>(null)
  const current = pending?.scope === scope ? pending.key : ''
  const listed = Boolean(current) && (listedKeys ?? []).includes(current)
  const latest = useRef({ refetch, select, unlisted })
  latest.current = { refetch, select, unlisted }

  useEffect(() => {
    if (pending && pending.scope !== scope) setPending(null)
  }, [pending, scope])

  useEffect(() => {
    if (!current) return
    if (listed || listFailed) {
      if (listed) latest.current.select(current)
      else latest.current.unlisted(current)
      setPending(null)
      return
    }
    let polls = 0
    const timer = setInterval(() => {
      if (++polls > MAX_POLLS) {
        clearInterval(timer)
        setPending(null)
        latest.current.unlisted(current)
        return
      }
      void latest.current.refetch()
    }, POLL_MS)
    return () => clearInterval(timer)
  }, [current, listed, listFailed])

  return useCallback((key: string) => setPending({ key, scope }), [scope])
}
