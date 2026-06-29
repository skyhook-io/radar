/**
 * Wrapper around `document.startViewTransition` that:
 *
 *   1. Falls back to a synchronous call to `update` when the View
 *      Transitions API isn't available (older Safari, Firefox without
 *      the flag, jsdom in tests).
 *   2. Swallows the `InvalidStateError` that the API rejects with
 *      whenever a new transition starts while a previous one is still
 *      pending. This is a normal symptom of impatient users (clicking
 *      tabs faster than 200ms) — the new transition has already
 *      replaced the in-flight one, so there is nothing for the caller
 *      to do. SKY-833 bug 49 surfaced this as a console exception
 *      stack on rapid Secrets / drawer-tab navigation.
 *
 * Other rejection types (programming errors, unsupported configs)
 * are re-thrown so we don't accidentally hide real bugs.
 */
export function startViewTransitionSafe(update: () => void): void {
  // Some lib.dom.d.ts versions don't yet expose `startViewTransition`
  // on Document. Narrow once via a typed cast.
  const doc = document as Document & {
    startViewTransition?: (cb: () => void) => {
      finished: Promise<void>
      ready: Promise<void>
      updateCallbackDone: Promise<void>
      skipTransition: () => void
    }
  }

  if (typeof doc.startViewTransition !== 'function') {
    update()
    return
  }

  const transition = doc.startViewTransition(update)

  // The `finished` promise rejects with InvalidStateError when a new
  // transition supersedes this one. That's not an error condition for
  // the user — they got what they wanted (the new state). Swallow it
  // so it doesn't become a noisy console exception.
  transition.finished.catch((err: unknown) => {
    if (isInvalidStateError(err)) return
    // Anything else really is unexpected — let it surface.
    throw err
  })
}

/**
 * Predicate that classifies the rejection thrown by the View
 * Transitions API when a new transition supersedes an in-flight one.
 * Used by `startViewTransitionSafe` to know what to swallow, and
 * exported so tests (and any future call site that wraps the API
 * differently) can pin the exact name we treat as expected.
 */
export function isInvalidStateError(err: unknown): boolean {
  return (
    !!err &&
    typeof err === 'object' &&
    'name' in err &&
    (err as { name: unknown }).name === 'InvalidStateError'
  )
}
