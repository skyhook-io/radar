import {
  HEALTH_META,
  HEALTH_ORDER,
  applicationDisplayHealth,
  healthOf,
  rolloutSummaryForApps,
  servingReadiness,
  worstHealth,
  type AppHealth,
  type AppRow,
  type AppWorkload,
} from './applications'
import { formatRelativeAgeTime } from './format'

// The one line an app page opens with: is it OK, where, since when, and which
// rollout. A workload or instance with no reading is reported as unknown, so a
// missing part can never read as healthy.

export type AppCause = 'app' | 'platform'

const APP_REASONS = new Set([
  'crashloopbackoff',
  'oomkilled',
  'error',
  'containercannotrun',
  'createcontainerconfigerror',
  'invalidimagename',
])

const PLATFORM_REASONS = new Set([
  'failedattachvolume',
  'failedcreatepodsandbox',
  'networknotready',
  'nodenotready',
  'evicted',
  'preempted',
])

// Follows from a failure either side can cause, so it says nothing about which.
// FailedScheduling is here because an app's own selector or requests cause it
// as often as a short cluster does.
const NEUTRAL_REASONS = new Set(['backoff', 'unhealthy', 'killing', 'failedscheduling'])

function isFailing(workload: AppWorkload): boolean {
  const health = healthOf(workload.health)
  return health === 'degraded' || health === 'unhealthy'
}

/** Whether a failing app points at its own code and config or at the platform.
 *  Every reason on record must agree: image pulls, volume mounts and anything
 *  unrecognized can be either, so they leave the answer open. */
export function classifyAppCause(app: Pick<AppRow, 'workloads' | 'events'>): AppCause | null {
  const failing = (app.workloads ?? []).filter(isFailing)
  if (failing.length === 0) return null

  const reasons = [
    ...failing.map((workload) => workload.reason),
    ...(app.events ?? []).map((event) => event.reason),
  ].filter((reason): reason is string => Boolean(reason))

  let cause: AppCause | null = null
  for (const raw of reasons) {
    const reason = raw.toLowerCase()
    if (NEUTRAL_REASONS.has(reason)) continue
    const side: AppCause | null = APP_REASONS.has(reason) ? 'app' : PLATFORM_REASONS.has(reason) ? 'platform' : null
    if (!side || (cause && cause !== side)) return null
    cause = side
  }
  return cause
}

export interface AppVerdictInstance {
  env: string
  health: AppHealth
}

export interface AppVerdict {
  health: AppHealth
  /** Where each state applies, e.g. "Degraded in prod · healthy in dev, staging". */
  summary: string
  /** When the current problem was first seen. */
  since?: string
  rollout?: string
  cause?: AppCause | null
  /** The instance `since`, `rollout` and `cause` describe, when there are several. */
  activeEnv?: string
}

const MAX_LISTED_ENVS = 3

function listEnvs(envs: string[]): string {
  const listed = envs.slice(0, MAX_LISTED_ENVS).join(', ')
  return envs.length > MAX_LISTED_ENVS ? `${listed} +${envs.length - MAX_LISTED_ENVS}` : listed
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

function earliestProblem(app: AppRow): string | undefined {
  let earliest: string | undefined
  for (const event of app.events ?? []) {
    if (!event.firstSeen || !Number.isFinite(Date.parse(event.firstSeen))) continue
    if (!earliest || Date.parse(event.firstSeen) < Date.parse(earliest)) earliest = event.firstSeen
  }
  return earliest
}

function rolloutOf(app: AppRow): string | undefined {
  const summary = rolloutSummaryForApps([app])
  if (!summary) return undefined
  if (summary.count !== 1) return summary.label
  // An app and its only workload usually share a name; saying it twice costs
  // the part of the line that explains the problem.
  const detail = summary.details[0]
  const named = `${app.name}: `
  return detail.startsWith(named) ? detail.slice(named.length) : detail
}

function instanceHealth(app: AppRow): AppHealth {
  const workloads = app.workloads ?? []
  if (workloads.length === 0 || workloads.some((w) => healthOf(w.health) === 'unknown')) {
    const known = applicationDisplayHealth(app)
    return known === 'degraded' || known === 'unhealthy' ? known : 'unknown'
  }
  return applicationDisplayHealth(app)
}

function singleSummary(app: AppRow, health: AppHealth): string {
  const workloads = app.workloads ?? []
  const where = app.identity?.env ? ` in ${app.identity.env}` : ''
  const label = `${HEALTH_META[health].label}${where}`
  if (workloads.length === 0) return `${label}: no workloads reported`

  const unknown = workloads.filter((w) => healthOf(w.health) === 'unknown').length
  if (health === 'unknown' && unknown > 0) return `${label}: no reading for ${unknown} of ${workloads.length} workloads`

  const failing = workloads.filter(isFailing)
  if (failing.length === 1) {
    const { name, ready, desired } = failing[0]
    return `${label}: ${name === app.name ? '' : `${name} `}${ready}/${desired} ready`
  }
  if (failing.length > 1) return `${label}: ${failing.length} of ${workloads.length} workloads`

  const { ready, desired } = servingReadiness(workloads)
  return desired > 0 ? `${label}, ${ready}/${desired} ready` : label
}

function multiSummary(instances: AppVerdictInstance[]): { health: AppHealth; summary: string } {
  const worstByEnv = new Map<string, AppHealth>()
  for (const { env, health } of instances) {
    worstByEnv.set(env, worstHealth([worstByEnv.get(env) ?? 'neutral', health]))
  }
  const parts = HEALTH_ORDER.flatMap((health) => {
    const envs = [...worstByEnv].filter(([, state]) => state === health).map(([env]) => env)
    return envs.length > 0 ? [`${HEALTH_META[health].label.toLowerCase()} in ${listEnvs(envs)}`] : []
  })
  return {
    health: worstHealth([...worstByEnv.values()]),
    summary: capitalize(parts.join(' · ')),
  }
}

/** `instances` is the app's identity siblings, this one included. */
export function buildAppVerdict({ app, instances, activeEnv }: {
  app: AppRow
  instances?: AppVerdictInstance[] | null
  /** The env of the instance `app` is, when there are several. */
  activeEnv?: string
}): AppVerdict {
  const active = instanceHealth(app)
  const failing = active === 'degraded' || active === 'unhealthy'
  const details = {
    since: failing ? earliestProblem(app) : undefined,
    rollout: rolloutOf(app),
    cause: failing ? classifyAppCause(app) : null,
  }
  if (instances && instances.length > 1) {
    return { ...multiSummary(instances), ...details, activeEnv }
  }
  return { health: active, summary: singleSummary(app, active), ...details }
}

const CAUSE_LABEL: Record<AppCause, string> = {
  app: 'likely the app',
  platform: 'likely the platform',
}

export function appVerdictText(verdict: AppVerdict): string {
  const scoped = (text: string) => (verdict.activeEnv ? `${verdict.activeEnv}: ${text}` : text)
  const details = [
    verdict.since && `since ${formatRelativeAgeTime(verdict.since)}`,
    verdict.cause && CAUSE_LABEL[verdict.cause],
    verdict.rollout,
  ].filter((detail): detail is string => Boolean(detail))
  return [verdict.summary, ...(details.length > 0 ? [scoped(details.join(' · '))] : [])].join(' · ')
}
