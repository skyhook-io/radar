import { describe, expect, it } from 'vitest'

import { ErrorBoundary } from './ErrorBoundary'

// Navigating re-renders this boundary rather than remounting it, so a boundary
// that only latched would keep the fallback on screen for the rest of the
// session. These drive the lifecycle hooks directly - a caught error needs a
// real render loop, which the SSR-string tests used elsewhere here cannot give.
const caught = (resetKey: string) => ({
  hasError: true,
  error: new Error("Cannot read properties of undefined (reading 'nodeCount')"),
  resetKey,
})

describe('ErrorBoundary reset', () => {
  it('clears a caught error when resetKey changes', () => {
    expect(ErrorBoundary.getDerivedStateFromProps({ children: null, resetKey: '/topology' }, caught('/'))).toEqual({
      hasError: false,
      error: null,
      resetKey: '/topology',
    })
  })

  it('clears it when only a later path segment changes', () => {
    // The view is just the first path segment, so /resources/pods and
    // /resources/services are the same view. Resetting per view would leave a
    // crash under /resources trapping the user as they switch kinds.
    const next = ErrorBoundary.getDerivedStateFromProps(
      { children: null, resetKey: '/resources/services' },
      caught('/resources/pods'),
    )
    expect(next?.hasError).toBe(false)
  })

  it('clears it when only the query changes', () => {
    // Selection rides in the query, so a path-only key would strand a crash on
    // the view that produced it.
    const next = ErrorBoundary.getDerivedStateFromProps(
      { children: null, resetKey: '/resources/pods?resource=kube-system/coredns' },
      caught('/resources/pods'),
    )
    expect(next?.hasError).toBe(false)
  })

  it('holds the error while resetKey is unchanged', () => {
    expect(ErrorBoundary.getDerivedStateFromProps({ children: null, resetKey: '/' }, caught('/'))).toBeNull()
  })

  it('records the error', () => {
    expect(ErrorBoundary.getDerivedStateFromError(new Error('boom'))).toEqual({
      hasError: true,
      error: new Error('boom'),
    })
  })
})
