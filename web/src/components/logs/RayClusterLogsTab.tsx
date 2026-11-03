import { useEffect, useRef } from 'react'
import { AlertBanner, PaneLoader, RadarUpgradeNote, type WorkloadPodInfo } from '@skyhook-io/k8s-ui'
import { ApiError, useRadarFeature, useWorkloadPods } from '../../api/client'
import type { RadarFeatureSupport } from '../../api/radarFeatures'
import type { ResourceRef } from '../../types'
import { MultiPodLogsTab } from './MultiPodLogsTab'

export function isKubeRayCluster(kind: string, group: string | undefined, apiVersion: string | undefined): boolean {
  if (kind !== 'RayCluster') return false
  return apiVersion ? apiVersion === 'ray.io/v1' : group === 'ray.io'
}

// The container a Pod's logs open on. Container lists put init containers
// first and only reported statuses say which those are, so with no statuses
// the viewer keeps its own default. A regular init container that isn't done
// while the Pod is Pending is what it waits on (a worker waiting for the head
// sits in KubeRay's wait-gcs-ready), though it may not have started yet; in a
// Failed Pod it is what failed. Native sidecars never finish, so they are
// never that container.
export function logsContainer(pod: WorkloadPodInfo): string | undefined {
  const statuses = pod.containerStatuses ?? []
  if (!statuses.length) return undefined
  const init = new Set(statuses.filter(status => status.init).map(status => status.name))
  const blocking = pod.phase === 'Pending' || pod.phase === 'Failed'
    ? pod.containers.find(name => statuses.some(status => status.init && !status.sidecar && status.name === name && !status.ready))
    : undefined
  return blocking ?? pod.containers.find(name => !init.has(name))
}

// What a failed read of a RayCluster's Pods means. A Radar of unknown version
// that answers 400 most likely predates the route, so say which Radar has it
// rather than echo the route's error.
export function rayClusterPodsError(error: unknown, support: RadarFeatureSupport): string | undefined {
  if (!error) return undefined
  if (error instanceof ApiError && error.status === 400 && support !== 'supported') return 'This Radar may not list a RayCluster\'s Pods; Radar v1.15 and later do.'
  return error instanceof Error ? error.message : 'The Pod list could not be read.'
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
  const owned = !unsupported && uid && query.data && !(query.error && listUnavailable(query.error)) ? query.data : undefined
  const defaultPod = owned?.pods[0]?.name
  const loading = !unsupported && (uid ? query.isLoading : rootLoading)

  // A pick made from the recorded Pods while the owned list was failing gives
  // way to the owned list's first Pod once it arrives, unless still owned.
  const showedRecorded = useRef(false)
  useEffect(() => {
    if (!owned) { if (!loading) showedRecorded.current = true; return }
    if (showedRecorded.current && defaultPod && selectedPod && !owned.pods.some(pod => pod.name === selectedPod)) onSelectPod(defaultPod)
    showedRecorded.current = false
  }, [owned, loading, defaultPod, selectedPod, onSelectPod])

  if (loading) return <PaneLoader label="Reading runtime Pods…" className="h-full" />

  // Shown by name so the strip holds still while health changes; the least
  // healthy Pod (the route's order) is the one opened first.
  const pods: ResourceRef[] = owned
    ? [...owned.pods].sort((a, b) => a.name.localeCompare(b.name)).map(pod => ({ kind: 'Pod', namespace, name: pod.name }))
    : historyPods
  const selected = owned?.pods.find(pod => pod.name === selectedPod)
  const container = selected && !(initialContainer && selected.containers.includes(initialContainer)) ? logsContainer(selected) : initialContainer
  const reason = unsupported ? '' : !uid ? rootError?.message ?? 'The RayCluster could not be read.' : rayClusterPodsError(query.error, support) ?? 'Its current Pods could not be read.'
  const refreshError = owned && query.error ? rayClusterPodsError(query.error, support) : undefined

  return (
    <div className="flex h-full flex-col">
      {unsupported && upgrade && <div className="shrink-0 px-4 pt-3"><RadarUpgradeNote requirement={upgrade} /></div>}
      {owned?.truncated && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="info" title="Pod list capped" message={`Showing the ${owned.pods.length} least healthy of ${owned.total} Pods. Runtime Pods on Overview can narrow the list to the head or one worker group.`} />
        </div>
      )}
      {refreshError && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="info" title="Pod list not refreshed" message={`Showing the last list Radar read. ${refreshError}`} />
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
          defaultPod={defaultPod}
          initialContainer={container}
          emptyMessage={owned ? 'This RayCluster has no Pods right now.' : undefined}
        />
      </div>
    </div>
  )
}
