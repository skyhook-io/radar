import { describe, expect, it } from 'vitest'
import {
  getArtifactGeneratorCount,
  getArtifactGeneratorSources,
  getArtifactGeneratorStatus,
  getExternalArtifactGenerator,
  getExternalArtifactRevision,
  getExternalArtifactStatus,
} from './resource-utils-flux'

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
    expect(getArtifactGeneratorCount({ ...ag, status: {} })).toBe(0)
  })
})
