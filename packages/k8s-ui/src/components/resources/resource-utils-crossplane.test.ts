import { describe, it, expect } from 'vitest'
import {
  isManagedResource,
  isComposite,
  isClaim,
  getCrossplaneStatus,
  getCrossplaneStatusReason,
  getProviderStatus,
  getProviderConfigStatus,
  getProviderConfigRef,
  getCrossplaneResourceRefs,
  getExternalName,
  getComposingXRRef,
  isCrossplanePaused,
} from './resource-utils-crossplane'

describe('isManagedResource', () => {
  it('returns true when spec.providerConfigRef is set (v1)', () => {
    const resource = {
      spec: { providerConfigRef: { name: 'default' } },
    } as const
    expect(isManagedResource(resource)).toBe(true)
  })

  it('returns true when spec.crossplane.providerConfigRef is set (v2)', () => {
    const resource = {
      spec: { crossplane: { providerConfigRef: { name: 'default' } } },
    } as const
    expect(isManagedResource(resource)).toBe(true)
  })

  it('returns false when no providerConfigRef exists at either path', () => {
    expect(isManagedResource({ spec: {} } as const)).toBe(false)
    expect(isManagedResource({ spec: { crossplane: {} } } as const)).toBe(false)
  })

  it('returns false when spec is missing or null', () => {
    expect(isManagedResource({} as const)).toBe(false)
    expect(isManagedResource({ spec: null } as const)).toBe(false)
    expect(isManagedResource(null as any)).toBe(false)
    expect(isManagedResource(undefined as any)).toBe(false)
  })
})

describe('isComposite', () => {
  it('returns true when spec.resourceRefs is an array (v1)', () => {
    const resource = {
      spec: {
        resourceRefs: [
          { apiVersion: 'rds.aws.upbound.io/v1beta1', kind: 'Instance', name: 'db-x' },
        ],
      },
    } as const
    expect(isComposite(resource)).toBe(true)
  })

  it('returns true when spec.crossplane.resourceRefs is an array (v2)', () => {
    const resource = {
      spec: {
        crossplane: {
          resourceRefs: [
            { apiVersion: 'rds.aws.upbound.io/v1beta1', kind: 'Instance', name: 'db-x' },
          ],
        },
      },
    } as const
    expect(isComposite(resource)).toBe(true)
  })

  it('returns true when resourceRefs is an empty array', () => {
    expect(isComposite({ spec: { resourceRefs: [] } } as const)).toBe(true)
  })

  it('returns false when isManagedResource is true (MR with providerConfigRef wins)', () => {
    const resource = {
      spec: {
        providerConfigRef: { name: 'default' },
        resourceRefs: [{ apiVersion: 'v1', kind: 'Foo', name: 'bar' }],
      },
    } as const
    expect(isComposite(resource)).toBe(false)
  })

  it('returns false when resourceRefs is missing', () => {
    expect(isComposite({ spec: {} } as const)).toBe(false)
    expect(isComposite({} as const)).toBe(false)
  })

  it('returns false when resourceRefs is not an array', () => {
    expect(isComposite({ spec: { resourceRefs: 'oops' } } as const)).toBe(false)
  })
})

describe('isClaim', () => {
  it('returns true when spec.resourceRef and spec.compositionRef both exist (v1)', () => {
    const resource = {
      spec: {
        resourceRef: { apiVersion: 'example.io/v1', kind: 'XPostgres', name: 'bound-xr' },
        compositionRef: { name: 'postgres-aws' },
      },
    } as const
    expect(isClaim(resource)).toBe(true)
  })

  it('returns false when only resourceRef exists', () => {
    const resource = {
      spec: {
        resourceRef: { apiVersion: 'example.io/v1', kind: 'XPostgres', name: 'bound-xr' },
      },
    } as const
    expect(isClaim(resource)).toBe(false)
  })

  it('returns false when only compositionRef exists', () => {
    expect(isClaim({ spec: { compositionRef: { name: 'foo' } } } as const)).toBe(false)
  })

  it('returns false for a Managed Resource even if both refs were set somehow', () => {
    const resource = {
      spec: {
        providerConfigRef: { name: 'default' },
        resourceRef: { kind: 'Foo', name: 'bar' },
        compositionRef: { name: 'baz' },
      },
    } as const
    expect(isClaim(resource)).toBe(false)
  })

  it('returns false when spec is missing', () => {
    expect(isClaim({} as const)).toBe(false)
    expect(isClaim(null as any)).toBe(false)
  })
})

