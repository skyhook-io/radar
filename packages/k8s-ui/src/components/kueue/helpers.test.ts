import { describe, expect, it } from 'vitest'
import { parseFlavorAttempts } from './flavors-tried'
import { namespaceEligibility } from './eligibility'
import { describeKueuePolicy } from './policy'
import { isDenied, unreadReason } from './related-read'

describe('parseFlavorAttempts', () => {
  it('parses the Kueue v0.18 message seen on a live 3-CPU Workload into both attempts', () => {
    const message = "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor gpu-night-813-small, previously considered podsets requests (0) + current podset request (3) > maximum capacity (1), insufficient quota for cpu in flavor gpu-night-813-spot, previously considered podsets requests (0) + current podset request (3) > maximum capacity (1)"
    expect(parseFlavorAttempts(message)).toEqual([{ podSet: 'main', attempts: [
      { kind: 'insufficient_quota', resource: 'cpu', flavor: 'gpu-night-813-small', text: 'insufficient quota for cpu in flavor gpu-night-813-small, previously considered podsets requests (0) + current podset request (3) > maximum capacity (1)' },
      { kind: 'insufficient_quota', resource: 'cpu', flavor: 'gpu-night-813-spot', text: 'insufficient quota for cpu in flavor gpu-night-813-spot, previously considered podsets requests (0) + current podset request (3) > maximum capacity (1)' },
    ] }])
  })

  it('splits pod sets and every reason format, keeping commas inside a reason', () => {
    const message = "couldn't assign flavors to pod set driver: flavor a doesn't match node affinity, insufficient unused quota for nvidia.com/gpu in flavor b, 2 more needed, untolerated taint gpu=true:NoSchedule in flavor c; couldn't assign flavors to pod set workers: flavor z not found, resource example.com/fpga unavailable in ClusterQueue, skipping flavor y due to WorkloadAllowedResourceFlavorAnnotation annotation, could not assign x flavor since the original workload is assigned: w"
    const parsed = parseFlavorAttempts(message)
    expect(parsed?.map(p => p.podSet)).toEqual(['driver', 'workers'])
    expect(parsed?.[0].attempts.map(a => [a.kind, a.flavor, a.resource])).toEqual([
      ['node_affinity', 'a', undefined], ['insufficient_unused_quota', 'b', 'nvidia.com/gpu'], ['untolerated_taint', 'c', undefined],
    ])
    expect(parsed?.[1].attempts.map(a => [a.kind, a.flavor, a.resource])).toEqual([
      ['flavor_not_found', 'z', undefined], ['resource_unavailable', undefined, 'example.com/fpga'], ['skipped_by_annotation', 'y', undefined], ['pinned_by_original', 'x', undefined],
    ])
  })

  it('returns null for anything it cannot account for, so the caller shows the message verbatim', () => {
    for (const message of [
      undefined,
      '',
      "LocalQueue no-such-localqueue doesn't exist",
      "Workload namespace doesn't match ClusterQueue selector",
      'failed to assign flavors to pod set main: boom',
      "couldn't assign flavors to pod set main: topology \"rack\" doesn't allow to fit any of 3 pod(s)",
      "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor a, request > maximum capacity (24 > 8), something new",
      "couldn't assign flavors to pod set main",
    ]) expect(parseFlavorAttempts(message)).toBeNull()
  })

  it('accepts an older quota detail shape as long as the reason prefix is known', () => {
    expect(parseFlavorAttempts("couldn't assign flavors to pod set main: insufficient quota for cpu in flavor default, request > maximum capacity (24 > 8)")?.[0].attempts[0].flavor).toBe('default')
  })
})

