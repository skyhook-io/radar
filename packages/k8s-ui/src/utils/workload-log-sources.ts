import type { WorkloadPodInfo, WorkloadPodContainerInfo } from '../types/core'

/** Identity of one container in one Pod; names can mean different things in different Pods. */
export function podContainerKey(pod: string, container: string): string {
  return `${pod}/${container}`
}

/** Whether a container of the named Pod is a regular init container. */
export function isInitContainer(pods: WorkloadPodInfo[], pod: string, container: string): boolean {
  const status = pods.find(p => p.name === pod)?.containerStatuses?.find(c => c.name === container)
  return Boolean(status?.init && !status.sidecar)
}

/** Container names that are a regular init container in every Pod that reports them. */
export function initContainerNames(pods: WorkloadPodInfo[]): Set<string> {
  const init = new Set<string>()
  const other = new Set<string>()
  for (const pod of pods) {
    for (const status of pod.containerStatuses ?? []) {
      if (status.init && !status.sidecar) init.add(status.name)
      else other.add(status.name)
    }
  }
  for (const name of other) init.delete(name)
  return init
}

/**
 * Whether "All containers" mixes output from several containers, so each line
 * should name its container: more than one app container, a shown Pod still
 * initializing, or lines from more than one container, or from an init
 * container or sidecar, on screen. One app container beside a finished init
 * container reads as a single source.
 */
export function viewMixesContainers(pods: WorkloadPodInfo[], entries: Iterable<{ pod?: string; container: string; initContainer?: boolean }>): boolean {
  const apps = new Set<string>()
  const supporting = new Set<string>()
  for (const pod of pods) {
    const statuses = pod.containerStatuses ?? []
    const roles = new Map(statuses.map(status => [status.name, status]))
    for (const name of pod.containers) {
      const status = roles.get(name)
      if (status?.init || status?.sidecar) supporting.add(podContainerKey(pod.name, name))
      else apps.add(name)
    }
    if (statuses.some(status => !status.init && status.state === 'waiting' && status.reason === 'PodInitializing')) return true
  }
  if (apps.size > 1) return true
  let first: string | undefined
  for (const entry of entries) {
    if (entry.initContainer || (entry.pod && supporting.has(podContainerKey(entry.pod, entry.container)))) return true
    if (first === undefined) first = entry.container
    else if (entry.container !== first) return true
  }
  return false
}

// The init container a Pod is waiting on: the first regular one that hasn't
// completed, or a native sidecar that hasn't started.
function blockingInitContainer(statuses: WorkloadPodContainerInfo[]): WorkloadPodContainerInfo | undefined {
  return statuses.find(status => status.init && (status.sidecar
    ? !status.started
    : !(status.state === 'terminated' && status.reason === 'Completed')))
}

function blockerText(status: WorkloadPodContainerInfo): string {
  if (status.sidecar) return `sidecar ${status.name} hasn't finished starting`
  const state = status.state === 'running' ? 'is still running'
    : status.state === 'terminated' ? (status.reason ? `exited (${status.reason})` : 'exited')
      : status.reason && status.reason !== 'PodInitializing' ? `is in ${status.reason}`
        : "hasn't finished"
  return `init container ${status.name} ${state}`
}

interface InitializingNoticeOptions {
  /** The container the viewer is filtered to; empty for all containers. */
  selectedContainer?: string
  /** Pod/container keys that already produced lines, so they have started. */
  containersWithOutput?: ReadonlySet<string>
  /** Short name for a Pod in the notice, matching the line prefix. */
  podLabel?: (pod: WorkloadPodInfo) => string
}

/**
 * Explains why app containers in the shown Pods have no output: they are
 * waiting for an init container. Empty when no shown Pod is initializing, or
 * when the Radar serving the logs doesn't report container states. Pod status
 * arrives when a Pod is first seen, so a container that has since produced
 * lines is no longer reported as waiting.
 */
export function initializingPodsNotice(pods: WorkloadPodInfo[], options: InitializingNoticeOptions = {}): string {
  const { selectedContainer = '', containersWithOutput, podLabel = pod => pod.name } = options
  const stuck: { pod: WorkloadPodInfo; message: string }[] = []
  for (const pod of pods) {
    const statuses = pod.containerStatuses ?? []
    const waiting = statuses.filter(status => !status.init && status.state === 'waiting' && status.reason === 'PodInitializing'
      && (selectedContainer === '' || status.name === selectedContainer)
      && !containersWithOutput?.has(podContainerKey(pod.name, status.name)))
    const blocker = blockingInitContainer(statuses)
    if (waiting.length === 0 || !blocker) continue
    const subject = waiting.length === 1 ? `${waiting[0].name} hasn't started` : `${waiting.map(status => status.name).join(', ')} haven't started`
    stuck.push({ pod, message: `${subject}: ${blockerText(blocker)}.` })
  }
  if (stuck.length === 0) return ''
  const distinct = new Set(stuck.map(item => item.message))
  if (pods.length === 1) return stuck[0].message
  if (stuck.length === 1) return `Pod ${podLabel(stuck[0].pod)}: ${stuck[0].message}`
  if (distinct.size === 1) return `${stuck.length} of ${pods.length} Pods: ${stuck[0].message}`
  return `${stuck.length} of ${pods.length} Pods have app containers waiting on init containers.`
}
