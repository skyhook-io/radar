import { Gauge } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import {
  AlertBanner,
  ConditionsSection,
  LabelSelectorDisplay,
  Property,
  PropertyList,
  ResourceLink,
  Section,
  type ConditionTone,
} from '../../ui/drawer-components'
import {
  getClusterQueueCohort,
  getClusterQueueStatus,
  getLocalQueueStatus,
  getKueueQueueCondition,
  getKueueQueueQuotaRows,
  isKueueConditionStale,
  type KueueQuotaRow,
} from '../resource-utils-kueue'

const KUEUE_GROUP = 'kueue.x-k8s.io'
interface QueueRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

const quantity = (value: string | number | undefined | null) => (value == null ? 'Not reported' : String(value))

function QueueStatus({ data, local = false }: Pick<QueueRendererProps, 'data'> & { local?: boolean }) {
  const condition = getKueueQueueCondition(data)
  const badge = local ? getLocalQueueStatus(data) : getClusterQueueStatus(data)
  const stale = isKueueConditionStale(condition, data.metadata?.generation)
  const stop = data.spec?.stopPolicy ?? 'None'
  const counts = [
    ['Pending', data.status?.pendingWorkloads],
    ['Reserving quota', data.status?.reservingWorkloads],
    ['Admitted', data.status?.admittedWorkloads],
  ] as const
  return (
    <>
      {stale && (
        <AlertBanner
          variant="info"
          title="Reported state describes an earlier generation"
          message={`Active condition: generation ${condition.observedGeneration}; queue: generation ${data.metadata.generation}. The reported state may not reflect the current specification.`}
        />
      )}
      <Section title="Queue Status" icon={Gauge}>
        <PropertyList>
          <Property label="Reported State" value={<Badge colorClass={badge.color}>{badge.text}</Badge>} />
          <Property label="Stop Policy" value={stop} />
          {counts.some(([, count]) => count != null) &&
            counts.map(([label, count]) => <Property key={label} label={label} value={quantity(count)} />)}
        </PropertyList>
        {condition?.message && (
          <p className="mt-2 whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{condition.message}</p>
        )}
        {!condition && counts.some(([, count]) => count != null) && (
          <p className="mt-2 text-sm text-theme-text-secondary">No Active condition reported.</p>
        )}
        {counts.every(([, count]) => count == null) ? (
          <p className="mt-2 text-xs text-theme-text-tertiary">
            {condition ? 'Workload counts have not been reported.' : 'Controller status has not been reported.'}
          </p>
        ) : (
          <p className="mt-2 text-xs text-theme-text-tertiary">
            Reserving counts include admitted Workloads; these counts are not additive. Admission does not establish
            that Pods are running.
          </p>
        )}
        {(stop === 'Hold' || stop === 'HoldAndDrain') && (
          <p className="mt-2 text-sm text-theme-text-secondary">
            Configured to stop new reservations and cancel reservations for Workloads awaiting admission.{' '}
            {stop === 'HoldAndDrain'
              ? 'Admitted Workloads are to be evicted.'
              : 'Already admitted Workloads may continue.'}{' '}
            The reported state above shows the controller observation.
          </p>
        )}
      </Section>
    </>
  )
}

function QueueConditions({ data }: Pick<QueueRendererProps, 'data'>) {
  const tone = (condition: any): ConditionTone => {
    if (isKueueConditionStale(condition, data.metadata?.generation) || condition.type !== 'Active') return 'unknown'
    if (condition.status === 'True') return 'ok'
    if (condition.status === 'False') return condition.reason === 'Stopped' ? 'unknown' : 'warning'
    return 'unknown'
  }
  return <ConditionsSection conditions={data.status?.conditions} getConditionTone={tone} defaultExpanded />
}

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
    <Section title={clusterQueue ? 'Quota by Flavor' : 'Quota Accounting'}>
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

export function LocalQueueRenderer({ data, onNavigate }: QueueRendererProps) {
  const parentInactive = getKueueQueueCondition(data)?.reason === 'ClusterQueueIsInactive'
  return (
    <>
      <QueueStatus data={data} local />
      <Section title="Cluster Queue">
        {data.spec?.clusterQueue ? (
          <ResourceLink
            name={data.spec.clusterQueue}
            kind="clusterqueues"
            group={KUEUE_GROUP}
            onNavigate={onNavigate}
          />
        ) : (
          <span className="text-sm text-theme-text-secondary">No ClusterQueue reference reported.</span>
        )}
        {parentInactive && (
          <p className="mt-2 text-sm text-theme-text-secondary">
            Inspect the ClusterQueue for the cause of its reported inactivity.
          </p>
        )}
      </Section>
      <QuotaAccounting data={data} onNavigate={onNavigate} />
      <QueueConditions data={data} />
    </>
  )
}

