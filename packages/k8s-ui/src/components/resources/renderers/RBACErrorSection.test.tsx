import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { RBACErrorSection } from './RBACErrorSection'

function err(message: string, status?: number): Error {
  return Object.assign(new Error(message), status === undefined ? {} : { status })
}

describe('RBACErrorSection', () => {
  it('renders 503 (SA cannot read RBAC) as a calm note, not a red error', () => {
    const html = renderToString(
      <RBACErrorSection title="Permissions" error={err('RBAC cache not available', 503)} />,
    )
    expect(html).toContain('RBAC visibility')
    expect(html).not.toContain('text-red-400')
  })

  it('renders a non-RBAC 503 (e.g. cluster disconnect) in red, not the calm RBAC note', () => {
    const html = renderToString(
      <RBACErrorSection title="Permissions" error={err('Not connected to cluster', 503)} />,
    )
    expect(html).toContain('text-red-400')
    expect(html).not.toContain('RBAC visibility')
  })

  it('renders 403 (viewer lacks permission) as a calm note, not a red error', () => {
    const html = renderToString(
      <RBACErrorSection title="Permissions" error={err('forbidden', 403)} />,
    )
    expect(html).toContain('permission to view RBAC bindings')
    expect(html).not.toContain('text-red-400')
  })

  it('renders genuine failures (500 / no status) in red with the prefix', () => {
    const html = renderToString(
      <RBACErrorSection title="Permissions" error={err('boom', 500)} />,
    )
    expect(html).toContain('text-red-400')
    expect(html).toContain('Could not load permissions')
    expect(html).toContain('boom')
  })

  it('honors a custom errorPrefix for genuine failures', () => {
    const html = renderToString(
      <RBACErrorSection title="Bindings" error={err('boom')} errorPrefix="Could not load RBAC data" />,
    )
    expect(html).toContain('Could not load RBAC data')
  })
})
