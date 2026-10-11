// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { HTTPRouteRenderer } from './HTTPRouteRenderer'
import { GRPCRouteRenderer } from './GRPCRouteRenderer'
import { SimpleRouteRenderer } from './SimpleRouteRenderer'

const backend = { name: 'custom-backend', kind: 'Widget', group: 'relationships.radar.test', namespace: 'backends' }
const parent = { name: 'mesh-parent', kind: 'Service', group: '', namespace: 'mesh' }

function routeData(kind: string) {
  return {
    metadata: { name: 'route', namespace: 'app' },
    status: { parents: [{ parentRef: parent, conditions: [{ type: 'Accepted', status: 'False', reason: 'Rejected' }] }] },
    spec: {
      parentRefs: [parent],
      rules: [{
        backendRefs: [backend, { name: 'default-service', port: 8080 }],
        ...(kind === 'HTTPRoute' ? { filters: [{ type: 'RequestMirror', requestMirror: { backendRef: { ...backend, name: 'mirror' } } }] } : {}),
      }],
    },
  }
}

describe('Gateway route reference navigation', () => {
  it.each(['HTTPRoute', 'GRPCRoute', 'TCPRoute', 'TLSRoute'] as const)('preserves backend and parent identities in %s', async kind => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const container = document.createElement('div')
    const root = createRoot(container)
    const onNavigate = vi.fn()
    const data = routeData(kind)
    const click = async (name: string) => {
      const button = [...container.querySelectorAll('button')].find(button => button.textContent?.includes(name))
      expect(button, `button ${name}`).toBeDefined()
      await act(async () => button!.click())
    }
    try {
      await act(async () => root.render(
        kind === 'HTTPRoute' ? <HTTPRouteRenderer data={data} onNavigate={onNavigate} />
          : kind === 'GRPCRoute' ? <GRPCRouteRenderer data={data} onNavigate={onNavigate} />
          : <SimpleRouteRenderer kind={kind} data={data} onNavigate={onNavigate} />,
      ))
      expect(container.textContent).toContain('Parents')
      expect(container.textContent).toContain('Parent "mesh-parent"')
      expect(container.textContent).not.toContain('Gateway "mesh-parent"')

      await click('custom-backend')
      expect(onNavigate).toHaveBeenLastCalledWith(backend)
      await click('default-service')
      expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'Service', group: '', namespace: 'app', name: 'default-service' })
      await click('mesh-parent')
      expect(onNavigate).toHaveBeenLastCalledWith(parent)
      if (kind === 'HTTPRoute') {
        await click('mirror')
        expect(onNavigate).toHaveBeenLastCalledWith({ ...backend, name: 'mirror' })
      }
    } finally {
      await act(async () => root.unmount())
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
    }
  })
})
