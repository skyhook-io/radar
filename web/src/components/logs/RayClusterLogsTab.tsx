import { useEffect, useRef } from 'react'
import { AlertBanner, PaneLoader, RadarUpgradeNote } from '@skyhook-io/k8s-ui'
import { logsContainer, podLogsUnavailableReason } from '@skyhook-io/k8s-ui/components/resources/resource-utils-ray'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { isStableClientError, useCapabilities, useRadarFeature, useWorkloadPods } from '../../api/client'
import type { ResourceRef } from '../../types'
import { MultiPodLogsTab } from './MultiPodLogsTab'
import { rayClusterPodsError, useSidecarsMarked } from './rayClusterPods'

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
  const { canViewLogs, logsUnknown } = useNamespacedCapabilities(namespace)
  const capabilitiesFailed = useCapabilities().isError
  const logsDenied = !canViewLogs && !logsUnknown && !capabilitiesFailed
  const unsupported = support === 'unsupported'
  const query = useWorkloadPods('rayclusters', namespace, uid && !unsupported ? name : '', { ownerUID: uid, limit: 200, refetchInterval: 5000 })
  const owned = !unsupported && uid && query.data && !isStableClientError(query.error) ? query.data : undefined
  const defaultPod = owned?.pods[0]?.name
  const loading = !unsupported && (uid ? query.isLoading : rootLoading)

  // A pick made from the recorded Pods while the owned list was failing gives
  // way to the owned list's first Pod once it arrives, unless still owned.
  // Only a recorded list actually shown counts: a selection that predates
  // this tab (a deep link, or a Pod that has since left the owned list) stays.
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
          <AlertBanner variant="info" title="Pod list capped" message={`Showing the ${owned.pods.length} least healthy of ${owned.total} Pods. Runtime Pods on Overview can narrow to the head or one worker group (each view is capped at 200), with a Logs button on each Pod.`} />
        </div>
      )}
      {!canViewLogs && (
        <div className="shrink-0 px-4 pt-3">
          {logsDenied
            ? <AlertBanner variant="warning" title="Pod logs not readable" message="Reading a Pod's logs in this namespace needs get on pods/log, which you don't have. The Pods are listed so you can see which exist." />
            : <AlertBanner variant="info" title="Permissions not checked" message="Radar hasn't confirmed whether you can read Pod logs here; a Pod's logs say so if you can't." />}
        </div>
      )}
      {refreshError && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant="info" title="Pod list not refreshed" message={`Showing the last list Radar read. ${sentence(refreshError)}`} />
        </div>
      )}
      {!owned && (historyPods.length > 0 || reason) && (
        <div className="shrink-0 px-4 pt-3">
          <AlertBanner variant={unsupported ? 'info' : 'warning'} title={unsupported ? 'Showing recorded Pods' : 'Current Pods unavailable'} message={`Showing Pods Radar has recorded for this RayCluster, which can include deleted ones.${reason ? ` ${sentence(reason)}` : ''}`} />
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
          selectedPodNote={logsDenied ? 'You can\'t read Pod logs in this namespace.' : selected ? podLogsUnavailableReason(selected) : undefined}
          emptyMessage={owned ? `This RayCluster has no Pods right now.${suspendRequested ? ' Its suspension is requested.' : ''}` : 'Radar hasn\'t recorded any Pods for this RayCluster.'}
        />
      </div>
    </div>
  )
}
