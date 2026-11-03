import type { ReactNode } from 'react'
import { Route } from 'lucide-react'
import { ResourceLink, Section, ownerNote } from '../ui/drawer-components'
import { gapReasonText } from '../../utils/read-actions'
import { FactSource, FactValue } from '../facts'
import type { Relationships } from '../../types'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { isKueueConditionStale } from '../resources/resource-utils-kueue'
import { localQueueStatusWithParent } from './queue-status'
import { isFlavorAssignmentMessage, parseFlavorAttempts, type FlavorAttempt, type FlavorAttemptKind } from './flavors-tried'
import { ClusterQueueReadSummary, ReadState, StatusPill, namespaceEligibilityFact, type Navigate } from './QueueEvidence'
import { KUEUE_GROUP } from './constants'
import type { RelatedRead } from './related-read'

/** Objects on a Workload's admission path that a host read as the viewer. */
export interface KueueWorkloadReads {
  localQueue?: RelatedRead
  /** Keyed by ClusterQueue name: the LocalQueue's target and, when different, the one holding the reservation. */
  clusterQueues?: Record<string, RelatedRead>
  namespace?: RelatedRead
  /** Keyed by ResourceFlavor name: the flavors the reservation assigned. */
  flavors?: Record<string, RelatedRead>
}

function Step({ label, children }: { label: string; children: ReactNode }) {
  return (
    <li className="relative grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 py-2.5 text-sm first:pt-0 last:pb-0">
      <span className="text-theme-text-tertiary">{label}</span>
      <div className="min-w-0 space-y-1">{children}</div>
    </li>
  )
}

const muted = 'text-xs text-theme-text-secondary'

