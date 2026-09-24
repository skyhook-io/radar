import type { DestinationCluster } from './useDestinationCluster'
import { parseContextForSwitcher } from '../../utils/context-name'

export interface DestinationToast {
  message: string
  detail: string
  // Set when the resource can be opened by switching to a kubeconfig context.
  actionLabel?: string
}

// What to tell someone who clicked a resource that lives on a remote GitOps
// object's destination cluster, and how they can open it.
export function destinationToast(resource: string, dest: DestinationCluster): DestinationToast {
  if (dest.context) {
    const cluster = parseContextForSwitcher(dest.context).clusterName
    if (!dest.canSwitch) {
      return {
        message: `${resource} is on ${cluster}`,
        detail: 'Switching clusters is turned off in this Radar.',
      }
    }
    return {
      message: `${resource} is on ${cluster}`,
      detail: 'Opening it switches Radar to that cluster.',
      actionLabel: `Open in ${cluster}`,
    }
  }
  if (dest.server) {
    return {
      message: `${resource} is on another cluster`,
      detail: `No kubeconfig context points at ${hostOf(dest.server)}. Add one to open it from Radar.`,
    }
  }
  return {
    message: `${resource} is on another cluster`,
    detail: "Radar couldn't tell which cluster, so it can't open it from here.",
  }
}

function hostOf(server: string): string {
  try {
    return new URL(server).host
  } catch {
    return server
  }
}
