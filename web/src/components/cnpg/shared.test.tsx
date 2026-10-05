import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { CNPGScreenGate } from './shared'
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test' } }) }))
it('keeps workspace data with its failed-refresh reason and age', () => {
  const query = { data: { installed: true }, isRefetchError: true, error: new Error('Workspace timeout'), dataUpdatedAt: Date.now() - 120_000 } as any
  const html = renderToStaticMarkup(<CNPGScreenGate query={query} fleet={{} as any}>{() => <span>retained Cluster</span>}</CNPGScreenGate>)
  expect(html).toContain('retained Cluster')
  expect(html).toContain('Last refresh failed: Workspace timeout')
  expect(html).toContain('2m ago')
})