describe('getCrossplaneStatus', () => {
  it('returns Ready/healthy when Ready=True and Synced=True', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'True' },
          { type: 'Synced', status: 'True' },
        ],
      },
    } as const
    const badge = getCrossplaneStatus(resource)
    expect(badge.text).toBe('Ready')
    expect(badge.level).toBe('healthy')
  })

  it('returns Out of sync/alert when Synced=False (regardless of Ready)', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'True' },
          { type: 'Synced', status: 'False', reason: 'ReconcileError', message: 'bad spec' },
        ],
      },
    } as const
    const badge = getCrossplaneStatus(resource)
    expect(badge.text).toBe('Out of sync')
    expect(badge.level).toBe('alert')
  })

  it('returns Ready.reason/unhealthy when Ready=False and Synced is not False', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'False', reason: 'Creating', message: 'still creating' },
          { type: 'Synced', status: 'True' },
        ],
      },
    } as const
    const badge = getCrossplaneStatus(resource)
    expect(badge.text).toBe('Creating')
    expect(badge.level).toBe('unhealthy')
  })

  it('falls back to "Not ready" when Ready=False has no reason', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'False' },
          { type: 'Synced', status: 'True' },
        ],
      },
    } as const
    const badge = getCrossplaneStatus(resource)
    expect(badge.text).toBe('Not ready')
    expect(badge.level).toBe('unhealthy')
  })

  it('returns Provisioning/degraded when Ready=Unknown', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'Unknown' },
          { type: 'Synced', status: 'True' },
        ],
      },
    } as const
    const badge = getCrossplaneStatus(resource)
    expect(badge.text).toBe('Provisioning')
    expect(badge.level).toBe('degraded')
  })

  it('returns Pending/neutral when no Ready or Synced conditions exist', () => {
    expect(getCrossplaneStatus({} as const)).toMatchObject({
      text: 'Pending',
      level: 'neutral',
    })
    expect(getCrossplaneStatus({ status: { conditions: [] } } as const)).toMatchObject({
      text: 'Pending',
      level: 'neutral',
    })
    expect(
      getCrossplaneStatus({
        status: { conditions: [{ type: 'Other', status: 'True' }] },
      } as const),
    ).toMatchObject({ text: 'Pending', level: 'neutral' })
  })

  it('returns Terminating/degraded when deletionTimestamp is set', () => {
    const resource = {
      metadata: { deletionTimestamp: '2026-05-18T00:00:00Z' },
      status: {
        conditions: [
          { type: 'Ready', status: 'True' },
          { type: 'Synced', status: 'True' },
        ],
      },
    } as const
    const badge = getCrossplaneStatus(resource)
    expect(badge.text).toBe('Terminating')
    expect(badge.level).toBe('degraded')
  })
})

describe('getCrossplaneStatusReason', () => {
  it('returns the Synced=False message when present (Synced wins over Ready)', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'False', message: 'ready message' },
          { type: 'Synced', status: 'False', message: 'synced message' },
        ],
      },
    } as const
    expect(getCrossplaneStatusReason(resource)).toBe('synced message')
  })

  it('returns the Ready=False message when Synced is True', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'False', message: 'pod crashlooping' },
          { type: 'Synced', status: 'True' },
        ],
      },
    } as const
    expect(getCrossplaneStatusReason(resource)).toBe('pod crashlooping')
  })

  it('falls back to the reason when message is empty', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Synced', status: 'False', reason: 'ReconcileError', message: '' },
        ],
      },
    } as const
    expect(getCrossplaneStatusReason(resource)).toBe('ReconcileError')
  })

  it('falls back to Ready.reason when Synced is True and Ready has no message', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'False', reason: 'Creating' },
          { type: 'Synced', status: 'True' },
        ],
      },
    } as const
    expect(getCrossplaneStatusReason(resource)).toBe('Creating')
  })

  it('returns null when all conditions are True', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Ready', status: 'True' },
          { type: 'Synced', status: 'True' },
        ],
      },
    } as const
    expect(getCrossplaneStatusReason(resource)).toBeNull()
  })

  it('returns null when no conditions exist', () => {
    expect(getCrossplaneStatusReason({} as const)).toBeNull()
    expect(getCrossplaneStatusReason({ status: {} } as const)).toBeNull()
  })
})

