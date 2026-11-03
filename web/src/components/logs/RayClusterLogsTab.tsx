import { useEffect, useRef } from 'react'
import { AlertBanner, PaneLoader, RadarUpgradeNote, type WorkloadPodInfo } from '@skyhook-io/k8s-ui'
import { isKubeRayClusterIdentity } from '@skyhook-io/k8s-ui/components/resources/resource-utils-ray'
import { podLogsUnavailableReason } from '@skyhook-io/k8s-ui/components/resources/renderers/RayClusterRenderer'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { ApiError, isStableClientError, useCapabilities, useRadarFeature, useWorkloadPods } from '../../api/client'
import type { RadarFeatureSupport } from '../../api/radarFeatures'
import type { ResourceRef } from '../../types'
import { MultiPodLogsTab } from './MultiPodLogsTab'

export function isKubeRayCluster(kind: string, group: string | undefined, apiVersion: string | undefined): boolean {
  return kind === 'RayCluster' && isKubeRayClusterIdentity(group, apiVersion)
}

// Pod container info marks native sidecars from the release that first
// advertised rayClusterPods; older Radars that list RayCluster Pods omit it.
export function useSidecarsMarked(): boolean {
  return useCapabilities().data?.features?.rayClusterPods === true
}

// The container a Pod's logs open on. Container lists put init containers
// first and only reported statuses say which those are, so with no statuses
// the viewer keeps its own default. A regular init container that isn't done
// while the Pod is Pending is what it waits on (a worker waiting for the head
// sits in KubeRay's wait-gcs-ready), though it may not have started yet; in a
// Failed Pod it is what failed. Native sidecars never finish, so they are
// never that container. Without sidecar marks (an older Radar) an unready
// init container can't be told from a sidecar, so one is chosen only in a
// Pending Pod and only when it is the sole unready init container.
export function logsContainer(pod: WorkloadPodInfo, sidecarsMarked: boolean): string | undefined {
  const statuses = pod.containerStatuses ?? []
  if (!statuses.length) return undefined
  const init = new Set(statuses.filter(status => status.init).map(status => status.name))
  const unfinished = pod.containers.filter(name => statuses.some(status => status.init && status.name === name && !status.ready && !(sidecarsMarked && status.sidecar)))
  const blocking = sidecarsMarked
    ? (pod.phase === 'Pending' || pod.phase === 'Failed' ? unfinished[0] : undefined)
    : (pod.phase === 'Pending' && unfinished.length === 1 ? unfinished[0] : undefined)
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


// The generic Logs tab lists Pods recalled from history, deleted ones
// included. A RayCluster's tab lists the Pods its current incarnation owns
// instead (the same request as the renderer's Runtime Pods), and falls back to
// the history list, saying so, when that list can't be read: the route needs
// get rayclusters and list pods, which a reader of Pod logs may lack.
export function RayClusterLogsTab({
  namespace,
  name,
  uid,
  suspendRequested,
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
  suspendRequested: boolean
  rootLoading: boolean
  rootError: Error | null
  historyPods: ResourceRef[]
  selectedPod: string | null
  onSelectPod: (name: string | null) => void
  initialContainer: string | null
}) {
  const { support, upgrade } = useRadarFeature('rayClusterPods')
  const sidecarsMarked = useSidecarsMarked()
  const { canViewLogs } = useNamespacedCapabilities(namespace)
  const unsupported = support === 'unsupported'
  const query = useWorkloadPods('rayclusters', namespace, uid && !unsupported ? name : '', { ownerUID: uid, limit: 200, refetchInterval: 5000 })
  const owned = !unsupported && uid && query.data && !isStableClientError(query.error) ? query.data : undefined
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
  const container = selected && !(initialContainer && selected.containers.includes(initialContainer)) ? logsContainer(selected, sidecarsMarked) : initialContainer
  const reason = unsupported ? '' : !uid ? rootError?.message ?? 'The RayCluster could not be read.' : rayClusterPodsError(query.error, support) ?? 'Its current Pods could not be read.'
  const refreshError = owned && query.error ? rayClusterPodsError(query.error, support) : undefined
  const sentence = (text: string) => /[.!?]$/.test(text) ? text : `${text}.`

  return (
    <div className="flex h-full flex-col">
      {unsupported && upgrade && <div className="shrink-0 px-4 pt-3"><RadarUpgradeNote requirement={upgrade} /></div>}
      {owned?.truncated && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="info" title="Pod list capped" message={`Showing the ${owned.pods.length} least healthy of ${owned.total} Pods. To reach the others, narrow Runtime Pods on Overview to the head or one worker group and use a Pod's Logs button.`} />
        </div>
      )}
      {!canViewLogs && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="warning" title="Pod logs not readable" message="Reading a Pod's logs in this namespace needs get on pods/log, which you don't have. The Pods are listed so you can see which exist." />
        </div>
      )}
      {refreshError && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="info" title="Pod list not refreshed" message={`Showing the last list Radar read. ${sentence(refreshError)}`} />
        </div>
      )}
      {!owned && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="warning" title="Current Pods unavailable" message={`Showing Pods Radar has recorded for this RayCluster, which can include deleted ones.${reason ? ` ${sentence(reason)}` : ''}`} />
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
          selectedPodNote={selected ? podLogsUnavailableReason(selected) : undefined}
          emptyMessage={owned ? `This RayCluster has no Pods right now.${suspendRequested ? ' Its suspension is requested.' : ''}` : undefined}
        />
      </div>
    </div>
  )
}
