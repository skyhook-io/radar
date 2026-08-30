import { describe, expect, it } from 'vitest'
import { getGenericResourceStatus } from './generic-status'

const withStatus = (status: any) => ({ status })

describe('getGenericResourceStatus', () => {
  describe('phase', () => {
    it('reports a known-healthy phase as healthy', () => {
      expect(getGenericResourceStatus(withStatus({ phase: 'Running' })))
        .toEqual({ text: 'Running', tone: 'healthy' })
    })

    it('reports a known-degraded phase as degraded and carries the message', () => {
      expect(getGenericResourceStatus(withStatus({ phase: 'Pending', message: 'waiting for quota' })))
        .toEqual({ text: 'Pending', tone: 'degraded', reason: 'waiting for quota' })
    })

    it('reports a known-failure phase as unhealthy', () => {
      expect(getGenericResourceStatus(withStatus({ phase: 'Failed' })))
        .toMatchObject({ text: 'Failed', tone: 'unhealthy' })
    })

    // The cell and drawer ladders both painted any unrecognized phase red,
    // so every operator-specific phase name read as an outage.
    it('prefers a known phase over conditions', () => {
      const s = getGenericResourceStatus(withStatus({
        phase: 'Running',
        conditions: [{ type: 'Ready', status: 'False' }],
      }))
      expect(s).toEqual({ text: 'Running', tone: 'healthy' })
    })

    // An unrecognized phase says nothing about health, so a condition or a
    // replica count that does must win. Short-circuiting on it would hide a
    // real failure behind an operator's private vocabulary.
    it('lets a failing condition outrank an unrecognized phase', () => {
      expect(getGenericResourceStatus(withStatus({
        phase: 'Rebalancing',
        conditions: [{ type: 'Ready', status: 'False', reason: 'QuorumLost' }],
      }))).toMatchObject({ text: 'QuorumLost', tone: 'unhealthy' })
    })

    it('lets a failing replica count outrank an unrecognized phase', () => {
      expect(getGenericResourceStatus(withStatus({ phase: 'Rebalancing', replicas: 3 })))
        .toEqual({ text: '0/3 Ready', tone: 'unhealthy' })
    })

    it('keeps an unrecognized phase as the last resort', () => {
      expect(getGenericResourceStatus(withStatus({ phase: 'Rebalancing' })))
        .toMatchObject({ text: 'Rebalancing', tone: 'unknown' })
    })

    it('prefers a bare state string to an unrecognized phase', () => {
      expect(getGenericResourceStatus(withStatus({ phase: 'Rebalancing', state: 'Draining' })))
        .toEqual({ text: 'Draining', tone: 'unknown' })
    })
  })

  describe('conditions with known polarity', () => {
    it('reads Ready=True as healthy', () => {
      expect(getGenericResourceStatus(withStatus({ conditions: [{ type: 'Ready', status: 'True' }] })))
        .toEqual({ text: 'Ready', tone: 'healthy' })
    })

    it('prefers the condition reason over a synthesized negative label', () => {
      expect(getGenericResourceStatus(withStatus({
        conditions: [{ type: 'Ready', status: 'False', reason: 'BackoffLimitExceeded', message: 'too many retries' }],
      }))).toEqual({ text: 'BackoffLimitExceeded', tone: 'unhealthy', reason: 'too many retries' })
    })

    it('synthesizes a negative label when there is no reason', () => {
      expect(getGenericResourceStatus(withStatus({ conditions: [{ type: 'Available', status: 'False' }] })))
        .toMatchObject({ text: 'Not Available', tone: 'unhealthy' })
    })

    // A controller that cannot evaluate a condition has not reported a failure.
    it('does not treat Unknown as False', () => {
      expect(getGenericResourceStatus(withStatus({ conditions: [{ type: 'Ready', status: 'Unknown' }] })))
        .toMatchObject({ text: 'Ready', tone: 'unknown' })
    })

    it('resolves Ready ahead of Available regardless of array order', () => {
      expect(getGenericResourceStatus(withStatus({
        conditions: [{ type: 'Available', status: 'False' }, { type: 'Ready', status: 'True' }],
      }))).toEqual({ text: 'Ready', tone: 'healthy' })
    })

    // Progressing=False is the resting state of a settled resource; the drawer
    // ladder treated it like a failed Ready and mislabeled healthy objects.
    it('does not infer health from Progressing', () => {
      expect(getGenericResourceStatus(withStatus({ conditions: [{ type: 'Progressing', status: 'False' }] })))
        .toMatchObject({ tone: 'unknown' })
    })
  })

  describe('conditions with unknown polarity', () => {
    // gmp PodMonitoring publishes only this; every TS ladder rendered a dash.
    it('surfaces an unrecognized condition type as text without a health claim', () => {
      expect(getGenericResourceStatus(withStatus({
        conditions: [{ type: 'ConfigurationCreateSuccess', status: 'True' }],
      }))).toEqual({ text: 'ConfigurationCreateSuccess', tone: 'unknown' })
    })

    it('reads GKE ComputeClass Health as a known positive condition', () => {
      expect(getGenericResourceStatus(withStatus({ conditions: [{ type: 'Health', status: 'True' }] })))
        .toEqual({ text: 'Health', tone: 'healthy' })
    })

    it('skips condition entries with no type', () => {
      expect(getGenericResourceStatus(withStatus({
        conditions: [{ status: 'True' }, { type: 'Settled', status: 'True' }],
      }))).toEqual({ text: 'Settled', tone: 'unknown' })
    })
  })

  describe('bare string status fields', () => {
    // kubefledged ImageCache publishes status.status; the ladders checked
    // status.state only, so it rendered a dash.
    it('falls back to status.status', () => {
      expect(getGenericResourceStatus(withStatus({ status: 'Succeeded', reason: 'ok' })))
        .toEqual({ text: 'Succeeded', tone: 'unknown' })
    })

    it('prefers status.state over status.status', () => {
      expect(getGenericResourceStatus(withStatus({ state: 'Bound', status: 'other' })))
        .toEqual({ text: 'Bound', tone: 'unknown' })
    })

    it('ignores state/status when they are not strings', () => {
      expect(getGenericResourceStatus(withStatus({ state: { phase: 'x' } }))).toBeNull()
    })
  })


  describe('known-negative conditions and replicas', () => {
    it('reads a Degraded=True condition as degraded', () => {
      expect(getGenericResourceStatus(withStatus({
        conditions: [{ type: 'Degraded', status: 'True', message: 'one shard offline' }],
      }))).toEqual({ text: 'Degraded', tone: 'degraded', reason: 'one shard offline' })
    })

    it('ignores a known-negative condition that is False', () => {
      expect(getGenericResourceStatus(withStatus({ conditions: [{ type: 'Degraded', status: 'False' }] })))
        .toMatchObject({ tone: 'unknown' })
    })

    it('reports full replica coverage as healthy', () => {
      expect(getGenericResourceStatus(withStatus({ replicas: 3, readyReplicas: 3 })))
        .toEqual({ text: '3/3 Ready', tone: 'healthy' })
    })

    it('grades partial replica coverage degraded and none unhealthy', () => {
      expect(getGenericResourceStatus(withStatus({ replicas: 3, readyReplicas: 1 })))
        .toEqual({ text: '1/3 Ready', tone: 'degraded' })
      expect(getGenericResourceStatus(withStatus({ replicas: 3 })))
        .toEqual({ text: '0/3 Ready', tone: 'unhealthy' })
    })

    it('falls back to availableReplicas when readyReplicas is absent', () => {
      expect(getGenericResourceStatus(withStatus({ replicas: 2, availableReplicas: 2 })))
        .toEqual({ text: '2/2 Ready', tone: 'healthy' })
    })

    // Replicas say more than a vendor condition name, and both only rank below
    // the condition types whose polarity is actually known.
    it('ranks replicas above an unknown-polarity condition but below Ready', () => {
      expect(getGenericResourceStatus(withStatus({
        replicas: 2, readyReplicas: 2,
        conditions: [{ type: 'ConfigurationCreateSuccess', status: 'True' }],
      }))).toEqual({ text: '2/2 Ready', tone: 'healthy' })

      expect(getGenericResourceStatus(withStatus({
        replicas: 2, readyReplicas: 2,
        conditions: [{ type: 'Ready', status: 'False', reason: 'Stalled' }],
      }))).toMatchObject({ text: 'Stalled', tone: 'unhealthy' })
    })

    it('ignores a zero or non-numeric replica count', () => {
      expect(getGenericResourceStatus(withStatus({ replicas: 0 }))).toBeNull()
      expect(getGenericResourceStatus(withStatus({ replicas: '3' }))).toBeNull()
    })
  })

  describe('nothing to report', () => {
    it.each([
      ['no resource', undefined],
      ['no status', {}],
      // A resource whose status IS a string, not a status object — guarded, or
      // reading .phase off a string would silently yield undefined forever.
      ['non-object status', { status: 'ok' }],
      ['empty status', { status: {} }],
      ['empty conditions', { status: { conditions: [] } }],
      ['blank phase', { status: { phase: '   ' } }],
      ['conditions that are not an array', { status: { conditions: 'Ready' } }],
    ])('returns null for %s', (_label, resource) => {
      expect(getGenericResourceStatus(resource as any)).toBeNull()
    })
  })
})
