import type { ReactNode } from 'react'
import { Route } from 'lucide-react'
import { ResourceLink, Section, ownerNote, ownershipUnanswered } from '../ui/drawer-components'
import { FactSource, FactValue } from '../facts'
import type { Relationships } from '../../types'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { getLocalQueueStatus, isKueueConditionStale } from '../resources/resource-utils-kueue'
import { parseFlavorAttempts, type FlavorAttempt, type FlavorAttemptKind } from './flavors-tried'
import { ClusterQueueReadSummary, KUEUE_GROUP, ReadState, StatusPill, namespaceEligibilityFact, type Navigate } from './QueueEvidence'
import type { RelatedRead } from './related-read'

/** Objects on a Workload's admission path that a host read as the viewer. */
export interface KueueWorkloadReads {
  localQueue?: RelatedRead
  /** Keyed by ClusterQueue name: the LocalQueue's target and, when different, the one holding the reservation. */
  clusterQueues?: Record<string, RelatedRead>
  namespace?: RelatedRead
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
  const owner = relationships?.owner
  const coverage = relationships?.ownership
  const clusterName: string | undefined = data.status?.clusterName
  let body: ReactNode
  if (owner) {
    body = <div className="flex flex-wrap items-center gap-2 break-all [&_button]:text-left">
      <span className="text-xs text-theme-text-tertiary">{owner.kind}</span>
      <ResourceLink name={owner.name} namespace={owner.namespace} kind={kindToPluralWithGroup(owner.kind, owner.group ?? '')} group={owner.group} onNavigate={onNavigate} />
    </div>
  } else if (!relationships) {
    body = <p className={muted}>Owner not read here.</p>
  } else if (coverage?.primaryOwnerState === 'several') {
    body = <p className={muted}>Several owner references; none is the controller.</p>
  } else {
    const unanswered = ownershipUnanswered(coverage)
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
  const configuredRead = configured ? reads?.clusterQueues?.[configured] : undefined
  const eligibility = configured && reads ? namespaceEligibilityFact({ namespace, clusterQueue: configured, parent: configuredRead, namespaceRead: reads.namespace }) : undefined
  const assignments: any[] = Array.isArray(data.status?.admission?.podSetAssignments) ? data.status.admission.podSetAssignments : []
  return (
    <Section variant="card" title="Admission Path" icon={Route}>
      <ol className="divide-y divide-theme-border">
        <OwnerStep data={data} relationships={relationships} onNavigate={onNavigate} />
        <Step label="LocalQueue">
          {!queueName ? <p className={muted}>No queue name: spec.queueName is empty, so the Workload names no LocalQueue.</p> : <>
            <div className="flex flex-wrap items-center gap-2 break-all [&_button]:text-left">
              <ResourceLink name={queueName} kind="localqueues" namespace={namespace} group={KUEUE_GROUP} onNavigate={onNavigate} />
              <ReadState read={localQueue} missing="Not found" found={localQueue?.state === 'found' ? <StatusPill status={getLocalQueueStatus(localQueue.object)} /> : undefined} />
            </div>
            {localQueue?.state === 'not_found' && <p className={muted}>No LocalQueue {queueName} exists in namespace {namespace}. Kueue can't admit this Workload until it does.</p>}
          </>}
        </Step>
        {queueName && reads && (
          <Step label="ClusterQueue">
            {configured ? <>
              <ClusterQueueReadSummary name={configured} read={configuredRead} onNavigate={onNavigate} />
              <p className="text-xs text-theme-text-tertiary">Configured by LocalQueue {queueName}.</p>
              {eligibility && <div className="text-xs"><span className="text-theme-text-tertiary">Namespace {namespace}: </span><FactValue fact={eligibility} /><FactSource fact={eligibility} /></div>}
            </> : <p className={muted}>
              {localQueue?.state === 'found' ? 'The LocalQueue names no ClusterQueue.' : localQueue?.state === 'loading' ? 'Checking…' : 'Unknown until the LocalQueue is read.'}
            </p>}
          </Step>
        )}
        <Step label="Reservation">
          {reserved ? <>
            <div className="flex flex-wrap items-center gap-2 break-all [&_button]:text-left">
              <span className="text-xs text-theme-text-tertiary">Quota reserved in</span>
              <ResourceLink name={reserved} kind="clusterqueues" group={KUEUE_GROUP} onNavigate={onNavigate} />
            </div>
            {configured && configured !== reserved && <p className="text-xs text-warning-text">Reserved in {reserved}; LocalQueue {queueName} names {configured}.</p>}
          </> : <p className={muted}>No reservation reported.</p>}
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
  if (quota?.status !== 'False' || !quota.message || isKueueConditionStale(quota, data.metadata?.generation)) return null
  const parsed = parseFlavorAttempts(quota.message)
  if (!parsed && !quota.message.startsWith("couldn't assign flavors") && !quota.message.startsWith('failed to assign flavors')) return null
  return (
    <Section variant="card" title="Reasons Reported per Flavor">
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
