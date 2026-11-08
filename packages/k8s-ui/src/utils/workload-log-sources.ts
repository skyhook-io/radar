import type { WorkloadPodInfo, WorkloadPodContainerInfo } from '../types/core'

/** Names of regular init containers, which run to completion before the app starts. */
export function initContainerNames(pods: WorkloadPodInfo[]): Set<string> {
  const names = new Set<string>()
  for (const pod of pods) {
    for (const status of pod.containerStatuses ?? []) {
      if (status.init && !status.sidecar) names.add(status.name)
    }
  }
  return names
}

/**
 * Whether "All containers" can mix output from several containers, so each
 * line should name its container: more than one app container, a shown Pod
 * still initializing, or init-container lines already on screen. A Pod with one
 * app container and a finished init container reads as a single source.
 */
export function viewMixesContainers(pods: WorkloadPodInfo[], entryContainers: Iterable<string>): boolean {
  const init = initContainerNames(pods)
  const sidecars = new Set<string>()
  for (const pod of pods) {
    for (const status of pod.containerStatuses ?? []) if (status.sidecar) sidecars.add(status.name)
  }
  const apps = new Set<string>()
  for (const pod of pods) {
    for (const name of pod.containers) if (!init.has(name) && !sidecars.has(name)) apps.add(name)
  }
  if (apps.size > 1) return true
  if (pods.some(pod => (pod.containerStatuses ?? []).some(status => !status.init && status.state === 'waiting' && status.reason === 'PodInitializing'))) return true
  for (const name of entryContainers) if (init.has(name) || sidecars.has(name)) return true
  return false
}

// The init container a Pod is waiting on: the first regular one that hasn't
// completed, or a native sidecar that hasn't started.
function blockingInitContainer(statuses: WorkloadPodContainerInfo[]): WorkloadPodContainerInfo | undefined {
  return statuses.find(status => status.init && (status.sidecar
    ? !status.started
    : !(status.state === 'terminated' && status.reason === 'Completed')))
}

function blockerState(status: WorkloadPodContainerInfo): string {
  if (status.state === 'running') return 'is still running'
  if (status.state === 'terminated') return status.reason ? `exited (${status.reason})` : 'exited'
  if (status.reason && status.reason !== 'PodInitializing') return `is in ${status.reason}`
  return "hasn't finished"
}

/**
 * Explains why app containers in the shown Pods have no output: they are
 * waiting for an init container. Empty when no shown Pod is initializing, or
 * when the Radar serving the logs doesn't report container states.
 */
export function initializingPodsNotice(pods: WorkloadPodInfo[], selectedContainer = ''): string {
  const messages: string[] = []
  for (const pod of pods) {
    const statuses = pod.containerStatuses ?? []
    const waiting = statuses.filter(status => !status.init && status.state === 'waiting' && status.reason === 'PodInitializing'
      && (selectedContainer === '' || status.name === selectedContainer))
    const blocker = blockingInitContainer(statuses)
    if (waiting.length === 0 || !blocker) continue
    const subject = waiting.length === 1 ? `${waiting[0].name} hasn't started` : `${waiting.map(status => status.name).join(', ')} haven't started`
    const holder = blocker.sidecar ? 'sidecar' : 'init container'
    messages.push(`${subject}: ${holder} ${blocker.name} ${blockerState(blocker)}.`)
  }
  if (messages.length === 0) return ''
  const distinct = [...new Set(messages)]
  if (distinct.length > 1) return `${messages.length} Pods are still initializing, so their app containers have no output yet.`
  return messages.length === 1 ? distinct[0] : `${messages.length} Pods: ${distinct[0]}`
}
