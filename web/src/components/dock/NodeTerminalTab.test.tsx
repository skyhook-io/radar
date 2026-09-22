import { renderToString } from 'react-dom/server'
import { afterEach, expect, it, vi } from 'vitest'
import type { NodeTerminalTabProps } from '@skyhook-io/k8s-ui'
import { NodeTerminalTab } from './NodeTerminalTab'

const captured = vi.hoisted(() => ({ props: null as NodeTerminalTabProps | null }))
vi.mock('@skyhook-io/k8s-ui', () => ({
  NodeTerminalTab: (props: NodeTerminalTabProps) => { captured.props = props; return null },
}))
vi.mock('../../api/config', () => ({
  apiUrl: (path: string) => `/api${path}`,
  getWsUrl: (path: string) => path,
  getAuthHeaders: () => ({ Authorization: 'Bearer test' }),
  getCredentialsMode: () => 'include',
}))
afterEach(() => vi.unstubAllGlobals())

it('passes the created UID to exact-pod cleanup query parameters with keepalive', async () => {
  const pod = { namespace: 'default', podName: 'debug-a', uid: 'uid/a+b', containerName: 'debug' }
  const fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify(pod)))
    .mockResolvedValueOnce(new Response('{}'))
  vi.stubGlobal('fetch', fetch)
  renderToString(<NodeTerminalTab nodeName="node/a" />)
  const props = captured.props!
  const result = await props.createNodeDebugPod('node/a')
  expect(result).toEqual(pod)
  await props.cleanupNodeDebugPod('node/a', result)
  const [url, options] = fetch.mock.calls[1]
  const parsed = new URL(url, 'http://localhost')
  expect(parsed.pathname).toBe('/api/nodes/node%2Fa/debug')
  expect(Object.fromEntries(parsed.searchParams)).toEqual({ namespace: 'default', podName: 'debug-a', uid: 'uid/a+b' })
  expect(options).toMatchObject({ method: 'DELETE', keepalive: true, credentials: 'include', headers: { Authorization: 'Bearer test' } })
  expect(options).not.toHaveProperty('body')
})
