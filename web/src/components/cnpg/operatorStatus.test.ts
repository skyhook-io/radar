import { describe, expect, it } from 'vitest'
import type { CNPGOperatorVerdict } from '../../api/cnpg'
import { cnpgOperatorActionNote, cnpgOperatorBannerModel } from './operatorStatus'

const down: CNPGOperatorVerdict = {
  state: 'notReconciling',
  reasons: ['no operator instance has renewed the leader lease cnpg-system/db9c8771.cnpg.io'],
  webhookRejects: true,
  webhookReason: 'the admission webhook Service cnpg-system/cnpg-webhook-service has no ready endpoint and fails closed, so the API server rejects writes to CloudNativePG objects',
  operator: 'cnpg-system/cnpg-controller-manager',
}
const up: CNPGOperatorVerdict = { state: 'reconciling', webhookRejects: false }
const unknown: CNPGOperatorVerdict = { state: 'unknown', unknown: 'the leader lease is not readable (needs get leases in cnpg-system)', webhookRejects: null }

describe('cnpgOperatorBannerModel', () => {
  it('names the namespaces whose operator is down and why', () => {
    const m = cnpgOperatorBannerModel({ pg: down, other: up }, ['pg', 'other', 'pg'])
    expect(m?.stale?.namespaces).toEqual(['pg'])
    expect(m?.stale?.reasons[0]).toMatch(/^No operator instance has renewed the leader lease/)
    expect(m?.rejects).toMatch(/no ready endpoint/)
  })
  it('stays quiet when reconciling or unknown', () => {
    expect(cnpgOperatorBannerModel({ pg: up }, ['pg'])).toBeNull()
    expect(cnpgOperatorBannerModel({ pg: unknown }, ['pg'])).toBeNull()
    expect(cnpgOperatorBannerModel(undefined, ['pg'])).toBeNull()
  })
})

describe('cnpgOperatorActionNote', () => {
  it('warns when the operator is observed down and notes when unknown', () => {
    expect(cnpgOperatorActionNote(down)?.tone).toBe('warning')
    expect(cnpgOperatorActionNote(unknown)).toEqual({ tone: 'info', text: expect.stringContaining('needs get leases') })
    expect(cnpgOperatorActionNote(up)).toBeNull()
  })
})

describe('cnpgOperatorActionNote when unknown', () => {
  it('says briefly what could not be confirmed', () => {
    expect(cnpgOperatorActionNote({ state: 'unknown', unknown: "couldn't read its leader lease" } as never)).toEqual({
      tone: 'info',
      text: "Radar couldn't confirm the operator is running (couldn't read its leader lease).",
    })
  })
})
