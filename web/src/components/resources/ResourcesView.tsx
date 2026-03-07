import { useState, useMemo } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ApiError, fetchJSON, isForbiddenError, useSecretCertExpiry, useTopPodMetrics, useTopNodeMetrics } from '../../api/client'
import { useAPIResources } from '../../api/apiResources'
import { usePinnedKinds } from '../../hooks/useFavorites'
import { useOpenLogs, useOpenWorkloadLogs } from '../dock'
import {
  ResourcesView as BaseResourcesView,
  CORE_RESOURCES,
} from '@skyhook-io/k8s-ui'
import type { ResourceQueryResult } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'

interface ResourceCountsResponse {
  counts: Record<string, number>
  forbidden?: string[]
}

interface ResourcesViewProps {
  namespaces: string[]
  selectedResource?: SelectedResource | null
  onResourceClick?: (resource: SelectedResource | null) => void
  onResourceClickYaml?: NavigateToResource
  onKindChange?: () => void
}

export function ResourcesView({ namespaces, selectedResource, onResourceClick, onResourceClickYaml, onKindChange }: ResourcesViewProps) {
  const location = useLocation()
  const navigate = useNavigate()

  // API resources discovery
  const { data: apiResources } = useAPIResources()

  // Track the selected kind from the k8s-ui component
  const [selectedKind, setSelectedKind] = useState<{ name: string; kind: string; group: string } | null>(null)

  // Lightweight resource counts for sidebar badges (~2KB instead of ~608MB)
  const namespacesParam = namespaces.join(',')
  const { data: countsData } = useQuery({
    queryKey: ['resource-counts', namespacesParam],
    queryFn: async () => {
      const params = new URLSearchParams()
      if (namespaces.length > 0) params.set('namespaces', namespacesParam)
      return fetchJSON<ResourceCountsResponse>(`/resource-counts?${params}`)
    },
    staleTime: 10000,
    refetchInterval: 15000,
  })

  // Determine if selected kind is a CRD (only CRDs should send ?group= to backend)
  const isSelectedCrd = useMemo(() => {
    if (!selectedKind) return false
    // Check API resources first, fall back to CORE_RESOURCES
    const match = apiResources?.find(r => r.name === selectedKind.name && r.group === selectedKind.group)
      ?? CORE_RESOURCES.find(r => r.name === selectedKind.name && r.group === selectedKind.group)
    return match?.isCrd ?? (!!selectedKind.group) // default: has group = likely CRD
  }, [selectedKind, apiResources])

  // Fetch full data only for the selected kind
  const selectedKindQuery = useQuery({
    queryKey: ['resources', selectedKind?.name, isSelectedCrd ? selectedKind?.group : '', namespaces],
    queryFn: async () => {
      if (!selectedKind) return []
      const params = new URLSearchParams()
      if (namespaces.length > 0) params.set('namespaces', namespacesParam)
      if (isSelectedCrd && selectedKind.group) params.set('group', selectedKind.group)
      const res = await fetch(`/api/resources/${selectedKind.name}?${params}`)
      if (!res.ok) {
        const errorData = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
        throw new ApiError(errorData.error || `Failed to fetch ${selectedKind.name}`, res.status, errorData)
      }
      return res.json()
    },
    enabled: !!selectedKind,
    staleTime: 30000,
    refetchInterval: 30000,
    retry: (failureCount: number, error: Error) => {
      if (isForbiddenError(error)) return false
      return failureCount < 3
    },
  })

  // Map to ResourceQueryResult shape
  const selectedKindQueryResult: ResourceQueryResult | undefined = useMemo(() => {
    if (!selectedKind) return undefined
    return {
      data: selectedKindQuery.data as any[] | undefined,
      isLoading: selectedKindQuery.isLoading,
      error: selectedKindQuery.error,
      refetch: selectedKindQuery.refetch,
      dataUpdatedAt: selectedKindQuery.dataUpdatedAt,
    }
  }, [selectedKind, selectedKindQuery.data, selectedKindQuery.isLoading, selectedKindQuery.error, selectedKindQuery.refetch, selectedKindQuery.dataUpdatedAt])

  // Metrics
  const { data: topPodMetrics } = useTopPodMetrics()
  const { data: topNodeMetrics } = useTopNodeMetrics()

  // Certificate expiry
  const { data: certExpiry, isError: certExpiryError } = useSecretCertExpiry()

  // Pinned kinds
  const { pinned, togglePin, isPinned } = usePinnedKinds()

  // Dock actions
  const openLogs = useOpenLogs()
  const openWorkloadLogs = useOpenWorkloadLogs()

  // Navigation adapter
  const handleNavigate = useMemo(() => {
    return (path: string, options?: { replace?: boolean }) => {
      navigate(path, { replace: options?.replace })
    }
  }, [navigate])

  return (
    <BaseResourcesView
      namespaces={namespaces}
      selectedResource={selectedResource}
      onResourceClick={onResourceClick}
      onResourceClickYaml={onResourceClickYaml}
      onKindChange={onKindChange}
      // Injected data
      apiResources={apiResources}
      // Lightweight counts for sidebar (replaces 233 parallel queries)
      resourceCounts={countsData?.counts}
      resourceForbidden={countsData?.forbidden}
      selectedKindQuery={selectedKindQueryResult}
      onSelectedKindChange={setSelectedKind}
      topPodMetrics={topPodMetrics}
      topNodeMetrics={topNodeMetrics}
      certExpiry={certExpiry}
      certExpiryError={certExpiryError}
      // Pinned kinds
      pinned={pinned}
      togglePin={togglePin}
      isPinned={(kind: string, group?: string) => isPinned(kind, group ?? '')}
      // Navigation
      locationSearch={location.search}
      locationPathname={location.pathname}
      onNavigate={handleNavigate}
      // Dock actions
      onOpenLogs={openLogs}
      onOpenWorkloadLogs={openWorkloadLogs}
    />
  )
}
