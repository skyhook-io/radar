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
const toast = vi.hoisted(() => ({ showApiError: vi.fn() }))
vi.mock('./ui/Toast', () => toast)
vi.mock('./CloudConnectFlow', () => ({
  CloudConnectFlow: ({ status, blocked, onExit }: { status: { state: string; prepareStep?: string }; blocked: unknown; onExit: () => void }) => (
    <div>
      Install flow [{blocked ? 'blocked' : status.state} {status.prepareStep ?? '-'}]
      <button onClick={onExit}>Leave flow</button>
    </div>
  ),
}))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let client: QueryClient
let lane: 'driver' | 'wizard'
let discovery: { connected: { namespace: string; deployment: string; clusterUrl: string }[] }
let discoveryStatus: number
let flowState: 'idle' | 'awaiting_approval'
// When set, /status answers with this body instead of { state: flowState }.
let statusBody: Record<string, unknown> | null
// When set, the prepare POST stays pending until the test settles it.
let prepareReply: { promise: Promise<Response>; resolve: (r: Response) => void; reject: (e: unknown) => void } | null
const requests: string[] = []

beforeEach(() => {
  connection.state = 'connecting'
  lane = 'driver'
  discovery = { connected: [] }
  discoveryStatus = 200
  flowState = 'idle'
  statusBody = null
  prepareReply = null
  toast.showApiError.mockClear()
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
      '/api/cloud/install/status': statusBody ?? { state: flowState },
      '/api/cloud/install/discover': discoveryStatus === 200 ? discovery : { error: 'Discovery failed' },
      '/api/cloud/install/prepare': { error: 'Not connected to cluster' },
      '/api/connect/info': {},
    }
    if (!(url.pathname in bodies)) throw new Error(`Unexpected request: ${input}`)
    if (url.pathname.endsWith('/prepare') && prepareReply) return prepareReply.promise
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

async function until(assertion: () => void, timeout = 1000) {
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    assertion()
  }, { timeout })
}

function holdPrepare() {
  let resolve!: (r: Response) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<Response>((res, rej) => { resolve = res; reject = rej })
  prepareReply = { promise, resolve, reject }
  return prepareReply
}

const statusPolls = () => requests.filter(path => path === '/api/cloud/install/status').length

