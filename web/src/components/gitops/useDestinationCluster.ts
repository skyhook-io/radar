import { useCapabilities, useContexts, useGitOpsDestination } from '../../api/client'
import type { ContextInfo } from '../../types'

export interface DestinationCluster {
  // The destination's API server host, when Radar can tell.
  server?: string
  // A kubeconfig context for that cluster other than the current one.
  context?: ContextInfo
  // Whether this Radar may switch contexts at all.
  canSwitch: boolean
}

// Resolves where a remote GitOps object deploys and which kubeconfig context
// reaches it. The server does the matching; this only asks for remote objects.
export function useDestinationCluster(
  remote: boolean | undefined,
  kind: string,
  namespace: string,
  name: string,
): DestinationCluster {
  const destination = useGitOpsDestination(kind, namespace, name, !!remote)
  const contexts = useContexts()
  const capabilities = useCapabilities()
  const contextName = destination.data?.contexts[0]
  return {
    server: destination.data?.server || undefined,
    context: contextName ? contexts.data?.find((c) => c.name === contextName) : undefined,
    canSwitch: capabilities.data?.configManagement !== 'operator',
  }
}
