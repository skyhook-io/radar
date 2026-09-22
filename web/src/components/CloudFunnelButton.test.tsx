// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConnectionStateType } from '../context/ConnectionContext'
import { CloudFunnelButton } from './CloudFunnelButton'

const connection = vi.hoisted(() => ({ state: 'connecting' as ConnectionStateType }))
vi.mock('../context/ConnectionContext', () => ({ useConnection: () => ({ connection }) }))
vi.mock('./ui/Tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))
vi.mock('./ui/Toast', () => ({ showApiError: vi.fn() }))
vi.mock('./CloudConnectFlow', () => ({ CloudConnectFlow: () => <div>Install flow</div> }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let client: QueryClient
let lane: 'driver' | 'wizard'
let discovery: { connected: { namespace: string; deployment: string; clusterUrl: string }[] }
let discoveryStatus: number
let flowState: 'idle' | 'awaiting_approval'
const requests: string[] = []

beforeEach(() => {
  connection.state = 'connecting'
  lane = 'driver'
  discovery = { connected: [] }
  discoveryStatus = 200
  flowState = 'idle'
  requests.length = 0
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://localhost')
    requests.push(url.pathname + url.search)
    const bodies: Record<string, unknown> = {
      '/api/capabilities': { cloudConnect: { lane, appUrl: 'https://cloud.example', apiUrl: 'https://cloud.example' }, deployment: { mode: 'local' } },
      '/api/cluster-info': { context: 'test-cluster' },
      '/api/cloud/install/status': { state: flowState },
      '/api/cloud/install/discover': discoveryStatus === 200 ? discovery : { error: 'Discovery failed' },
      '/api/cloud/install/prepare': { error: 'Not connected to cluster' },
      '/api/connect/info': {},
    }
    if (!(url.pathname in bodies)) throw new Error(`Unexpected request: ${input}`)
    const status = url.pathname.endsWith('/prepare') ? 503 : url.pathname.endsWith('/discover') ? discoveryStatus : 200
    return new Response(JSON.stringify(bodies[url.pathname]), { status })
  }))
})

afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  document.body.replaceChildren()
  vi.unstubAllGlobals()
})

async function render() {
  await act(async () => {
    root.render(<QueryClientProvider client={client}><CloudFunnelButton /></QueryClientProvider>)
  })
}

async function until(assertion: () => void) {
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    assertion()
  })
}

function button(text: string) {
  return Array.from(document.querySelectorAll('button')).find(element => element.textContent === text)
}

async function openDialog() {
  await render()
  await until(() => expect(document.querySelector('[aria-label="Radar Cloud"]')).not.toBeNull())
  await act(async () => { (document.querySelector('[aria-label="Radar Cloud"]') as HTMLButtonElement).click() })
}

describe('Cloud dialog connection availability', () => {
  it.each(['connecting', 'disconnected'] as const)('offers Cloud directly while %s without inspecting a cluster', async state => {
    connection.state = state
    await openDialog()
    await until(() => expect(requests).toContain('/api/connect/info?lane=wizard&mode=local'))
    const link = Array.from(document.querySelectorAll('a')).find(element => element.textContent === 'Continue in Radar Cloud')!
    expect(link.href).toBe('https://cloud.example/signup?utm_source=radar-oss&utm_medium=app&utm_campaign=cloud-modal&utm_content=driver-footer-browser-link')
    expect(button('Connect this cluster…')).toBeUndefined()
    expect(button('Try again')).toBeUndefined()
    expect(requests).not.toContain('/api/cloud/install/discover')
    expect(requests).not.toContain('/api/cloud/install/prepare')
    await act(async () => { button('How it works and what it costs')!.click() })
    expect(document.body.textContent).toContain('You approve the connection before anything is installed.')
    expect(document.body.textContent).not.toContain('Setup runs here in the app')
    expect(document.body.textContent).not.toContain('Nothing installs on click')
  })

  it('restores cluster discovery and Connect in the open dialog when the connection recovers', async () => {
    await openDialog()
    connection.state = 'connected'
    await render()
    await until(() => expect(button('Connect this cluster…')?.disabled).toBe(false))
    expect(requests).toContain('/api/cloud/install/discover')
    expect(requests).toContain('/api/connect/info?lane=driver&mode=local')
    connection.state = 'disconnected'
    await render()
    expect(button('Connect this cluster…')).toBeUndefined()
    expect(document.body.textContent).toContain('Continue in Radar Cloud')
    connection.state = 'connected'
    await render()
    await until(() => expect(button('Connect this cluster…')?.disabled).toBe(false))
    expect(requests.filter(path => path === '/api/cloud/install/discover')).toHaveLength(2)
  })

  it('keeps the existing already-connected destination after discovery', async () => {
    connection.state = 'connected'
    discovery.connected = [{ namespace: 'radar', deployment: 'radar', clusterUrl: 'https://cloud.example/clusters/one' }]
    await openDialog()
    await until(() => expect(document.body.textContent).toContain('This cluster is already connected to Radar Cloud.'))
    expect(document.querySelector('a[href="https://cloud.example/clusters/one"]')?.textContent).toBe('Open in Radar Cloud')
    expect(button('Connect this cluster…')).toBeUndefined()
    connection.state = 'disconnected'
    await render()
    expect(document.body.textContent).not.toContain('This cluster is already connected to Radar Cloud.')
    expect(document.body.textContent).toContain('Continue in Radar Cloud')
  })

  it('still allows inspection after discovery fails and preserves 503 handoff attribution', async () => {
    connection.state = 'connected'
    discoveryStatus = 500
    await openDialog()
    await until(() => expect(button('Connect this cluster…')?.disabled).toBe(false))
    await act(async () => { button('Connect this cluster…')!.click() })
    await until(() => expect(button('Try again')).toBeDefined())
    expect(document.querySelector('a[href*="driver-footer-browser-link"]')?.getAttribute('href')).toContain('radar_outcome=radar_not_connected_to_cluster')
    expect(document.body.textContent).toContain('Not connected to cluster')
  })

  it('keeps wizard-only deployments on their existing signup path', async () => {
    lane = 'wizard'
    await openDialog()
    expect(document.querySelector('a[href*="wizard-signup-button"]')?.textContent).toBe('Try Cloud free')
    expect(document.body.textContent).not.toContain('Continue in Radar Cloud')
    expect(requests).not.toContain('/api/cloud/install/discover')
  })

  it('keeps an active server-owned install visible even while disconnected', async () => {
    connection.state = 'disconnected'
    flowState = 'awaiting_approval'
    await openDialog()
    await until(() => expect(document.body.textContent).toContain('Install flow'))
    expect(document.body.textContent).not.toContain('Continue in Radar Cloud')
  })
})
