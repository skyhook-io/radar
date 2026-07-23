import { describe, it, expect } from 'vitest'
import { parse as yamlParse } from 'yaml'
import {
  cleanResourceForYaml,
  normalizeYamlForReview,
  resourceToYaml,
  splitYamlDocuments,
} from './yaml'

function makePod() {
  return {
    apiVersion: 'v1',
    kind: 'Pod',
    metadata: {
      name: 'nginx',
      namespace: 'default',
      uid: 'abcd-1234',
      resourceVersion: '99999',
      creationTimestamp: '2026-01-01T00:00:00Z',
      generation: 1,
      managedFields: [{ manager: 'kubectl', operation: 'Update' }],
      labels: { app: 'nginx' },
      annotations: { 'kubectl.kubernetes.io/last-applied-configuration': '{}' },
      ownerReferences: [{ kind: 'ReplicaSet', name: 'nginx-rs', uid: 'rs-uid' }],
    },
    spec: {
      containers: [{ name: 'nginx', image: 'nginx:1.25' }],
    },
    status: {
      phase: 'Running',
      podIP: '10.0.0.1',
    },
  }
}

describe('cleanResourceForYaml', () => {
  it('strips status', () => {
    const cleaned = cleanResourceForYaml(makePod())
    expect(cleaned.status).toBeUndefined()
  })

  it('strips all 5 server-generated metadata fields', () => {
    const cleaned = cleanResourceForYaml(makePod())
    expect(cleaned.metadata.uid).toBeUndefined()
    expect(cleaned.metadata.resourceVersion).toBeUndefined()
    expect(cleaned.metadata.creationTimestamp).toBeUndefined()
    expect(cleaned.metadata.generation).toBeUndefined()
    expect(cleaned.metadata.managedFields).toBeUndefined()
  })

  it('preserves user metadata', () => {
    const cleaned = cleanResourceForYaml(makePod())
    expect(cleaned.metadata.name).toBe('nginx')
    expect(cleaned.metadata.namespace).toBe('default')
    expect(cleaned.metadata.labels).toEqual({ app: 'nginx' })
    expect(cleaned.metadata.annotations).toEqual({
      'kubectl.kubernetes.io/last-applied-configuration': '{}',
    })
    expect(cleaned.metadata.ownerReferences).toHaveLength(1)
  })

  it('preserves spec', () => {
    const cleaned = cleanResourceForYaml(makePod())
    expect(cleaned.spec).toEqual({
      containers: [{ name: 'nginx', image: 'nginx:1.25' }],
    })
  })

  it('does not mutate the input', () => {
    const input = makePod()
    cleanResourceForYaml(input)
    expect(input.status).toBeDefined()
    expect(input.metadata.uid).toBe('abcd-1234')
    expect(input.metadata.managedFields).toHaveLength(1)
  })

  it('returns null/undefined/primitives unchanged', () => {
    expect(cleanResourceForYaml(null)).toBeNull()
    expect(cleanResourceForYaml(undefined)).toBeUndefined()
    expect(cleanResourceForYaml('not an object' as any)).toBe('not an object')
    expect(cleanResourceForYaml(42 as any)).toBe(42)
  })

  it('handles object with no metadata field', () => {
    const cleaned = cleanResourceForYaml({ kind: 'Pod', spec: {} })
    expect(cleaned).toEqual({ kind: 'Pod', spec: {} })
  })

  it('handles metadata present but null', () => {
    const cleaned = cleanResourceForYaml({
      kind: 'Pod',
      metadata: null,
      spec: {},
    })
    expect(cleaned).toEqual({ kind: 'Pod', metadata: null, spec: {} })
  })
})

describe('resourceToYaml', () => {
  it('returns empty string for null/undefined', () => {
    expect(resourceToYaml(null)).toBe('')
    expect(resourceToYaml(undefined)).toBe('')
  })

  it('round-trips through yaml.parse to the cleaned object', () => {
    const cleaned = cleanResourceForYaml(makePod())
    const yaml = resourceToYaml(makePod())
    expect(yamlParse(yaml)).toEqual(cleaned)
  })
})

describe('splitYamlDocuments', () => {
  it('uses the server document boundaries and drops empty segments', () => {
    expect(
      splitYamlDocuments(`apiVersion: v1
kind: ConfigMap
---

---
apiVersion: v1
kind: Service
`),
    ).toEqual([
      {
        content: 'apiVersion: v1\nkind: ConfigMap',
        startLine: 1,
        schemaIndex: 0,
      },
      {
        content: 'apiVersion: v1\nkind: Service',
        startLine: 5,
        schemaIndex: 2,
      },
    ])
  })
})

describe('normalizeYamlForReview', () => {
  it('strips server noise while preserving admitted defaults', () => {
    const normalized = normalizeYamlForReview(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  resourceVersion: "42"
spec:
  replicas: 2
status:
  availableReplicas: 2
`)
    expect(normalized).not.toContain('resourceVersion')
    expect(normalized).not.toContain('status:')
    expect(normalized).toContain('replicas: 2')
  })

  it('masks Secret payloads and annotations on every document', () => {
    const normalized = normalizeYamlForReview(`apiVersion: v1
kind: Secret
metadata:
  name: credentials
  annotations:
    token: annotation-secret
data:
  password: c2VjcmV0
`)
    expect(normalized).not.toContain('annotation-secret')
    expect(normalized).not.toContain('c2VjcmV0')
    expect(normalized).toContain('<redacted:unchanged>')
    expect(normalized).toContain('password:')
  })

  it('preserves server-issued differential markers while masking malformed Secret fields', () => {
    const normalized = normalizeYamlForReview(`apiVersion: v1
kind: Secret
metadata:
  name: credentials
  annotations: annotation-secret
data:
  changed: <redacted:after>
  raw: c2VjcmV0
stringData: plain-secret
`)
    expect(normalized).toContain('<redacted:after>')
    expect(normalized).toContain('<redacted:unchanged>')
    expect(normalized).not.toContain('annotation-secret')
    expect(normalized).not.toContain('c2VjcmV0')
    expect(normalized).not.toContain('plain-secret')
  })

  it('never returns an unparseable Secret draft verbatim', () => {
    const normalized = normalizeYamlForReview(`apiVersion: v1
kind: Secret
data:
  password: c2VjcmV0
broken: [
`)
    expect(normalized).toBe('# YAML could not be normalized safely for review.\n')
    expect(normalized).not.toContain('c2VjcmV0')
  })
})
