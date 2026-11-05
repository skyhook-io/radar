// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { WorkloadLogsViewer, type WorkloadLogsResult } from './WorkloadLogsViewer'
vi.mock('../ui/Toast', () => ({ useToast: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
it('uses a host empty state and disables source controls until a source is read', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  let result: WorkloadLogsResult = { pods: [], logs: [], emptyMessage: 'No instance Pods yet' }
  const stream = vi.fn()
  const fetchAll = vi.fn(async () => result)
  await act(async () => root.render(<WorkloadLogsViewer name="analytics" fetchAll={fetchAll} createStream={stream} disableSourceControlsWithoutSource emptySourceState={<><p>The first instance cannot start.</p><a href="?tab=overview">Overview’s problem</a></>} />))
  expect(host.textContent).toContain('The first instance cannot start')
  expect(host.querySelector('a')?.getAttribute('href')).toBe('?tab=overview')
  expect(host.querySelector('fieldset')?.disabled).toBe(true)
  expect([...host.querySelectorAll('select')].every((el) => el.matches(':disabled'))).toBe(true)
  const streamButton = [...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.textContent === 'Stream')!
  expect(streamButton.disabled).toBe(true)
  act(() => streamButton.click())
  expect(stream).not.toHaveBeenCalled()
  result = { pods: [{ name: 'analytics-1', containers: ['postgres'], ready: true }], logs: [] }
  await act(async () => [...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.querySelector('.lucide-rotate-ccw'))!.click())
  expect(host.querySelector('fieldset')?.disabled).toBe(false)
  expect(streamButton.disabled).toBe(false)
  expect(host.textContent).not.toContain('The first instance cannot start')
  await act(async () => root.unmount())
})

it('keeps streaming available for hosts that wait for Pods to appear', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  await act(async () => root.render(<WorkloadLogsViewer name="pending-job" fetchAll={async () => ({ pods: [], logs: [] })} createStream={vi.fn()} />))
  expect([...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.textContent === 'Stream')?.disabled).toBe(false)
  expect(host.querySelector('fieldset')?.disabled).toBe(false)
  await act(async () => root.unmount())
})


it('keeps the all-container choice available by default', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async () => ({ pods: [{ name: 'analytics-1', containers: ['postgres', 'plugin-barman-cloud'], ready: true }], logs: [] }))
  await act(async () => root.render(<WorkloadLogsViewer name="analytics" fetchAll={fetchAll} />))
  const select = host.querySelector<HTMLSelectElement>('select')!
  expect([...select.options].map((option) => option.value)).toEqual(['', 'postgres', 'plugin-barman-cloud'])
  expect(select.options[0].text).toBe('All containers')
  expect(fetchAll).toHaveBeenCalledWith(expect.objectContaining({ container: undefined }))
  await act(async () => root.unmount())
})

it('offers only named containers when the backend cannot read all containers', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async (_params: unknown) => ({ pods: [{ name: 'analytics-1', containers: ['postgres', 'plugin-barman-cloud'], ready: true }], logs: [] }))
  await act(async () => root.render(<WorkloadLogsViewer name="analytics" fetchAll={fetchAll} initialContainer="postgres" allowAllContainers={false} />))
  const select = host.querySelector<HTMLSelectElement>('select')!
  expect([...select.options].map((option) => option.value)).toEqual(['postgres', 'plugin-barman-cloud'])
  expect(host.textContent).not.toContain('All containers')
  expect(select.value).toBe('postgres')
  expect(fetchAll).toHaveBeenCalledWith(expect.objectContaining({ container: 'postgres' }))
  await act(async () => {
    select.value = 'plugin-barman-cloud'
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  expect(fetchAll).toHaveBeenLastCalledWith(expect.objectContaining({ container: 'plugin-barman-cloud' }))
  await act(async () => root.unmount())
})

function buttonNamed(host: HTMLElement, name: string) {
  return [...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.textContent === name)
}

const healthyPod = { name: 'api-1', containers: ['app'], ready: true, healthLevel: 'healthy' as const }
const failingPod = { name: 'api-2', containers: ['app'], ready: false, healthLevel: 'unhealthy' as const }

it('shows no shortcuts unless the host asks for them', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  await act(async () => root.render(<WorkloadLogsViewer name="api" fetchAll={async () => ({ pods: [healthyPod, failingPod], logs: [] })} />))
  expect(buttonNamed(host, 'Failing pods only')).toBeUndefined()
  expect(buttonNamed(host, 'Since last deploy')).toBeUndefined()
  await act(async () => root.unmount())
})

