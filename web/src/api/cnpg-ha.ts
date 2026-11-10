import { useQuery } from '@tanstack/react-query'
import type { CNPGClusterHA } from '@skyhook-io/k8s-ui'
import { fetchJSON, useRadarFeature } from './client'

// /api/cnpg/clusters/{ns}/{name}/ha — quorum, disruption budgets, Leases,
// failure domains, Jobs, image drift and certificate renewal ownership. Each
// part is authorized on its own and says when it could not be read.
export function useCNPGClusterHA(namespace: string, name: string, options?: { enabled?: boolean; refetchInterval?: number | false }) {
  const { guard, gatedKey } = useRadarFeature('cnpgWorkspace')
  return useQuery<CNPGClusterHA>({
    queryKey: ['cnpg', 'ha', namespace, name, ...gatedKey],
    queryFn: ({ signal }) => guard(() => fetchJSON<CNPGClusterHA>(`/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/ha`, signal)),
    enabled: (options?.enabled ?? true) && !!name,
    staleTime: 10_000,
    refetchInterval: options?.refetchInterval ?? 30_000,
    refetchIntervalInBackground: false,
    retry: false,
  })
}
