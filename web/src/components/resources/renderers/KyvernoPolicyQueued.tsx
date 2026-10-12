import { Workflow } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner } from '@skyhook-io/k8s-ui'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import { HEALTH_BADGE_COLORS } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { formatAge } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { isForbiddenError } from '../../../api/client'
import { isKyvernoMutateExistingEnabled } from '@skyhook-io/k8s-ui/components/resources/resource-utils-kyverno-modern'
import { usePolicyQueued } from '../../../api/policy'
import { TimeValue } from '@skyhook-io/k8s-ui/components/ui/ScheduleValue'

/** A request sitting this long is not mid-flight. Kyverno retries on every
 *  reconcile, so past this point a backlog grows rather than drains. */
const STALLED_MINUTES = 5

/** Enough at once to be worth raising even while it is still moving. */
const BACKLOG_SIZE = 25

/**
 * The whole banner, or null when a queue is doing what a queue does. Headline
 * and body are decided together — split across two places, the body went on
 * diagnosing a stalled controller under a headline that had stopped claiming one.
 *
 * Only the first case has measured that the work stopped. The second says how
 * much is waiting and nothing more: everything in it is younger than the stall
 * threshold, so it is as likely to be a burst draining normally, and diagnosing
 * a controller from that is a claim the evidence argues against.
 */
export function queueBanner(
  pending: number,
  stalledMinutes: number,
  oldestAge: string,
): { title: string; message: string } | null {
  if (stalledMinutes >= STALLED_MINUTES) {
    return {
      title: `Queued work has not moved for ${oldestAge}`,
      message:
        'Requests build up when the background controller cannot keep up or cannot reach what it needs. They are retried on every reconcile, so a backlog grows rather than drains.',
    }
  }
  if (pending >= BACKLOG_SIZE) {
    return {
      title: `${pending} requests are queued`,
      message: `Nothing here has been waiting longer than ${STALLED_MINUTES} minutes, so this may be a burst still draining. Worth watching: if the count holds or the oldest keeps ageing, the controller is not keeping up.`,
    }
  }
  return null
}

/**
 * Whether this policy can queue background work at all.
 *
 * Decides whether a denial is worth mentioning. A validate-only policy never
 * queues anything, so "you can't see the queue" there is noise on the majority
 * of policy pages. A policy that generates, or mutates resources that already
 * exist, does queue — and for those a denial hides a backlog that looks exactly
 * like a healthy policy with nothing pending.
 */
export function policyQueuesWork(data: any): boolean {
  if (data?.kind === 'GeneratingPolicy') return true
  if (isKyvernoMutateExistingEnabled(data)) return true
  return (data?.spec?.rules ?? []).some(
    (r: any) => !!r?.generate || Array.isArray(r?.mutate?.targets),
  )
}

/**
 * Whether a queued request belongs to this policy.
 *
 * Exactly one form matches, never both. A namespaced Policy is recorded as
 * `namespace/name` and a cluster-scoped one bare, and Kyverno permits the two
 * to share a name — so accepting the bare form as a fallback hands a namespaced
 * policy the backlog of a ClusterPolicy it has nothing to do with. The coverage
 * lookup refuses the same fallback for the same reason.
 */
export function requestBelongsTo(request: any, name: string, namespace: string): boolean {
  const policy = request?.spec?.policy
  if (!policy || !name) return false
  return policy === (namespace ? `${namespace}/${name}` : name)
}

/**
 * What this policy has queued and not finished.
 *
 * A generate or mutate-existing rule does its work through queued requests, and
 * the way that fails in practice is a pile-up rather than one bad request:
 * upstream reports describe thousands stuck in Pending, never cleaned up, taken
 * up again on every reconcile. That is a count, and a count belongs next to the
 * policy — the requests have their own page, but nobody watches it.
 *
 * Silent when the policy has queued nothing, so it never adds an empty section
 * to the majority of policies that only validate.
 */
export function KyvernoPolicyQueued({ data }: { data: any }) {
  const name = data?.metadata?.name ?? ''
  const namespace = data?.metadata?.namespace ?? ''

  // Server-side, because the answer's scope is not the subject's: Kyverno keeps
  // these in its own namespace, and the generic resource list would have
  // answered for whatever namespaces the reader happens to be viewing.
  const { data: queued, error } = usePolicyQueued(name, namespace, !!name)

  const total = queued?.requests ?? 0
  if (total === 0) {
    // An empty list and an unreadable one are not the same answer. A denial is
    // cluster-static, so disclosing it on every policy page would be noise on the
    // majority that only validate and never queue anything — but staying silent
    // about it on a policy that DOES queue hides a backlog behind a page that
    // looks like a healthy one with nothing pending. So it is disclosed exactly
    // where it can cost something.
    if (!error) return null
    if (isForbiddenError(error) && !policyQueuesWork(data)) return null
    return (
      <Section title="Queued Work" icon={Workflow}>
        <LookupFailureNote errors={[error]} what="what this policy has queued" />
      </Section>
    )
  }

  const byState = queued?.byState ?? {}
  const pending = byState['Pending'] ?? 0
  const failed = byState['Failed'] ?? 0
  const messages = queued?.messages ?? []
  const oldestPending = queued?.oldestPending

  const stalledMinutes = oldestPending
    ? Math.floor((Date.now() - new Date(oldestPending).getTime()) / 60000)
    : 0
  const banner = queueBanner(pending, stalledMinutes, oldestPending ? formatAge(oldestPending) : '')

  return (
    <>
      {banner && (
        <AlertBanner variant="warning" title={banner.title} message={banner.message} />
      )}
      <Section title="Queued Work" icon={Workflow} defaultExpanded={pending > 0 || failed > 0}>
        <PropertyList>
          <Property label="Requests" value={String(total)} />
          {Object.entries(byState).map(([state, n]) => (
            <Property
              key={state}
              label={state}
              value={
                <span
                  className={clsx(
                    'badge',
                    HEALTH_BADGE_COLORS[
                      (state === 'Failed'
                        ? 'unhealthy'
                        : state === 'Pending'
                          ? 'degraded'
                          : state === 'Completed'
                            ? 'healthy'
                            : 'unknown') as keyof typeof HEALTH_BADGE_COLORS
                    ],
                  )}
                >
                  {n}
                </span>
              }
            />
          ))}
          {oldestPending && (
            <Property label="Oldest Pending" value={<TimeValue timestamp={oldestPending} />} />
          )}
        </PropertyList>
        {messages.length > 0 && (
          <div className="mt-2 pt-2 border-t border-theme-border space-y-1">
            {/* Kyverno writes why it could not complete a request into
                status.message, and it is the only diagnosis this object
                carries. A count without it says something is wrong and leaves
                you to go and find out what. */}
            {messages.map((m, i) => (
              <div key={i} className="text-xs text-warning-text">{m}</div>
            ))}
          </div>
        )}
        <div className="mt-2 pt-2 border-t border-theme-border text-xs text-theme-text-secondary">
          These are deleted seconds after they complete, so this counts what is in flight right now
          rather than everything this policy has ever done.
        </div>
      </Section>
    </>
  )
}