it('narrows to failing pods and back', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = async () => ({
    pods: [healthyPod, failingPod],
    logs: [
      { pod: 'api-1', container: 'app', timestamp: '2026-11-05T10:00:00Z', content: 'all fine' },
      { pod: 'api-2', container: 'app', timestamp: '2026-11-05T10:00:01Z', content: 'it broke' },
    ],
  })
  await act(async () => root.render(<WorkloadLogsViewer name="api" fetchAll={fetchAll} shortcuts={{}} />))
  expect(host.textContent).toContain('2/2 pods')

  await act(async () => buttonNamed(host, 'Failing pods only')!.click())
  expect(host.textContent).toContain('1/2 pods')
  expect(buttonNamed(host, 'Failing pods only')!.getAttribute('aria-pressed')).toBe('true')

  await act(async () => buttonNamed(host, 'Failing pods only')!.click())
  expect(host.textContent).toContain('2/2 pods')
  await act(async () => root.unmount())
})

it('hides the failing shortcut when it would not change anything', async () => {
  for (const pods of [[healthyPod], [failingPod], [{ ...healthyPod, healthLevel: undefined }, failingPod].slice(1)]) {
    const host = document.createElement('div'); const root = createRoot(host)
    await act(async () => root.render(<WorkloadLogsViewer name="api" fetchAll={async () => ({ pods, logs: [] })} shortcuts={{}} />))
    expect(buttonNamed(host, 'Failing pods only')).toBeUndefined()
    await act(async () => root.unmount())
  }
})

it('reads logs since the last deploy, and back', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async (_params: { sinceSeconds?: number; tailLines?: number }) => ({ pods: [healthyPod], logs: [] }))
  const lastDeployAt = new Date(Date.now() - 600_000).toISOString()
  await act(async () => root.render(<WorkloadLogsViewer name="api" fetchAll={fetchAll} shortcuts={{ lastDeployAt }} />))

  await act(async () => buttonNamed(host, 'Since last deploy')!.click())
  const { sinceSeconds } = fetchAll.mock.lastCall![0]
  expect(sinceSeconds).toBeGreaterThanOrEqual(600)
  expect(sinceSeconds).toBeLessThan(620)
  expect(host.textContent).toContain('Since last deploy')

  await act(async () => buttonNamed(host, 'Since last deploy')!.click())
  expect(fetchAll.mock.lastCall![0]).toMatchObject({ tailLines: 100, sinceSeconds: undefined })
  await act(async () => root.unmount())
})

it('hides Since last deploy with no usable time', async () => {
  for (const lastDeployAt of [undefined, 'not a date', new Date(Date.now() + 60_000).toISOString()]) {
    const host = document.createElement('div'); const root = createRoot(host)
    await act(async () => root.render(<WorkloadLogsViewer name="api" fetchAll={async () => ({ pods: [healthyPod], logs: [] })} shortcuts={{ lastDeployAt }} />))
    expect(buttonNamed(host, 'Since last deploy')).toBeUndefined()
    await act(async () => root.unmount())
  }
})

it('reopens a running stream with the new range', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const listeners = new Map<string, (event: { data: string }) => void>()
  const createStream = vi.fn((_params: { sinceSeconds?: number }) => ({
    addEventListener: (type: string, listener: (event: { data: string }) => void) => listeners.set(type, listener),
    close: vi.fn(),
  }) as unknown as EventSource)
  const lastDeployAt = new Date(Date.now() - 600_000).toISOString()
  await act(async () => root.render(<WorkloadLogsViewer name="api" fetchAll={async () => ({ pods: [], logs: [] })} createStream={createStream} autoStream shortcuts={{ lastDeployAt }} />))
  await act(async () => listeners.get('connected')!({ data: JSON.stringify({ pods: [healthyPod] }) }))
  expect(createStream).toHaveBeenCalledTimes(1)
  expect(createStream.mock.lastCall![0].sinceSeconds).toBeUndefined()

  await act(async () => buttonNamed(host, 'Since last deploy')!.click())
  expect(createStream).toHaveBeenCalledTimes(2)
  expect(createStream.mock.lastCall![0].sinceSeconds).toBeGreaterThanOrEqual(600)
  await act(async () => root.unmount())
})