describe('getProviderStatus', () => {
  it('returns Healthy/healthy when Installed=True and Healthy=True', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Installed', status: 'True' },
          { type: 'Healthy', status: 'True' },
        ],
      },
    } as const
    const badge = getProviderStatus(resource)
    expect(badge.text).toBe('Healthy')
    expect(badge.level).toBe('healthy')
  })

  it('returns Installed=False reason as unhealthy', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Installed', status: 'False', reason: 'ImagePullBackOff' },
          { type: 'Healthy', status: 'True' },
        ],
      },
    } as const
    const badge = getProviderStatus(resource)
    expect(badge.text).toBe('ImagePullBackOff')
    expect(badge.level).toBe('unhealthy')
  })

  it('falls back to "Install failed" when Installed=False has no reason', () => {
    const resource = {
      status: { conditions: [{ type: 'Installed', status: 'False' }] },
    } as const
    expect(getProviderStatus(resource)).toMatchObject({
      text: 'Install failed',
      level: 'unhealthy',
    })
  })

  it('returns alert when Healthy=False and Installed is not False', () => {
    const resource = {
      status: {
        conditions: [
          { type: 'Installed', status: 'True' },
          { type: 'Healthy', status: 'False', reason: 'CrashLoopBackOff' },
        ],
      },
    } as const
    const badge = getProviderStatus(resource)
    expect(badge.text).toBe('CrashLoopBackOff')
    expect(badge.level).toBe('alert')
  })

  it('returns Installed/degraded when only Installed=True (no Healthy condition)', () => {
    const resource = {
      status: { conditions: [{ type: 'Installed', status: 'True' }] },
    } as const
    const badge = getProviderStatus(resource)
    expect(badge.text).toBe('Installed')
    expect(badge.level).toBe('degraded')
  })

  it('returns Terminating/degraded when deletionTimestamp is set', () => {
    const resource = {
      metadata: { deletionTimestamp: '2026-05-18T00:00:00Z' },
      status: {
        conditions: [
          { type: 'Installed', status: 'True' },
          { type: 'Healthy', status: 'True' },
        ],
      },
    } as const
    expect(getProviderStatus(resource)).toMatchObject({
      text: 'Terminating',
      level: 'degraded',
    })
  })
})

describe('getProviderConfigStatus', () => {
  it('returns "{n} in use"/healthy when status.users > 0', () => {
    const resource = { status: { users: 3 } } as const
    const badge = getProviderConfigStatus(resource)
    expect(badge.text).toBe('3 in use')
    expect(badge.level).toBe('healthy')
  })

  it('returns Idle/neutral when status.users is 0', () => {
    const resource = { status: { users: 0 } } as const
    const badge = getProviderConfigStatus(resource)
    expect(badge.text).toBe('Idle')
    expect(badge.level).toBe('neutral')
  })

  it('returns Idle/neutral when status.users is missing', () => {
    expect(getProviderConfigStatus({} as const)).toMatchObject({
      text: 'Idle',
      level: 'neutral',
    })
  })

  it('returns Ready=False reason as unhealthy (takes precedence over users count)', () => {
    const resource = {
      status: {
        users: 5,
        conditions: [{ type: 'Ready', status: 'False', reason: 'AuthFailure' }],
      },
    } as const
    const badge = getProviderConfigStatus(resource)
    expect(badge.text).toBe('AuthFailure')
    expect(badge.level).toBe('unhealthy')
  })

  it('falls back to "Not ready" when Ready=False has no reason', () => {
    const resource = {
      status: { conditions: [{ type: 'Ready', status: 'False' }] },
    } as const
    expect(getProviderConfigStatus(resource)).toMatchObject({
      text: 'Not ready',
      level: 'unhealthy',
    })
  })
})

describe('getProviderConfigRef', () => {
  it('reads spec.providerConfigRef (v1)', () => {
    const resource = {
      spec: { providerConfigRef: { name: 'aws-default', kind: 'ProviderConfig' } },
    } as const
    expect(getProviderConfigRef(resource)).toEqual({
      name: 'aws-default',
      kind: 'ProviderConfig',
    })
  })

  it('reads spec.crossplane.providerConfigRef (v2) and prefers it over v1', () => {
    const resource = {
      spec: {
        providerConfigRef: { name: 'v1-name' },
        crossplane: { providerConfigRef: { name: 'v2-name', kind: 'ProviderConfig' } },
      },
    } as const
    expect(getProviderConfigRef(resource)).toEqual({
      name: 'v2-name',
      kind: 'ProviderConfig',
    })
  })

  it('returns null when no providerConfigRef exists', () => {
    expect(getProviderConfigRef({ spec: {} } as const)).toBeNull()
    expect(getProviderConfigRef({} as const)).toBeNull()
  })

  it('returns null when providerConfigRef has no name', () => {
    expect(
      getProviderConfigRef({ spec: { providerConfigRef: { kind: 'ProviderConfig' } } } as const),
    ).toBeNull()
  })
})

