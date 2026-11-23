import { describe, expect, it } from 'vitest'
import { describeTrafficNode } from './traffic-export'

describe('describeTrafficNode', () => {
  it('makes the addon group the only container', () => {
    expect(describeTrafficNode({ id: 'addon-group', type: 'addonGroup', data: {} })).toMatchObject({ shape: 'group', tone: 'addon' })
    const node = describeTrafficNode({ id: 'shop/web', type: 'traffic', data: { kind: 'Pod', label: 'web', namespace: 'shop', namespaceColor: '#1e3a5f' } })
    expect(node).toMatchObject({ shape: 'resource', name: 'web', detail: 'shop', fill: '#1e3a5f' })
  })

  it('shows a workload by its workload kind', () => {
    expect(describeTrafficNode({ id: 'shop/api', type: 'traffic', data: { kind: 'Workload', workloadKind: 'Deployment', label: 'api' } }))
      .toMatchObject({ kind: 'Deployment', kindLabel: 'Deployment' })
  })

  it('keeps the category of an external service and no namespace tint', () => {
    const node = describeTrafficNode({ id: 'db.example.com', type: 'traffic', data: { kind: 'External', label: 'db.example.com', serviceCategory: 'database', namespaceColor: '#123456' } })
    expect(node).toMatchObject({ shape: 'resource', kind: 'External', category: 'database' })
    expect(node?.fill).toBeUndefined()
  })

  it('lists ports with their counts, up to the card limit, or the connection total', () => {
    const ports = [80, 443, 8080, 9090, 9100].map((port, i) => ({ port, connections: i === 1 ? 0 : 1500 }))
    const node = describeTrafficNode({ id: 'shop/api', type: 'traffic', data: { kind: 'Pod', label: 'api', ports, isHotPath: true } })
    expect(node?.rows).toEqual([
      { text: ':80', code: true, hot: true, value: '2K' },
      { text: ':443', code: true, hot: true },
      { text: ':8080', code: true, hot: true, value: '2K' },
      { text: ':9090', code: true, hot: true, value: '2K' },
      { text: '+1 more', muted: true },
    ])

    const client = describeTrafficNode({ id: 'demo/client', type: 'traffic', data: { kind: 'Pod', label: 'client', ports: [{ port: 0, connections: 5 }], totalConnections: 540, connLabel: 'conn' } })
    expect(client?.rows).toEqual([{ text: '540 conn', hot: false }])
    expect(describeTrafficNode({ id: 'idle', type: 'traffic', data: { kind: 'Pod', label: 'idle' } })?.rows).toBeUndefined()
  })

  it('washes an external card in its category color', () => {
    expect(describeTrafficNode({ id: 'web', type: 'traffic', data: { kind: 'External', label: 'HTTPS', serviceCategory: 'web' } })?.tint).toEqual({ color: '#3B82F6', opacity: 0.2 })
    expect(describeTrafficNode({ id: 'x', type: 'traffic', data: { kind: 'External', label: 'x' } })?.tint).toEqual({ color: '#EAB308', opacity: 0.1 })
    expect(describeTrafficNode({ id: 'shop/web', type: 'traffic', data: { kind: 'Pod', label: 'web' } })?.tint).toBeUndefined()
  })

  it('draws both Internet kinds as Internet', () => {
    expect(describeTrafficNode({ id: 'internet', type: 'traffic', data: { kind: 'Internet', label: 'internet' } })?.shape).toBe('internet')
    expect(describeTrafficNode({ id: 'addon-internet', type: 'traffic', data: { kind: 'AddonInternet', label: 'Internet' } })?.shape).toBe('internet')
  })
})
