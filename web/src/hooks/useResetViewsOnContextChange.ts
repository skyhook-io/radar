import { useEffect, type MutableRefObject } from 'react'

/**
 * Resets the tab's views when its connection moves to another cluster by a
 * path that delivered no context change to it: an untracked switch (CAPI
 * connect, Retry, a synchronous switch) whose SSE frame this tab missed, seen
 * only through a status poll or a reconnected stream. The tab already names
 * the new connection on its requests, so what it shows, including any open
 * drawer or confirmation, must be reset to match.
 *
 * shownContext holds the context the views were last reset for; the caller's
 * context-change handler keeps it current, so a change it already applied is
 * not applied twice.
 */
export function useResetViewsOnContextChange(
  state: string,
  context: string | undefined,
  shownContext: MutableRefObject<string | null>,
  reset: (context: string) => void,
) {
  useEffect(() => {
    if (state !== 'connected' || !context) return
    const shown = shownContext.current
    shownContext.current = context
    if (shown !== null && shown !== context) reset(context)
  }, [state, context, shownContext, reset])
}
