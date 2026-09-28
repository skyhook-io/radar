import { useQuery } from '@tanstack/react-query'
import type { CNPGWorkspaceResponse } from '@skyhook-io/k8s-ui'
import { fetchJSON } from './client'

// /api/cnpg/workspace
//
// Every CloudNativePG object the caller may read, with per-kind coverage, the
// CNPG issues on them and the no-schedule audit finding. One query feeds the
// workspace screens, the Resources sidebar counts and the composed summaries,
// so they can never disagree about what they count.
export function useCNPGWorkspace(namespaces: string[], options?: { enabled?: boolean }) {
  const ns = [...namespaces].sort().join(',')
  return useQuery<CNPGWorkspaceResponse>({
    queryKey: ['cnpg', 'workspace', ns],
    queryFn: ({ signal }) => fetchJSON<CNPGWorkspaceResponse>(`/cnpg/workspace${ns ? `?namespaces=${encodeURIComponent(ns)}` : ''}`, signal),
    enabled: options?.enabled ?? true,
    staleTime: 10_000,
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  })
}
