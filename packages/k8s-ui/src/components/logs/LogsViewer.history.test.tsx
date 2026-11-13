// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { ToastProvider } from '../ui/Toast'
import { LogsViewer, type LogsHistoryResult, type LogsViewerProps } from './LogsViewer'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root | undefined
let element: HTMLDivElement

afterEach(() => {
  act(() => root?.unmount())
  element?.remove()
})

async function render(props: Partial<LogsViewerProps>) {
  element = document.createElement('div')
  document.body.append(element)
  root = createRoot(element)
  await act(async () => {
    root!.render(
      <ToastProvider>
        <LogsViewer namespace="team" podName="app-1" containers={['web']} fetchLogs={async () => ({ web: '2026-11-13T10:00:00Z live line' })} createStream={() => new EventSource('x')} {...props} />
      </ToastProvider>,
    )
  })
  await act(async () => { await new Promise(r => setTimeout(r, 150)) })
}

const result = (patch: Partial<LogsHistoryResult> = {}): LogsHistoryResult => ({
  containers: ['web'], logs: { web: '2026-11-13T09:00:00Z history line\n' }, truncated: false, limit: 5000,
  window: { start: '2026-11-13T04:00:00Z', end: '2026-11-13T10:00:00Z' }, ...patch,
})

const button = (name: string) => [...element.querySelectorAll('button')].find(b => b.textContent?.trim() === name)

it('shows no History switch without fetchHistory', async () => {
  await render({})
  expect(button('History')).toBeUndefined()
})

it('reads History from Loki with the selected container and turns off streaming', async () => {
  const fetchHistory = vi.fn(async () => result())
  await render({ fetchHistory })
  await act(async () => { button('History')!.click() })
  await act(async () => { await new Promise(r => setTimeout(r, 150)) })
  expect(fetchHistory).toHaveBeenCalledWith({ container: 'web', since: '6h' })
  // jsdom has no layout, so the virtual list renders no rows; the level counter shows the line arrived.
  expect(element.textContent).toContain('OTHER 1')
  expect(element.textContent).toContain('From Loki')
  expect(element.textContent).not.toContain('Previous')
  expect(element.querySelector('[aria-label="Start streaming"], [title="Start streaming"]')).toBeNull()
})

it('says when Loki cut the result', async () => {
  await render({ fetchHistory: async () => result({ truncated: true }), initialMode: 'history' })
  expect(element.textContent).toContain('the newest 5,000 lines (the limit')
})

it('says why History may be empty', async () => {
  await render({ fetchHistory: async () => result({ containers: [], logs: {} }), initialMode: 'history', historyEmptyHint: <span>See Log shipping</span> })
  expect(element.textContent).toContain("Loki has no lines for this pod in this window. If the pod's logs aren't shipped to Loki, they were never stored.")
  expect(element.textContent).toContain('See Log shipping')
})

it('takes the containers of a gone pod from the History result', async () => {
  const fetchHistory = vi.fn(async () => result({ containers: ['sidecar', 'web'], logs: { sidecar: '2026-11-13T09:00:00Z side\n', web: '2026-11-13T09:00:01Z main\n' } }))
  await render({ containers: [], fetchHistory, initialMode: 'history' })
  expect(fetchHistory).toHaveBeenCalledWith({ container: undefined, since: '6h' })
  const options = [...element.querySelectorAll('option')].map(o => o.textContent)
  expect(options).toContain('sidecar')
  expect(options).toContain('web')
})

it('opens History once the host can read it, when asked to start there', async () => {
  const fetchHistory = vi.fn(async () => result())
  await render({ initialMode: 'history' })
  expect(fetchHistory).not.toHaveBeenCalled()
  await act(async () => {
    root!.render(
      <ToastProvider>
        <LogsViewer namespace="team" podName="app-1" containers={['web']} fetchLogs={async () => ({})} initialMode="history" fetchHistory={fetchHistory} />
      </ToastProvider>,
    )
  })
  await act(async () => { await new Promise(r => setTimeout(r, 150)) })
  expect(fetchHistory).toHaveBeenCalled()
})

it('starts History on the window the host asks for', async () => {
  const fetchHistory = vi.fn(async () => result())
  await render({ containers: [], fetchHistory, initialMode: 'history', initialHistoryWindow: '7d' })
  expect(fetchHistory).toHaveBeenCalledWith({ container: undefined, since: '7d' })
})

it('keeps learning a gone pod\'s containers as the window widens', async () => {
  let call = 0
  const fetchHistory = vi.fn(async () => (++call === 1
    ? result({ containers: ['web'], logs: { web: '2026-11-13T09:00:00.000000000Z a\n' } })
    : result({ containers: ['sidecar', 'web'], logs: { sidecar: '2026-11-13T08:00:00.000000000Z b\n', web: '2026-11-13T09:00:00.000000000Z a\n' } })))
  await render({ containers: [], fetchHistory, initialMode: 'history', initialHistoryWindow: '1h' })
  const windowSelect = [...element.querySelectorAll('select')].find(s => [...s.options].some(o => o.value === '7d'))!
  await act(async () => {
    windowSelect.value = '7d'
    windowSelect.dispatchEvent(new Event('change', { bubbles: true }))
  })
  await act(async () => { await new Promise(r => setTimeout(r, 150)) })
  expect([...element.querySelectorAll('option')].map(o => o.textContent)).toContain('sidecar')
})

it('shows whole-pod lines when Loki has no container label, even with a container selected', async () => {
  const fetchHistory = vi.fn(async () => result({ containers: ['(all containers)'], logs: { '(all containers)': '2026-11-13T09:00:00Z whole pod\n' } }))
  await render({ fetchHistory, initialMode: 'history' })
  expect(fetchHistory).toHaveBeenCalledWith({ container: 'web', since: '6h' })
  expect(element.textContent).toContain('OTHER 1')
  expect(element.textContent).toContain('all containers (Loki has no container label)')
})

it('streams when the user turns a History tab to Live, but not before History is ready', async () => {
  const createStream = vi.fn(() => ({ close() {}, addEventListener() {}, removeEventListener() {} }) as unknown as EventSource)
  await render({ autoStream: true, createStream, initialMode: 'history' })
  expect(createStream).not.toHaveBeenCalled() // waiting for the host to be able to read History
  await act(async () => {
    root!.render(
      <ToastProvider>
        <LogsViewer namespace="team" podName="app-1" containers={['web']} fetchLogs={async () => ({})} createStream={createStream} autoStream initialMode="history" fetchHistory={async () => result()} />
      </ToastProvider>,
    )
  })
  await act(async () => { button('Live')!.click() })
  await act(async () => { await new Promise(r => setTimeout(r, 150)) })
  expect(createStream).toHaveBeenCalled()
})
