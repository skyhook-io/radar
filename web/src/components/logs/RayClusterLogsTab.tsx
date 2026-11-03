import { AlertBanner, PaneLoader } from '@skyhook-io/k8s-ui'
import { useRadarFeature, useWorkloadPods } from '../../api/client'
import type { ResourceRef } from '../../types'
import { MultiPodLogsTab } from './MultiPodLogsTab'

export function isKubeRayCluster(kind: string, group: string | undefined, apiVersion: string | undefined): boolean {
  if (kind !== 'RayCluster') return false
  return apiVersion ? apiVersion === 'ray.io/v1' : group === 'ray.io'
}

// The generic Logs tab lists Pods recalled from history, deleted ones
// included. A RayCluster's tab lists the Pods its current incarnation owns
// instead (the same request as the renderer's Runtime Pods), and falls back to
// the history list, saying so, when that list can't be read: the route needs
// get rayclusters and list pods, which a reader of Pod logs may lack.
export function RayClusterLogsTab({
  namespace,
  name,
  uid,
  historyPods,
  selectedPod,
  onSelectPod,
  initialContainer,
}: {
  namespace: string
  name: string
  uid: string | undefined
  historyPods: ResourceRef[]
  selectedPod: string | null
  onSelectPod: (name: string | null) => void
  initialContainer: string | null
}) {
  const { support } = useRadarFeature('rayClusterPods')
  const readable = Boolean(uid) && support !== 'unsupported'
  const query = useWorkloadPods('rayclusters', namespace, readable ? name : '', { ownerUID: uid, limit: 200, refetchInterval: 5000 })
  if (readable && query.isLoading) return <PaneLoader label="Reading runtime Pods…" className="h-full" />

  const owned = readable && query.data && !query.error ? query.data : undefined
  const pods: ResourceRef[] = owned ? owned.pods.map(pod => ({ kind: 'Pod', namespace, name: pod.name })) : historyPods
  const notice = owned
    ? owned.truncated ? `Showing ${owned.pods.length} of ${owned.total} Pods, failures first. Narrow the list from Runtime Pods on Overview.` : null
    : `Showing Pods Radar has recorded for this RayCluster, which can include deleted ones. ${support === 'unsupported' ? 'This Radar can\'t list the Pods it owns now.' : query.error?.message ?? 'Its current Pods could not be read.'}`

  return (
    <div className="flex h-full flex-col">
      {notice && <div className="shrink-0 px-4 pt-3"><AlertBanner variant={owned ? 'info' : 'warning'} title={owned ? 'Pod list capped' : 'Current Pods unavailable'} message={notice} /></div>}
      <div className="min-h-0 flex-1">
        <MultiPodLogsTab
          pods={pods}
          namespace={namespace}
          selectedPod={selectedPod}
          onSelectPod={onSelectPod}
          initialContainer={initialContainer}
          emptyMessage={owned ? 'This RayCluster has no Pods right now.' : undefined}
        />
      </div>
    </div>
  )
}
