import { describe, expect, it } from 'vitest'
import { coverageEmpty, coverageLabel, incompleteKindsText } from './shared'

describe('coverage wording', () => {
  it('names every cause the server named, whatever the overall state', () => {
    const mixed = { state: 'uncached' as const, uncachedNamespaces: ['b'], deniedNamespaces: ['c'] }
    expect(coverageEmpty(mixed, 'Pods')).toBe('No Pods visible: no access in c; Radar does not cache Pods in b.')
    expect(coverageLabel(mixed)).toBe('no access in c; not cached by Radar in b')
  })
  it('falls back to the state when no namespace is named', () => {
    expect(coverageEmpty({ state: 'uncached' }, 'Pods')).toBe('Radar does not cache Pods in this scope.')
    expect(coverageEmpty({ state: 'partial' }, 'Pods')).toBe('No Pods visible. Some namespaces were not read.')
    expect(coverageLabel({ state: 'denied' })).toBe('no access')
  })
  it('says each reason once for the kinds it applies to', () => {
    const text = incompleteKindsText(['clusters', 'backups', 'clusterImageCatalogs'], {
      clusters: { state: 'partial' },
      backups: { state: 'partial' },
      clusterImageCatalogs: { state: 'uncached' },
    })
    expect(text).toBe('Cluster, Backup (not read in some namespaces); ClusterImageCatalog (not cached by Radar)')
  })
  it("lists Jobs it could not read, since a Cluster's Job Pods are read only where they are", () => {
    expect(incompleteKindsText(['clusters'], { clusters: { state: 'partial' } }, { state: 'denied' })).toBe('Cluster (not read in some namespaces); Job (no access)')
    expect(incompleteKindsText([], {}, { state: 'full' })).toBe('')
  })
})
