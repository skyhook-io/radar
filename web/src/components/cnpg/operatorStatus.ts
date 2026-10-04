import type { CNPGOperatorComponent, CNPGOperatorResponse, CNPGOperatorVerdict } from '../../api/cnpg'
import { formatAge } from '@skyhook-io/k8s-ui'

export interface CNPGOperatorBannerModel {
  /** The operator watching these namespaces is not leading, so their CNPG status may be stale. */
  stale: { reasons: string[]; namespaces: string[] } | null
  /** Why the admission webhook rejects CNPG writes, when it does. */
  rejects: string | null
}

function sentence(s: string): string {
  const t = s.trim()
  if (!t) return t
  return t[0].toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? '' : '.')
}

/**
 * What the fleet and cluster pages say about the operator for the namespaces
 * they show. Only an observed "not leading" or "webhook rejects" raises
 * anything: an unknown or unwatched state stays quiet here and the Operator
 * screen explains it.
 */
export function cnpgOperatorBannerModel(verdicts: Record<string, CNPGOperatorVerdict> | undefined, namespaces: string[]): CNPGOperatorBannerModel | null {
  if (!verdicts) return null
  const staleNs: string[] = []
  const reasons = new Set<string>()
  let rejects: string | null = null
  for (const ns of [...new Set(namespaces)].sort()) {
    const v = verdicts[ns]
    if (!v) continue
    if (v.state === 'notReconciling') {
      staleNs.push(ns)
      for (const r of v.reasons ?? []) reasons.add(sentence(r))
    }
    if (v.webhookRejects === true && v.webhookReason) rejects = sentence(v.webhookReason)
  }
  if (staleNs.length === 0 && !rejects) return null
  return { stale: staleNs.length > 0 ? { reasons: [...reasons], namespaces: staleNs } : null, rejects }
}

/** A dialog's note about the operator: a warning when it is observed down, a note when unknown. */
export function cnpgOperatorActionNote(v: CNPGOperatorVerdict | undefined): { tone: 'warning' | 'info'; text: string } | null {
  if (!v) return null
  if (v.state === 'notReconciling') {
    const why = (v.reasons ?? []).map(sentence).join(' ')
    return {
      tone: 'warning',
      text: `The CloudNativePG operator is not reconciling this cluster. ${why} The API server may accept this change, but nothing acts on it until an operator instance leads again.`.replace(/\s+/g, ' ').trim(),
    }
  }
  if (v.state === 'unknown' && v.unknown) {
    return { tone: 'info', text: `Radar couldn't confirm the operator is running (${v.unknown}).` }
  }
  return null
}

/** A restart that ended within this long is "recent": the operator may still be crash-looping. */
export const CNPG_OPERATOR_RECENT_RESTART_MS = 60 * 60 * 1000

export interface CNPGOperatorConcern {
  tone: 'unhealthy' | 'degraded' | 'neutral'
  text: string
}

/** A component's restart history in one line, with when the last one ended and why; null when it never restarted. */
export function cnpgRestartHistory(c: Pick<CNPGOperatorComponent, 'pods'>, now = Date.now()): { text: string; recent: boolean } | null {
  const pods = c.pods ?? []
  const restarts = pods.reduce((n, p) => n + p.restarts, 0)
  if (restarts === 0) return null
  const last = pods
    .map((p) => p.lastTermination)
    .filter((t): t is NonNullable<typeof t> => !!t?.finishedAt)
    .sort((a, b) => Date.parse(b.finishedAt!) - Date.parse(a.finishedAt!))[0]
  const ended = last?.finishedAt ? Date.parse(last.finishedAt) : NaN
  const recent = Number.isFinite(ended) && now - ended <= CNPG_OPERATOR_RECENT_RESTART_MS
  const why = last ? ` (${[last.reason, `exit ${last.exitCode}`].filter(Boolean).join(', ')})` : ''
  const when = last?.finishedAt ? ` · last ended ${formatAge(last.finishedAt)} ago${why}` : ''
  return { text: `${restarts} restart${restarts === 1 ? '' : 's'} since the Pod was created${when}`, recent }
}

function componentName(c: CNPGOperatorComponent): string {
  return c.role === 'operator' ? 'The operator' : `Plugin ${c.pluginName ?? c.deployment}`
}

/**
 * What the Operator screen leads with: the states that stop or slow
 * reconciliation, then restart history with when it last happened. Restart
 * totals are cumulative, so only a recent one is worded as current trouble.
 */
export function cnpgOperatorConcerns(op: CNPGOperatorResponse, now = Date.now()): CNPGOperatorConcern[] {
  const out: CNPGOperatorConcern[] = []
  for (const c of op.components) {
    if (c.readyReplicas !== null && c.replicas !== null && c.replicas > 0 && c.readyReplicas < c.replicas) {
      out.push({ tone: c.readyReplicas === 0 ? 'unhealthy' : 'degraded', text: `${componentName(c)} has ${c.readyReplicas} of ${c.replicas} replicas ready.` })
    }
  }
  for (const d of op.diagnosis ?? []) {
    if (d.leader.state === 'ok' && d.leader.stale) out.push({ tone: 'unhealthy', text: `No operator instance is leading in ${d.namespace}: the leader Lease was not renewed, so nothing reconciles.` })
    for (const svc of d.webhookServices) {
      const fails = d.webhooks.some((w) => w.state === 'ok' && w.webhooks.some((h) => h.failurePolicy === 'Fail' && h.service === `${svc.namespace}/${svc.name}`))
      if (svc.state === 'ok' && svc.readyEndpoints === 0 && fails) {
        out.push({ tone: 'unhealthy', text: `The admission webhook Service ${svc.namespace}/${svc.name} has no ready endpoint and its failure policy is Fail: every CloudNativePG write is rejected.` })
      }
    }
  }
  for (const c of op.components) {
    const h = cnpgRestartHistory(c, now)
    if (h) out.push({ tone: h.recent ? 'degraded' : 'neutral', text: `${componentName(c)}: ${h.text}.` })
  }
  return out
}
