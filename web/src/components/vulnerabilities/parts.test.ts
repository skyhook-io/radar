import { describe, expect, it } from 'vitest'
import { ApiError } from '../../api/client'
import { hasVulnerabilityReports, isKindUnknown, parseWorkloadParam, vulnerabilitiesPath, workloadParam } from '../../api/vulnerabilities'
import { exposedCountTip, exposureGapSentence, exposureLookup, exposureUnknownIn, feedNote, feedStatusText, findingsCertainty, intelNotes, refreshCanHelp, gapSentence, imageParts, routesTip, summaryNames } from './parts'

describe('vulnerabilities helpers', () => {
  it('builds the request from filters', () => {
    expect(vulnerabilitiesPath({ view: 'cve', namespaces: ['shop', 'prod'], severities: ['critical', 'high'], fixableOnly: true, query: ' zlib ' }))
      .toBe('/vulnerabilities?view=cve&namespaces=shop%2Cprod&severity=critical%2Chigh&fixable=1&q=zlib')
    expect(vulnerabilitiesPath({ view: 'image', workload: { kind: 'Deployment', namespace: 'shop', name: 'web' } }))
      .toBe('/vulnerabilities?view=image&workloadKind=Deployment&workloadNamespace=shop&workloadName=web')
    expect(vulnerabilitiesPath({ view: 'fix', exposed: true })).toBe('/vulnerabilities?view=fix&exposed=1')
    expect(vulnerabilitiesPath({ view: 'fix', exposed: false })).toBe('/vulnerabilities?view=fix')
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
    const unknown = { extent: 'unknown' }
    expect(gapSentence([{ code: 'radar_namespace_not_collected', kinds: { count: 1, names: [{ kind: 'VulnerabilityReport', group: 'aquasecurity.github.io' }] }, namespaces: unknown }]))
      .toBe("Radar doesn't collect reports in some namespaces. Counts cover the rest.")
    expect(gapSentence([{ code: 'user_forbidden', kinds: { count: 1, names: [{ kind: 'Pod' }] }, namespaces: { count: 1, names: ['kube-system'] } }]))
      .toBe("Your access doesn't include some Pods in 1 namespace (kube-system); their scans aren't counted. Counts cover the rest.")
    // Without ReplicaSets only the workload names change; every count holds.
    expect(gapSentence([{ code: 'radar_kind_not_collected', kinds: { count: 1, names: [{ kind: 'ReplicaSet', group: 'apps' }] }, namespaces: unknown }]))
      .toBe("Radar doesn't collect ReplicaSets, so Deployments there are named and counted by their ReplicaSets.")
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

  const w = (name: string, container = 'app', namespace = 'shop') => ({ kind: 'Deployment', group: 'apps', namespace, name, container })
  const route = { via: 'LoadBalancer', namespace: 'kube-system', name: 'traefik' }

  it('summarizes workloads by distinct name', () => {
    const names = summaryNames([w('a'), w('a', 'sidecar'), w('b'), w('c'), w('d')])
    expect(names.names.map((n) => n.name)).toEqual(['a', 'b'])
    expect(names.rest).toBe(2)
  })

  it('lists exposed workloads first and marks only them, whatever the container', () => {
    const exposure = exposureLookup([{ kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'c', routes: [route] }])
    const names = summaryNames([w('a'), w('b'), w('c', 'sidecar')], exposure)
    expect(names.names.map((n) => [n.name, n.exposed?.routes])).toEqual([['c', [route]], ['a', undefined]])
    expect(names.rest).toBe(1)
    // Same name in another namespace is another workload.
    expect(summaryNames([w('c', 'app', 'other')], exposure).names[0].exposed).toBeUndefined()
  })

  it('says how many of a count have a route, listing the first few', () => {
    const exposed = ['a', 'b', 'c', 'd'].map((name) => ({ kind: 'Deployment', group: 'apps', namespace: 'shop', name, routes: [route] }))
    const tip = exposedCountTip(exposed, 5, 9)
    expect(tip.startsWith('5 of 9 workloads are exposed')).toBe(true)
    expect(tip).toContain('a (LoadBalancer kube-system/traefik)')
    expect(tip).not.toContain('d (')
    expect(tip.endsWith('and 2 more')).toBe(true)
    expect(exposedCountTip(exposed.slice(0, 1), 1, 5)).not.toContain('more')
    // A list read names three routes per workload and counts the rest.
    const capped = [{ ...exposed[0], routes: [route, route, route], routesTotal: 200 }]
    expect(exposedCountTip(capped, 1, 5)).toContain('and 197 more)')
    expect(routesTip(capped[0].routes, 200)).toMatch(/and 197 more$/)
  })

  it('reads a workload namespace as unknown only where an exposure gap covers it', () => {
    const ingress = { count: 1, names: [{ kind: 'Ingress', group: 'networking.k8s.io' }] }
    const listed = { code: 'user_forbidden', kinds: ingress, namespaces: { count: 2, names: ['shop', 'prod'] } }
    expect(exposureUnknownIn(undefined, 'shop')).toBe(false)
    expect(exposureUnknownIn([listed], 'shop')).toBe(true)
    expect(exposureUnknownIn([listed], 'dev')).toBe(false)
    expect(exposureUnknownIn([{ ...listed, namespaces: { extent: 'all' } }], 'dev')).toBe(true)
    expect(exposureUnknownIn([{ ...listed, namespaces: { extent: 'unknown' } }], 'dev')).toBe(true)
    // A capped list names 64 of 65: the 65th may be in it.
    expect(exposureUnknownIn([{ ...listed, namespaces: { count: 65, names: Array.from({ length: 64 }, (_, i) => `ns-${i}`) } }], 'ns-64')).toBe(true)
  })

  it('names the route kinds and namespaces exposure could not be read in', () => {
    const ingress = { count: 1, names: [{ kind: 'Ingress', group: 'networking.k8s.io' }] }
    expect(exposureGapSentence(undefined)).toBeNull()
    expect(exposureGapSentence([])).toBeNull()
    const two = exposureGapSentence([{ code: 'user_forbidden', kinds: ingress, namespaces: { count: 2, names: ['a', 'b'] } }])
    expect(two).toContain('Ingresses')
    expect(two).toContain('2 namespaces')
    expect(exposureGapSentence([{ code: 'radar_kind_not_collected', kinds: { count: 2, names: [{ kind: 'HTTPRoute' }, { kind: 'GRPCRoute' }] }, namespaces: { extent: 'all' } }]))
      .toContain("couldn't read HTTPRoutes and GRPCRoutes, so")
    const hidden = exposureGapSentence([{ code: 'user_forbidden', kinds: { count: 1, names: [{ kind: 'HTTPRoute' }] }, namespaces: { count: 1, names: ['shop'] } }])
    expect(hidden).not.toContain("Radar couldn't read")
    expect(hidden).toContain('HTTPRoutes')
    expect(hidden).toContain('1 namespace')
  })
})

describe('feed status', () => {
  const age = (iso: string) => (iso === '2026-11-24T10:00:00Z' ? '3m ago' : '?')
  it('says which catalog and score date Radar holds and when it checked', () => {
    expect(feedStatusText({ kev: { state: 'ready', version: '2026.11.20', checkedAt: '2026-11-24T10:00:00Z' }, epss: { state: 'ready', version: '2026-11-22' } }, age))
      .toBe('CISA catalog 2026.11.20, checked 3m ago. EPSS scores from 2026-11-22.')
  })
  it('says why a stale copy is stale', () => {
    expect(feedStatusText({ kev: { state: 'ready', version: '2026.11.20', checkedAt: '2026-11-24T10:00:00Z', stale: true, error: 'the feed answered HTTP 503' }, epss: { state: 'unavailable' } }, age))
      .toBe("CISA catalog 2026.11.20, checked 3m ago. Out of date: Radar couldn't refresh it (the feed answered HTTP 503).")
  })
})

describe('without a feed', () => {
  it('explains what the feed adds and how to provide it, calmly', () => {
    const air = feedNote('KEV', { state: 'unavailable', origin: 'public', error: 'dial tcp: i/o timeout' })!
    expect(air).toContain('--kev-feed')
    expect(air).not.toContain('i/o timeout')
    expect(air).toContain('vulnerabilities.kevFeed (Helm)')
    expect(feedNote('KEV', { state: 'disabled' })).toContain('turned off')
    expect(feedNote('KEV', { state: 'disabled', downloadsOff: true })).toBe("Feed downloads are turned off on this Radar. To mark the CVEs that are known to be exploited, put a copy of CISA's Known Exploited Vulnerabilities catalog in a file and set vulnerabilities.kevFeed (Helm) or --kev-feed to its path.")
    expect(feedNote('KEV', { state: 'unavailable', origin: 'mirror', error: 'HTTP 404' })).toContain('HTTP 404')
    // The public server answering an error is not a network without internet.
    const answered = feedNote('KEV', { state: 'unavailable', origin: 'public', error: 'the feed answered HTTP 503' })!
    expect(answered).not.toContain('internet')
    expect(answered).toContain('HTTP 503')
    expect(intelNotes({ kev: { state: 'unavailable', origin: 'public' }, epss: { state: 'disabled' } })).toEqual([])
  })
  it('offers Refresh only where a reload could change something', () => {
    expect(refreshCanHelp({ kev: { state: 'unavailable', origin: 'public' }, epss: { state: 'disabled' } })).toBe(false)
    expect(refreshCanHelp({ kev: { state: 'unavailable', origin: 'mirror' }, epss: { state: 'disabled' } })).toBe(true)
    expect(refreshCanHelp({ kev: { state: 'ready', origin: 'public' }, epss: { state: 'unavailable', origin: 'public' } })).toBe(true)
  })
})
