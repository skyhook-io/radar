import { describe, expect, it } from 'vitest'
import { fluxVersionAtLeast, imageTag, pickFluxReport, summarizeFluxReport } from './flux-report'

const report = (version: string, reconcilers: any[]) => ({
  metadata: { namespace: 'flux-system', name: 'flux' },
  spec: {
    distribution: { version, managedBy: 'flux-operator' }, operator: { version: 'v0.62.1' },
    components: [{ name: 'helm-controller', image: 'ghcr.io/fluxcd/helm-controller:v1.4.5@sha256:abc', ready: true }, { name: 'kustomize-controller', image: 'x:v1', ready: false }],
    reconcilers,
  },
  status: { conditions: [{ type: 'Ready', status: 'True', lastTransitionTime: '2026-11-12T09:00:00Z' }] },
})
const rec = (kind: string, running = 0, failing = 0, suspended = 0) => ({ kind, stats: { running, failing, suspended } })

describe('imageTag', () => {
  it('takes the tag before a digest and ignores a registry port', () => {
    expect(imageTag('ghcr.io/fluxcd/helm-controller:v1.4.5@sha256:abc')).toBe('v1.4.5')
    expect(imageTag('registry:5000/fluxcd/helm-controller')).toBeUndefined()
    expect(imageTag('helm-controller')).toBeUndefined()
  })
})

describe('fluxVersionAtLeast', () => {
  it('compares major.minor and says when it cannot tell', () => {
    expect(fluxVersionAtLeast('v2.7.5', 2, 8)).toBe(false)
    expect(fluxVersionAtLeast('v2.8.0', 2, 8)).toBe(true)
    expect(fluxVersionAtLeast('2.10.1', 2, 8)).toBe(true)
    expect(fluxVersionAtLeast('latest', 2, 8)).toBeNull()
    expect(fluxVersionAtLeast(undefined, 2, 8)).toBeNull()
  })
})

describe('pickFluxReport', () => {
  it('picks the first by namespace and name', () => {
    expect(pickFluxReport([])).toBeNull()
    const picked = pickFluxReport([{ metadata: { namespace: 'b', name: 'flux' } }, { metadata: { namespace: 'a', name: 'flux' } }])
    expect(picked?.report.metadata?.namespace).toBe('a')
    expect(picked?.total).toBe(2)
  })
})

describe('summarizeFluxReport', () => {
  it('orders failing, then suspended, then running; drops empty kinds; folds small running kinds', () => {
    const s = summarizeFluxReport(report('v2.7.5', [
      rec('GitRepository', 1), rec('Bucket'), rec('HelmRelease', 140, 0, 3), rec('Kustomization', 20, 2), rec('ArtifactGenerator', 1), rec('OCIRepository', 30),
    ]), 1)
    expect(s.rows.map((r) => r.kind)).toEqual(['Kustomization', 'HelmRelease', 'OCIRepository'])
    expect(s.also).toEqual([{ kind: 'GitRepository', running: 1 }, { kind: 'ArtifactGenerator', running: 1 }])
    expect(s.failing).toBe(2)
    expect(s.controllersReady).toBe(1)
    expect(s.controllers[0]).toEqual({ name: 'helm-controller', tag: 'v1.4.5', ready: true })
    expect(s.reportedAt).toBe('2026-11-12T09:00:00Z')
    expect(s.version).toBe('v2.7.5')
    expect(s.operatorVersion).toBe('v0.62.1')
  })

  it('notes the 2.8 inventory requirement only for older Flux with HelmReleases', () => {
    expect(summarizeFluxReport(report('v2.7.5', [rec('HelmRelease', 1)]), 1).needsInventoryNote).toBe(true)
    expect(summarizeFluxReport(report('v2.7.5', [rec('HelmRelease', 0, 0, 1)]), 1).needsInventoryNote).toBe(true)
    expect(summarizeFluxReport(report('v2.8.0', [rec('HelmRelease', 1)]), 1).needsInventoryNote).toBe(false)
    expect(summarizeFluxReport(report('v2.7.5', [rec('Kustomization', 4)]), 1).needsInventoryNote).toBe(false)
    expect(summarizeFluxReport(report('dev', [rec('HelmRelease', 1)]), 1).needsInventoryNote).toBe(false)
  })

  it('carries the report count', () => {
    const s = summarizeFluxReport(report('v2.7.5', []), 2)
    expect(s.total).toBe(2)
  })
  it('leaves controller and failing counts unknown when the report has none', () => {
    const s = summarizeFluxReport({ spec: { distribution: { version: 'v2.7.5' } } }, 1)
    expect(s.controllersReady).toBeUndefined()
    expect(s.failing).toBeUndefined()
  })
})
