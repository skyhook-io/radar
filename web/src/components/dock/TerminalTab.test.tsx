import { renderToString } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import type { TerminalTabProps } from '@skyhook-io/k8s-ui'
import { TerminalTab } from './TerminalTab'
vi.mock('./PodDebugSession', () => ({ PodDebugSession: ({ children }: { children: React.ReactNode }) => children }))
const captured = vi.hoisted(() => ({ props: null as TerminalTabProps | null }))
vi.mock('@skyhook-io/k8s-ui', () => ({ TerminalTab: (props: TerminalTabProps) => { captured.props = props; return null } }))
vi.mock('../../api/config', async (importOriginal) => ({ ...await importOriginal<typeof import('../../api/config')>(), apiUrl: (path: string) => `/api${path}`, getWsUrl: (path: string) => `ws://localhost/api${path}` }))
it.each([undefined, '/bin/custom+command'])('forwards the executable and context through the shared terminal (%s)', async shell => {
  renderToString(<TerminalTab namespace="app" podName="worker" containerName="app" containers={['app', 'sidecar']} shell={shell} note="Primary" />)
  const props = captured.props!
  expect(props.command).toBe(shell); expect(props.note).toBe('Primary')
  expect(props.createDebugContainer).toBeUndefined(); expect(Boolean(props.renderDebugAction)).toBe(!shell)
  const { wsUrl } = await props.createSession('app')
  const url = new URL(wsUrl)
  expect(url.pathname).toBe('/api/pods/app/worker/exec'); expect(url.searchParams.get('container')).toBe('app')
  expect(url.searchParams.get('shell')).toBe(shell ?? null)
  expect(url.searchParams.has('uid')).toBe(false)
})

it('reconnects on the connection the tab was opened on, not the latest', async () => {
  const { observeConnectionStatus, resetConnectionBindingForTest } = await import('../../api/connectionBinding')
  resetConnectionBindingForTest()
  observeConnectionStatus({ connectionId: 'n-1' })
  renderToString(<TerminalTab namespace="app" podName="worker" containerName="app" containers={['app']} />)
  observeConnectionStatus({ connectionId: 'n-2' }) // switched since the tab opened
  const { wsUrl } = await captured.props!.createSession('app')
  expect(new URL(wsUrl).searchParams.get('connection')).toBe('n-1')
  resetConnectionBindingForTest()
})

it('reconnects an existing debug session to its reviewed connection without offering another container', async () => {
  const { observeConnectionStatus, resetConnectionBindingForTest } = await import('../../api/connectionBinding')
  observeConnectionStatus({ connectionId: 'next-cluster' })
  renderToString(<TerminalTab namespace="demo" podName="api" containerName="debug-1" containers={['debug-1']} debugSession={{ connectionId: 'reviewed-cluster', apiBase: '/api', podUID: 'reviewed', image: 'mirror.example/debug' }} />)
  const { wsUrl } = await captured.props!.createSession('debug-1')
  expect(new URL(wsUrl).searchParams.get('connection')).toBe('reviewed-cluster')
  expect(new URL(wsUrl).searchParams.get('uid')).toBe('reviewed')
  expect(captured.props!.renderDebugAction).toBeUndefined()
  resetConnectionBindingForTest()
})
