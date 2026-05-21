import { describe, it, expect } from 'vitest'
import { normalizeForCompare, toComparableYaml } from './normalize'

describe('normalizeForCompare', () => {
  it('strips server-assigned metadata fields', () => {
    const input = {
      apiVersion: 'apps/v1',
      kind: 'Deployment',
      metadata: {
        name: 'api',
        namespace: 'prod',
        resourceVersion: '12345',
        uid: 'abc-def',
        generation: 3,
        creationTimestamp: '2024-01-01T00:00:00Z',
        selfLink: '/apis/apps/v1/...',
        managedFields: [{ manager: 'kubectl' }],
      },
      spec: { replicas: 3 },
    }
    const out = normalizeForCompare(input) as any
    expect(out.metadata.name).toBe('api')
    expect(out.metadata.namespace).toBe('prod')
    expect(out.metadata.resourceVersion).toBeUndefined()
    expect(out.metadata.uid).toBeUndefined()
    expect(out.metadata.generation).toBeUndefined()
    expect(out.metadata.creationTimestamp).toBeUndefined()
    expect(out.metadata.selfLink).toBeUndefined()
    expect(out.metadata.managedFields).toBeUndefined()
  })

  it('drops kubectl last-applied annotation', () => {
    const input = {
      metadata: {
        name: 'api',
        annotations: {
          'kubectl.kubernetes.io/last-applied-configuration': '{"large":"blob"}',
          'deployment.kubernetes.io/revision': '5',
          'custom-annotation': 'keep-me',
        },
      },
    }
    const out = normalizeForCompare(input) as any
    expect(out.metadata.annotations['custom-annotation']).toBe('keep-me')
    expect(out.metadata.annotations['kubectl.kubernetes.io/last-applied-configuration']).toBeUndefined()
    expect(out.metadata.annotations['deployment.kubernetes.io/revision']).toBeUndefined()
  })

  it('removes annotations object entirely when all keys are stripped', () => {
    const input = {
      metadata: {
        name: 'api',
        annotations: {
          'kubectl.kubernetes.io/last-applied-configuration': '{}',
        },
      },
    }
    const out = normalizeForCompare(input) as any
    expect(out.metadata.annotations).toBeUndefined()
  })

  it('strips pod-template-hash label noise', () => {
    const input = {
      metadata: {
        name: 'api',
        labels: {
          app: 'api',
          'pod-template-hash': 'abc123',
          'controller-revision-hash': 'xyz789',
        },
      },
    }
    const out = normalizeForCompare(input) as any
    expect(out.metadata.labels.app).toBe('api')
    expect(out.metadata.labels['pod-template-hash']).toBeUndefined()
    expect(out.metadata.labels['controller-revision-hash']).toBeUndefined()
  })

  it('drops status when specOnly is true', () => {
    const input = {
      spec: { replicas: 3 },
      status: { readyReplicas: 3, conditions: [{ type: 'Available' }] },
    }
    const out = normalizeForCompare(input, { specOnly: true }) as any
    expect(out.spec.replicas).toBe(3)
    expect(out.status).toBeUndefined()
  })

  it('keeps status when specOnly is false', () => {
    const input = {
      spec: { replicas: 3 },
      status: { readyReplicas: 3 },
    }
    const out = normalizeForCompare(input) as any
    expect(out.status.readyReplicas).toBe(3)
  })

  it('keeps server-assigned metadata when rawMetadata is true', () => {
    const input = {
      metadata: {
        name: 'api',
        resourceVersion: '12345',
        uid: 'abc-def',
        managedFields: [{ manager: 'kubectl' }],
        annotations: { 'kubectl.kubernetes.io/last-applied-configuration': '{}' },
        labels: { 'pod-template-hash': 'abc123', app: 'api' },
      },
    }
    const out = normalizeForCompare(input, { rawMetadata: true }) as any
    expect(out.metadata.resourceVersion).toBe('12345')
    expect(out.metadata.uid).toBe('abc-def')
    expect(out.metadata.managedFields).toEqual([{ manager: 'kubectl' }])
    expect(out.metadata.annotations['kubectl.kubernetes.io/last-applied-configuration']).toBe('{}')
    expect(out.metadata.labels['pod-template-hash']).toBe('abc123')
  })

  it('still drops status when rawMetadata is true and specOnly is true', () => {
    const input = { metadata: { name: 'api', uid: 'x' }, status: { phase: 'Running' } }
    const out = normalizeForCompare(input, { rawMetadata: true, specOnly: true }) as any
    expect(out.metadata.uid).toBe('x')
    expect(out.status).toBeUndefined()
  })

  it('does not mutate the input', () => {
    const input = {
      metadata: { name: 'api', resourceVersion: '1', managedFields: [{ manager: 'kubectl' }] },
    }
    normalizeForCompare(input)
    expect((input as any).metadata.resourceVersion).toBe('1')
    expect((input as any).metadata.managedFields).toEqual([{ manager: 'kubectl' }])
  })

  it('returns input unchanged when not an object', () => {
    expect(normalizeForCompare(null)).toBe(null)
    expect(normalizeForCompare(undefined)).toBe(undefined)
    expect(normalizeForCompare('string')).toBe('string')
  })

  it('handles missing metadata gracefully', () => {
    const out = normalizeForCompare({ spec: { replicas: 1 } }) as any
    expect(out.spec.replicas).toBe(1)
    expect(out.metadata).toBeUndefined()
  })

  it('handles non-object metadata gracefully (defensive)', () => {
    expect(() => normalizeForCompare({ metadata: 'oops', spec: {} })).not.toThrow()
  })

  it('handles null annotations object', () => {
    const out = normalizeForCompare({ metadata: { name: 'x', annotations: null } }) as any
    expect(out.metadata.name).toBe('x')
    expect(out.metadata.annotations).toBeNull()
  })

  // Recursing into spec.template.metadata risks stripping user-defined selector
  // labels that are themselves the diff signal — leave the template alone.
  it('does NOT strip pod-template-hash inside spec.template.metadata.labels', () => {
    const out = normalizeForCompare({
      spec: {
        template: {
          metadata: {
            labels: { app: 'api', 'pod-template-hash': 'abc' },
          },
        },
      },
    }) as any
    expect(out.spec.template.metadata.labels['pod-template-hash']).toBe('abc')
    expect(out.spec.template.metadata.labels.app).toBe('api')
  })
})

describe('toComparableYaml', () => {
  it('produces deterministic YAML output', () => {
    const input = {
      apiVersion: 'apps/v1',
      kind: 'Deployment',
      metadata: { name: 'api', namespace: 'prod', resourceVersion: '123' },
      spec: { replicas: 3 },
    }
    const yaml = toComparableYaml(input)
    expect(yaml).toContain('apiVersion: apps/v1')
    expect(yaml).toContain('name: api')
    expect(yaml).toContain('replicas: 3')
    // Stripped fields should be absent
    expect(yaml).not.toContain('resourceVersion')
  })

  it('identical inputs produce identical YAML', () => {
    const a = { metadata: { name: 'x' }, spec: { replicas: 2 } }
    const b = { metadata: { name: 'x' }, spec: { replicas: 2 } }
    expect(toComparableYaml(a)).toBe(toComparableYaml(b))
  })
})
