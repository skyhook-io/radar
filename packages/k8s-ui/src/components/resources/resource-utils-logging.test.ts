import { describe, expect, it } from 'vitest'
import { getLoggingFlowRules, getLoggingOutputAddress, getLoggingOutputType, getLoggingStatus, safeAddress } from './resource-utils-logging'

describe('Logging operator utils', () => {
  it('reduces an address to where the backend is', () => {
    expect(safeAddress('https://u:p@loki.example/push?t=1#f')).toBe('https://loki.example/push')
    expect(safeAddress('loki.svc:3100')).toBe('loki.svc:3100')
    expect(safeAddress('http://[bad')).toBe('[redacted]')
  })
  it('reads the output type and redacted address', () => {
    expect(getLoggingOutputType({ spec: { loggingRef: 'x', loki: {} } })).toBe('loki')
    expect(getLoggingOutputType({ spec: { protected: true } })).toBeNull()
    expect(getLoggingOutputAddress({ spec: { loki: { url: 'https://user:pw@loki.example/push?x=1' } } })).toBe('https://loki.example/push')
    expect(getLoggingOutputAddress({ spec: { s3: { s3_bucket: 'b' } } })).toBeNull()
  })
  it('lists flow rules with their conditions', () => {
    expect(getLoggingFlowRules({ spec: { match: [{ select: { labels: { a: '1' }, container_names: ['c'] } }, { exclude: { hosts: ['n1'] } }] } }))
      .toEqual([{ exclude: false, conditions: ['label a=1', 'container in [c]'] }, { exclude: true, conditions: ['node in [n1]'] }])
    expect(getLoggingFlowRules({ spec: { selectors: { app: 'x' } } })).toEqual([{ exclude: false, conditions: ['label app=x'] }])
    expect(getLoggingFlowRules({ spec: {} })).toEqual([])
  })
  it('reads active, inactive, problems and unknown', () => {
    expect(getLoggingStatus({ status: { active: true } }).text).toBe('Active')
    expect(getLoggingStatus({ status: { active: false } }).level).toBe('unhealthy')
    expect(getLoggingStatus({ status: { active: true, problemsCount: 2 } }).text).toBe('2 problems')
    expect(getLoggingStatus({}).level).toBe('unknown')
  })
})
