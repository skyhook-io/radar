import type { ProbeResult, ProbeLayer } from './types'
import type { StatusTone } from '../ui/status-tone'
import { hostFromTarget } from './reachVerdict'

// A Pods-hop probe target is "<name> port <N>" (apiserver path) or "<ip>:<port>"
// (data path). The pod identity is the first token with any :port stripped, which
// joins back to the roster's name (apiserver) or ip (data). hostFromTarget is
// IPv6-aware for the ip:port form.
export function podProbeKey(target?: string): string {
  return hostFromTarget((target || '').split(' ')[0])
}

/**
 * Summarizes ONE pod's probe results into a single reachability cell: its worst
 * live outcome (a failure wins, so a bad pod is never masked), else the deepest
 * layer it reached, with the vantage that produced it.
 */
export function podReach(probes: ProbeResult[]): { tone: StatusTone; text: string; vantage: string } {
  const live = probes.filter((p) => !p.skipped)
  if (live.length === 0) return { tone: 'neutral', text: 'not tested', vantage: '' }
  const vantage = live.some((p) => p.vantage === 'in-cluster') ? 'real' : live.some((p) => p.path === 'apiserver') ? 'via API server' : ''
  const failed = live.find((p) => !p.ok || p.tone === 'unhealthy')
  if (failed) return { tone: 'unhealthy', text: failed.detail || failed.error || `${failed.layer.toUpperCase()} failed`, vantage }
  const order: ProbeLayer[] = ['http', 'tls', 'tcp', 'dns']
  const best = order.map((L) => live.find((p) => p.layer === L && p.ok)).find(Boolean)
  // Only a real-traffic HTTP 2xx is "verified" (green). A reached 3xx/4xx (tone
  // 'reached') or a transport-only TCP/TLS/DNS success proves the port answers, not
  // that the real path serves - so it reads neutral ("reached, not verified"),
  // matching the route pills, instead of a green dot the headline contradicts.
  const tone: StatusTone =
    best?.tone === 'degraded'
      ? 'degraded'
      : best?.layer === 'http' && best?.tone !== 'reached'
        ? 'healthy'
        : 'neutral'
  return { tone, text: best?.detail || 'reached', vantage }
}