describe('namespaceEligibility', () => {
  it('reads null as no namespace and {} as every namespace without needing labels', () => {
    expect(namespaceEligibility(null, undefined)).toEqual({ state: 'not_eligible', basis: 'none' })
    expect(namespaceEligibility(undefined, undefined)).toEqual({ state: 'not_eligible', basis: 'none' })
    expect(namespaceEligibility({}, undefined)).toEqual({ state: 'eligible', basis: 'all' })
    expect(namespaceEligibility({ matchLabels: {}, matchExpressions: [] }, undefined)).toEqual({ state: 'eligible', basis: 'all' })
  })

  it('needs labels for a selector with terms, and says so when they were not read', () => {
    expect(namespaceEligibility({ matchLabels: { team: 'a' } }, undefined)).toEqual({ state: 'unknown', why: 'labels_unread' })
    expect(namespaceEligibility({ matchLabels: { team: 'a' } }, { team: 'a' }).state).toBe('eligible')
    expect(namespaceEligibility({ matchLabels: { team: 'a' } }, { team: 'b' }).state).toBe('not_eligible')
    expect(namespaceEligibility({ matchLabels: { team: 'a' } }, {}).state).toBe('not_eligible')
  })

  it('evaluates every selector operator as Kubernetes does', () => {
    const sel = (operator: string, values?: string[]) => ({ matchExpressions: [{ key: 'tier', operator, values }] })
    expect(namespaceEligibility(sel('In', ['gpu']), { tier: 'gpu' }).state).toBe('eligible')
    expect(namespaceEligibility(sel('In', ['gpu']), {}).state).toBe('not_eligible')
    expect(namespaceEligibility(sel('NotIn', ['gpu']), {}).state).toBe('eligible')
    expect(namespaceEligibility(sel('NotIn', ['gpu']), { tier: 'gpu' }).state).toBe('not_eligible')
    expect(namespaceEligibility(sel('Exists'), { tier: '' }).state).toBe('eligible')
    expect(namespaceEligibility(sel('DoesNotExist'), { tier: '' }).state).toBe('not_eligible')
    expect(namespaceEligibility(sel('Gt', ['1']), { tier: '2' })).toEqual({ state: 'unknown', why: 'unsupported_operator', operator: 'Gt' })
  })
})

describe('describeKueuePolicy', () => {
  it('keeps a dictionary per API version and shows unknown values raw', () => {
    expect(describeKueuePolicy('kueue.x-k8s.io/v1beta1', 'flavorFungibility.whenCanBorrow', 'Borrow').meaning).toContain('Equivalent to MayStopSearch in newer Kueue')
    expect(describeKueuePolicy('kueue.x-k8s.io/v1beta2', 'flavorFungibility.whenCanBorrow', 'Borrow')).toEqual({ raw: 'Borrow' })
    expect(describeKueuePolicy('kueue.x-k8s.io/v1beta2', 'admissionScope.admissionMode', 'NoAdmissionFairSharing').meaning).toBeTruthy()
    expect(describeKueuePolicy('kueue.x-k8s.io/v1', 'stopPolicy', 'Hold')).toEqual({ raw: 'Hold' })
    expect(describeKueuePolicy('kueue.x-k8s.io/v1beta2', 'stopPolicy', 'Hold').meaning).toContain('admitted Workloads run to completion')
  })
})

describe('related reads', () => {
  it('tells a denial apart from a read that failed for another reason', () => {
    expect(isDenied({ state: 'unavailable', code: 'user_forbidden' })).toBe(true)
    expect(isDenied({ state: 'unavailable', code: 'kind_sync_timed_out' })).toBe(false)
    expect(isDenied({ state: 'not_found' })).toBe(false)
    expect(unreadReason({ state: 'unavailable', code: 'user_forbidden' })).toBe('you can’t read it')
    expect(unreadReason({ state: 'unavailable', code: 'radar_forbidden' })).toBe('Radar can’t read it')
    expect(unreadReason({ state: 'unavailable', code: 'rbac_denied' })).toBe('you can’t read it')
    expect(unreadReason({ state: 'loading' })).toBe('checking…')
    expect(unreadReason({ state: 'unavailable' })).toBe('the read failed')
  })
})

describe('policy dictionaries against the served CRDs', () => {
  it('describe every enum value Kueue v0.18 serves, per version', async () => {
    const { enums } = (await import('./testdata/kueue-v0.18-policy-enums.json')).default as { enums: Record<string, Record<string, string[]>> }
    for (const [version, fields] of Object.entries(enums)) {
      for (const [field, values] of Object.entries(fields)) {
        for (const value of values) expect(describeKueuePolicy(version, field as any, value).meaning, `${version} ${field}=${value}`).toBeTruthy()
      }
    }
  })
})
