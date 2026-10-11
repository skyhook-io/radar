import { describe, expect, it } from 'vitest'
import { gatewayBackendResourceRef, gatewayParentResourceRef } from './gateway-references'

describe('Gateway API references', () => {
  it('applies the distinct backend and parent defaults', () => {
    expect(gatewayBackendResourceRef({ name: 'api' }, 'app')).toEqual({ kind: 'Service', group: '', namespace: 'app', name: 'api' })
    expect(gatewayParentResourceRef({ name: 'edge' }, 'app')).toEqual({ kind: 'Gateway', group: 'gateway.networking.k8s.io', namespace: 'app', name: 'edge' })
  })

  it('preserves custom kinds, exact groups and explicit namespaces', () => {
    expect(gatewayBackendResourceRef({ name: 'api', kind: 'Widget', group: 'custom.example.io', namespace: 'backends' }, 'app'))
      .toEqual({ kind: 'Widget', group: 'custom.example.io', namespace: 'backends', name: 'api' })
    expect(gatewayBackendResourceRef({ name: 'api', kind: 'Service', group: 'serving.knative.dev' }, 'app').group).toBe('serving.knative.dev')
    expect(gatewayParentResourceRef({ name: 'mesh', kind: 'Service', group: '', namespace: 'front' }, 'app'))
      .toEqual({ kind: 'Service', group: '', namespace: 'front', name: 'mesh' })
  })
})
