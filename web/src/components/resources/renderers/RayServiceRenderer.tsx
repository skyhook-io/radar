import { apiVersionToGroup } from '@skyhook-io/k8s-ui/utils/navigation'
import { RayServiceRenderer as BaseRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/RayServiceRenderer'
import { isRayObservationStale } from '@skyhook-io/k8s-ui/components/resources/resource-utils-ray'
import { AlertBanner, ConditionsSection, type ConditionTone } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { ApiError, useResource } from '../../../api/client'

function runtimeConditionTone(condition: any): ConditionTone {
  if (!['True', 'False'].includes(condition.status)) return 'unknown'
  if (condition.type === 'RayClusterReplicaFailure') return condition.status === 'True' ? 'fail' : 'unknown'
  if (['HeadPodReady', 'RayClusterProvisioned'].includes(condition.type)) return condition.status === 'True' ? 'ok' : 'warning'
  return 'unknown'
}

function RuntimeEvidence({ root, name }: { root: any; name: string }) {
  const query = useResource<any>('rayclusters', root.metadata.namespace, name, 'ray.io')
  if (query.error) return <AlertBanner variant="warning" title="Runtime evidence unavailable" message={query.error instanceof ApiError && query.error.status === 404 ? 'The named RayCluster was not found; it may have been cleaned up.' : `Could not read RayCluster: ${query.error.message}`} />
  if (!query.data) return <p className="mt-2 text-sm text-theme-text-tertiary">Reading RayCluster status…</p>
  const cluster = query.data
  const owned = root.metadata.uid && cluster.apiVersion === 'ray.io/v1' && cluster.kind === 'RayCluster' && cluster.metadata?.namespace === root.metadata.namespace && cluster.metadata?.name === name && cluster.metadata.ownerReferences?.some((owner: any) => owner.controller === true && apiVersionToGroup(owner.apiVersion) === 'ray.io' && owner.kind === 'RayService' && owner.name === root.metadata.name && owner.uid === root.metadata.uid)
  if (!owned) return <AlertBanner variant="warning" title="Runtime ownership mismatch" message="The named RayCluster does not report this RayService as its controller. Runtime status is not attributed to this revision." />
  const conditions = (cluster.status?.conditions ?? []).filter((condition: any) => ['HeadPodReady', 'RayClusterProvisioned', 'RayClusterReplicaFailure', 'RayClusterSuspending', 'RayClusterSuspended'].includes(condition.type))
  const stale = isRayObservationStale(cluster.metadata.generation, cluster.status?.observedGeneration)
  return <div className="mt-2">
    <p className="text-xs text-theme-text-tertiary">Directly observed RayCluster conditions, separate from the RayService application snapshot.</p>
    {stale && <p className="mt-2 text-sm text-theme-text-secondary">RayCluster status describes an earlier generation.</p>}
    {conditions.length === 0 ? <p className="mt-2 text-sm text-theme-text-secondary">Runtime conditions are not reported by this RayCluster.</p> : <ConditionsSection conditions={conditions} getConditionTone={condition => stale ? 'unknown' : runtimeConditionTone(condition)} defaultExpanded />}
  </div>
}

export function RayServiceRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const active = data.status?.activeServiceStatus?.rayClusterName
  const pending = data.status?.pendingServiceStatus?.rayClusterName
  return <BaseRenderer data={data} onNavigate={onNavigate} runtimeEvidence={{
    active: active ? <RuntimeEvidence root={data} name={active} /> : undefined,
    pending: pending ? <RuntimeEvidence root={data} name={pending} /> : undefined,
  }} />
}
