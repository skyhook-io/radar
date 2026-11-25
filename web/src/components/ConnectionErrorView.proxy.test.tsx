// @vitest-environment jsdom
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

vi.mock('@skyhook-io/k8s-ui', () => ({ ClusterName: ({ name }: { name: string }) => <span>{name}</span> }))
vi.mock('./dock/DockContext', () => ({ useOpenLocalTerminal: () => vi.fn() }))
vi.mock('../api/client', () => ({ useAuthMe: () => ({ data: { authEnabled: false } }), useContexts: () => ({ data: undefined }) }))
vi.mock('../contexts/CapabilitiesContext', () => ({ useCapabilitiesContext: () => ({ localTerminal: false, clusterProxy: true }) }))
vi.mock('./ContextSwitcher', () => ({ ContextSwitcher: () => null }))
vi.mock('./settings/ClusterProxyForm', () => ({
  ClusterProxyForm: ({ context }: { context: string }) => {
    const [draftFor] = useState(context)
    return <div data-draft-for={draftFor} />
  },
}))

const { ConnectionErrorView } = await import('./ConnectionErrorView')
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// The failed-switch screen is reused when another switch fails: a proxy typed,
// or still being checked, for the first cluster must not carry over to the next.
it('starts a new proxy form for each failed context', () => {
  const host = document.createElement('div')
  const root = createRoot(host)
  const render = (context: string) => act(() => root.render(
    <ConnectionErrorView connection={{ state: 'disconnected', context, errorType: 'network', error: 'x' }} onRetry={() => {}} isRetrying={false} />,
  ))
  render('staging')
  render('prod')
  expect(host.querySelector('[data-draft-for]')?.getAttribute('data-draft-for')).toBe('prod')
  act(() => root.unmount())
})