export function ClusterQueueRenderer({ data, onNavigate }: QueueRendererProps) {
  const spec = data.spec ?? {}
  const cohort = getClusterQueueCohort(data)
  const checks: { name: string; onFlavors?: string[] }[] = [
    ...(data.apiVersion === 'kueue.x-k8s.io/v1beta1'
      ? (spec.admissionChecks ?? []).map((name: string) => ({ name }))
      : []),
    ...(spec.admissionChecksStrategy?.admissionChecks ?? []),
  ]
  return (
    <>
      <QueueStatus data={data} />
      <QuotaAccounting data={data} clusterQueue onNavigate={onNavigate} />
      <Section title="Admission Policy">
        <PropertyList>
          <Property
            label="Eligible Namespaces"
            value={
              <LabelSelectorDisplay
                selector={spec.namespaceSelector}
                emptyText={spec.namespaceSelector == null ? 'No namespaces eligible' : 'All namespaces'}
              />
            }
          />
          <Property label="Queueing Strategy" value={spec.queueingStrategy ?? 'BestEffortFIFO'} />
          <Property label="Cohort" value={cohort === '-' ? 'None' : cohort} />
          {spec.preemption?.withinClusterQueue != null && (
            <Property label="Preemption within Queue" value={spec.preemption.withinClusterQueue} />
          )}
          {cohort !== '-' && spec.preemption?.reclaimWithinCohort != null && (
            <Property label="Reclaim within Cohort" value={spec.preemption.reclaimWithinCohort} />
          )}
          {cohort !== '-' && spec.preemption?.borrowWithinCohort?.policy != null && (
            <Property label="Borrow with Preemption" value={spec.preemption.borrowWithinCohort.policy} />
          )}
          {cohort !== '-' && spec.preemption?.borrowWithinCohort?.maxPriorityThreshold != null && (
            <Property
              label="Preemption Priority Threshold"
              value={spec.preemption.borrowWithinCohort.maxPriorityThreshold}
            />
          )}
          {cohort !== '-' && spec.flavorFungibility?.whenCanBorrow != null && (
            <Property label="Flavor when Borrowing" value={spec.flavorFungibility.whenCanBorrow} />
          )}
          {spec.flavorFungibility?.whenCanPreempt != null && (
            <Property label="Flavor when Preempting" value={spec.flavorFungibility.whenCanPreempt} />
          )}
          {spec.flavorFungibility?.preference != null && (
            <Property label="Flavor Preference" value={spec.flavorFungibility.preference} />
          )}
          {spec.fairSharing?.weight != null && (
            <Property label="Fair Sharing Weight" value={String(spec.fairSharing.weight)} />
          )}
          {data.status?.fairSharing?.weightedShare != null && (
            <Property label="Reported Weighted Share" value={String(data.status.fairSharing.weightedShare)} />
          )}
        </PropertyList>
        {cohort !== '-' && (
          <p className="mt-2 text-xs text-theme-text-tertiary">
            Absent an explicit limit, borrowing has no configured cap and all nominal quota may be lent. Borrowing
            depends on available cohort quota; a borrowing limit does not guarantee capacity. Reported borrowed amounts
            are already included in their totals.
          </p>
        )}
      </Section>
      {checks.length > 0 && (
        <Section title="Required Admission Checks">
          <div className="space-y-3">
            {checks.map((check, index) => (
              <div key={`${check.name}-${index}`} className="text-sm">
                <ResourceLink name={check.name} kind="admissionchecks" group={KUEUE_GROUP} onNavigate={onNavigate} />
                <p className="mt-1 break-words text-xs text-theme-text-secondary">
                  {check.onFlavors?.length
                    ? `Applies to flavors: ${check.onFlavors.join(', ')}`
                    : 'Applies to all flavors'}
                </p>
              </div>
            ))}
          </div>
        </Section>
      )}
      <QueueConditions data={data} />
    </>
  )
}
