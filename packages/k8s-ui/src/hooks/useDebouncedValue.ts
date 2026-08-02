import { useEffect, useState } from 'react'

/** Trailing-edge debounce of a value. Unlike useDeferredValue (which yields to
 *  React's scheduler but still emits once per input change), this coalesces a
 *  burst of changes into one emission after `delayMs` of quiet — the right tool
 *  when each downstream emission has a fixed cost regardless of render pressure
 *  (router URL writes, live-order re-ranks).
 *
 *  `flushWhen` values bypass the delay (e.g. an emptied search box: a delayed
 *  clear makes the × button feel broken). The flush must reset the pending
 *  timer state INSIDE the hook — masking it at the call site
 *  (`v === '' ? '' : debounced`) leaves the stale value armed, and it
 *  resurfaces if the user types again before the timer fires. */
export function useDebouncedValue<T>(value: T, delayMs: number, flushWhen?: (value: T) => boolean): T {
  const [debounced, setDebounced] = useState(value)

  useEffect(() => {
    if (Object.is(debounced, value)) return
    if (flushWhen?.(value)) {
      setDebounced(value)
      return
    }
    const timer = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- re-arm only when the input changes; `debounced` catching up (or an inline `flushWhen` identity) must not restart the timer
  }, [value, delayMs])

  return debounced
}
