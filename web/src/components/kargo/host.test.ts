import { describe, expect, it } from 'vitest'
import type { KargoFreight } from '@skyhook-io/k8s-ui'
import { warehouseFreights } from './host'
import { project } from './kargo.fixtures'

const freight = (name: string, warehouse: string): KargoFreight => ({ metadata: { name, namespace: 'kargo-demo' }, origin: { kind: 'Warehouse', name: warehouse } })

describe('Warehouse drawer Freight', () => {
  it('passes the Warehouse’s own Freight with its total', () => {
    const data = project({ freight: [freight('a', 'kargo-demo'), freight('b', 'kargo-demo'), freight('c', 'other')], freightByWarehouse: { 'kargo-demo': 2, other: 1 } })
    const got = warehouseFreights(data, 'kargo-demo')
    expect(got?.freights.map((f) => f.metadata?.name)).toEqual(['a', 'b'])
    expect(got?.total).toBe(2)
  })

  it('passes the newest it has with the full total when the answer holds only part of it', () => {
    // A manual Warehouse has no lastFreightID, so the list and the total are all the drawer has.
    const data = project({ freight: [freight('a', 'kargo-demo')], freightByWarehouse: { 'kargo-demo': 51 } })
    const got = warehouseFreights(data, 'kargo-demo')
    expect(got?.freights).toHaveLength(1)
    expect(got?.total).toBe(51)
  })

  it('passes nothing when Freight wasn’t read', () => {
    expect(warehouseFreights(project({ freightRead: false }), 'kargo-demo')).toBeUndefined()
  })

  it('reads only the Warehouse’s own count, even for a name an object inherits', () => {
    const data = project({ freight: [freight('a', 'other')], freightByWarehouse: { other: 1 } })
    expect(warehouseFreights(data, 'constructor')).toEqual({ freights: [], total: undefined })
  })
})
