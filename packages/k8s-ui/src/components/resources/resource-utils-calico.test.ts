import { describe, expect, it } from 'vitest'
import {
  CALICO_SELECTOR_NOT_APPLICABLE,
  getCalicoApiGroup,
  getCalicoIPPoolAllowedUses,
  getCalicoIPPoolBlockSize,
  getCalicoIPPoolEncapsulation,
  getCalicoPolicyKindLabel,
  getCalicoPolicySelector,
  getCalicoPolicyTypes,
  getCalicoTierRef,
  isCalicoApiVersion,
  isCalicoPolicyResource,
  isCalicoStagedDeletion,
  isCalicoStagedKubernetesNetworkPolicyKind,
  isCoreNetworkPolicyKind,
} from './resource-utils-calico'

describe('Calico API group matching', () => {
  it('extracts only supported Calico API groups', () => {
    expect(getCalicoApiGroup('projectcalico.org/v3')).toBe('projectcalico.org')
    expect(getCalicoApiGroup('crd.projectcalico.org/v1')).toBe(
      'crd.projectcalico.org',
    )
    expect(getCalicoApiGroup('extension.projectcalico.org/v1')).toBeUndefined()
  })

  it('builds a cluster-scoped Tier reference with the policy API group', () => {
    expect(
      getCalicoTierRef({
        apiVersion: 'crd.projectcalico.org/v1',
        spec: { tier: 'security' },
      }),
    ).toEqual({
      kind: 'Tier',
      namespace: '',
      name: 'security',
      group: 'crd.projectcalico.org',
    })
  })

  it.each(['projectcalico.org/v3', 'crd.projectcalico.org/v1'])(
    'accepts the exact Calico group: %s',
    (apiVersion) => {
      expect(isCalicoApiVersion(apiVersion)).toBe(true)
      expect(
        isCalicoPolicyResource({ apiVersion, kind: 'NetworkPolicy' }),
      ).toBe(true)
    },
  )

  it.each(['projectcalico.org/v3', 'crd.projectcalico.org/v1'])(
    'recognizes staged Kubernetes network policies in %s',
    (apiVersion) => {
      const policy = {
        apiVersion,
        kind: 'StagedKubernetesNetworkPolicy',
        spec: {
          podSelector: { matchLabels: { app: 'frontend' } },
          policyTypes: ['Ingress'],
        },
      }
      expect(isCalicoStagedKubernetesNetworkPolicyKind(policy.kind)).toBe(true)
      expect(isCalicoPolicyResource(policy)).toBe(true)
      expect(getCalicoPolicySelector(policy)).toBe('app=frontend')
      expect(getCalicoPolicyKindLabel(policy.kind)).toBe(
        'CalicoStagedKubernetesNetworkPolicy',
      )
    },
  )

  it.each([
    'extension.projectcalico.org/v1',
    'projectcalico.org.evil/v1',
    'networking.example.io/v1',
    'v1',
  ])('rejects a non-Calico group: %s', (apiVersion) => {
    expect(isCalicoApiVersion(apiVersion)).toBe(false)
    expect(isCalicoPolicyResource({ apiVersion, kind: 'NetworkPolicy' })).toBe(
      false,
    )
  })

  it('recognizes only the core networking.k8s.io policy', () => {
    expect(
      isCoreNetworkPolicyKind('NetworkPolicy', 'networking.k8s.io/v1'),
    ).toBe(true)
    expect(
      isCoreNetworkPolicyKind('NetworkPolicy', 'projectcalico.org/v3'),
    ).toBe(false)
    expect(
      isCoreNetworkPolicyKind('NetworkPolicy', 'other.example.io/v1'),
    ).toBe(false)
    expect(isCoreNetworkPolicyKind('NetworkPolicy', undefined)).toBe(true)
    expect(
      isCoreNetworkPolicyKind('NetworkPolicy', undefined, 'networking.k8s.io'),
    ).toBe(true)
    expect(
      isCoreNetworkPolicyKind('NetworkPolicy', undefined, 'other.example.io'),
    ).toBe(false)
  })

  it('does not let the exact core fallback classify foreign kinds', () => {
    expect(
      isCoreNetworkPolicyKind('GlobalNetworkPolicy', 'networking.k8s.io/v1'),
    ).toBe(false)
    expect(isCoreNetworkPolicyKind('Widget', undefined)).toBe(false)
  })
})

