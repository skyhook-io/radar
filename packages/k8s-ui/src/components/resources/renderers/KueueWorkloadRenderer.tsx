import { Cpu, Gauge, ListChecks } from 'lucide-react'
import { formatRelativeAgeTime } from '../../../utils/format'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import {
  AlertBanner,
  ConditionsSection,
  Property,
  PropertyList,
  ResourceLink,
  Section,
  type ConditionTone,
} from '../../ui/drawer-components'
import { formatResources } from '../resource-utils'
import { getKueueWorkloadStatus, getKueueWorkloadPriority, getKueueWorkloadStatusCondition, isKueueWorkloadFailureReason, isKueueConditionStale } from '../resource-utils-kueue'

const KUEUE_GROUP = 'kueue.x-k8s.io'

interface KueueWorkloadRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

function admissionCheckSeverity(state: string): BadgeSeverity {
  switch (state) {
    case 'Ready':
      return 'success'
    case 'Rejected':
      return 'error'
    case 'Retry':
      return 'warning'
    default:
      return 'neutral'
  }
}

function resourceList(resources: any): string | null {
  if (!resources || typeof resources !== 'object' || Object.keys(resources).length === 0) return null
  return formatResources(Object.fromEntries(Object.entries(resources).map(([name, quantity]) => [name, String(quantity)])))
}

export function getKueueWorkloadConditionTone(condition: any): ConditionTone {
  if (condition?.status !== 'True' && condition?.status !== 'False') return 'unknown'

  if (condition.type === 'Finished') {
    if (condition.status !== 'True') return 'unknown'
    return isKueueWorkloadFailureReason(condition.reason) ? 'fail' : condition.reason === 'Succeeded' ? 'ok' : 'unknown'
  }

  if (['Evicted', 'Preempted', 'DeactivationTarget', 'BlockedOnPreemptionGates', 'WaitingForReplacementPods'].includes(condition.type)) {
    return condition.status === 'True' ? 'warning' : 'ok'
  }

  if (['Admitted', 'QuotaReserved', 'PodsReady'].includes(condition.type)) {
    return condition.status === 'True' ? 'ok' : 'unknown'
  }

  return 'unknown'
}

function FlavorAssignments({ flavors, onNavigate }: { flavors: any; onNavigate?: KueueWorkloadRendererProps['onNavigate'] }) {
  if (!flavors || typeof flavors !== 'object' || Object.keys(flavors).length === 0) return null
  return (
    <div className="space-y-1 break-all [&_button]:text-left">
      {Object.entries(flavors).map(([resource, flavor]) => (
        <div key={resource} className="text-xs">
          {resource}:{' '}
          <ResourceLink name={String(flavor)} kind="resourceflavors" group={KUEUE_GROUP} onNavigate={onNavigate} />
        </div>
      ))}
    </div>
  )
}

