import { describe, test, expect, afterEach } from 'vitest'
import { kindToPlural, pluralToKind, refToSelectedResource, initNavigationMap, resetNavigationMap, laneId, laneResourceKey, groupQualifiesLaneId, parseLaneId } from './navigation'

afterEach(() => {
  resetNavigationMap()
})

describe('kindToPlural', () => {
  test('singular PascalCase to plural lowercase', () => {
    expect(kindToPlural('Secret')).toBe('secrets')
    expect(kindToPlural('Deployment')).toBe('deployments')
    expect(kindToPlural('Pod')).toBe('pods')
    expect(kindToPlural('Service')).toBe('services')
    expect(kindToPlural('ConfigMap')).toBe('configmaps')
    expect(kindToPlural('Node')).toBe('nodes')
    expect(kindToPlural('Job')).toBe('jobs')
    expect(kindToPlural('CronJob')).toBe('cronjobs')
  })

  test('handles kinds ending in s/x/ch/sh (adds -es)', () => {
    expect(kindToPlural('Ingress')).toBe('ingresses')
  })

  test('handles kinds ending in consonant+y (changes to -ies)', () => {
    expect(kindToPlural('NetworkPolicy')).toBe('networkpolicies')
  })

  test('handles already-plural kind names (Endpoints)', () => {
    // The Kind "Endpoints" IS its resource name; englishPlural would wrongly
    // yield "endpointses" (ends in s → +es) without the builtin map entry.
    expect(kindToPlural('Endpoints')).toBe('endpoints')
    expect(pluralToKind('endpoints')).toBe('Endpoints')
  })

  test('handles EndpointSlice before discovery loads', () => {
    expect(kindToPlural('EndpointSlice')).toBe('endpointslices')
    expect(pluralToKind('endpointslices')).toBe('EndpointSlice')
  })

  test('handles kinds ending in ss (Class-suffix)', () => {
    expect(kindToPlural('StorageClass')).toBe('storageclasses')
    expect(kindToPlural('IngressClass')).toBe('ingressclasses')
    expect(kindToPlural('PriorityClass')).toBe('priorityclasses')
    expect(kindToPlural('RuntimeClass')).toBe('runtimeclasses')
    expect(kindToPlural('GatewayClass')).toBe('gatewayclasses')
    expect(kindToPlural('EC2NodeClass')).toBe('ec2nodeclasses')
  })

  test('idempotent on known plurals (prevents double-pluralization)', () => {
    // This was the original bug: "secrets" → "secretses"
    expect(kindToPlural('secrets')).toBe('secrets')
    expect(kindToPlural('services')).toBe('services')
    expect(kindToPlural('ingresses')).toBe('ingresses')
    expect(kindToPlural('deployments')).toBe('deployments')
    expect(kindToPlural('pods')).toBe('pods')
    expect(kindToPlural('configmaps')).toBe('configmaps')
    expect(kindToPlural('nodes')).toBe('nodes')
    expect(kindToPlural('storageclasses')).toBe('storageclasses')
    expect(kindToPlural('networkpolicies')).toBe('networkpolicies')
    expect(kindToPlural('horizontalpodautoscalers')).toBe('horizontalpodautoscalers')
  })

  test('handles aliases', () => {
    expect(kindToPlural('HorizontalPodAutoscaler')).toBe('horizontalpodautoscalers')
    expect(kindToPlural('pvc')).toBe('persistentvolumeclaims')
    expect(kindToPlural('PodGroup')).toBe('pods')
  })
})

// Demonstrate that the naive .toLowerCase() + 's' pattern used by renderers is broken.
// These tests prove WHY renderers must use kindToPlural() instead of ad-hoc pluralization.
describe('naive pluralization (renderer bug demonstration)', () => {
  const naivePlural = (kind: string) => kind.toLowerCase() + 's'

  test('breaks for Class-suffix kinds (triple-s)', () => {
    // What HPARenderer, KarpenterNodePoolRenderer, etc. actually produce
    expect(naivePlural('EC2NodeClass')).toBe('ec2nodeclasss')   // WRONG
    expect(kindToPlural('EC2NodeClass')).toBe('ec2nodeclasses')  // CORRECT
  })

  test('breaks for Policy-suffix kinds', () => {
    expect(naivePlural('NetworkPolicy')).toBe('networkpolicys')  // WRONG
    expect(kindToPlural('NetworkPolicy')).toBe('networkpolicies') // CORRECT
  })

  test('breaks for Ingress-like kinds (ending in s)', () => {
    expect(naivePlural('Ingress')).toBe('ingresss')   // WRONG
    expect(kindToPlural('Ingress')).toBe('ingresses')  // CORRECT
  })

  test('breaks for Repository-suffix kinds', () => {
    expect(naivePlural('GitRepository')).toBe('gitrepositorys')    // WRONG
    expect(kindToPlural('GitRepository')).toBe('gitrepositories')  // CORRECT
  })
})

