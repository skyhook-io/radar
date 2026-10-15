import type { HealthLevel } from '@skyhook-io/k8s-ui'
import type { CNPGInstanceSettings, CNPGParametersResponse, CNPGTimelineSwitch } from '../../api/cnpg-inspect'

/**
 * What a timeline switch's reason says, in words. PostgreSQL writes the reason
 * into the history file when it promotes: the recovery target it stopped at,
 * or "no recovery target specified" when it replayed all the WAL it had —
 * which is also what a failover or switchover writes.
 */
export function cnpgRecoveryStopText(sw: CNPGTimelineSwitch): string {
  const r = sw.reason.trim()
  let m = /^(before|after) transaction (\d+)$/.exec(r)
  if (m) return `stopped ${m[1]} transaction ${m[2]}`
  m = /^(before|after) LSN ([0-9A-Fa-f]+\/[0-9A-Fa-f]+)$/.exec(r)
  if (m) return `stopped ${m[1]} LSN ${m[2]}`
  m = /^at restore point "(.*)"$/.exec(r)
  if (m) return `stopped at restore point “${m[1]}”`
  m = /^(before|after) (\d{4}-\d{2}-\d{2} .+)$/.exec(r)
  if (m) return `stopped ${m[1]} the transaction that committed at ${m[2]}`
  if (r === 'reached consistency') return 'stopped as soon as it was consistent (immediate target)'
  if (r === 'no recovery target specified') return 'promoted without a recovery target: it replayed all the WAL it had (a failover or switchover reads the same)'
  return r || 'no reason recorded'
}

/** The declared spec.bootstrap.recovery.recoveryTarget, in words. */
export function cnpgDeclaredTargetText(target: Record<string, unknown> | undefined): string {
  if (!target) return 'None: replay all the WAL the archive holds'
  const parts: string[] = []
  if (typeof target.targetTime === 'string') parts.push(`time ${target.targetTime}`)
  if (typeof target.targetLSN === 'string') parts.push(`LSN ${target.targetLSN}`)
  if (typeof target.targetXID === 'string') parts.push(`transaction ${target.targetXID}`)
  if (typeof target.targetName === 'string') parts.push(`restore point “${target.targetName}”`)
  if (target.targetImmediate === true) parts.push('as soon as consistent')
  if (typeof target.targetTLI === 'string') parts.push(`timeline ${target.targetTLI}`)
  if (typeof target.backupID === 'string') parts.push(`from backup ${target.backupID}`)
  if (target.exclusive === true) parts.push('exclusive')
  return parts.length > 0 ? parts.join(' · ') : 'Declared without a target field'
}

/** When a change to a parameter takes effect, from pg_settings.context. */
export function cnpgSettingTakesEffect(context: string | undefined): string {
  switch (context) {
    case 'postmaster':
      return 'restart'
    case 'sighup':
      return 'reload'
    case 'user':
    case 'superuser':
      return 'reload · a session can override it'
    case 'backend':
    case 'superuser-backend':
      return 'new connections'
    case 'internal':
      return 'fixed (cannot be changed)'
    default:
      return 'unknown'
  }
}

export interface CNPGParameterRow {
  name: string
  declared: string
  /** The value every instance that answered reports, when they agree. */
  value?: string
  /** Pod → value, when instances report different values. */
  perInstance?: { pod: string; value: string }[]
  takesEffect: string
  source?: string
  setByClient: boolean
  pendingRestart: string[]
  /** Instances that answered but did not report this parameter. */
  unreported: string[]
}

export interface CNPGParametersView {
  rows: CNPGParameterRow[]
  read: CNPGInstanceSettings[]
  unread: CNPGInstanceSettings[]
  summary: { text: string; tone: HealthLevel; attention: boolean }
}

/**
 * Declared parameters beside what each instance reports. No declared-vs-
 * effective verdict: PostgreSQL normalizes units ("1024MB" reads "1GB"), and
 * CloudNativePG merges some values itself. Instances disagreeing or a pending
 * restart are observations — a rolling restart passes through both.
 */