function OwnerStep({ data, relationships, onNavigate }: { data: any; relationships?: Relationships; onNavigate?: Navigate }) {
  const coverage = relationships?.ownership
  // Without the ownership answer (a Radar that predates it), `owner` may come
  // from display edges that check neither group nor UID, so it isn't linked;
  // the Workload's own controller reference is named instead.
  const controllerRef = (data.metadata?.ownerReferences ?? []).find((ref: any) => ref?.controller)
  const owner = coverage ? relationships?.owner : undefined
  const clusterName: string | undefined = data.status?.clusterName
  let body: ReactNode
  if (owner) {
    body = <div className="flex flex-wrap items-center gap-2 break-all [&_button]:text-left">
      <span className="text-xs text-theme-text-tertiary">{owner.kind}</span>
      <ResourceLink name={owner.name} namespace={owner.namespace} kind={kindToPluralWithGroup(owner.kind, owner.group ?? '')} group={owner.group} onNavigate={onNavigate} />
    </div>
  } else if (!relationships) {
    body = <p className={muted}>Owner not read here.</p>
  } else if (!coverage && controllerRef) {
    body = <p className={muted}>Controller reference: {controllerRef.kind} {controllerRef.name} (this Radar doesn't resolve owners).</p>
  } else if (coverage?.primaryOwnerState === 'several') {
    body = <p className={muted}>Several owner references; none is the controller.</p>
  } else {
    const reason = coverage?.unavailableReason
    const unanswered = !reason ? undefined
      : reason === 'user_forbidden' ? "The owner isn't shown: your access lets you open this Workload, but not list its kind here."
        : `The owner couldn't be read: ${gapReasonText(reason)}.`
    const note = ownerNote(coverage)
    body = <p className={muted}>{unanswered ?? note?.text ?? (data.metadata?.ownerReferences?.length ? 'Owner not resolved.' : 'No owner reference: this Workload was created directly.')}</p>
  }
  return <Step label="Owner">
    {body}
    {clusterName && <p className={muted}>Kueue reports cluster <code>{clusterName}</code> for this Workload (status.clusterName). Radar shows this cluster only and doesn't follow that name elsewhere.</p>}
  </Step>
}

export function AdmissionPath({ data, reads, relationships, onNavigate }: { data: any; reads?: KueueWorkloadReads; relationships?: Relationships; onNavigate?: Navigate }) {
  const namespace: string = data.metadata?.namespace ?? ''
  const queueName: string | undefined = data.spec?.queueName
  const localQueue = reads?.localQueue
  const configured: string | undefined = localQueue?.state === 'found' ? localQueue.object?.spec?.clusterQueue : undefined
  const reserved: string | undefined = data.status?.admission?.clusterQueue
  const finished = (data.status?.conditions ?? []).some((condition: any) => condition?.type === 'Finished' && condition.status === 'True')
  const admitted = (data.status?.conditions ?? []).some((condition: any) => condition?.type === 'Admitted' && condition.status === 'True')
  const configuredRead = configured ? reads?.clusterQueues?.[configured] : undefined
  const eligibility = configured && reads ? namespaceEligibilityFact({ clusterQueue: configured, parent: configuredRead, namespaceRead: reads.namespace }) : undefined
  const assignments: any[] = Array.isArray(data.status?.admission?.podSetAssignments) ? data.status.admission.podSetAssignments : []
  // Kueue's own word on why quota isn't reserved, verbatim. Flavor-assignment
  // messages are left to "Reasons reported per flavor".
  const quota = (data.status?.conditions ?? []).find((condition: any) => condition?.type === 'QuotaReserved')
  const pendingMessage: string | undefined = quota?.status === 'False' && quota.message && !isFlavorAssignmentMessage(quota.message) ? quota.message : undefined
  const pendingStale = isKueueConditionStale(quota, data.metadata?.generation)
  return (
    <Section variant="card" title="Admission Path" icon={Route}>
      <ol className="divide-y divide-theme-border">
        <OwnerStep data={data} relationships={relationships} onNavigate={onNavigate} />
        <Step label="LocalQueue">
          {!queueName ? <p className={muted}>No queue name: spec.queueName is empty, so the Workload names no LocalQueue.</p> : <>
            <div className="flex flex-wrap items-center gap-2 break-all [&_button]:text-left">
              <ResourceLink name={queueName} kind="localqueues" namespace={namespace} group={KUEUE_GROUP} onNavigate={onNavigate} />
              <ReadState read={localQueue} found={localQueue?.state === 'found' ? <StatusPill status={localQueueStatusWithParent(localQueue.object, configuredRead)} /> : undefined} />
            </div>
            {localQueue?.state === 'not_found' && <p className={muted}>{reserved
              ? `No LocalQueue ${queueName} exists in namespace ${namespace} now; the reservation below was made through it earlier.`
              : finished || data.spec?.active === false
                ? `No LocalQueue ${queueName} exists in namespace ${namespace}.`
                : `No LocalQueue ${queueName} exists in namespace ${namespace}. Kueue can't admit this Workload until it does.`}</p>}
          </>}
        </Step>
        {queueName && reads && (
          <Step label="ClusterQueue">
            {configured ? <>
              <ClusterQueueReadSummary name={configured} read={configuredRead} onNavigate={onNavigate} />
              <p className="text-xs text-theme-text-tertiary">Configured by LocalQueue {queueName}.</p>
              {eligibility && !reserved && <div className="text-xs"><span className="text-theme-text-tertiary">Namespace {namespace}: </span><FactValue fact={eligibility} /><FactSource fact={eligibility} /></div>}
            </> : <p className={muted}>
              {localQueue?.state === 'found' ? 'The LocalQueue names no ClusterQueue.'
                : localQueue?.state === 'not_found' ? `None: LocalQueue ${queueName} doesn't exist.`
                  : localQueue?.state === 'unconfirmed' ? `Unknown: this Radar can't confirm LocalQueue ${queueName} exists.`
                  : localQueue?.state === 'loading' ? 'Checking…' : 'Unknown until the LocalQueue is read.'}
            </p>}
          </Step>
        )}
        <Step label="Reservation">
          {reserved ? <>
            <p className="text-xs text-theme-text-tertiary">{finished ? 'Reservation recorded in (the Workload finished, so its quota was released)' : 'Quota reserved in'}</p>
            {reads && reserved !== configured
              ? <ClusterQueueReadSummary name={reserved} read={reads.clusterQueues?.[reserved]} onNavigate={onNavigate} />
              : <div className="break-all [&_button]:text-left"><ResourceLink name={reserved} kind="clusterqueues" group={KUEUE_GROUP} onNavigate={onNavigate} /></div>}
            {configured && configured !== reserved && (finished || admitted
              ? <p className="text-xs text-theme-text-tertiary">Reserved in {reserved}; LocalQueue {queueName}, as it exists now, names {configured}.</p>
              : <p className="text-xs text-warning-text">Reserved in {reserved}; LocalQueue {queueName}, as it exists now, names {configured}.</p>)}
          </> : <>
            <p className={muted}>No reservation reported.</p>
            {pendingMessage && <p className="whitespace-pre-wrap break-words text-xs text-theme-text-secondary"><span className="text-theme-text-tertiary">{pendingStale ? `Controller-reported on QuotaReserved for generation ${quota.observedGeneration} (now ${data.metadata?.generation}): ` : 'Controller-reported on QuotaReserved: '}</span>{pendingMessage}</p>}
          </>}
        </Step>
        {assignments.length > 0 && (
          <Step label="Flavors">
            <div className="space-y-2">
              {assignments.map((assignment, index) => (
                <div key={assignment?.name ?? index} className="space-y-0.5">
                  {assignments.length > 1 && <p className="text-xs font-medium text-theme-text-secondary">{assignment?.name ?? `PodSet ${index + 1}`}</p>}
                  {Object.entries(assignment?.flavors ?? {}).length === 0
                    ? <p className={muted}>No flavors reported.</p>
                    : Object.entries(assignment.flavors).map(([resource, flavor]) => (
                      <div key={resource} className="flex flex-wrap items-center gap-1.5 break-all text-xs [&_button]:text-left">
                        <span className="text-theme-text-secondary">{resource}</span>
                        <span className="text-theme-text-tertiary">→</span>
                        <ResourceLink name={String(flavor)} kind="resourceflavors" group={KUEUE_GROUP} onNavigate={onNavigate} />
                        {finished && reads?.flavors?.[String(flavor)]?.state === 'not_found'
                          ? <span className="text-xs text-theme-text-tertiary">No longer exists</span>
                          : reads?.flavors?.[String(flavor)]?.state !== 'found' && <ReadState read={reads?.flavors?.[String(flavor)]} />}
                      </div>
                    ))}
                </div>
              ))}
            </div>
          </Step>
        )}
      </ol>
    </Section>
  )
}

/** Reasons grouped under the flavor they name, in the order flavors first appear in the message. */
function groupByFlavor(attempts: FlavorAttempt[]): [string | undefined, FlavorAttempt[]][] {
  const groups = new Map<string | undefined, FlavorAttempt[]>()
  for (const attempt of attempts) groups.set(attempt.flavor, [...(groups.get(attempt.flavor) ?? []), attempt])
  return [...groups]
}

const attemptLabel: Record<FlavorAttemptKind, string> = {
  insufficient_quota: 'Over maximum capacity',
  insufficient_unused_quota: 'Not enough unused quota',
  untolerated_taint: 'Untolerated taint',
  node_affinity: 'Node affinity mismatch',
  flavor_not_found: 'Flavor not found',
  resource_unavailable: 'Resource not in ClusterQueue',
  skipped_by_annotation: 'Skipped by annotation',
  pinned_by_original: 'Held by original Workload',
}

/**
 * The reasons Kueue gave per flavor in the Workload's pending QuotaReserved
 * message, parsed when every part is recognised and verbatim otherwise. It is
 * one controller report, not an attempt history.
 */
export function FlavorReasons({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const conditions: any[] = data.status?.conditions ?? []
  const quota = conditions.find(condition => condition?.type === 'QuotaReserved')
  if (quota?.status !== 'False' || !isFlavorAssignmentMessage(quota.message)) return null
  const stale = isKueueConditionStale(quota, data.metadata?.generation)
  const parsed = parseFlavorAttempts(quota.message)
  return (
    <Section variant="card" title="Reasons Reported per Flavor">
      {stale && <p className="mb-2 text-xs text-warning-text">Reported for generation {quota.observedGeneration} (now {data.metadata?.generation}); the Workload has changed since.</p>}
      <p className="mb-3 text-xs text-theme-text-tertiary">From the controller-reported message on QuotaReserved. Kueue lists the reasons alphabetically, not in the order it tried flavors, and may not report every flavor.</p>
      {parsed ? (
        <div className="space-y-3">
          {parsed.map(podSet => (
            <div key={podSet.podSet} className="space-y-2">
              <p className="text-xs font-medium text-theme-text-secondary">PodSet {podSet.podSet}</p>
              <ul className="divide-y divide-theme-border">
                {groupByFlavor(podSet.attempts).map(([flavor, attempts]) => (
                  <li key={flavor ?? ''} className="space-y-1 py-2 text-sm first:pt-0 last:pb-0">
                    <div className="break-all [&_button]:text-left">
                      {flavor ? <ResourceLink name={flavor} kind="resourceflavors" group={KUEUE_GROUP} onNavigate={onNavigate} /> : <span className="text-xs text-theme-text-tertiary">No flavor named</span>}
                    </div>
                    {attempts.map((attempt, index) => (
                      <div key={index} className="space-y-0.5 pl-3">
                        <div className="flex flex-wrap items-center gap-2 text-xs">
                          {attempt.resource && <code className="text-theme-text-secondary">{attempt.resource}</code>}
                          <span className="text-theme-text-tertiary">{attemptLabel[attempt.kind]}</span>
                        </div>
                        <p className="whitespace-pre-wrap break-words text-xs text-theme-text-secondary">{attempt.text}</p>
                      </div>
                    ))}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      ) : (
        <p className="whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{quota.message}</p>
      )}
    </Section>
  )
}