describe('getCrossplaneResourceRefs', () => {
  it('returns refs from spec.resourceRefs (v1)', () => {
    const resource = {
      spec: {
        resourceRefs: [
          { apiVersion: 'rds.aws.upbound.io/v1beta1', kind: 'Instance', name: 'db-x' },
          { apiVersion: 'ec2.aws.upbound.io/v1beta1', kind: 'VPC', name: 'vpc-x' },
        ],
      },
    } as const
    expect(getCrossplaneResourceRefs(resource)).toHaveLength(2)
  })

  it('returns refs from spec.crossplane.resourceRefs (v2) and prefers it over v1', () => {
    const resource = {
      spec: {
        resourceRefs: [{ apiVersion: 'a/v1', kind: 'A', name: 'a-v1' }],
        crossplane: {
          resourceRefs: [{ apiVersion: 'b/v1', kind: 'B', name: 'b-v2' }],
        },
      },
    } as const
    const refs = getCrossplaneResourceRefs(resource)
    expect(refs).toHaveLength(1)
    expect(refs[0].name).toBe('b-v2')
  })

  it('filters out entries missing kind or name', () => {
    const resource = {
      spec: {
        resourceRefs: [
          { apiVersion: 'v1', kind: 'A', name: 'good' },
          { apiVersion: 'v1', kind: 'B' },
          { apiVersion: 'v1', name: 'no-kind' },
          {},
          null,
        ],
      },
    } as const
    const refs = getCrossplaneResourceRefs(resource)
    expect(refs).toHaveLength(1)
    expect(refs[0].name).toBe('good')
  })

  it('returns empty array when resourceRefs is missing or not an array', () => {
    expect(getCrossplaneResourceRefs({} as const)).toEqual([])
    expect(getCrossplaneResourceRefs({ spec: {} } as const)).toEqual([])
    expect(getCrossplaneResourceRefs({ spec: { resourceRefs: 'oops' } } as const)).toEqual([])
  })
})

describe('getExternalName', () => {
  it('reads the crossplane.io/external-name annotation', () => {
    const resource = {
      metadata: { annotations: { 'crossplane.io/external-name': 'arn:aws:rds:db-x' } },
    } as const
    expect(getExternalName(resource)).toBe('arn:aws:rds:db-x')
  })

  it('returns null when the annotation is missing', () => {
    expect(getExternalName({ metadata: { annotations: {} } } as const)).toBeNull()
    expect(getExternalName({ metadata: {} } as const)).toBeNull()
    expect(getExternalName({} as const)).toBeNull()
  })
})

describe('getComposingXRRef', () => {
  it('returns the controller=true ownerReference', () => {
    const resource = {
      metadata: {
        ownerReferences: [
          { apiVersion: 'example.io/v1', kind: 'XOther', name: 'not-controller', controller: false },
          { apiVersion: 'example.io/v1', kind: 'XPostgres', name: 'the-xr', controller: true },
        ],
      },
    } as const
    expect(getComposingXRRef(resource)).toEqual({
      apiVersion: 'example.io/v1',
      kind: 'XPostgres',
      name: 'the-xr',
    })
  })

  it('falls back to refs[0] when no ownerReference has controller=true', () => {
    const resource = {
      metadata: {
        ownerReferences: [
          { apiVersion: 'example.io/v1', kind: 'XFirst', name: 'first' },
          { apiVersion: 'example.io/v1', kind: 'XSecond', name: 'second' },
        ],
      },
    } as const
    expect(getComposingXRRef(resource)).toEqual({
      apiVersion: 'example.io/v1',
      kind: 'XFirst',
      name: 'first',
    })
  })

  it('returns null when ownerReferences is empty or missing', () => {
    expect(getComposingXRRef({} as const)).toBeNull()
    expect(getComposingXRRef({ metadata: {} } as const)).toBeNull()
    expect(getComposingXRRef({ metadata: { ownerReferences: [] } } as const)).toBeNull()
  })

  it('returns null when the picked ref has no kind or name', () => {
    const resource = {
      metadata: { ownerReferences: [{ apiVersion: 'v1', controller: true }] },
    } as const
    expect(getComposingXRRef(resource)).toBeNull()
  })
})

describe('isCrossplanePaused', () => {
  it('returns true when annotation is the string "true"', () => {
    const resource = {
      metadata: { annotations: { 'crossplane.io/paused': 'true' } },
    } as const
    expect(isCrossplanePaused(resource)).toBe(true)
  })

  it('returns false when annotation is missing', () => {
    expect(isCrossplanePaused({} as const)).toBe(false)
    expect(isCrossplanePaused({ metadata: {} } as const)).toBe(false)
    expect(isCrossplanePaused({ metadata: { annotations: {} } } as const)).toBe(false)
  })

  it('returns false for empty string, "false", or boolean true', () => {
    expect(
      isCrossplanePaused({ metadata: { annotations: { 'crossplane.io/paused': '' } } } as const),
    ).toBe(false)
    expect(
      isCrossplanePaused({
        metadata: { annotations: { 'crossplane.io/paused': 'false' } },
      } as const),
    ).toBe(false)
    expect(
      isCrossplanePaused({
        metadata: { annotations: { 'crossplane.io/paused': true as any } },
      } as const),
    ).toBe(false)
  })
})

// Pluralization moved to the shared kindToPlural in utils/navigation.ts
// (discovery-aware, handles provider-defined irregular plurals).