export function cnpgParametersView(resp: CNPGParametersResponse, declared = resp.declared, scope?: { declaredInstances?: number; expectedInstances?: string[] }): CNPGParametersView {
  const read = resp.instances.filter((i) => i.state === 'ok' && i.settings)
  const unread = resp.instances.filter((i) => !(i.state === 'ok' && i.settings))
  const queriedNames = new Set(resp.declared.filter((p) => !resp.skipped?.includes(p.name)).map((p) => p.name.toLowerCase()))
  const rows = declared.map((d): CNPGParameterRow => {
    const key = d.name.toLowerCase()
    const seen = read.map((i) => ({ pod: i.pod, s: i.settings!.find((x) => x.name.toLowerCase() === key) }))
    const reported = seen.filter((x) => x.s)
    const values = reported.filter((x) => x.s!.value !== null).map((x) => ({ pod: x.pod, value: x.s!.value as string }))
    const distinct = new Set(values.map((v) => v.value))
    const first = reported[0]?.s
    const sources = new Set(reported.map((x) => x.s!.source))
    return {
      name: d.name,
      declared: d.value,
      value: distinct.size === 1 ? values[0].value : undefined,
      perInstance: distinct.size > 1 ? values : undefined,
      takesEffect: new Set(reported.map((x) => x.s!.context)).size > 1
        ? reported.map((x) => `${x.pod}: ${cnpgSettingTakesEffect(x.s!.context)}`).join(' · ')
        : cnpgSettingTakesEffect(first?.context),
      source: sources.size === 1 ? [...sources][0] : sources.size > 1 ? 'differs by instance' : undefined,
      setByClient: reported.some((x) => x.s!.setByClient),
      pendingRestart: reported.filter((x) => x.s!.pendingRestart).map((x) => x.pod),
      unreported: seen.filter((x) => !x.s).map((x) => x.pod),
    }
  })
  const pending = new Set(rows.flatMap((r) => r.pendingRestart))
  const differing = rows.filter((r) => r.perInstance).length
  const unsampled = rows.filter((r) => !queriedNames.has(r.name.toLowerCase())).length
  if (queriedNames.size === 0) {
    return { rows, read, unread, summary: { text: `${rows.length} declared · no parameters sampled`, tone: 'unknown', attention: false } }
  }
  if (resp.instances.length === 0) {
    // Hibernated, or no instance Pod yet: nothing was read, which is not calm.
    return { rows, read, unread, summary: { text: `${rows.length} declared · no instance Pod to read`, tone: 'unknown', attention: false } }
  }
  const bits = [`${rows.length} declared`]
  if (unsampled > 0) bits.push(`${rows.length - unsampled} sampled`, `${unsampled} not sampled`)
  bits.push(read.length > 0 ? `read on ${read.map((i) => i.pod).join(', ')}` : 'no instance answered')
  const missing = (scope?.expectedInstances ?? []).filter((pod) => !resp.instances.some((i) => i.pod === pod))
  if (missing.length > 0) bits.push(`${missing.join(', ')} not running`)
  if (scope?.declaredInstances !== undefined) bits.push(`${scope.declaredInstances} instance${scope.declaredInstances === 1 ? '' : 's'} declared`)
  if (unread.length > 0) bits.push(`not read on ${unread.map((i) => i.pod).join(', ')}`)
  if (pending.size > 0) bits.push(`restart pending on ${[...pending].sort().join(', ')}`)
  if (differing > 0) bits.push(`${differing} differ between instances`)
  const attention = pending.size > 0 || differing > 0
  return {
    rows,
    read,
    unread,
    summary: { text: bits.join(' · '), tone: attention ? 'degraded' : unread.length > 0 || unsampled > 0 || missing.length > 0 || (scope?.declaredInstances !== undefined && read.length < scope.declaredInstances) ? 'unknown' : 'healthy', attention },
  }
}
