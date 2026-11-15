import { describe, expect, it } from 'vitest'
import {
  getArtifactGeneratorArtifacts,
  getArtifactGeneratorCount,
  getArtifactGeneratorSources,
  getArtifactGeneratorStatus,
  getExternalArtifactGenerator,
  getExternalArtifactRevision,
  getExternalArtifactStatus,
  shortArtifactRevision,
} from './resource-utils-flux'
import { isColumnSortable } from './ResourcesView'

const ready = (status: string) => ({ conditions: [{ type: 'Ready', status }] })

describe('ExternalArtifact helpers', () => {
  const ea = {
    apiVersion: 'source.toolkit.fluxcd.io/v1', kind: 'ExternalArtifact',
    spec: { sourceRef: { apiVersion: 'source.extensions.fluxcd.io/v1beta1', kind: 'ArtifactGenerator', name: 'gen' } },
    status: { ...ready('True'), artifact: { revision: 'latest@sha256:abc', size: 2048 } },
  }

  it('reads status, revision and generator', () => {
    expect(getExternalArtifactStatus(ea).text).toBe('Ready')
    expect(getExternalArtifactStatus({ ...ea, status: ready('False') }).text).toBe('Not Ready')
    expect(getExternalArtifactStatus({ ...ea, status: undefined }).text).toBe('Unknown')
    expect(getExternalArtifactRevision(ea)).toBe('latest@sha256:abc')
    expect(getExternalArtifactGenerator(ea)).toBe('gen')
  })

  it('names no generator for another producer', () => {
    expect(getExternalArtifactGenerator({ ...ea, spec: { sourceRef: { kind: 'Something', name: 'x' } } })).toBe('')
    expect(getExternalArtifactGenerator({ ...ea, spec: { sourceRef: { apiVersion: 'example.com/v1', kind: 'ArtifactGenerator', name: 'x' } } })).toBe('')
    expect(getExternalArtifactGenerator({ ...ea, spec: { sourceRef: { kind: 'ArtifactGenerator', name: 'gen' } } })).toBe('gen')
  })

  it('shortens a digest revision the same way everywhere', () => {
    expect(shortArtifactRevision('latest@sha256:53884aebf2e5aa11223344')).toBe('latest@53884aebf2e5')
    expect(shortArtifactRevision('5.5.0')).toBe('5.5.0')
  })

  it('sorts the artifacts count but leaves size to the kinds that define it', () => {
    expect(isColumnSortable({ key: 'artifacts', label: 'Artifacts' })).toBe(true)
  })
})

describe('ArtifactGenerator helpers', () => {
  const ag = {
    apiVersion: 'source.extensions.fluxcd.io/v1beta1', kind: 'ArtifactGenerator',
    spec: { sources: [{ alias: 'repo', kind: 'GitRepository', name: 'platform' }, { kind: 'OCIRepository', name: 'charts' }] },
    status: { ...ready('True'), inventory: [{ name: 'a' }, { name: 'b' }, { name: 'c' }] },
  }

  it('reads status, sources and the generated count', () => {
    expect(getArtifactGeneratorStatus(ag).text).toBe('Ready')
    expect(getArtifactGeneratorStatus({ ...ag, status: undefined }).text).toBe('Unknown')
    expect(getArtifactGeneratorSources(ag)).toBe('repo→GitRepository/platform, OCIRepository/charts')
    expect(getArtifactGeneratorCount(ag)).toBe(3)
    expect(getArtifactGeneratorCount({ ...ag, status: { inventory: [] } })).toBe(0)
  })

  it('counts only artifacts, and nothing when Radar has no inventory', () => {
    // source-watcher 2.3 also records the namespaces it manages; earlier entries have no kind.
    const mixed = { ...ag, status: { inventory: [{ kind: 'Namespace', name: 'team-a' }, { kind: 'ExternalArtifact', name: 'a', namespace: 'team-a' }, { name: 'b' }] } }
    expect(getArtifactGeneratorCount(mixed)).toBe(2)
    expect(getArtifactGeneratorArtifacts(mixed)?.map((a) => a.name)).toEqual(['a', 'b'])
    expect(getArtifactGeneratorCount({ ...ag, status: {} })).toBeUndefined()
    expect(getArtifactGeneratorArtifacts({ ...ag, status: {} })).toBeUndefined()
  })
})