describe('pluralToKind', () => {
  test('reverse mapping for known plurals', () => {
    expect(pluralToKind('secrets')).toBe('Secret')
    expect(pluralToKind('deployments')).toBe('Deployment')
    expect(pluralToKind('horizontalpodautoscalers')).toBe('HorizontalPodAutoscaler')
    expect(pluralToKind('ingresses')).toBe('Ingress')
    expect(pluralToKind('configmaps')).toBe('ConfigMap')
    expect(pluralToKind('networkpolicies')).toBe('NetworkPolicy')
    expect(pluralToKind('storageclasses')).toBe('StorageClass')
  })

  test('PascalCase input returned as-is', () => {
    expect(pluralToKind('Deployment')).toBe('Deployment')
    expect(pluralToKind('Secret')).toBe('Secret')
  })

  test('fallback de-pluralization for unknown kinds', () => {
    expect(pluralToKind('widgets')).toBe('Widget')
  })

  test('fallback handles -ies suffix', () => {
    // Unknown kind not in the map
    expect(pluralToKind('batteries')).toBe('Battery')
  })

  test('fallback handles -ses suffix', () => {
    // "databases" triggers the -ses rule (strips 2 chars) — a known limitation
    // of the heuristic fallback. Known kinds use the PLURAL_TO_KIND map instead.
    expect(pluralToKind('databases')).toBe('Databas')
  })
})

describe('initNavigationMap', () => {
  test('discovered API resources override heuristic pluralization', () => {
    // Before init, an unknown CRD would hit the heuristic fallback
    // After init, it uses the discovered plural name
    initNavigationMap([
      { group: 'external-secrets.io', version: 'v1', kind: 'SecretStore', name: 'secretstores', namespaced: true, isCrd: true, verbs: ['get'] },
      { group: 'external-secrets.io', version: 'v1', kind: 'ClusterSecretStore', name: 'clustersecretstores', namespaced: false, isCrd: true, verbs: ['get'] },
    ])
    expect(kindToPlural('SecretStore')).toBe('secretstores')
    expect(kindToPlural('ClusterSecretStore')).toBe('clustersecretstores')
    expect(pluralToKind('secretstores')).toBe('SecretStore')
    expect(pluralToKind('clustersecretstores')).toBe('ClusterSecretStore')
  })

  test('prevents double-pluralization of discovered plurals', () => {
    initNavigationMap([
      { group: 'external-secrets.io', version: 'v1', kind: 'SecretStore', name: 'secretstores', namespaced: true, isCrd: true, verbs: ['get'] },
    ])
    // Passing an already-plural kind should be idempotent
    expect(kindToPlural('secretstores')).toBe('secretstores')
  })

  test('builtin core mappings win over colliding discovered resources', () => {
    // metrics.k8s.io exposes a resource named "pods" with kind "PodMetrics".
    // Without first-wins on builtins, this clobbers core "pods" → "Pod" and
    // every Pod-keyed lookup (timeline kind filter, badge color, etc.) breaks.
    initNavigationMap([
      { group: '', version: 'v1', kind: 'Pod', name: 'pods', namespaced: true, isCrd: false, verbs: ['get'] },
      { group: 'metrics.k8s.io', version: 'v1beta1', kind: 'PodMetrics', name: 'pods', namespaced: true, isCrd: false, verbs: ['get'] },
    ])
    expect(pluralToKind('pods')).toBe('Pod')
  })
})

describe('refToSelectedResource', () => {
  test('converts singular kind to plural for navigation', () => {
    const result = refToSelectedResource({
      kind: 'Secret',
      name: 'test-tls',
      namespace: 'platform',
    })
    expect(result).toEqual({
      kind: 'secrets',
      name: 'test-tls',
      namespace: 'platform',
      group: undefined,
    })
  })

  test('preserves group field', () => {
    const result = refToSelectedResource({
      kind: 'Certificate',
      name: 'my-cert',
      namespace: 'default',
      group: 'cert-manager.io',
    })
    expect(result).toEqual({
      kind: 'certificates',
      name: 'my-cert',
      namespace: 'default',
      group: 'cert-manager.io',
    })
  })
})

describe('lane identity helpers', () => {
  test('groupQualifiesLaneId: only CRD groups qualify', () => {
    expect(groupQualifiesLaneId('')).toBe(false)          // core
    expect(groupQualifiesLaneId(undefined)).toBe(false)
    expect(groupQualifiesLaneId('apps')).toBe(false)       // built-in
    expect(groupQualifiesLaneId('batch')).toBe(false)
    expect(groupQualifiesLaneId('networking.k8s.io')).toBe(false)
    expect(groupQualifiesLaneId('postgresql.cnpg.io')).toBe(true)
    expect(groupQualifiesLaneId('cluster.x-k8s.io')).toBe(true)
  })

  test('laneId: bare for core/built-in, qualified for CRD groups', () => {
    expect(laneId('Pod', '', 'team-a', 'x')).toBe('Pod/team-a/x')
    expect(laneId('Deployment', 'apps', 'ns', 'web')).toBe('Deployment/ns/web')
    expect(laneId('Cluster', 'postgresql.cnpg.io', 'prod', 'main-db')).toBe('Cluster.postgresql.cnpg.io/prod/main-db')
  })

  test('laneResourceKey is always group-less', () => {
    expect(laneResourceKey('Cluster', 'prod', 'main-db')).toBe('Cluster/prod/main-db')
    expect(laneResourceKey('Pod', 'team-a', 'x')).toBe('Pod/team-a/x')
  })

  test('parseLaneId round-trips both bare and qualified ids', () => {
    expect(parseLaneId('Pod/team-a/x')).toEqual({ kind: 'Pod', group: '', namespace: 'team-a', name: 'x' })
    expect(parseLaneId('Cluster.postgresql.cnpg.io/prod/main-db')).toEqual({
      kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'prod', name: 'main-db',
    })
    expect(parseLaneId('bogus')).toBeNull()
  })
})
