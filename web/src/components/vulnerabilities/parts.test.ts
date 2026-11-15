import { describe, expect, it } from 'vitest'
import { ApiError } from '../../api/client'
import { hasVulnerabilityReports, isKindUnknown, parseWorkloadParam, vulnerabilitiesPath, workloadParam } from '../../api/vulnerabilities'
import { findingsCertainty, gapSentence, imageParts, workloadSummary } from './parts'

describe('vulnerabilities helpers', () => {
  it('builds the request from filters', () => {
    expect(vulnerabilitiesPath({ view: 'cve', namespaces: ['shop', 'prod'], severities: ['critical', 'high'], fixableOnly: true, query: ' zlib ' }))
      .toBe('/vulnerabilities?view=cve&namespaces=shop%2Cprod&severity=critical%2Chigh&fixable=1&q=zlib')
    expect(vulnerabilitiesPath({ view: 'image', workload: { kind: 'Deployment', namespace: 'shop', name: 'web' } }))
      .toBe('/vulnerabilities?view=image&workloadKind=Deployment&workloadNamespace=shop&workloadName=web')
  })

  it('round-trips the workload filter and rejects anything else', () => {
    const w = { kind: 'Deployment', namespace: 'shop', name: 'checkout' }
    expect(parseWorkloadParam(workloadParam(w))).toEqual(w)
    expect(parseWorkloadParam('checkout')).toBeUndefined()
    expect(parseWorkloadParam('Deployment//checkout')).toBeUndefined()
    expect(parseWorkloadParam(null)).toBeUndefined()
  })

  it('reads a 404 as no scanner only when the handler says the kind is unknown', () => {
    expect(isKindUnknown(new ApiError('no kind', 404, { error_code: 'kind_unknown' }))).toBe(true)
    expect(isKindUnknown(new ApiError('not found', 404))).toBe(false)
    expect(isKindUnknown(new ApiError('boom', 500, { error_code: 'kind_unknown' }))).toBe(false)
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
    const unknown = { count: 0, extent: 'unknown' }
    expect(gapSentence([{ code: 'radar_namespace_not_collected', kinds: { count: 1, names: [{ kind: 'VulnerabilityReport', group: 'aquasecurity.github.io' }] }, namespaces: unknown }]))
      .toBe("Radar doesn't collect reports in some namespaces. Counts cover the rest.")
    expect(gapSentence([{ code: 'user_forbidden', kinds: { count: 1, names: [{ kind: 'Pod' }] }, namespaces: { count: 1, names: ['kube-system'] } }]))
      .toBe("Your access doesn't include some Pods in 1 namespace (kube-system); their scans aren't counted. Counts cover the rest.")
    // Without ReplicaSets only the workload names change; every count holds.
    expect(gapSentence([{ code: 'radar_kind_not_collected', kinds: { count: 1, names: [{ kind: 'ReplicaSet', group: 'apps' }] }, namespaces: unknown }]))
      .toBe("Radar doesn't collect ReplicaSets, so Deployments there are named by their ReplicaSets.")
    // A ReplicaSet the caller may not read withholds its scans.
    expect(gapSentence([{ code: 'user_forbidden', kinds: { count: 1, names: [{ kind: 'ReplicaSet', group: 'apps' }] }, namespaces: { count: 1, names: ['shop'] } }]))
      .toBe("Your access doesn't include some ReplicaSets in 1 namespace (shop); their scans aren't counted. Counts cover the rest.")
  })

  it('keeps totals exact when only rows are capped or names change', () => {
    const summary = { imagesScanned: 3, imagesUnscanned: 0 } as Parameters<typeof findingsCertainty>[0]['summary']
    const coverage = { complete: false, gaps: [], inactiveReports: 0, rowsOmitted: 40 }
    expect(findingsCertainty({ summary, coverage })).toBe('exact')
    const rs = { code: 'radar_kind_not_collected', kinds: { count: 1, names: [{ kind: 'ReplicaSet' }] } }
    expect(findingsCertainty({ summary, coverage: { ...coverage, gaps: [rs] } })).toBe('exact')
    expect(findingsCertainty({ summary, coverage: { ...coverage, gaps: [{ code: 'user_forbidden' }] } })).toBe('lower_bound')
    expect(findingsCertainty({ summary, coverage: { ...coverage, gaps: [{ ...rs, code: 'user_forbidden' }] } })).toBe('lower_bound')
  })

  it('leads an image with the part that tells images apart', () => {
    expect(imageParts('us-central1-docker.pkg.dev/online-boutique-ci/microservices-demo/currencyservice:v0.10.7'))
      .toEqual({ name: 'currencyservice:v0.10.7', path: 'us-central1-docker.pkg.dev/online-boutique-ci/microservices-demo' })
    expect(imageParts('redis:alpine')).toEqual({ name: 'redis:alpine', path: '' })
    expect(imageParts('ghcr.io/acme/app:1@sha256:abc').name).toBe('app:1')
  })

  it('summarizes workloads by distinct name', () => {
    const w = (name: string, container = 'app') => ({ kind: 'Deployment', group: 'apps', namespace: 'shop', name, container })
    expect(workloadSummary([w('a'), w('a', 'sidecar'), w('b'), w('c'), w('d')])).toBe('a, b +2')
  })
})
