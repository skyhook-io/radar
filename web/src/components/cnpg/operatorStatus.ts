import { coverageLabel } from './shared'
import type { CNPGOperatorComponent, CNPGOperatorResponse, CNPGOperatorVerdict } from '../../api/cnpg'
import { cnpgClusterPlugins, cnpgPluginPhase, formatAge, type CNPGFleetRow } from '@skyhook-io/k8s-ui'

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

export interface CNPGOperatorState {
  concerns: CNPGOperatorConcern[]
  /** What was read and found in order, said only from evidence. */
  confirmed: string[]
  /** What could not be read, so its absence from `concerns` proves nothing. */
  unread: string[]
}

/**
 * What the Operator screen leads with: the states that stop or slow
 * reconciliation, then restart history with when it last happened, and what
 * could not be read. Restart totals are cumulative, so only a recent one is
 * worded as current trouble.
 */
export function cnpgOperatorConcerns(op: CNPGOperatorResponse, now = Date.now()): CNPGOperatorConcern[] {
  return cnpgOperatorState(op, now).concerns
}

export function cnpgOperatorState(op: CNPGOperatorResponse, now = Date.now(), clusters: CNPGFleetRow[] = []): CNPGOperatorState {
  const concerns: CNPGOperatorConcern[] = cnpgPluginBlockedClusters(op, clusters, now)
  const confirmed: string[] = []
  const unread: string[] = []
  const operators = op.components.filter((c) => c.role === 'operator')
  const coverageGaps = (['deployments', 'services'] as const).filter((k) => op.coverage[k]?.state !== 'full')
  if (coverageGaps.length > 0) unread.push(`${operators.length === 0 ? 'Operator health cannot be assessed with your access' : 'Other operator or plugin components may be outside this read'}: ${coverageGaps.map((k) => `${k === 'deployments' ? 'Deployments' : 'Services'} ${coverageLabel(op.coverage[k])}`).join('; ')}.`)
  else if (operators.length === 0) unread.push('No operator Deployment was found in the namespaces you can read.')
  let allReady = op.components.length > 0
  for (const c of op.components) {
    if (c.readyReplicas === null || c.replicas === null) {
      allReady = false
      unread.push(`${componentName(c)}: readiness not reported.`)
    } else if (c.replicas === 0) {
      allReady = false
      concerns.push({ tone: c.role === 'operator' ? 'unhealthy' : 'degraded', text: `${componentName(c)} is scaled to 0${c.role === 'operator' ? ': nothing reconciles' : ''}.` })
    } else if (c.readyReplicas < c.replicas) {
      allReady = false
      concerns.push({ tone: c.readyReplicas === 0 ? 'unhealthy' : 'degraded', text: `${componentName(c)} has ${c.readyReplicas} of ${c.replicas} replicas ready.` })
    }
  }
  // Ready now says little about a component that keeps restarting.
  const restartedRecently = op.components.some((c) => cnpgRestartHistory(c, now)?.recent)
  if (allReady) confirmed.push(restartedRecently ? 'every component reports ready right now (see the restarts above)' : 'every component is ready')
  const diagnoses = op.diagnosis ?? []
  if (operators.length > 0 && diagnoses.length === 0) unread.push('Leadership and webhooks were not read.')
  let leading = diagnoses.length > 0
  let webhooksServed = diagnoses.length > 0
  for (const d of diagnoses) {
    if (d.leader.state === 'ok') {
      if (d.leader.stale) {
        leading = false
        concerns.push({ tone: 'unhealthy', text: `No operator instance is leading in ${d.namespace}: the leader Lease was not renewed, so nothing reconciles.` })
      }
    } else if (d.leader.state !== 'disabled') {
      leading = false
      unread.push(`Leadership in ${d.namespace} not read${d.leader.reason ? `: ${d.leader.reason}` : ''}.`)
    }
    for (const svc of d.webhookServices) {
      const fails = d.webhooks.some((w) => w.state === 'ok' && w.webhooks.some((h) => h.failurePolicy === 'Fail' && h.service === `${svc.namespace}/${svc.name}`))
      if (svc.state !== 'ok' || svc.readyEndpoints === null) {
        webhooksServed = false
        unread.push(`Endpoints of the webhook Service ${svc.namespace}/${svc.name} not read.`)
      } else if (svc.readyEndpoints === 0) {
        webhooksServed = false
        if (fails) concerns.push({ tone: 'unhealthy', text: `The admission webhook Service ${svc.namespace}/${svc.name} has no ready endpoint and its failure policy is Fail: every CloudNativePG write is rejected.` })
      }
    }
    const unreadHooks = d.webhooks.filter((w) => w.state !== 'ok')
    if (unreadHooks.length > 0) {
      // Services are found through readable configurations only, so an unread
      // one may name a Service with no endpoint.
      webhooksServed = false
      unread.push(`Admission webhook configurations not read (${unreadHooks.map((w) => w.name).join(', ')}).`)
    }
    if (d.webhookServices.length === 0) webhooksServed = false
  }
  if (leading) confirmed.push('the operator is leading')
  if (webhooksServed) confirmed.push('its webhooks have ready endpoints')
  for (const c of op.components) {
    const h = cnpgRestartHistory(c, now)
    if (h) concerns.push({ tone: h.recent ? 'degraded' : 'neutral', text: `${componentName(c)}: ${h.text}.` })
    else if (!c.pods && c.podCoverage && c.podCoverage.state !== 'ok') unread.push(`${componentName(c)}: restart history not read${c.podCoverage.reason ? ` (${c.podCoverage.reason})` : ''}.`)
  }
  return { concerns, confirmed, unread }
}

/**
 * Clusters whose phase says the operator is stuck on a plugin, joined to what
 * this screen knows about that plugin: whether its Deployment is here, ready,
 * and when it last restarted. Facts side by side; which one caused the other
 * is not claimed.
 */
export function cnpgPluginBlockedClusters(op: CNPGOperatorResponse, clusters: CNPGFleetRow[], now = Date.now()): CNPGOperatorConcern[] {
  const out: CNPGOperatorConcern[] = []
  for (const r of clusters) {
    const phase = cnpgPluginPhase(r.cluster)
    if (!phase) continue
    const what = phase === 'unknownPlugin' ? 'requires a plugin the operator does not know' : 'hit an error talking to a plugin'
    const plugins = cnpgClusterPlugins(r.cluster)
    const parts = plugins.map((name) => {
      const c = op.components.find((x) => x.role === 'plugin' && x.pluginName === name)
      if (!c) return `${name}: no Deployment serving it was found`
      const ready = c.readyReplicas !== null && c.replicas !== null ? `${c.readyReplicas}/${c.replicas} ready` : 'readiness not reported'
      const h = cnpgRestartHistory(c, now)
      return `${name}: ${ready}${h ? `, ${h.text}` : ''}`
    })
    out.push({ tone: 'unhealthy', text: `Cluster ${r.namespace}/${r.name} ${what}${parts.length ? ` — ${parts.join('; ')}` : ''}.` })
  }
  return out
}
