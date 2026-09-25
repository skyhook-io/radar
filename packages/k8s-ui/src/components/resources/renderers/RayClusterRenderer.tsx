import { useRef, useState } from 'react'
import { Activity, Boxes, Server } from 'lucide-react'
import type { ResourceRef, WorkloadPodInfo } from '../../../types'
import { apiVersionToGroup } from '../../../utils/navigation'
import { Badge } from '../../ui/Badge'
import { SelectMenu } from '../../ui/SelectMenu'
import { AlertBanner, ConditionsSection, type ConditionTone, Property, PropertyList, ResourceLink, Section } from '../../ui/drawer-components'
import { PodRow, PodListFrame, PodListToggle, workloadPodDetail } from '../../workload/PodList'
import { isRayObservationStale, rayClusterWorkerCounts } from '../resource-utils-ray'

export interface RayClusterPodSelection { nodeType?: 'head' | 'worker'; workerGroup?: string }
export interface RayClusterRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
  podEvidence?: { pods: WorkloadPodInfo[]; total: number; truncated: boolean }
  podsLoading?: boolean
  podsError?: string
  selection?: RayClusterPodSelection
  onSelectPods?: (selection: RayClusterPodSelection) => void
}

export function rayClusterConditionTone(condition: any): ConditionTone {
  if (!['True', 'False'].includes(condition.status)) return 'unknown'
  if (condition.type === 'HeadPodReady') return condition.status === 'True' ? 'ok' : 'warning'
  if (condition.type === 'RayClusterReplicaFailure') return condition.status === 'True' ? 'fail' : 'unknown'
  if (condition.type === 'RayClusterSuspending') return condition.status === 'True' ? 'warning' : 'unknown'
  return 'unknown'
}

