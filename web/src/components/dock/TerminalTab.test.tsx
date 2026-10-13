import { renderToString } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import type { TerminalTabProps } from '@skyhook-io/k8s-ui'
import { TerminalTab } from './TerminalTab'
const captured = vi.hoisted(() => ({ props: null as TerminalTabProps | null }))
vi.mock('@skyhook-io/k8s-ui', () => ({ TerminalTab: (props: TerminalTabProps) => { captured.props = props; return null } }))
vi.mock('../../api/config', () => ({ apiUrl: (path: string) => `/api${path}`, getWsUrl: (path: string) => `ws://localhost/api${path}` }))
it.each([undefined, '/bin/custom+command'])('forwards the executable and context through the shared terminal (%s)', async shell => {
  renderToString(<TerminalTab namespace="app" podName="worker" containerName="app" containers={['app', 'sidecar']} shell={shell} note="Primary" />)
  const props = captured.props!
  expect(props.command).toBe(shell); expect(props.note).toBe('Primary')
  expect(Boolean(props.createDebugContainer)).toBe(!shell)
  const { wsUrl } = await props.createSession('app')
  const url = new URL(wsUrl)
  expect(url.pathname).toBe('/api/pods/app/worker/exec'); expect(url.searchParams.get('container')).toBe('app')
  expect(url.searchParams.get('shell')).toBe(shell ?? null)
})
