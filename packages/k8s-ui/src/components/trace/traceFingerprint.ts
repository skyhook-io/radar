import type { Trace, Hop, HopConfig, Finding } from './types'

// traceFingerprint reduces a trace to a cheap, stable string over its
// PROBE-INVARIANT content: the static-derived cluster state the trace was built
// from. The invariant that makes it useful: a ?probe=true trace (including the
// in-cluster merged trace) and a ?probe=false trace built from the SAME cluster
// state produce IDENTICAL fingerprints. That lets the probe response ITSELF be
// the staleness baseline - it embeds the exact static content the probe ran
// against - and each later static poll is compared against it; a mismatch means
// the cluster changed under the snapshot.
//
// Included (identical across the two shapes, verified against internal/trace):
//   - hop identity: kind / namespace / name / edge (hops are built by the
//     static walk; probing never adds or removes one)
//   - meta.ready / meta.selected (endpoint counts from the cache)
//   - finding code:severity (sorted - probing re-sorts findings)
//   - declared route identity from hop config: service ports, serviceType,
//     hostnames, TLS hosts, ingress/route rules (host+path+backend), gateway
//     listeners
//   - config IDENTITY the static walk sets but probing never touches: the
//     Service selector, clusterIP, and declared addresses (LB/ExternalName).
//     A selector flip (app=a→app=b) can leave ready/selected counts EQUAL, so
//     without the selector the fingerprint would miss a real backend-set change
//     and a stale snapshot would keep vouching for the old backend set.
//
// Excluded, because probing mutates them and a same-state pair would diverge:
//   - probes (only ?probe=true carries them) and verdict / brokenAt / reason /
//     headline / coverage / diagnosis (reviseVerdictWithProbes + the coverage
//     collapse rewrite them from probe outcomes)
//   - routes / notTested: routeFromProbes only emits a route once a probe ran,
//     so a static trace carries a different (usually empty) route list than a
//     probed one - the declared config rules above carry route identity instead
//   - probe:* findings (attachPathDivergenceFindings adds them only when
//     probes attach)
//   - netpol:would-deny (reconcilePolicyFindings / reconcileInClusterPolicy
//     rewrite its severity and can remove it outright once live traffic
//     settles the prediction). KNOWN BLIND SPOT: because this finding is
//     necessarily excluded (probing rewrites/removes it), a NetworkPolicy
//     APPLIED AFTER a probe snapshot won't move the fingerprint and so won't
//     invalidate the snapshot - an accepted limitation of the client-side
//     fingerprint. The deferred server-side static-state version/etag will
//     resolve it (the server sees the policy add directly).
//   - the SEVERITY of svc:targetport-no-listener (reconcileTargetPortAdvisory
//     downgrades warning → info when a live probe reaches the suspect port);
//     its presence is stable, so the code still counts
//   - finding messages (free-form text can embed volatile detail)
//   - pod rosters (config.pods/podNames/podIPs/containerPorts) - name churn
//     from ordinary pod replacement isn't a state change the counts and
//     findings don't already capture

const findingsFp = (findings: Finding[] | undefined): string =>
  (findings ?? [])
    .filter((f) => !(f.code ?? '').startsWith('probe:') && f.code !== 'netpol:would-deny')
    .map((f) => (f.code === 'svc:targetport-no-listener' ? f.code : `${f.code}:${f.severity}`))
    .sort()
    .join('+')

const configFp = (c: HopConfig | undefined): string => {
  if (!c) return ''
  // name + appProtocol decide HTTP-vs-HTTPS-vs-TCP for the in-cluster request,
  // so an edit to them MUST invalidate the trace: consent renders from the
  // displayed trace while the server re-derives fresh state at run time, and a
  // fingerprint blind to these fields let consent describe different traffic
  // than the Job then sent.
  const ports = (c.ports ?? []).map((p) => `${p.port}>${p.targetPort ?? ''}/${p.protocol ?? ''}/${p.name ?? ''}/${p.appProtocol ?? ''}`).join(',')
  const rules = (c.rules ?? [])
    .map((r) =>
      [
        (r.hosts ?? []).join(','),
        (r.paths ?? []).join(','),
        (r.backends ?? []).map((b) => `${b.kind}/${b.namespace ?? ''}/${b.name}:${b.port ?? ''}`).join(','),
      ].join('~'),
    )
    .join(';')
  const listeners = (c.listeners ?? []).map((l) => `${l.name ?? ''}/${l.port}/${l.protocol ?? ''}/${l.hostname ?? ''}`).join(',')
  const selector = Object.keys(c.selector ?? {})
    .sort()
    .map((k) => `${k}=${c.selector![k]}`)
    .join(',')
  const addresses = (c.addresses ?? []).join(',')
  return [c.serviceType ?? '', ports, (c.hostnames ?? []).join(','), (c.tlsHosts ?? []).join(','), rules, listeners, c.clusterIP ?? '', selector, addresses].join('^')
}

const hopFp = (h: Hop): string =>
  [
    h.resource.kind,
    h.resource.namespace ?? '',
    h.resource.name,
    h.edge,
    String(h.meta?.ready ?? ''),
    String(h.meta?.selected ?? ''),
    findingsFp(h.findings),
    configFp(h.config),
  ].join('|')

export function traceFingerprint(t: Trace): string {
  return [(t.downstream ?? []).map(hopFp).join(';'), (t.upstreams ?? []).map(hopFp).join(';')].join('#')
}

// staticPollUnreliable reports whether a fresh static poll is too degraded to
// serve as staleness evidence against a probe snapshot. BuildTraceWithOptions
// returns HTTP 200 even when it could NOT fully build the trace:
//   - a total-budget timeout mid-walk yields a PARTIAL trace (fewer hops) with
//     verdict 'unknown' + unknownClass 'investigate' (internal/trace/trace.go)
//   - a transient pod-lister / RBAC read failure stamps a hop
//     meta.endpointSource === 'unknown' and untrustworthy ready/selected counts
//     (internal/trace/entries.go)
// Both shift the fingerprint spuriously, so a mismatch against such a poll is
// NOT evidence the cluster changed - the staleness mask must SKIP the comparison
// and keep the snapshot. A poll we couldn't fully build proves nothing.
//
// A 'by-design' unknown (selectorless Service) is a STABLE read and stays
// comparable; only the 'investigate' class means we tried and couldn't read
// state. Trace.truncated is a DETERMINISTIC fan-out cap (large Gateway/Ingress),
// not a transient degradation - it fingerprints consistently poll-to-poll, so it
// is deliberately NOT treated as unreliable (doing so would permanently disable
// change detection for a truncated subject).
export function staticPollUnreliable(t: Trace): boolean {
  if (t.verdict === 'unknown' && t.unknownClass === 'investigate') return true
  const hops = [...(t.downstream ?? []), ...(t.upstreams ?? [])]
  return hops.some((h) => h.meta?.endpointSource === 'unknown')
}