describe('staged deletions', () => {
  const deletion = {
    apiVersion: 'projectcalico.org/v3',
    kind: 'StagedNetworkPolicy',
    metadata: { name: 'retire-frontend', namespace: 'demo' },
    // The Calico API rejects any other spec field alongside a Delete action.
    spec: { stagedAction: 'Delete', tier: 'default' },
  }

  it('does not read an absent selector as every workload', () => {
    expect(isCalicoStagedDeletion(deletion)).toBe(true)
    expect(getCalicoPolicySelector(deletion)).toBe(
      CALICO_SELECTOR_NOT_APPLICABLE,
    )
  })

  it('leaves a staged Set reading its real selector', () => {
    const set = {
      apiVersion: 'projectcalico.org/v3',
      kind: 'StagedNetworkPolicy',
      metadata: { name: 'tighten', namespace: 'demo' },
      spec: { stagedAction: 'Set', selector: "app == 'web'" },
    }
    expect(isCalicoStagedDeletion(set)).toBe(false)
    expect(getCalicoPolicySelector(set)).toBe("app == 'web'")
  })

  it('still reads an enforced policy with no selector as every workload', () => {
    const enforced = {
      apiVersion: 'projectcalico.org/v3',
      kind: 'NetworkPolicy',
      metadata: { name: 'catch-all', namespace: 'demo' },
      spec: {},
    }
    expect(getCalicoPolicySelector(enforced)).toBe('all workloads')
  })
})

describe('IPPool derivations', () => {
  it("applies Calico's block size default per address family", () => {
    expect(getCalicoIPPoolBlockSize({ spec: { cidr: '192.168.0.0/16' } })).toBe(
      26,
    )
    expect(getCalicoIPPoolBlockSize({ spec: { cidr: 'fd00::/48' } })).toBe(122)
    expect(
      getCalicoIPPoolBlockSize({ spec: { cidr: '10.0.0.0/8', blockSize: 24 } }),
    ).toBe(24)
    expect(getCalicoIPPoolBlockSize({ spec: {} })).toBeUndefined()
  })

  it('names the tunnel a pool encapsulates through', () => {
    expect(getCalicoIPPoolEncapsulation({ spec: {} })).toBe('None')
    expect(getCalicoIPPoolEncapsulation({ spec: { ipipMode: 'Always' } })).toBe(
      'IPIP Always',
    )
    expect(
      getCalicoIPPoolEncapsulation({ spec: { vxlanMode: 'CrossSubnet' } }),
    ).toBe('VXLAN CrossSubnet')
  })

  it("falls back to Calico's default allowed uses", () => {
    expect(getCalicoIPPoolAllowedUses({ spec: {} })).toBe('Workload, Tunnel')
    expect(
      getCalicoIPPoolAllowedUses({ spec: { allowedUses: ['Tunnel'] } }),
    ).toBe('Tunnel')
  })

  it('treats an empty allowedUses list as the default, not as no uses', () => {
    // Calico: "If not specified or empty, defaults to ["Tunnel", "Workload"]".
    // A dash here would read as a pool that allocates for nothing.
    expect(getCalicoIPPoolAllowedUses({ spec: { allowedUses: [] } })).toBe('Workload, Tunnel')
  })
})

describe('derived policy types', () => {
  // Verified against Calico v3.32.1: applying these without spec.types and
  // reading them back yields exactly these values.
  const policy = (spec: any) => ({
    apiVersion: 'projectcalico.org/v3',
    kind: 'NetworkPolicy',
    spec: { selector: "app == 'x'", ...spec },
  })

  it('derives the types from which rule lists exist', () => {
    expect(getCalicoPolicyTypes(policy({ egress: [{ action: 'Allow' }] }))).toEqual(['Egress'])
    expect(getCalicoPolicyTypes(policy({ ingress: [{ action: 'Allow' }] }))).toEqual(['Ingress'])
    expect(
      getCalicoPolicyTypes(policy({ ingress: [{ action: 'Allow' }], egress: [{ action: 'Allow' }] })),
    ).toEqual(['Ingress', 'Egress'])
    expect(getCalicoPolicyTypes(policy({}))).toEqual(['Ingress'])
  })

  it('follows the Kubernetes rule for the Kubernetes-shaped staged kind', () => {
    // Kubernetes: every policy affects ingress, egress only when an egress
    // section exists. Calico leaves policyTypes empty on this kind, so the
    // derivation below is what the UI renders.
    const staged = (spec: any) => ({
      apiVersion: 'projectcalico.org/v3',
      kind: 'StagedKubernetesNetworkPolicy',
      spec: { podSelector: { matchLabels: { app: 'x' } }, stagedAction: 'Set', ...spec },
    })
    expect(getCalicoPolicyTypes(staged({ egress: [{ to: [] }] }))).toEqual(['Ingress', 'Egress'])
    expect(getCalicoPolicyTypes(staged({ ingress: [{ from: [] }] }))).toEqual(['Ingress'])
    expect(getCalicoPolicyTypes(staged({}))).toEqual(['Ingress'])
  })

  it('prefers what the policy actually declares', () => {
    expect(getCalicoPolicyTypes(policy({ types: ['Egress'], ingress: [{ action: 'Allow' }] }))).toEqual(['Egress'])
    expect(getCalicoPolicyTypes({ spec: undefined })).toEqual([])
  })
})
