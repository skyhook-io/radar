import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { ContextInfo } from '../types'

const requestSwitch = vi.fn()
const contexts: ContextInfo[] = [
  { name: 'kind-radar-ph-video', cluster: 'kind-radar-ph-video', user: 'u', namespace: '', isCurrent: false } as ContextInfo,
  { name: 'gke_acme_europe-west1_staging-eu', cluster: 'staging-eu', user: 'u', namespace: '', isCurrent: true } as ContextInfo,
]

vi.mock('../api/client', () => ({ useContexts: () => ({ data: contexts }) }))
vi.mock('./useContextSwitchFlow', () => ({ useContextSwitchFlow: () => ({ requestSwitch, isPending: false, confirmDialog: null }) }))
vi.mock('../context/ContextSwitchContext', () => ({ useContextSwitch: () => ({ failedSwitch: null, setFailedSwitch: vi.fn() }) }))
vi.mock('./ConnectionErrorView', () => ({
  ConnectionErrorView: ({ back }: { back?: { label: string } }) => <div>{back ? `back:${back.label}` : 'no back'}</div>,
}))

const { DisconnectedView } = await import('./SwitchAwareConnectionError')

const down = { state: 'disconnected' as const, context: 'gke_acme_europe-west1_staging-eu', errorType: 'timeout' }

describe('DisconnectedView', () => {
  it('offers the last connected cluster after a switch that left it', () => {
    const markup = renderToStaticMarkup(
      <DisconnectedView connection={down} onRetry={() => {}} isRetrying={false} lastConnectedContext="kind-radar-ph-video" />,
    )
    expect(markup).toContain('back:kind-radar-ph-video')
  })

  it('offers nothing when the cluster that is down is the one that last worked', () => {
    const markup = renderToStaticMarkup(
      <DisconnectedView connection={down} onRetry={() => {}} isRetrying={false} lastConnectedContext="gke_acme_europe-west1_staging-eu" />,
    )
    expect(markup).toContain('no back')
  })

  it('offers nothing before anything has connected', () => {
    const markup = renderToStaticMarkup(
      <DisconnectedView connection={down} onRetry={() => {}} isRetrying={false} lastConnectedContext={null} />,
    )
    expect(markup).toContain('no back')
  })
})