export function KueueWorkloadRenderer({ data, onNavigate }: KueueWorkloadRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const namespace = data.metadata?.namespace || ''
  const workloadStatus = getKueueWorkloadStatus(data)
  const priority = getKueueWorkloadPriority(data)
  const statusCondition = getKueueWorkloadStatusCondition(data)
  const stale = isKueueConditionStale(statusCondition, data.metadata?.generation)
  const failure = workloadStatus.level === 'unhealthy' && !stale ? statusCondition : undefined
  const podSets = Array.isArray(spec.podSets) ? spec.podSets : []
  const assignments = new Map(
    (Array.isArray(status.admission?.podSetAssignments) ? status.admission.podSetAssignments : []).map((assignment: any) => [
      assignment.name,
      assignment,
    ]),
  )
  const requests = new Map(
    (Array.isArray(status.resourceRequests) ? status.resourceRequests : []).map((request: any) => [request.name, request]),
  )
  const reclaimable = new Map(
    (Array.isArray(status.reclaimablePods) ? status.reclaimablePods : []).map((entry: any) => [entry.name, entry.count]),
  )
  const checks = Array.isArray(status.admissionChecks) ? status.admissionChecks : []
  const checkCounts = new Map<string, number>(['Rejected', 'Retry', 'Pending', 'Ready'].map(state => [state, 0]))
  for (const check of checks) {
    const state = check?.state || 'Unknown'
    checkCounts.set(state, (checkCounts.get(state) || 0) + 1)
  }

  return (
    <>
      {failure && <AlertBanner variant="error" title="Workload finished unsuccessfully" message={failure.message || failure.reason} />}

      {stale && <AlertBanner variant="info" title="Reported state describes an earlier generation" message={`Workload generation ${data.metadata.generation}. Stale condition: ${statusCondition?.type} (generation ${statusCondition?.observedGeneration}). Reported state may not reflect the current specification.`} />}

      <Section title="Admission" icon={Gauge} defaultExpanded>
        <PropertyList>
          <Property label="Reported State" value={<Badge colorClass={stale ? undefined : workloadStatus.color} severity={stale ? 'neutral' : undefined}>{workloadStatus.text}</Badge>} />
          <Property
            label="Local Queue"
            value={
              spec.queueName ? (
                <ResourceLink name={spec.queueName} kind="localqueues" namespace={namespace} group={KUEUE_GROUP} onNavigate={onNavigate} />
              ) : (
                'Not assigned'
              )
            }
          />
          <Property
            label="Cluster Queue"
            value={
              status.admission?.clusterQueue ? (
                <ResourceLink name={status.admission.clusterQueue} kind="clusterqueues" group={KUEUE_GROUP} onNavigate={onNavigate} />
              ) : (
                'No reservation reported'
              )
            }
          />
          {priority !== '-' && <Property label="Priority" value={priority} />}
          {checks.length > 0 && (
            <Property label="Reported Checks" value={Array.from(checkCounts).filter(([, count]) => count > 0).map(([state, count]) => `${count} ${state}`).join(' · ')} />
          )}
          <Property label="Workload Active" value={spec.active === false ? 'No' : 'Yes'} />
        </PropertyList>
      </Section>

      {checks.length > 0 && (
        <Section title={`Admission Checks (${checks.length})`} icon={ListChecks} defaultExpanded>
          <div className="max-w-2xl space-y-2">
            {checks.map((check: any, index: number) => (
              <div key={check?.name || index} className="card-inner">
                <div className="mb-2 grid max-w-md grid-cols-[minmax(0,1fr)_6rem] items-start gap-2">
                  <span className="min-w-0 break-words text-sm font-medium text-theme-text-primary [&_button]:text-left">
                    <ResourceLink
                      name={check?.name || `check-${index + 1}`}
                      kind="admissionchecks"
                      group={KUEUE_GROUP}
                      onNavigate={onNavigate}
                    />
                  </span>
                  <Badge severity={admissionCheckSeverity(check?.state)} size="sm" className="justify-self-start">
                    {check?.state || 'Unknown'}
                  </Badge>
                </div>
                {check?.message && <p className="whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{check.message}</p>}
                {check?.lastTransitionTime && (
                  <div className="mt-2">
                    <PropertyList>
                      <Property label="Last Transition" value={formatRelativeAgeTime(check.lastTransitionTime)} />
                    </PropertyList>
                  </div>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={status.conditions} getConditionTone={(condition) => isKueueConditionStale(condition, data.metadata?.generation) ? 'unknown' : getKueueWorkloadConditionTone(condition)} defaultExpanded />

      <Section title={`Pod Sets (${podSets.length})`} icon={Cpu} defaultExpanded>
        <p className="mb-2 text-xs text-theme-text-tertiary">
          Controller-reported requests for each PodSet, not measured usage. Reservation values are recorded when quota is assigned; they do not decrease after quota reclamation or completion.
        </p>
        {podSets.length > 0 ? (
          <div className="space-y-2">
            {podSets.map((podSet: any) => {
              const name = podSet?.name || 'main'
              const assignment: any = assignments.get(name)
              const request: any = requests.get(name)
              const evaluatedDemand = resourceList(request?.resources)
              const reservedResources = resourceList(assignment?.resourceUsage)
              const reserved = status.admission != null
              const reclaimableCount = reclaimable.get(name)
              return (
                <div key={name} className="card-inner">
                  <div className="mb-2 flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium text-theme-text-primary">{name}</span>
                    <Badge tone="structural" size="sm">
                      desired {String(podSet?.count ?? 'Not reported')}
                    </Badge>
                    {typeof podSet?.minCount === 'number' && (
                      <Badge tone="note" size="sm">
                        minimum {podSet.minCount}
                      </Badge>
                    )}
                  </div>
                  <PropertyList>
                    {assignment && <Property label="Pods at Reservation" value={assignment.count ?? 'Not reported'} />}
                    {typeof reclaimableCount === 'number' && <Property label="Reclaimable Pods" value={reclaimableCount} />}
                    <Property
                      label={reserved ? 'Resources at Reservation' : 'Evaluated Requests'}
                      value={reserved ? reservedResources || 'Not reported' : evaluatedDemand || 'Not reported'}
                    />
                    {assignment?.flavors && (
                      <Property
                        label="Resource Flavors"
                        value={<FlavorAssignments flavors={assignment.flavors} onNavigate={onNavigate} />}
                      />
                    )}
                  </PropertyList>
                </div>
              )
            })}
          </div>
        ) : (
          <div className="text-sm text-theme-text-tertiary">No PodSets declared</div>
        )}
      </Section>

    </>
  )
}
