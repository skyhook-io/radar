import { describe, expect, it } from 'vitest'
import { getFluxOperatorColumnValue, getFluxOperatorStatus, redactUrlCredentials } from './resource-utils-flux-operator'

const ready = (status: string) => ({ conditions: [{ type: 'Ready', status }] })

describe('Flux Operator status', () => {
  it('reads the Ready condition', () => {
    expect(getFluxOperatorStatus({ status: ready('True') }).text).toBe('Ready')
    expect(getFluxOperatorStatus({ status: ready('False') }).text).toBe('Not Ready')
    expect(getFluxOperatorStatus({}).text).toBe('Unknown')
  })
})

describe('Flux Operator columns', () => {
  it('reads each kind’s columns', () => {
    const instance = { spec: { distribution: { version: '2.7.x' } }, status: { lastAppliedRevision: 'v2.7.5@sha256:abc', components: [{}, {}, {}] } }
    expect(getFluxOperatorColumnValue('fluxinstances', instance, 'version')).toBe('2.7.x')
    expect(getFluxOperatorColumnValue('fluxinstances', instance, 'applied')).toBe('v2.7.5@sha256:abc')
    expect(getFluxOperatorColumnValue('fluxinstances', instance, 'components')).toBe('3')
    const report = { spec: { distribution: { version: 'v2.7.5' }, operator: { version: 'v0.62.1' }, reconcilers: [{ stats: { failing: 2 } }, { stats: { failing: 1 } }, { stats: {} }] } }
    expect(getFluxOperatorColumnValue('fluxreports', report, 'version')).toBe('v2.7.5')
    expect(getFluxOperatorColumnValue('fluxreports', report, 'operator')).toBe('v0.62.1')
    expect(getFluxOperatorColumnValue('fluxreports', report, 'failing')).toBe('3')
    const set = { spec: { inputs: [{}, {}], inputsFrom: [{}] }, status: { inventory: { entries: [{}, {}, {}, {}] } } }
    expect(getFluxOperatorColumnValue('resourcesets', set, 'inputs')).toBe('3')
    expect(getFluxOperatorColumnValue('resourcesets', set, 'resources')).toBe('4')
    const provider = { spec: { type: 'GitHubPullRequest', url: 'https://user:token@github.com/org/repo' } }
    expect(getFluxOperatorColumnValue('resourcesetinputproviders', provider, 'type')).toBe('GitHubPullRequest')
    expect(getFluxOperatorColumnValue('resourcesetinputproviders', provider, 'url')).toBe('https://github.com/org/repo')
  })

  it('leaves other columns to the table', () => {
    expect(getFluxOperatorColumnValue('fluxinstances', {}, 'age')).toBeUndefined()
    expect(getFluxOperatorColumnValue('pods', {}, 'version')).toBeUndefined()
  })
})

describe('redactUrlCredentials', () => {
  it('drops user info', () => {
    expect(redactUrlCredentials('https://user:pass@example.com/x')).toBe('https://example.com/x')
    expect(redactUrlCredentials('https://example.com/x')).toBe('https://example.com/x')
    expect(redactUrlCredentials('oci://ghcr.io/org/chart')).toBe('oci://ghcr.io/org/chart')
  })

  it('never returns credentials it cannot parse', () => {
    expect(redactUrlCredentials('not a url with secret@host')).toBe('')
    expect(redactUrlCredentials('plain-text')).toBe('plain-text')
  })
})
