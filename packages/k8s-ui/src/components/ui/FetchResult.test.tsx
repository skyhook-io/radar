import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { FetchResult } from './FetchResult'

function shaped(message: string, status: number) {
  return Object.assign(new Error(message), { status })
}

describe('FetchResult', () => {
  it('renders the loader when loading, ignoring error', () => {
    const html = renderToString(<FetchResult loading={true} error={shaped('forbidden', 403)} />)
    expect(html).toContain('Loading')
    expect(html).not.toContain('Access denied')
  })

  it('renders notFoundMessage when neither loading nor error (disabled-query fallback)', () => {
    const html = renderToString(<FetchResult loading={false} notFoundMessage="Pod not found" />)
    expect(html).toContain('Pod not found')
  })

  it('default notFoundMessage is "Resource not found"', () => {
    const html = renderToString(<FetchResult loading={false} />)
    expect(html).toContain('Resource not found')
  })

  it('shows "Access denied" headline and the server message on 403', () => {
    const html = renderToString(
      <FetchResult
        loading={false}
        error={shaped('no access to clusterroles (cluster-scoped resource requires explicit RBAC)', 403)}
      />,
    )
    expect(html).toContain('Access denied')
    expect(html).toContain('no access to clusterroles')
  })

  it('uses notFoundMessage and renders the server message on 404', () => {
    const html = renderToString(
      <FetchResult
        loading={false}
        error={shaped('pods web-1 not found', 404)}
        notFoundMessage="Pod not found"
      />,
    )
    expect(html).toContain('Pod not found')
    expect(html).toContain('pods web-1 not found')
  })

  it('renders "Cluster unavailable" on 503', () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('Resource cache not available', 503)} />,
    )
    expect(html).toContain('Cluster unavailable')
    expect(html).toContain('Resource cache not available')
  })

  it('renders "Sign-in required" on 401', () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('Unauthorized', 401)} />,
    )
    expect(html).toContain('Sign-in required')
  })

  it("renders a generic 'Couldn't load' for other 5xx", () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('internal server error', 500)} />,
    )
    expect(html).toContain('Couldn')
    expect(html).toContain('internal server error')
  })

  it('handles a network failure (Error without .status) via the generic branch', () => {
    const html = renderToString(
      <FetchResult loading={false} error={new Error('Failed to fetch')} />,
    )
    expect(html).toContain('Couldn')
    expect(html).toContain('Failed to fetch')
  })

  it('handles an AbortError-style throw without a status field', () => {
    const aborted = new Error('The user aborted a request.')
    aborted.name = 'AbortError'
    const html = renderToString(<FetchResult loading={false} error={aborted} />)
    expect(html).toContain('Couldn')
  })

  it('renders the Copy-error button when an error message is present', () => {
    const html = renderToString(
      <FetchResult loading={false} error={shaped('forbidden', 403)} />,
    )
    expect(html).toContain('Copy error')
  })
})
