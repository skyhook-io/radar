import { useState } from 'react'

// React Query clears the error of a query that has never loaded each time it
// refetches, so a failure that persists across polls would flicker to a loader
// on every interval. This keeps the last error on screen until data arrives.
// Once data is loaded it returns null: a failing poll then belongs to the
// stale-data banner, not the full-screen error.
export function useStickyError(error: Error | null | undefined, hasData: boolean): Error | null {
  const [shown, setShown] = useState<Error | null>(null)
  const next = hasData ? null : (error ?? shown)
  // Adjusting state during render: converges in one extra pass because `next`
  // is a fixed point once stored.
  if (next !== shown) setShown(next)
  return next
}