export function RayClusterRenderer({ data, onNavigate, podEvidence, podsLoading, podsError, selection = {}, onSelectPods }: RayClusterRendererProps) {
  const [expanded, setExpanded] = useState(false)
  const podsRef = useRef<HTMLDivElement>(null)
  const status = data.status ?? {}
  const spec = data.spec ?? {}
  const conditions = status.conditions ?? []
  const stale = isRayObservationStale(data.metadata?.generation, status.observedGeneration)
  const head = conditions.find((c: any) => c.type === 'HeadPodReady')
  const counts = rayClusterWorkerCounts(data)
  const groups: any[] = spec.workerGroupSpecs ?? []
  const owners = (data.metadata?.ownerReferences ?? []).filter((owner: any) => owner.controller === true && apiVersionToGroup(owner.apiVersion) === 'ray.io' && ['RayService', 'RayJob'].includes(owner.kind))
  const scope = selection.workerGroup != null ? `Worker group: ${selection.workerGroup}` : selection.nodeType === 'head' ? 'Head' : 'All runtime Pods'
  const select = (value: RayClusterPodSelection) => { setExpanded(false); onSelectPods?.(value) }
  const row = (pod: WorkloadPodInfo) => <PodRow key={pod.name} name={pod.name} namespace={data.metadata.namespace} ready={pod.ready} healthLevel={pod.healthLevel} detail={workloadPodDetail(pod)} onNavigate={onNavigate} />
  return <>
    {stale && <AlertBanner variant="info" title="Status describes an earlier generation" message={`Specification generation ${data.metadata.generation}; controller observed ${status.observedGeneration}. Counts and conditions below retain that observation.`} />}
    <Section title="Runtime Health" icon={Activity} defaultExpanded>
      <PropertyList>
        <Property label="Ray Version" value={spec.rayVersion || 'Not specified'} />
        <Property label="Head" value={<Badge severity={stale ? 'neutral' : head?.status === 'True' ? 'success' : head?.status === 'False' ? 'warning' : 'neutral'}>{head?.status === 'True' ? 'Ready' : head?.status === 'False' ? 'Not ready' : 'Not reported'}{stale ? ' (stale)' : ''}</Badge>} />
        <Property label="Suspension Requested" value={spec.suspend === true ? 'Yes' : 'No'} />
        {status.state && <Property label="Reported State" value={status.state} />}
      </PropertyList>
      {head?.message && <p className="mt-2 whitespace-pre-wrap break-words text-sm text-theme-text-secondary">{head.message}</p>}
      <div className="mt-3 grid grid-cols-3 gap-2">
        {(['ready', 'running', 'desired'] as const).map(key => <div className="card-inner min-w-0" key={key}><p className="text-xs capitalize text-theme-text-secondary">{key} workers</p><p className="mt-1 text-lg font-semibold text-theme-text-primary">{counts[key] ?? 'Not reported'}</p></div>)}
      </div>
      <p className="mt-2 text-xs text-theme-text-tertiary">Controller-reported worker Pod counts. Running does not mean ready. These label-based counts can differ from the directly owned Pods below.</p>
      {status.observedGeneration == null && <p className="mt-2 text-xs text-theme-text-tertiary">Observed generation is not reported.</p>}
      {conditions.some((c: any) => c.type === 'RayClusterProvisioned' && c.status === 'True') && <p className="mt-2 text-xs text-theme-text-tertiary">Provisioned records initial provisioning, not continuing runtime health.</p>}
    </Section>
    {onSelectPods && <div ref={podsRef}><Section title="Runtime Pods" icon={Server} defaultExpanded>
      <SelectMenu ariaLabel="Pod scope" className="mb-3" searchPlaceholder="Find a worker group…"
        value={selection.workerGroup != null ? `group:${selection.workerGroup}` : selection.nodeType === 'head' ? 'head' : 'all'}
        options={[{ value: 'all', label: 'All runtime Pods' }, { value: 'head', label: 'Head' }, ...groups.map((g: any) => ({ value: `group:${g.groupName}`, label: `Worker group: ${g.groupName}` }))]}
        onChange={value => select(value === 'all' ? {} : value === 'head' ? { nodeType: 'head' } : { nodeType: 'worker', workerGroup: value.slice(6) })} />
      <p className="mb-2 text-xs text-theme-text-tertiary">Directly controlled by this RayCluster incarnation. Cleanup Job descendants are excluded. Open a Pod for logs.</p>
      {podsError ? <AlertBanner variant="warning" title="Pod evidence unavailable" message={podsError} /> : podsLoading ? <p className="text-sm text-theme-text-secondary">Reading runtime Pods…</p> : podEvidence ? <>
        <p className="mb-2 text-sm text-theme-text-secondary">{scope} · {podEvidence.total} {podEvidence.total === 1 ? 'Pod' : 'Pods'}{podEvidence.truncated ? ` (showing ${podEvidence.pods.length}, failures first)` : ''}</p>
        {podEvidence.total === 0 && <p className="text-sm text-theme-text-secondary">No directly owned Pods in this scope.</p>}
        <PodListFrame expanded={expanded} hasOverflow={podEvidence.pods.length > 20} overflow={podEvidence.pods.slice(20).map(row)} toggle={<PodListToggle expanded={expanded} hiddenCount={podEvidence.pods.length - 20} label="Pods" onToggle={() => setExpanded(!expanded)} />}>{podEvidence.pods.slice(0,20).map(row)}</PodListFrame>
      </> : <p className="text-sm text-theme-text-secondary">Pod evidence is not available.</p>}
    </Section></div>}
    <Section title={`Worker Groups (${groups.length})`} icon={Boxes} defaultExpanded>
      <p className="mb-3 text-xs text-theme-text-tertiary">Declared sizing, not observed group health. Replicas can span multiple hosts; controller-reported worker counts above are Pods. In-tree autoscaling: {spec.enableInTreeAutoscaling === true ? 'enabled' : 'disabled'}.</p>
      <div className="space-y-3">{groups.map((group: any) => <div className="card-inner min-w-0" key={group.groupName}>
        <div className="mb-2 flex items-start justify-between gap-3"><span className="break-all text-sm font-medium text-theme-text-primary">{group.groupName}</span>{onSelectPods && <button className="shrink-0 text-xs text-accent-text hover:underline" onClick={() => { select({ nodeType: 'worker', workerGroup: group.groupName }); podsRef.current?.scrollIntoView({ block: 'start' }) }}>View Pods</button>}</div>
        <PropertyList>
          <Property label="Replicas" value={group.replicas ?? 'Not specified'} />
          <Property label="Min / Max Replicas" value={`${group.minReplicas ?? 'Not specified'} / ${group.maxReplicas === 2147483647 ? 'No limit' : group.maxReplicas ?? 'Not specified'}`} />
          <Property label="Hosts per Replica" value={group.numOfHosts ?? 'Not specified'} />
          {group.suspend != null && <Property label="Controller Suspension" value={group.suspend ? 'Requested' : 'Not requested'} />}
        </PropertyList>
      </div>)}</div>
      {groups.length === 0 && <p className="text-sm text-theme-text-secondary">No worker groups declared.</p>}
    </Section>
    {(owners.length > 0 || status.head?.podName || status.head?.serviceName) && <Section title="References" defaultExpanded><PropertyList>
      {owners.map((owner: any) => <Property key={owner.uid} label="Controller" value={<ResourceLink kind={owner.kind === 'RayService' ? 'rayservices' : 'rayjobs'} group="ray.io" namespace={data.metadata.namespace} name={owner.name} onNavigate={onNavigate} />} />)}
      {status.head?.podName && <Property label="Reported Head Pod" value={<ResourceLink kind="pods" namespace={data.metadata.namespace} name={status.head.podName} onNavigate={onNavigate} />} />}
      {status.head?.serviceName && <Property label="Reported Head Service" value={<ResourceLink kind="services" namespace={data.metadata.namespace} name={status.head.serviceName} onNavigate={onNavigate} />} />}
    </PropertyList></Section>}
    <ConditionsSection conditions={conditions} getConditionTone={c => stale ? 'unknown' : rayClusterConditionTone(c)} defaultExpanded />
  </>
}
