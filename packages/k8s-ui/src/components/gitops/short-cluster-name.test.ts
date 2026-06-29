import { describe, test, expect } from 'vitest'

import { shortClusterName } from './GitOpsTableView'

// shortClusterName strips kubectl-context-style prefixes so the Cluster
// column shows the human-recognizable suffix. The orphan-empty-string
// fallback ("|| full") is the function's main contract — without it,
// malformed inputs would render as empty cells. These tests pin both
// the happy paths and the fallback.

describe('shortClusterName', () => {
  test('GKE kubectl context strips to trailing name', () => {
    expect(shortClusterName('gke_koalabackend_me-west1-a_management-cluster')).toBe('management-cluster')
    expect(shortClusterName('gke_proj_us-east1-b_nonprod')).toBe('nonprod')
  })

  test('EKS ARN strips to suffix after final slash', () => {
    expect(shortClusterName('arn:aws:eks:us-east-1:123456789:cluster/prod-east')).toBe('prod-east')
  })

  test('user-named clusters (kind, k3d, plain) pass through unchanged', () => {
    expect(shortClusterName('k3d-radar-demo')).toBe('k3d-radar-demo')
    expect(shortClusterName('kind-radar-gitops-demo')).toBe('kind-radar-gitops-demo')
    expect(shortClusterName('production')).toBe('production')
  })

  // Malformed-input fallback: the function must not return empty/garbage
  // for inputs that "look like" a recognized prefix but don't have a
  // trailing segment.
  test('malformed GKE prefix with trailing underscore falls back to full string', () => {
    expect(shortClusterName('gke_proj_region_')).toBe('gke_proj_region_')
  })
  test('malformed EKS ARN with trailing slash falls back to full string', () => {
    expect(shortClusterName('arn:aws:eks:us-east-1:123:cluster/')).toBe('arn:aws:eks:us-east-1:123:cluster/')
  })
})
