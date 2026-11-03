import type { ReactNode } from 'react'
import { Gauge, Link2, ListChecks, Scale } from 'lucide-react'
import {
  AlertBanner,
  ConditionsSection,
  LabelSelectorDisplay,
  ResourceLink,
  Section,
  type ConditionTone,
} from '../../ui/drawer-components'
import { FactGrid, FactRow, FactSource, FactValue } from '../../facts'
import {
  getClusterQueueCohort,
  getClusterQueueStatus,
  getKueueQueueCondition,
  getKueueQueueQuotaRows,
  isKueueConditionStale,
  isKueueStopOnly,
  type KueueQuotaRow,
} from '../resource-utils-kueue'
import { describeKueuePolicy, type KueuePolicyField } from '../../kueue/policy'
import { isDenied, unreadReason, type RelatedRead } from '../../kueue/related-read'
import { localQueueInactiveBecauseParentStopped, localQueueStatusWithParent } from '../../kueue/queue-status'
import {
  AdmissionCheckRow,
  ClusterQueueReadSummary,
  ConfiguredStop,
  namespaceEligibilityFact,
  QueueCounts,
  ReadState,
  ReportedActive,
  type Navigate,
} from '../../kueue/QueueEvidence'
import { KUEUE_GROUP } from '../../kueue/constants'

interface QueueRendererProps {
  data: any
  onNavigate?: Navigate
}

/**
 * Related objects a host read as the viewer. Absent: the host doesn't read
 * them, and names are shown without read state.
 */
export interface LocalQueueReads {
  clusterQueue?: RelatedRead
  namespace?: RelatedRead
}

export interface ClusterQueueReads {
  flavors?: Record<string, RelatedRead>
  admissionChecks?: Record<string, RelatedRead>
  /** Other ClusterQueues naming the same cohort, from Radar's ClusterQueue list. */
  cohortMembers?: RelatedRead<string[]>
  /** Whether the ClusterQueue list the members came from covered every ClusterQueue. */
  cohortMembersCompleteness?: 'complete' | 'partial' | 'unknown'
}

const quantity = (value: string | number | undefined | null) => (value == null ? 'Not reported' : String(value))

