import { describe, expect, it } from 'vitest'
import { getCellFilterValue } from './resource-utils'

// The table renders Istio Gateway through IstioGatewayCell, which reads the
// spec. The filter dropdown and the sort key have to read the same thing, or
// the dropdown offers values that appear on no row and the column sorts on a
// string the user never saw — the failure this file exists to pin.
const gateway = (spec: any) => ({ apiVersion: 'networking.istio.io/v1', kind: 'Gateway', spec })

describe('Istio Gateway filter values', () => {
  it('reads status from the spec, not the generic derivation', () => {
    const tls = gateway({ servers: [{ port: { number: 443 }, tls: { mode: 'SIMPLE' } }] })
    const plain = gateway({ servers: [{ port: { number: 80 } }] })
    const none = gateway({ servers: [] })
    expect(getCellFilterValue(tls, 'status', 'istiogateways')).toBe('TLS')
    expect(getCellFilterValue(plain, 'status', 'istiogateways')).toBe('Active')
    expect(getCellFilterValue(none, 'status', 'istiogateways')).toBe('No Servers')
  })

  // Read as a Gateway API Gateway instead, the same object filters as
  // "Unknown" — a value that appears on no row, since the cell shows Active.
  // That divergence is what routing the normalized key fixes.
  it('diverges from the row when read under the Gateway API key', () => {
    const g = gateway({ servers: [{ port: { number: 80 } }] })
    expect(getCellFilterValue(g, 'status', 'gateways')).not.toBe(
      getCellFilterValue(g, 'status', 'istiogateways'),
    )
  })

  it('reads the server count and selector its columns display', () => {
    const g = gateway({ servers: [{ port: { number: 80 } }, { port: { number: 443 } }], selector: { istio: 'ingressgateway' } })
    expect(getCellFilterValue(g, 'servers', 'istiogateways')).toBe('2')
    expect(getCellFilterValue(g, 'selector', 'istiogateways')).toBe('istio=ingressgateway')
  })

  it('renders an absent selector the way the cell does', () => {
    expect(getCellFilterValue(gateway({ servers: [] }), 'selector', 'istiogateways')).toBe('-')
  })

  // Those column keys are Istio's. A kind that happens to share them gets
  // nothing rather than a value read off the wrong shape.
  it('claims servers and selector only for Istio Gateways', () => {
    const other = { apiVersion: 'other.io/v1', kind: 'Thing', spec: { servers: [{ port: { number: 1 } }] } }
    expect(getCellFilterValue(other, 'servers', 'things')).toBe('')
    expect(getCellFilterValue(other, 'selector', 'things')).toBe('')
  })
})

describe('Kyverno Policy filter values', () => {
  // Reachable only once `policies` normalizes to `kyvernopolicies`; the filter
  // call sites pass the normalized key, so this is the string they see.
  it('reads the Kyverno status rather than the generic derivation', () => {
    const policy = {
      apiVersion: 'kyverno.io/v1',
      kind: 'Policy',
      spec: { rules: [{ name: 'r' }] },
      status: { conditions: [{ type: 'Ready', status: 'True' }] },
    }
    expect(getCellFilterValue(policy, 'status', 'kyvernopolicies')).not.toBe('')
  })
})
