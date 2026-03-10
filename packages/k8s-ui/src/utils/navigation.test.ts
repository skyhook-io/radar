import { describe, test, expect } from 'vitest'
import { kindToPlural, pluralToKind, refToSelectedResource } from './navigation'

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
