// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LiveDebugSection } from './LiveDebugSection'
import { RadarFeatureUnsupportedError } from '../../../api/radarFeatures'

const mock = vi.hoisted(() => ({
  mode: 'local',
  apiBase: '/api', context: 'test',
  status: { data: { state: 'installed' }, isLoading: false, error: undefined } as any,
  start: vi.fn(), stop: vi.fn(), fetch: vi.fn(), query: vi.fn(),
}))
vi.mock('@tanstack/react-query', () => ({ useQuery: (options: any) => { mock.query(options); return mock.status } }))
vi.mock('../../../api/client', () => ({ ApiError: class extends Error { status = 0 }, useRadarFeature: () => ({ gatedKey: [], guard: (request: () => unknown) => request() }) }))
vi.mock('../../../api/config', () => ({ getApiBase: () => mock.apiBase }))
vi.mock('../../../contexts/CapabilitiesContext', () => ({ useCapabilitiesContext: () => ({ deployment: { mode: mock.mode } }) }))
vi.mock('../../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: mock.context } }) }))
vi.mock('../../../api/igdebug', () => ({
  fetchIGStatus: vi.fn(), fetchIGRun: (...args: unknown[]) => mock.fetch(...args),
  startIGCapture: (...args: unknown[]) => mock.start(...args),
  stopIGRun: (...args: unknown[]) => mock.stop(...args),
}))
vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const target = { namespace: 'app', pod: 'api', container: 'app', podUid: 'old', node: 'node-a', containerId: '123' }
const pod = (uid = 'old', containerID = '123') => ({
  metadata: { namespace: 'app', name: 'api', uid },
  spec: { containers: [{ name: 'app' }] },
  status: { containerStatuses: [{ name: 'app', containerID, state: { running: {} } }] },
})
const result = { id: 'run-1', target, state: 'complete', aspect: 'processes', result: {
  target, aspect: 'processes', processes: [{ command: 'old-process', pid: 1 }],
  scope: 'container', capturedAt: '2026-10-13T00:00:00Z', eventCount: 1, eventLoss: 'unmeasurable', truncated: false, captureDurationMs: 0,
} }
let element: HTMLDivElement
let root: Root
beforeEach(() => {
  mock.mode = 'local'
  mock.apiBase = '/api'; mock.context = 'test'
  mock.status = { data: { state: 'installed' }, isLoading: false }
  mock.start.mockReset(); mock.fetch.mockReset(); mock.stop.mockReset(); mock.stop.mockResolvedValue({}); mock.query.mockClear()
  element = document.createElement('div'); root = createRoot(element)
})
afterEach(async () => { await act(async () => root.unmount()) })
const expand = () => Array.from(element.querySelectorAll('button')).find(button => button.textContent?.includes('Live Debug'))!.click()
const capture = () => Array.from(element.querySelectorAll('button')).find(button => button.textContent?.includes('Capture'))!

describe('Live Debug capture identity', () => {
  it('blocks duplicate submissions while the create request is pending', async () => {
    let resolve!: (run: any) => void
    mock.start.mockReturnValue(new Promise(done => { resolve = done }))
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    await act(async () => { capture().click(); capture().click() })
    expect(mock.start).toHaveBeenCalledTimes(1)
    expect(capture().disabled).toBe(true)
    await act(async () => resolve(result))
    expect(element.textContent).toContain('old-process')
    expect(element.textContent).toContain('Pod UID: old')
    expect(element.textContent).toContain('Container ID: 123')
  })

  it.each(['pod', 'container'])('ignores a capture response after the %s is replaced', async replacement => {
    let resolve!: (run: any) => void
    mock.start.mockReturnValue(new Promise(done => { resolve = done }))
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    await act(async () => capture().click())
    await act(async () => root.render(<LiveDebugSection data={replacement === 'pod' ? pod('new') : pod('old', '456')} />))
    await act(async () => resolve(result))
    expect(element.textContent).not.toContain('old-process')
    expect(mock.stop).toHaveBeenCalledWith('run-1')
    expect(capture().disabled).toBe(false)
    if (replacement === 'container') expect(element.textContent).toContain('The container restarted while the capture request was pending')
  })

  it('keeps the replacement capture pending when the old Pod response arrives', async () => {
    let oldResponse!: (run: typeof result) => void
    let newResponse!: (run: typeof result) => void
    mock.start.mockReturnValueOnce(new Promise(done => { oldResponse = done }))
      .mockReturnValueOnce(new Promise(done => { newResponse = done }))
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    await act(async () => capture().click())
    await act(async () => root.render(<LiveDebugSection data={pod('new', '456')} />))
    expect(capture().disabled).toBe(false)
    await act(async () => capture().click())
    expect(mock.start).toHaveBeenCalledTimes(2)
    await act(async () => oldResponse(result))
    expect(mock.stop).toHaveBeenCalledWith('run-1')
    expect(element.textContent).not.toContain('old-process')
    expect(capture().disabled).toBe(true)
    const next = { ...result, id: 'run-2', target: { ...target, podUid: 'new', containerId: '456' }, result: { ...result.result, target: { ...target, podUid: 'new', containerId: '456' }, processes: [{ command: 'new-process', pid: 2 }] } }
    await act(async () => newResponse(next))
    expect(element.textContent).toContain('new-process')
    expect(element.textContent).not.toContain('old-process')
    expect(mock.stop).not.toHaveBeenCalledWith('run-2')
    const again = Array.from(element.querySelectorAll('button')).find(button => button.textContent?.includes('Run again'))!
    expect(again.disabled).toBe(false)
  })

  it.each(['pod', 'container'])('retains a partial capture and the chosen container after the %s restarts', async replacement => {
    let resolve!: (run: any) => void
    mock.start.mockResolvedValue({ id: 'run-1', target, aspect: 'processes', state: 'running' })
    mock.fetch.mockReturnValue(new Promise(done => { resolve = done }))
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    await act(async () => capture().click())
    await act(async () => root.render(<LiveDebugSection data={replacement === 'pod' ? pod('new', '456') : pod('old', '456')} />))
    expect(mock.stop).not.toHaveBeenCalled()
    await act(async () => resolve({ ...result, state: 'partial', reason: 'target container was replaced' }))
    expect(element.textContent).toContain('target container was replaced')
    expect(element.textContent).toContain('These results refer to the displayed Pod UID and container ID')
    expect(element.textContent).toContain('old-process')
  })

  it.each(['pod name', 'namespace', 'API base', 'context'])('resets completed evidence when the %s changes', async change => {
    mock.start.mockResolvedValue(result)
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    await act(async () => capture().click())
    expect(element.textContent).toContain('old-process')
    const next = pod()
    if (change === 'pod name') next.metadata.name = 'other'
    if (change === 'namespace') next.metadata.namespace = 'other'
    if (change === 'API base') mock.apiBase = '/other/api'
    if (change === 'context') mock.context = 'other'
    await act(async () => root.render(<LiveDebugSection data={next} />))
    expect(element.textContent).not.toContain('old-process')
    expect(mock.stop).not.toHaveBeenCalled()
    await act(async () => expand())
    expect(capture().disabled).toBe(false)
  })

  it.each(['pod name', 'API base', 'context', 'unmount'])('handles a late capture response after %s without crossing connections', async change => {
    let resolve!: (run: typeof result) => void
    mock.start.mockReturnValue(new Promise(done => { resolve = done }))
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    await act(async () => capture().click())
    const next = pod()
    if (change === 'pod name') next.metadata.name = 'other'
    if (change === 'API base') mock.apiBase = '/other/api'
    if (change === 'context') mock.context = 'other'
    await act(async () => root.render(change === 'unmount' ? null : <LiveDebugSection data={next} />))
    await act(async () => resolve(result))
    expect(element.textContent).not.toContain('old-process')
    if (change === 'pod name' || change === 'unmount') expect(mock.stop).toHaveBeenCalledWith('run-1')
    else expect(mock.stop).not.toHaveBeenCalled()
  })

  it.each(['pod name', 'API base', 'context', 'unmount'])('cancels a known active run after %s only on its original connection', async change => {
    mock.start.mockResolvedValue({ ...result, state: 'running', result: undefined })
    mock.fetch.mockReturnValue(new Promise(() => {}))
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    await act(async () => capture().click())
    const signal = mock.fetch.mock.calls[0][1] as AbortSignal
    const next = pod()
    if (change === 'pod name') next.metadata.name = 'other'
    if (change === 'API base') mock.apiBase = '/other/api'
    if (change === 'context') mock.context = 'other'
    await act(async () => root.render(change === 'unmount' ? null : <LiveDebugSection data={next} />))
    expect(signal.aborted).toBe(true)
    if (change === 'pod name' || change === 'unmount') expect(mock.stop).toHaveBeenCalledWith('run-1')
    else expect(mock.stop).not.toHaveBeenCalled()
  })

  it('keeps a chosen container through a restart and resets evidence when selecting another', async () => {
    const multi = { ...pod(), spec: { containers: [{ name: 'app' }, { name: 'sidecar' }] } }
    mock.start.mockResolvedValue(result)
    await act(async () => root.render(<LiveDebugSection data={multi} />))
    await act(async () => expand())
    await act(async () => capture().click())
    const restarted = { ...multi, status: { containerStatuses: [{ name: 'app', containerID: 'containerd://999', state: { waiting: {} } }, { name: 'sidecar', containerID: '456', state: { running: {} } }] } }
    await act(async () => root.render(<LiveDebugSection data={restarted} />))
    expect(element.querySelector('select')?.value).toBe('app')
    expect(element.textContent).toContain('old-process')
    expect(element.textContent).toContain('These results refer to the displayed Pod UID and container ID')
    await act(async () => {
      const select = element.querySelector('select')!
      select.value = 'sidecar'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    expect(element.textContent).not.toContain('old-process')
    expect(element.querySelector('button')?.getAttribute('aria-expanded')).toBe('true')
    expect(element.querySelector('select')?.value).toBe('sidecar')
    expect(capture().disabled).toBe(false)
  })

  it('resets the selected container to the new Pod default', async () => {
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    const next = { ...pod(), metadata: { ...pod().metadata, name: 'other' }, spec: { containers: [{ name: 'sidecar' }] }, status: { containerStatuses: [{ name: 'sidecar', containerID: '456', state: { running: {} } }] } }
    mock.start.mockResolvedValue({ ...result, target: { ...target, container: 'sidecar' } })
    await act(async () => root.render(<LiveDebugSection data={next} />))
    await act(async () => expand())
    await act(async () => capture().click())
    expect(mock.start).toHaveBeenCalledWith(expect.objectContaining({ pod: 'other', container: 'sidecar', containerId: '456' }))
  })

  it('retains the selected capture aspect when changing containers', async () => {
    const multi = { ...pod(), spec: { containers: [{ name: 'app' }, { name: 'sidecar' }] }, status: { containerStatuses: [...pod().status.containerStatuses, { name: 'sidecar', containerID: '456', state: { running: {} } }] } }
    mock.start.mockResolvedValue(result)
    await act(async () => root.render(<LiveDebugSection data={multi} />))
    await act(async () => expand())
    await act(async () => Array.from(element.querySelectorAll('button')).find(button => button.textContent === 'DNS')!.click())
    await act(async () => {
      const duration = element.querySelectorAll('select')[1]
      duration.value = '30'
      duration.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await act(async () => {
      const select = element.querySelector('select')!
      select.value = 'sidecar'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await act(async () => capture().click())
    expect(mock.start).toHaveBeenCalledWith(expect.objectContaining({ aspect: 'dns', container: 'sidecar', durationSeconds: 30 }))
  })

  it('cancels a known run when its selected container disappears', async () => {
    mock.start.mockResolvedValue({ ...result, state: 'running', result: undefined })
    mock.fetch.mockReturnValue(new Promise(() => {}))
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    await act(async () => expand())
    await act(async () => capture().click())
    const next = { ...pod(), spec: { containers: [{ name: 'sidecar' }] }, status: { containerStatuses: [{ name: 'sidecar', containerID: '456', state: { running: {} } }] } }
    await act(async () => root.render(<LiveDebugSection data={next} />))
    expect(mock.stop).toHaveBeenCalledWith('run-1')
    expect(capture().disabled).toBe(false)
  })

  it('enables Capture when a container starts after the drawer opened', async () => {
    const waiting = pod()
    waiting.status.containerStatuses = []
    await act(async () => root.render(<LiveDebugSection data={waiting} />))
    await act(async () => expand())
    expect(capture().disabled).toBe(true)
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    expect(capture().disabled).toBe(false)
  })

  it('hides unavailable instrumentation on a Cloud connection even when the backend is older', async () => {
    mock.mode = 'cloud'
    mock.status = { error: new RadarFeatureUnsupportedError('liveDebug', { currentVersion: 'v1.16.0' }) }
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    expect(element.textContent).toBe('')
    expect(mock.query.mock.calls.at(-1)?.[0].enabled).toBe(false)
  })

  it('renders an upgrade note for an older backend, and hides an absent installation', async () => {
    mock.status = { error: new RadarFeatureUnsupportedError('liveDebug', { currentVersion: 'v1.16.0' }) }
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    expect(element.textContent).toContain('Live Debug captures need a newer Radar')
    expect(element.querySelector('button')?.getAttribute('aria-expanded')).toBe('false')
    expect(element.textContent).not.toContain('could not be verified')
    mock.status = { data: { state: 'not-installed' } }
    await act(async () => root.render(<LiveDebugSection data={pod()} />))
    expect(element.textContent).toBe('')
  })
})
