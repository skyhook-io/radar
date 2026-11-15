import { describe, expect, it } from 'vitest'
import { hasVulnerabilityReports, vulnerabilitiesPath } from '../../api/vulnerabilities'
import { gapSentence, workloadSummary } from './parts'

describe('vulnerabilities helpers', () => {
  it('builds the request from filters', () => {
    expect(vulnerabilitiesPath({ view: 'cve', namespaces: ['shop', 'prod'], severities: ['critical', 'high'], fixableOnly: true, query: ' zlib ' }))
      .toBe('/vulnerabilities?view=cve&namespaces=shop%2Cprod&severity=critical%2Chigh&fixable=1&q=zlib')
    expect(vulnerabilitiesPath({ view: 'image', workload: { kind: 'Deployment', namespace: 'shop', name: 'web' } }))
      .toBe('/vulnerabilities?view=image&workloadKind=Deployment&workloadNamespace=shop&workloadName=web')
  })

  it('detects the report kind by group and plural, not by kind name alone', () => {
    expect(hasVulnerabilityReports(undefined)).toBe(false)
    expect(hasVulnerabilityReports([{ name: 'vulnerabilityreports', kind: 'VulnerabilityReport', group: 'example.com', version: 'v1', namespaced: true, verbs: [], isCrd: true }])).toBe(false)
    expect(hasVulnerabilityReports([{ name: 'vulnerabilityreports', kind: 'VulnerabilityReport', group: 'aquasecurity.github.io', version: 'v1alpha1', namespaced: true, verbs: [], isCrd: true }])).toBe(true)
  })

  it('names withheld namespaces and says counts cover the rest', () => {
    expect(gapSentence([])).toBeNull()
    expect(gapSentence([{ code: 'user_forbidden', namespaces: { count: 2, names: ['a', 'b'] } }]))
      .toBe("Your access doesn't include the reports in 2 namespaces (a, b). Counts cover the rest.")
  })

  it('summarizes workloads by distinct name', () => {
    const w = (name: string, container = 'app') => ({ kind: 'Deployment', group: 'apps', namespace: 'shop', name, container })
    expect(workloadSummary([w('a'), w('a', 'sidecar'), w('b'), w('c'), w('d')])).toBe('a, b +2')
  })
})