function QuotaAccounting({ data, clusterQueue = false, onNavigate }: QueueRendererProps & { clusterQueue?: boolean }) {
  const rows = getKueueQueueQuotaRows(data, clusterQueue)
  const flavors = new Map<string, KueueQuotaRow[]>()
  for (const row of rows) {
    const entries = flavors.get(row.flavor) ?? []
    entries.push(row)
    flavors.set(row.flavor, entries)
  }
  const hasCohort = clusterQueue && getClusterQueueCohort(data) !== '-'
  return (
    <Section variant="card" title={clusterQueue ? 'Quota by Flavor' : 'Quota Accounting'} icon={Gauge}>
      <p className="mb-3 text-xs text-theme-text-tertiary">
        Reserved and admitted usage are quota accounting from resource requests, not measured utilization. Admitted
        usage is included in reservations.
        {hasCohort && ' Nominal quota is not a hard ceiling when borrowing is available.'}
      </p>
      {rows.length === 0 ? (
        <p className="text-sm text-theme-text-secondary">
          {clusterQueue ? 'No quota configuration or accounting reported.' : 'No quota accounting reported.'}
        </p>
      ) : (
        <div className="space-y-4">
          {[...flavors].map(([flavor, entries]) => (
            <div key={flavor} className="min-w-0">
              <div className="mb-2 break-all text-sm font-medium [&_button]:text-left">
                <ResourceLink name={flavor} kind="resourceflavors" group={KUEUE_GROUP} onNavigate={onNavigate} />
              </div>
              <table className="w-full table-fixed text-left text-xs">
                <thead className="text-theme-text-tertiary">
                  <tr>
                    <th className="w-[32%] py-2 pr-2 font-medium">Resource</th>
                    {clusterQueue && <th className="px-2 py-2 font-medium">Nominal</th>}
                    <th className="px-2 py-2 font-medium">Reserved</th>
                    <th className="py-2 pl-2 font-medium">Admitted usage</th>
                  </tr>
                </thead>
                <tbody>
                  {entries.map((row) => (
                    <QuotaRow key={row.resource} row={row} clusterQueue={clusterQueue} hasCohort={hasCohort} />
                  ))}
                </tbody>
              </table>
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}

function QuotaRow({ row, clusterQueue, hasCohort }: { row: KueueQuotaRow; clusterQueue: boolean; hasCohort: boolean }) {
  const borrowed = [row.borrowedReservation, row.borrowedUsage].some((value) => value != null && String(value) !== '0')
  const limits = hasCohort && row.configured && (row.borrowingLimit != null || row.lendingLimit != null)
  return (
    <>
      <tr className="border-t border-theme-border align-top">
        <td className="break-all py-2 pr-2 font-medium text-theme-text-primary">
          {row.resource}
          {clusterQueue && !row.configured && (
            <span className="mt-1 block text-theme-text-tertiary font-normal">Not in current configuration</span>
          )}
        </td>
        {clusterQueue && (
          <td className="break-all px-2 py-2 font-mono text-theme-text-secondary">{quantity(row.nominal)}</td>
        )}
        <td className="break-all px-2 py-2 font-mono text-theme-text-secondary">{quantity(row.reserved)}</td>
        <td className="break-all py-2 pl-2 font-mono text-theme-text-secondary">{quantity(row.used)}</td>
      </tr>
      {clusterQueue && (limits || borrowed) && (
        <tr>
          <td colSpan={4} className="pb-3 text-theme-text-tertiary">
            {limits && (
              <div className="flex flex-wrap gap-x-4 gap-y-1">
                {row.borrowingLimit != null && <span>Borrowing limit: {String(row.borrowingLimit)}</span>}
                {row.lendingLimit != null && <span>Lending limit: {String(row.lendingLimit)}</span>}
              </div>
            )}
            {borrowed && (
              <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1">
                <span>Borrowed reservation: {quantity(row.borrowedReservation)}</span>
                <span>Borrowed admitted usage: {quantity(row.borrowedUsage)}</span>
              </div>
            )}
          </td>
        </tr>
      )}
    </>
  )
}


function StaleBanner({ data }: { data: any }) {
  const condition = getKueueQueueCondition(data)
  if (!isKueueConditionStale(condition, data.metadata?.generation)) return null
  return (
    <AlertBanner
      variant="info"
      className="mb-0"
      title="Reported state describes an earlier generation"
      message={`Active condition: generation ${condition.observedGeneration}; queue: generation ${data.metadata.generation}. The reported state may not reflect the current specification.`}
    />
  )
}

function QueueConditions({ data, clusterQueue = false, parentStopped = false }: { data: any; clusterQueue?: boolean; parentStopped?: boolean }) {
  const tone = (condition: any): ConditionTone => {
    if (isKueueConditionStale(condition, data.metadata?.generation) || condition.type !== 'Active') return 'unknown'
    if (condition.status === 'True') return 'ok'
    if (condition.status === 'False') return isKueueStopOnly(condition, clusterQueue) || (parentStopped && condition.reason === 'ClusterQueueIsInactive') ? 'unknown' : 'warning'
    return 'unknown'
  }
  return <ConditionsSection variant="card" conditions={data.status?.conditions} getConditionTone={tone} defaultExpanded />
}

/** Reported (left) and configured (right) side by side once the content is wide enough. */
function Situation({ reported, configured }: { reported: ReactNode; configured: ReactNode }) {
  return (
    <div className="grid min-w-0 items-start gap-4 @min-[760px]/queue:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
      <Section variant="card" title="Reported by controller">{reported}</Section>
      <Section variant="card" title="Configured">{configured}</Section>
    </div>
  )
}

export function LocalQueueRenderer({ data, onNavigate, reads }: QueueRendererProps & { reads?: LocalQueueReads }) {
  const parentName: string | undefined = data.spec?.clusterQueue
  const namespace: string = data.metadata?.namespace ?? ''
  const parentStopped = localQueueInactiveBecauseParentStopped(data, reads?.clusterQueue)
  const reportsParentInactive = getKueueQueueCondition(data)?.reason === 'ClusterQueueIsInactive'
  const eligibility = parentName && reads ? namespaceEligibilityFact({ clusterQueue: parentName, parent: reads.clusterQueue, namespaceRead: reads.namespace }) : undefined
  return (
    <div className="@container/queue space-y-4">
      <StaleBanner data={data} />
      <Situation
        reported={<ReportedActive data={data} status={localQueueStatusWithParent(data, reads?.clusterQueue)}>
          {parentStopped && <p className="text-xs text-theme-text-secondary">ClusterQueue {parentName} reports Stopped (read from the ClusterQueue), so this LocalQueue's inactivity follows an intentional stop.</p>}
          {reportsParentInactive && !parentStopped && <p className="text-xs text-theme-text-secondary">{isDenied(reads?.clusterQueue)
            ? `The cause is on ClusterQueue ${parentName}, which you can’t read; a cluster administrator can check it.`
            : 'Inspect the ClusterQueue for the cause of its reported inactivity.'}</p>}
          <div className="border-t border-theme-border pt-3"><QueueCounts data={data} kind="LocalQueue" /></div>
        </ReportedActive>}
        configured={<div className="space-y-3">
          <ConfiguredStop data={data} />
          <div className="text-sm">
            <span className="text-theme-text-tertiary">ClusterQueue </span>
            {parentName
              ? <span className="break-all [&_button]:text-left"><ResourceLink name={parentName} kind="clusterqueues" group={KUEUE_GROUP} onNavigate={onNavigate} /></span>
              : <span className="text-theme-text-secondary">No ClusterQueue reference reported.</span>}
          </div>
        </div>}
      />
      {parentName && reads && (
        <Section variant="card" title="ClusterQueue" icon={Link2}>
          <div className="space-y-3">
            <ClusterQueueReadSummary name={parentName} read={reads.clusterQueue} onNavigate={onNavigate} />
            {reads.clusterQueue?.state === 'not_found' && <p className="text-sm text-theme-text-secondary">This LocalQueue points at a ClusterQueue that doesn't exist; Workloads submitted here can't be admitted until it does.</p>}
            {eligibility && <FactGrid>
              <FactRow label={`Namespace ${namespace}`}><FactValue fact={eligibility} /><FactSource fact={eligibility} /></FactRow>
            </FactGrid>}
          </div>
        </Section>
      )}
      <QuotaAccounting data={data} onNavigate={onNavigate} />
      <QueueConditions data={data} parentStopped={parentStopped} />
    </div>
  )
}

function PolicyRow({ data, label, field, value }: { data: any; label: string; field: KueuePolicyField; value: unknown }) {
  if (value == null) return null
  const policy = describeKueuePolicy(data.apiVersion, field, value)
  return (
    <FactRow label={label}>
      <code className="text-theme-text-primary">{policy.raw}</code>
      <div className="mt-0.5 text-xs text-theme-text-secondary">{policy.meaning ?? `Radar has no description of this value for ${data.apiVersion}.`}</div>
    </FactRow>
  )
}

function AdmissionPolicy({ data }: { data: any }) {
  const spec = data.spec ?? {}
  const cohort = getClusterQueueCohort(data)
  const inCohort = cohort !== '-'
  return (
    <Section variant="card" title="Admission Policy" icon={Scale}>
      <FactGrid>
        <FactRow label="Eligible namespaces">
          <LabelSelectorDisplay selector={spec.namespaceSelector} emptyText={spec.namespaceSelector == null ? 'No namespaces eligible (namespaceSelector not set)' : 'All namespaces ({})'} />
        </FactRow>
        <PolicyRow data={data} label="Queueing strategy" field="queueingStrategy" value={spec.queueingStrategy} />
        <PolicyRow data={data} label="Preemption within queue" field="preemption.withinClusterQueue" value={spec.preemption?.withinClusterQueue} />
        {inCohort && <PolicyRow data={data} label="Reclaim within cohort" field="preemption.reclaimWithinCohort" value={spec.preemption?.reclaimWithinCohort} />}
        {inCohort && <PolicyRow data={data} label="Borrow with preemption" field="preemption.borrowWithinCohort.policy" value={spec.preemption?.borrowWithinCohort?.policy} />}
        {inCohort && spec.preemption?.borrowWithinCohort?.maxPriorityThreshold != null && (
          <FactRow label="Preemption priority threshold">{String(spec.preemption.borrowWithinCohort.maxPriorityThreshold)}</FactRow>
        )}
        {inCohort && <PolicyRow data={data} label="Flavor when borrowing" field="flavorFungibility.whenCanBorrow" value={spec.flavorFungibility?.whenCanBorrow} />}
        <PolicyRow data={data} label="Flavor when preempting" field="flavorFungibility.whenCanPreempt" value={spec.flavorFungibility?.whenCanPreempt} />
        <PolicyRow data={data} label="Flavor preference" field="flavorFungibility.preference" value={spec.flavorFungibility?.preference} />
        <PolicyRow data={data} label="Admission fair sharing" field="admissionScope.admissionMode" value={spec.admissionScope?.admissionMode} />
        {spec.fairSharing?.weight != null && <FactRow label="Fair sharing weight">{String(spec.fairSharing.weight)}</FactRow>}
        {data.status?.fairSharing?.weightedShare != null && <FactRow label="Reported weighted share">{String(data.status.fairSharing.weightedShare)}</FactRow>}
      </FactGrid>
      <p className="mt-3 text-xs text-theme-text-tertiary">Values include any defaults the API server filled in.</p>
      {inCohort && (
        <p className="mt-3 text-xs text-theme-text-tertiary">
          Absent an explicit limit, borrowing has no configured cap and all nominal quota may be lent. Borrowing
          depends on available cohort quota; a borrowing limit does not guarantee capacity. Reported borrowed amounts
          are already included in their totals.
        </p>
      )}
    </Section>
  )
}

function clusterQueueChecks(data: any): { name: string; onFlavors?: string[] }[] {
  const spec = data.spec ?? {}
  return [
    ...(data.apiVersion === 'kueue.x-k8s.io/v1beta1' ? (spec.admissionChecks ?? []).map((name: string) => ({ name })) : []),
    ...(spec.admissionChecksStrategy?.admissionChecks ?? []),
  ]
}

/** Flavor names per resource group, in the order Kueue tries them. */
function clusterQueueFlavorGroups(data: any): { resources: string[]; flavors: string[] }[] {
  return (data.spec?.resourceGroups ?? []).map((group: any) => ({
    resources: group.coveredResources ?? [],
    flavors: (group.flavors ?? []).map((flavor: any) => flavor?.name).filter(Boolean),
  }))
}

export function clusterQueueRelatedNames(data: any): { flavors: string[]; admissionChecks: string[] } {
  return {
    flavors: [...new Set(clusterQueueFlavorGroups(data).flatMap(group => group.flavors))],
    admissionChecks: [...new Set(clusterQueueChecks(data).map(check => check.name))],
  }
}

function RelatedObjects({ data, reads, onNavigate }: QueueRendererProps & { reads?: ClusterQueueReads }) {
  const cohort = getClusterQueueCohort(data)
  const groups = clusterQueueFlavorGroups(data)
  const checks = clusterQueueChecks(data)
  const members = reads?.cohortMembers
  const heading = 'mb-2 text-xs font-medium uppercase tracking-wider text-theme-text-secondary'
  return (
    <Section variant="card" title="Related Objects" icon={ListChecks}>
      <div className="space-y-4">
        <div>
          <h4 className={heading}>Cohort</h4>
          {cohort === '-' ? <p className="text-sm text-theme-text-secondary">None: this ClusterQueue neither lends nor borrows quota.</p> : (
            <div className="space-y-1 text-sm">
              <code className="break-all text-theme-text-primary">{cohort}</code>
              {members && <p className="text-xs text-theme-text-secondary">
                {members.state === 'found'
                  ? members.object.length > 0 ? <>Other ClusterQueues naming this cohort: {members.object.map((name, index) => <span key={name} className="[&_button]:text-left">{index > 0 && ', '}<ResourceLink name={name} kind="clusterqueues" group={KUEUE_GROUP} onNavigate={onNavigate} /></span>)}</> : 'No other ClusterQueue names this cohort.'
                  : `Other members not read: ${unreadReason(members)}.`}
                {members.state === 'found' && reads?.cohortMembersCompleteness === 'partial' && ' Radar’s ClusterQueue list was incomplete, so there may be more.'}
                {members.state === 'found' && reads?.cohortMembersCompleteness === 'unknown' && ' This Radar doesn’t say whether its ClusterQueue list is complete.'}
              </p>}
              <p className="text-xs text-theme-text-tertiary">A cohort exists through the ClusterQueues that name it. Radar doesn't read Cohort objects, so a parent cohort or cohort-level quota isn't shown here.</p>
            </div>
          )}
        </div>
        <div>
          <h4 className={heading}>Resource flavors, in the order tried</h4>
          {groups.length === 0 ? <p className="text-sm text-theme-text-secondary">No resource groups configured.</p> : (
            <div className="space-y-3">
              {groups.map((group, index) => (
                <div key={index} className="space-y-1">
                  {groups.length > 1 && <p className="text-xs text-theme-text-tertiary">{group.resources.length > 0 ? group.resources.join(', ') : `Resource group ${index + 1}`}</p>}
                  <ol className="space-y-1">
                    {group.flavors.map((name, position) => (
                      <li key={name} className="flex flex-wrap items-center gap-2 break-all text-sm [&_button]:text-left">
                        <span className="w-4 shrink-0 text-right text-xs text-theme-text-tertiary">{position + 1}.</span>
                        <ResourceLink name={name} kind="resourceflavors" group={KUEUE_GROUP} onNavigate={onNavigate} />
                        <ReadState read={reads?.flavors?.[name]} />
                      </li>
                    ))}
                  </ol>
                </div>
              ))}
            </div>
          )}
        </div>
        {checks.length > 0 && (
          <div>
            <h4 className={heading}>Required admission checks</h4>
            <div className="space-y-3">
              {checks.map((check, index) => <AdmissionCheckRow key={`${check.name}-${index}`} name={check.name} onFlavors={check.onFlavors} read={reads?.admissionChecks?.[check.name]} onNavigate={onNavigate} />)}
            </div>
          </div>
        )}
      </div>
    </Section>
  )
}

export function ClusterQueueRenderer({ data, onNavigate, reads }: QueueRendererProps & { reads?: ClusterQueueReads }) {
  return (
    <div className="@container/queue space-y-4">
      <StaleBanner data={data} />
      <Situation
        reported={<ReportedActive data={data} status={getClusterQueueStatus(data)}>
          <div className="border-t border-theme-border pt-3"><QueueCounts data={data} kind="ClusterQueue" /></div>
        </ReportedActive>}
        configured={<ConfiguredStop data={data} />}
      />
      <RelatedObjects data={data} reads={reads} onNavigate={onNavigate} />
      <QuotaAccounting data={data} clusterQueue onNavigate={onNavigate} />
      <AdmissionPolicy data={data} />
      <QueueConditions data={data} clusterQueue />
    </div>
  )
}
