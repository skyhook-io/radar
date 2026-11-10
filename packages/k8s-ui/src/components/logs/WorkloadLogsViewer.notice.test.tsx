// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { VirtuosoMockContext } from 'react-virtuoso'
import { ToastProvider } from '../ui/Toast'
import { WorkloadLogsViewer } from './WorkloadLogsViewer'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

class FakeEventSource extends EventTarget {
  static last: FakeEventSource | undefined
  constructor(readonly url: string) {
    super()
    FakeEventSource.last = this
  }
  close() {}
  emit(type: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }))
  }
}

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource)
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.unstubAllGlobals()
})

it('clears the unreadable-sources notice when the stream ends', async () => {
  const notice = '1 source could not be read: web-0/app: Kubernetes did not return this container\'s logs'
  await act(async () =>
    root.render(
      <ToastProvider>
        <WorkloadLogsViewer
          name="web"
          fetchAll={async () => ({ pods: [], logs: [] })}
          createStream={() => new EventSource('/stream')}
          autoStream
        />
      </ToastProvider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')

  await act(async () => source.emit('connected', { pods: [{ name: 'web-0', containers: ['app'] }] }))
  await act(async () => source.emit('notice', { notice }))
  expect(element.textContent).toContain(notice)

  await act(async () => source.emit('end', { reason: 'no pods found' }))
  expect(element.textContent).not.toContain(notice)
})

// Renders the viewer streaming from a fake source, with the virtualized list
// mocked so lines render in jsdom.
async function mountViewer() {
  await act(async () =>
    root.render(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 20 }}>
        <ToastProvider>
          <WorkloadLogsViewer name="web" fetchAll={async () => ({ pods: [], logs: [] })} createStream={() => new EventSource('/stream')} autoStream />
        </ToastProvider>
      </VirtuosoMockContext.Provider>,
    ),
  )
  const source = FakeEventSource.last
  if (!source) throw new Error('stream was not opened')
  return source
}
const send = (source: FakeEventSource, type: string, data: unknown) => act(async () => source.emit(type, data))
const line = (pod: string, container: string, content: string) => ({ pod, container, timestamp: '2026-11-08T00:00:00Z', content })
// Lets the buffered lines flush into the list.
const settle = () => act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })

interface Status { name: string; init?: boolean; state: string; reason?: string; started?: boolean; ready?: boolean }
const initContainer = (name: string, state: string, reason?: string): Status => ({ name, init: true, state, reason })
const waitingApp = (name: string): Status => ({ name, state: 'waiting', reason: 'PodInitializing' })
const runningApp = (name: string): Status => ({ name, state: 'running', started: true, ready: true })
const podInfo = (name: string, ...statuses: Status[]) => ({
  name,
  containers: statuses.map(status => status.name),
  ready: statuses.every(status => status.ready),
  containerStatuses: statuses.map(status => ({ ready: false, restartCount: 0, ...status })),
})
const stuckPredictor = (name = 'predictor-abc-xyz') => podInfo(name, initContainer('storage-initializer', 'waiting', 'CrashLoopBackOff'), waitingApp('kserve-container'))

it('names each line\'s container, marks init containers and says why the app has no output', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [stuckPredictor()] })
  await send(source, 'log', line('predictor-abc-xyz', 'storage-initializer', 'Storage initialization failed'))
  await settle()
  expect(element.textContent).toContain("kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
  expect(element.querySelector('[title="Init container storage-initializer"]')?.textContent).toBe('storage-initializerinit')
  const option = [...element.querySelectorAll('option')].find(o => o.value === 'storage-initializer')
  expect(option?.textContent).toBe('storage-initializer (init)')

  await send(source, 'log', line('predictor-abc-xyz', 'kserve-container', 'model server listening'))
  await settle()
  expect(element.textContent).not.toContain("hasn't started")

  await send(source, 'pod_removed', { pod: 'predictor-abc-xyz', reason: 'terminated' })
  await settle()
  expect(element.querySelector('[title="Init container storage-initializer"]')).not.toBeNull()
})

it('keeps a started container out of the notice after the buffer is cleared', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [stuckPredictor()] })
  expect(element.textContent).toContain("hasn't started")
  await send(source, 'log', line('predictor-abc-xyz', 'kserve-container', 'model server listening'))
  await settle()
  const clear = element.querySelector<HTMLButtonElement>('button[aria-label="Clear logs"]')
  if (!clear) throw new Error('clear button not found')
  await act(async () => clear.click())
  expect(element.textContent).not.toContain("hasn't started")
})

