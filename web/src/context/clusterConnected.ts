import { createContext, useContext } from 'react'

// Whether the cluster is connected right now, for data hooks that poll it.
// Kept apart from ConnectionContext so api hooks can read it without a
// circular import. Defaults to true outside the provider (tests, hosts).
export const ClusterConnectedContext = createContext(true)

export function useClusterConnected(): boolean {
  return useContext(ClusterConnectedContext)
}
