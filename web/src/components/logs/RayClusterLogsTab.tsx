import { AlertBanner, PaneLoader, RadarUpgradeNote, type WorkloadPodInfo } from '@skyhook-io/k8s-ui'
import { ApiError, useRadarFeature, useWorkloadPods } from '../../api/client'
import type { ResourceRef } from '../../types'
import { MultiPodLogsTab } from './MultiPodLogsTab'

export function isKubeRayCluster(kind: string, group: string | undefined, apiVersion: string | undefined): boolean {
  if (kind !== 'RayCluster') return false
  return apiVersion ? apiVersion === 'ray.io/v1' : group === 'ray.io'
}

// The container a Pod's logs open on. Container lists put init containers
// first and only reported statuses say which those are, so with no statuses
// the viewer keeps its own default. A Pod stays Pending until its regular init
// containers finish, so while it is Pending the first unready init container
// is what it is waiting on (a worker waiting for the head sits in KubeRay's
// wait-gcs-ready), though it may itself not have started yet. Past Pending,
// an unready init container is a native sidecar, never ahead of the
// application.
export function logsContainer(pod: WorkloadPodInfo): string | undefined {
  const statuses = pod.containerStatuses ?? []
  if (!statuses.length) return undefined
  const init = new Set(statuses.filter(status => status.init).map(status => status.name))
  const blockingInit = pod.phase === 'Pending' ? pod.containers.find(name => statuses.some(status => status.init && status.name === name && !status.ready)) : undefined
  return blockingInit ?? pod.containers.find(name => !init.has(name))
}

// A refresh that fails for a passing reason keeps the last list; these
// answers mean the list itself can't be had (denied, gone, recreated, or a
// Radar that doesn't serve the route).
function listUnavailable(error: unknown): boolean {
  return error instanceof ApiError && [400, 403, 404, 409].includes(error.status)
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
  rootLoading,
  rootError,
  historyPods,
  selectedPod,
  onSelectPod,
  initialContainer,
}: {
  namespace: string
  name: string
  uid: string | undefined
  rootLoading: boolean
  rootError: Error | null
  historyPods: ResourceRef[]
  selectedPod: string | null
  onSelectPod: (name: string | null) => void
  initialContainer: string | null
}) {
  const { support, upgrade } = useRadarFeature('rayClusterPods')
  const unsupported = support === 'unsupported'
  const query = useWorkloadPods('rayclusters', namespace, uid && !unsupported ? name : '', { ownerUID: uid, limit: 200, refetchInterval: 5000 })
  if (!unsupported && (uid ? query.isLoading : rootLoading)) return <PaneLoader label="Reading runtime Pods…" className="h-full" />

  const owned = !unsupported && uid && query.data && !(query.error && listUnavailable(query.error)) ? query.data : undefined
  // Shown by name so the strip holds still while health changes; the least
  // healthy Pod (the route's order) is the one opened first.
  const pods: ResourceRef[] = owned
    ? [...owned.pods].sort((a, b) => a.name.localeCompare(b.name)).map(pod => ({ kind: 'Pod', namespace, name: pod.name }))
    : historyPods
  const selected = owned?.pods.find(pod => pod.name === selectedPod)
  const reason = unsupported ? ''
    : !uid ? rootError?.message ?? 'The RayCluster could not be read.'
    : query.error instanceof ApiError && query.error.status === 400 && support !== 'supported' ? 'This Radar may not list a RayCluster\'s Pods; Radar v1.15 and later do.'
    : query.error?.message ?? 'Its current Pods could not be read.'

  return (
    <div className="flex h-full flex-col">
      {unsupported && upgrade && <div className="shrink-0 px-4 pt-3"><RadarUpgradeNote requirement={upgrade} /></div>}
      {owned?.truncated && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="info" title="Pod list capped" message={`Showing the ${owned.pods.length} least healthy of ${owned.total} Pods. Choosing a worker group under Runtime Pods on Overview narrows the list there, with a Logs button on each Pod.`} />
        </div>
      )}
      {!owned && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="warning" title="Current Pods unavailable" message={`Showing Pods Radar has recorded for this RayCluster, which can include deleted ones.${reason ? ` ${reason}` : ''}`} />
        </div>
      )}
      <div className="min-h-0 flex-1">
        <MultiPodLogsTab
          pods={pods}
          namespace={namespace}
          selectedPod={selectedPod}
          onSelectPod={onSelectPod}
          defaultPod={owned?.pods[0]?.name}
          initialContainer={initialContainer ?? (selected ? logsContainer(selected) : undefined)}
          emptyMessage={owned ? 'This RayCluster has no Pods right now.' : undefined}
        />
      </div>
    </div>
  )
}