it('treats a replaced Pod with the same name as a new incarnation', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [podInfo('web-0', initContainer('migrate', 'terminated', 'Completed'), runningApp('app'))] })
  await send(source, 'log', line('web-0', 'app', 'serving'))
  await settle()
  expect(element.textContent).not.toContain("hasn't started")

  await send(source, 'pod_removed', { pod: 'web-0', reason: 'replaced' })
  await send(source, 'pod_added', { pods: [podInfo('web-0', initContainer('migrate', 'waiting', 'CrashLoopBackOff'), waitingApp('app'))] })
  expect(element.textContent).toContain("app hasn't started: init container migrate is in CrashLoopBackOff.")
})

it('keeps the init marker when a Pod is added, logs and is removed in one batch', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [] })
  await act(async () => {
    source.emit('pod_added', { pods: [podInfo('web-1', initContainer('migrate', 'running'), waitingApp('app'))] })
    source.emit('log', line('web-1', 'migrate', 'migrating'))
    source.emit('pod_removed', { pod: 'web-1', reason: 'terminated' })
  })
  await settle()
  expect(element.querySelector('[title="Init container migrate"]')).not.toBeNull()
})

it('follows fresh Pod status: clears the notice once initialized, and shows it again on re-initialization', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [podInfo('p-0', initContainer('storage-initializer', 'running'), waitingApp('kserve-container'))] })
  expect(element.textContent).toContain("kserve-container hasn't started: init container storage-initializer is still running.")
  await send(source, 'pod_updated', { pods: [podInfo('p-0', initContainer('storage-initializer', 'terminated', 'Completed'), runningApp('kserve-container'))] })
  expect(element.textContent).not.toContain("hasn't started")

  await send(source, 'log', line('p-0', 'kserve-container', 'serving'))
  await settle()
  await send(source, 'pod_updated', { pods: [stuckPredictor('p-0')] })
  expect(element.textContent).toContain("kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
})

it('marks each line by its container\'s role when it arrived, across a same-name replacement', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [podInfo('web-0', initContainer('setup', 'running'), waitingApp('app'))] })
  await send(source, 'log', line('web-0', 'setup', 'old init line'))
  await send(source, 'pod_removed', { pod: 'web-0', reason: 'replaced' })
  await send(source, 'pod_added', { pods: [podInfo('web-0', runningApp('setup'), runningApp('app'))] })
  await send(source, 'log', line('web-0', 'setup', 'new app line'))
  await settle()
  const labels = [...element.querySelectorAll('[title$="ontainer setup"]')].map(label => label.getAttribute('title'))
  expect(labels).toEqual(['Init container setup', 'Container setup'])
})

it('drops the initializing notice when the stream ends with no Pods left', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [podInfo('p-0', initContainer('init', 'running'), waitingApp('app'))] })
  expect(element.textContent).toContain("app hasn't started")
  await send(source, 'end', { reason: 'no pods found' })
  expect(element.textContent).not.toContain("hasn't started")
})

it('hides the previous stream\'s initializing notice while a new stream connects', async () => {
  const first = await mountViewer()
  await send(first, 'connected', { pods: [podInfo('p-0', initContainer('init', 'running'), waitingApp('app'))] })
  expect(element.textContent).toContain("app hasn't started")
  const select = element.querySelector('select') as HTMLSelectElement
  await act(async () => {
    select.value = 'app'
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  expect(FakeEventSource.last).not.toBe(first)
  expect(element.textContent).not.toContain("hasn't started")
})

it('names each line\'s Pod only while the view holds more than one Pod', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [podInfo('web-0', runningApp('app'))] })
  await send(source, 'log', line('web-0', 'app', 'serving'))
  await settle()
  expect(element.textContent).toContain('serving')
  expect(element.querySelector('[title="web-0"]')).toBeNull()

  await send(source, 'pod_added', { pods: [podInfo('web-1', runningApp('app'))] })
  await send(source, 'log', line('web-1', 'app', 'also serving'))
  await settle()
  expect(element.querySelector('[title="web-0"]')?.textContent).toBe('[web-0]')
  expect(element.querySelector('[title="web-1"]')?.textContent).toBe('[web-1]')
})

it('offers a slow init container\'s logs from the status line, since All containers leaves it out', async () => {
  const source = await mountViewer()
  await send(source, 'connected', { pods: [podInfo('p-0', initContainer('storage-initializer', 'running'), waitingApp('kserve-container'))] })
  expect(element.textContent).toContain("kserve-container hasn't started: init container storage-initializer is still running.")
  expect(element.querySelector('[title^="Container "]')).toBeNull()
  const show = [...element.querySelectorAll('button')].find(button => button.textContent === 'Show its logs')
  if (!show) throw new Error('no Show its logs action')
  await act(async () => show.click())
  const picker = [...element.querySelectorAll('select')].find(select => [...select.options].some(option => option.value === 'storage-initializer'))
  expect(picker?.value).toBe('storage-initializer')
})