async function startConnect() {
  connection.state = 'connected'
  await openDialog()
  await until(() => expect(button('Connect this cluster…')?.disabled).toBe(false))
  await act(async () => { button('Connect this cluster…')!.click() })
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
    expect(document.body.textContent).toContain('Radar runs in your cluster and tunnels outward to Radar Cloud')
    expect(document.body.textContent).not.toContain('Setup runs here in the app')
    expect(document.body.textContent).not.toContain('see what gets installed')
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

describe('Cloud connect preparation progress', () => {
  it('polls the server while the prepare request is pending and shows its step', async () => {
    holdPrepare()
    await startConnect()
    await until(() => expect(document.body.textContent).toContain('Install flow [preparing -]'))
    const before = statusPolls()
    statusBody = { flowId: 'f1', state: 'preparing', prepareStep: 'prepare' }
    await until(() => expect(document.body.textContent).toContain('Install flow [preparing prepare]'), 3000)
    expect(statusPolls()).toBeGreaterThan(before)
  })

  it('keeps the last step seen when the server clears a blocked preparation before answering', async () => {
    const reply = holdPrepare()
    await startConnect()
    statusBody = { flowId: 'f1', state: 'preparing', prepareStep: 'preflight' }
    await until(() => expect(document.body.textContent).toContain('Install flow [preparing preflight]'), 3000)
    statusBody = { state: 'idle' }
    const before = statusPolls()
    await until(() => expect(statusPolls()).toBeGreaterThan(before), 3000)
    expect(document.body.textContent).toContain('Install flow [preparing preflight]')

    await act(async () => {
      reply.resolve(new Response(JSON.stringify({ state: 'blocked', blocked: { reason: 'preflight', message: 'No permission' } }), { status: 200 }))
    })
    await until(() => expect(document.body.textContent).toContain('Install flow [blocked -]'))
    const settled = statusPolls()
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 1300)) })
    expect(statusPolls()).toBe(settled)
  })

  it.each(['ready', 'failed'] as const)('stays on a flow the poll saw %s when the prepare request itself is lost', async state => {
    const reply = holdPrepare()
    await startConnect()
    statusBody = { flowId: 'f1', state: 'preparing', prepareStep: 'preflight' }
    await until(() => expect(document.body.textContent).toContain('Install flow [preparing preflight]'), 3000)
    statusBody = { flowId: 'f1', state }
    await until(() => expect(document.body.textContent).toContain(`Install flow [${state} -]`), 3000)
    await act(async () => { reply.reject(new TypeError('Failed to fetch')) })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(document.body.textContent).toContain(`Install flow [${state} -]`)
    expect(toast.showApiError).not.toHaveBeenCalled()
    expect(button('Try again')).toBeUndefined()
  })

  it('keeps a reattached flow that moved on when the retry\'s own answer is lost', async () => {
    // The first request fails while the server is still preparing f1, so the
    // dialog returns to the pitch with f1 cached as preparing.
    const first = holdPrepare()
    await startConnect()
    statusBody = { flowId: 'f1', state: 'preparing', prepareStep: 'inspect' }
    await until(() => expect(document.body.textContent).toContain('Install flow [preparing inspect]'), 3000)
    await act(async () => { first.reject(new TypeError('Failed to fetch')) })
    await until(() => expect(button('Try again')).toBeDefined())
    // The retry attaches to the same flow; it reaches ready, and this answer
    // is lost too. Same flow id, but a state seen only after the retry began.
    const retry = holdPrepare()
    await act(async () => { button('Try again')!.click() })
    statusBody = { flowId: 'f1', state: 'ready' }
    await until(() => expect(document.body.textContent).toContain('Install flow [ready -]'), 3000)
    toast.showApiError.mockClear()
    await act(async () => { retry.reject(new TypeError('Failed to fetch')) })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(document.body.textContent).toContain('Install flow [ready -]')
    expect(toast.showApiError).not.toHaveBeenCalled()
  })

  it('reports a failed attempt even when an older flow arrived in the status cache after the click', async () => {
    const reply = holdPrepare()
    await startConnect()
    // The status fetched as the dialog opened answers late, with a failed
    // flow from an earlier session that this attempt never saw preparing.
    statusBody = { flowId: 'f0', state: 'failed' }
    await until(() => expect(document.body.textContent).toContain('Install flow [failed -]'), 3000)
    await act(async () => { reply.reject(new TypeError('Failed to fetch')) })
    await until(() => expect(toast.showApiError).toHaveBeenCalled())
    expect(button('Try again')).toBeDefined()
  })

  it('ignores a late answer from an earlier attempt once a new one has started', async () => {
    const first = holdPrepare()
    await startConnect()
    statusBody = { flowId: 'f1', state: 'preparing', prepareStep: 'inspect' }
    await until(() => expect(document.body.textContent).toContain('Install flow [preparing inspect]'), 3000)
    statusBody = { flowId: 'f1', state: 'ready' }
    await until(() => expect(document.body.textContent).toContain('Install flow [ready -]'), 3000)
    // The person leaves the first plan and connects again while the first
    // request still has not answered.
    statusBody = { state: 'idle' }
    await act(async () => { button('Leave flow')!.click() })
    await until(() => expect(button('Connect this cluster…')?.disabled).toBe(false), 3000)
    holdPrepare()
    await act(async () => { button('Connect this cluster…')!.click() })
    statusBody = { flowId: 'f2', state: 'preparing', prepareStep: 'prepare' }
    await until(() => expect(document.body.textContent).toContain('Install flow [preparing prepare]'), 3000)

    await act(async () => { first.reject(new TypeError('Failed to fetch')) })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(document.body.textContent).toContain('Install flow [preparing prepare]')
    expect(toast.showApiError).not.toHaveBeenCalled()
  })
})
