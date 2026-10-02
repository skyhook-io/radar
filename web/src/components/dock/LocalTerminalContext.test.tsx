// @vitest-environment jsdom
import { act } from 'react'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { DockProvider, useDock, useOpenLocalTerminal as useSharedOpenLocalTerminal } from '../../../../packages/k8s-ui/src/components/dock/DockContext'
import { useOpenLocalTerminal } from './DockContext'
import { LocalTerminalTab } from './LocalTerminalTab'
import { ToastProvider } from '../../../../packages/k8s-ui/src/components/ui/Toast'

const state = vi.hoisted(() => ({ context: 'A', status: 'connected', mountTabs: true, open: null as null | (() => void) }))
vi.mock('../../context/ConnectionContext', () => ({
  useConnection: () => ({ connection: { context: state.context, state: state.status } }),
}))
vi.mock('@skyhook-io/k8s-ui', async () => ({
  ...await import('../../../../packages/k8s-ui/src/components/dock/DockContext'),
  ...await import('../../../../packages/k8s-ui/src/components/dock/LocalTerminalTab'),
  ...await import('../../../../packages/k8s-ui/src/components/ui/ClusterName'),
  ...await import('../../../../packages/k8s-ui/src/components/ui/Tooltip'),
  ...await import('../../../../packages/k8s-ui/src/components/ui/Toast'),
  ...await import('../../../../packages/k8s-ui/src/utils/context-name'),
}))
vi.mock('../../../../packages/k8s-ui/src/components/dock/terminalClipboard', () => ({ setupTerminalClipboard: () => () => {}, copyTerminalSelection: vi.fn() }))
vi.mock('../../../../packages/k8s-ui/src/components/dock/TerminalClipboardToolbar', () => ({ TerminalClipboardToolbar: () => null }))
vi.mock('../../../../packages/k8s-ui/src/components/dock/useMultilinePasteConfirm', () => ({ useMultilinePasteConfirm: () => ({ confirmPaste: vi.fn(), pasteDialog: null }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const { Terminal } = createRequire(resolve(__dirname, '../../../../packages/k8s-ui/package.json'))('@xterm/xterm')

class Socket {
  static OPEN = 1
  static instances: Socket[] = []
  readyState = 0
  onopen?: () => void
  onclose?: () => void
  onerror?: () => void
  onmessage?: (event: { data: string }) => void
  send = vi.fn()
  constructor(public url: string) { Socket.instances.push(this) }
  open() { this.readyState = 1; this.onopen?.() }
  close() { this.readyState = 3; this.onclose?.() }
  session(context: string) {
    this.onmessage?.({ data: JSON.stringify({ type: 'session', context, kubeconfigIsolated: true }) })
  }
}
let root: Root
let element: HTMLDivElement
let client: QueryClient
function Harness() {
  const { tabs } = useDock()
  const open = useOpenLocalTerminal()
  const sharedOpen = useSharedOpenLocalTerminal()
  state.open ??= () => open({ title: 'Auth', initialCommand: 'auth-marker' })
  return <>
    <button onClick={() => open({ title: 'Auth', initialCommand: 'auth-marker' })}>Open</button>
    <button onClick={() => sharedOpen()}>Open shared</button>
    {state.mountTabs && tabs.map(tab => <section key={tab.id}>
      <h2>{tab.title}</h2>
      <LocalTerminalTab tabId={tab.id} title={tab.title} intendedContext={tab.localTerminalContext} initialCommand={tab.initialCommand} />
    </section>)}
  </>
}
async function render() {
  await act(async () => root.render(<QueryClientProvider client={client}><ToastProvider><DockProvider><Harness /></DockProvider></ToastProvider></QueryClientProvider>))
}
async function click(text: string, section: ParentNode = element) {
  await act(async () => [...section.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === text)!.click())
}
beforeEach(() => {
  vi.spyOn(Terminal.prototype, 'open').mockImplementation(() => {})
  state.context = 'A'
  state.status = 'connected'
  state.mountTabs = true
  state.open = null
  Socket.instances = []
  vi.useFakeTimers()
  vi.stubGlobal('WebSocket', Socket)
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.stubGlobal('requestAnimationFrame', () => 0)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  client.clear()
  element.remove()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('captures A at the action before mounting, and keeps first delivery pending while B is selected', async () => {
  state.mountTabs = false
  await render()
  await click('Open')
  state.context = 'B'
  state.mountTabs = true
  await render()
  expect(Socket.instances).toHaveLength(0)
  expect(element.textContent).toContain('Requested:A')
  expect(element.querySelector('button[aria-label="New terminal for B"]')).not.toBeNull()
  expect([...element.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Reconnect')?.disabled).toBe(true)
  expect(element.textContent).toContain('Different context')
  state.context = 'A'
  await render()
  await click('Reconnect')
  expect(new URL(Socket.instances[0].url).searchParams.get('expectedContext')).toBe('A')
  await act(async () => { Socket.instances[0].open(); Socket.instances[0].session('A'); vi.advanceTimersByTime(300) })
  expect(Socket.instances[0].send.mock.calls.filter(([data]) => JSON.parse(data).type === 'input')).toEqual([[JSON.stringify({ type: 'input', data: 'auth-marker\n' })]])
})

it('preserves A through a refused reconnect, creates B independently, and reconnects A without replay', async () => {
  await render()
  await click('Open')
  await act(async () => { Socket.instances[0].open(); Socket.instances[0].session('A'); vi.advanceTimersByTime(300) })
  state.context = 'B'
  await render()
  expect(Socket.instances[0].readyState).toBe(1)
  await act(async () => Socket.instances[0].close())
  await click('Reconnect')
  expect(Socket.instances).toHaveLength(1)
  expect(element.querySelector('h2')?.textContent).toBe('Auth · A')
  await click('New terminal')
  expect(new URL(Socket.instances[1].url).searchParams.get('expectedContext')).toBe('B')
  await act(async () => { Socket.instances[1].open(); Socket.instances[1].session('B') })
  expect([...element.querySelectorAll('h2')].map(e => e.textContent)).toEqual(['Auth · A', 'Terminal · B'])
  state.context = 'A'
  await render()
  await click('Reconnect', element.querySelector('section')!)
  await act(async () => { Socket.instances[2].open(); Socket.instances[2].session('A'); vi.advanceTimersByTime(300) })
  expect(new URL(Socket.instances[2].url).searchParams.get('expectedContext')).toBe('A')
  expect(Socket.instances.flatMap(s => s.send.mock.calls).filter(([data]) => JSON.parse(data).type === 'input')).toHaveLength(1)
})

it('refreshes connection state after handshake failure and can retry without consuming the command', async () => {
  const refresh = vi.spyOn(client, 'invalidateQueries')
  await render()
  await click('Open')
  await act(async () => { Socket.instances[0].onerror?.(); Socket.instances[0].close() })
  expect(refresh).toHaveBeenCalledWith({ queryKey: ['connection-status'] })
  state.context = 'B'
  await render()
  expect([...element.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Retry')?.disabled).toBe(true)
  await click('Retry')
  expect(Socket.instances).toHaveLength(1)
  state.context = 'A'
  await render()
  await click('Retry')
  await act(async () => { Socket.instances[1].open(); Socket.instances[1].session('A'); vi.advanceTimersByTime(300) })
  expect(Socket.instances[1].send.mock.calls.filter(([data]) => JSON.parse(data).type === 'input')).toHaveLength(1)
})

it('explains unknown-context refusal and lets an earlier callback read the latest committed context', async () => {
  state.context = ''
  state.status = 'connecting'
  await render()
  await click('Open')
  expect(element.querySelector('section')).toBeNull()
  expect(element.textContent).toContain('Waiting for a context. Try opening the terminal again.')
  expect(Socket.instances).toHaveLength(0)
  state.context = 'A & B/qualified'
  await render()
  expect(Socket.instances).toHaveLength(0)
  await act(async () => state.open!())
  expect(new URL(Socket.instances[0].url).searchParams.get('expectedContext')).toBe('A & B/qualified')
})

it('gives a shared-hook tab without an intent an explicit state and a fresh context-aware action', async () => {
  await render()
  await click('Open shared')
  expect(element.textContent).toContain('No context selected')
  expect(Socket.instances).toHaveLength(0)
  expect([...element.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Reconnect')?.disabled).toBe(true)
  await click('New terminal')
  expect(new URL(Socket.instances[0].url).searchParams.get('expectedContext')).toBe('A')
})

it('preserves the no-kubeconfig recovery shell with an explicit empty intent, then blocks reconnect under A', async () => {
  state.context = ''
  state.status = 'disconnected'
  await render()
  await click('Open')
  const query = new URL(Socket.instances[0].url).searchParams
  expect(query.has('expectedContext')).toBe(true)
  expect(query.get('expectedContext')).toBe('')
  await act(async () => { Socket.instances[0].open(); Socket.instances[0].onmessage?.({ data: JSON.stringify({ type: 'session', context: '', kubeconfigIsolated: false }) }); vi.advanceTimersByTime(300) })
  expect(element.textContent).toContain('Context not confirmed')
  expect(Socket.instances[0].send.mock.calls.filter(([data]) => JSON.parse(data).type === 'input')).toHaveLength(1)
  state.context = 'A'
  state.status = 'connected'
  await render()
  expect(Socket.instances[0].readyState).toBe(1)
  await act(async () => Socket.instances[0].close())
  expect([...element.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Reconnect')?.disabled).toBe(true)
  await click('New terminal')
  expect(new URL(Socket.instances[1].url).searchParams.get('expectedContext')).toBe('A')
})

it('opens a recovery shell for the known context when startup reports a config error', async () => {
  state.context = 'production@secondary'
  state.status = 'disconnected'
  await render()
  await click('Open')
  expect(new URL(Socket.instances[0].url).searchParams.get('expectedContext')).toBe('production@secondary')
  await act(async () => { Socket.instances[0].open(); Socket.instances[0].session('production@secondary'); vi.advanceTimersByTime(300) })
  expect(Socket.instances[0].send.mock.calls.filter(([data]) => JSON.parse(data).type === 'input')).toHaveLength(1)
})
