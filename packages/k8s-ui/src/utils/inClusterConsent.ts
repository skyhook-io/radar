// Consent memory for the mutating in-cluster reachability test (it creates a
// short-lived Job/pod). Keyed by an opaque source identity so a "don't ask again" on one cluster
// never silently suppresses the confirm on another - the cluster the pod lands
// in is the whole point of asking. Switching kube-context re-prompts.
//
// No source identity → no memory of ANY kind: the operator is asked on every
// run. localStorage is per-ORIGIN, and one origin (Radar Hub) can front many
// clusters, so a shared fallback key would let "don't ask again" on one cluster
// suppress the pod-creating confirm on all of them - and even an in-memory
// session flag is shared across every identity-less cluster in the session.
// The dialog hides its "don't ask again" checkbox in that case, so nothing is
// promised that doesn't stick.

const key = (clusterKey: string) => `radar.inClusterConsent.v2.${clusterKey}`

export function inClusterConsentGiven(clusterKey?: string): boolean {
  if (!clusterKey) return false
  try {
    return localStorage.getItem(key(clusterKey)) === '1'
  } catch {
    // localStorage unavailable (private mode / non-browser) - fail toward asking.
    return false
  }
}

export function rememberInClusterConsent(clusterKey?: string): void {
  if (!clusterKey) return
  try {
    localStorage.setItem(key(clusterKey), '1')
  } catch {
    // Non-fatal: if we can't persist, the user is simply asked again next time.
  }
}

/** One consent row per runnable route - the words the operator approves.
 *  This is the MUTATION CONSENT, so it must name the traffic the Job actually
 *  sends: a TCP-only candidate (redis, postgres) opens a socket and sends no
 *  request - presenting it as "GET http://database:6379/" asks approval for
 *  traffic that will never happen. Path overrides apply to HTTP(S) only. */
export function consentRequestRows(
  routes: Array<{
    route: string
    target?: string
    benign?: boolean
    inClusterRequest?: { protocol?: string; scheme?: string; host?: string; path?: string }
  }>,
  override: string,
): { route: string; request: string }[] {
  const rows: { route: string; request: string }[] = []
  for (const r of routes) {
    const req = r.inClusterRequest
    if (!req) continue
    // The runner skips benign (deliberately scaled-to-0) routes outright, so
    // listing them as requests overstates what the operator is agreeing to.
    if (r.benign) continue
    const dialled = r.target || ''
    if (req.protocol === 'tcp') {
      rows.push({ route: r.route, request: `TCP connection to ${dialled || req.host || ''}`.trim() })
      continue
    }
    const path = override || req.path || '/'
    // The probe dials the SERVICE and passes the hostname as a Host/SNI header.
    // Showing only the hostname put a public-looking address on a consent
    // screen for traffic that never leaves the cluster - so name the address
    // actually dialled, and the Host header as the header it is.
    const scheme = req.scheme || 'http'
    const addr = dialled ? `${scheme}://${dialled}${path}` : `${scheme}://${req.host ?? ''}${path}`
    const asHost = req.host && dialled ? ` (Host: ${req.host})` : ''
    rows.push({ route: r.route, request: `GET ${addr}${asHost}`.trim() })
  }
  return rows
}
