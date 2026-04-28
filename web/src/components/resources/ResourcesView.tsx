import { useState, useMemo, useCallback, useEffect } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ApiError, fetchJSON, isForbiddenError, useSecretCertExpiry, useTopPodMetrics, useTopNodeMetrics } from '../../api/client'
import { apiUrl, getAuthHeaders, getCredentialsMode, getBasename } from '../../api/config'
import { useAPIResources } from '../../api/apiResources'
import { initNavigationMap } from '@skyhook-io/k8s-ui'
import { usePinnedKinds } from '../../hooks/useFavorites'
import { useOpenLogs, useOpenWorkloadLogs } from '../dock'
import {
  ResourcesView as BaseResourcesView,
  CORE_RESOURCES,
} from '@skyhook-io/k8s-ui'
import type { ResourceQueryResult } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { getSkeletonYaml } from '../../utils/skeleton-yaml'

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

  // Initialize navigation kind↔plural maps from discovered API resources
  useEffect(() => {
    if (apiResources) initNavigationMap(apiResources)
  }, [apiResources])

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
    refetchInterval: 60000, // Safety net — SSE k8s_event drives near-real-time invalidation
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
      const res = await fetch(apiUrl(`/resources/${selectedKind.name}?${params}`), {
        credentials: getCredentialsMode(),
        headers: getAuthHeaders(),
      })
      if (!res.ok) {
        const errorData = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
        throw new ApiError(errorData.error || `Failed to fetch ${selectedKind.name}`, res.status, errorData)
      }
      return res.json()
    },
    enabled: !!selectedKind,
    staleTime: 30000,
    refetchInterval: 120000, // Safety net — SSE k8s_event drives near-real-time invalidation
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

  // Navigation adapter. k8s-ui constructs paths from `basePath` (which
  // includes the router basename so they line up with window.location.pathname
  // for path-equality checks) and from `window.location.pathname` directly.
  // React Router's navigate() applies the basename itself, so handing it a
  // path that already contains the basename double-prefixes it
  // (e.g. /c/abc/c/abc/resources/pods). Under that URL, getViewFromPath()
  // sees 'c' as the first segment and falls through to 'home' — which
  // manifests as "click a resource → bounced to the home dashboard" in
  // any host that mounts RadarApp under a non-empty basename (Radar Cloud).
  // Strip the basename here so react-router can re-apply it cleanly.
  const handleNavigate = useMemo(() => {
    const base = getBasename()
    return (path: string, options?: { replace?: boolean }) => {
      let p = path
      if (base && (p === base || p.startsWith(base + '/') || p.startsWith(base + '?'))) {
        p = p.slice(base.length) || '/'
      }
      navigate(p, { replace: options?.replace })
    }
  }, [navigate])

  // Create resource dialog
  const [createDialogOpen, setCreateDialogOpen] = useState(false)
  const [createDialogYaml, setCreateDialogYaml] = useState('')
  const [createDialogTitle, setCreateDialogTitle] = useState<string | undefined>()

  const handleCreateResource = useCallback((kind: { name: string; kind: string; group: string } | null) => {
    if (kind?.kind) {
      setCreateDialogYaml(getSkeletonYaml(kind.kind, kind.group))
      setCreateDialogTitle(`Create ${kind.kind}`)
    } else {
      setCreateDialogYaml('')
      setCreateDialogTitle(undefined)
    }
    setCreateDialogOpen(true)
  }, [])

  return (
    <>
    <BaseResourcesView
      key={location.pathname}
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
      // Navigation. basePath is basename-relative. React Router's useLocation
      // strips the basename from `location.pathname`, so reading the current
      // kind compares basename-relative paths on both sides. URL writes go
      // through `handleNavigate`, which strips any leading basename before
      // handing off to react-router (which re-applies it). Embedding hosts
      // (e.g. Radar Cloud at /c/{cluster}/resources) work without ResourcesView
      // needing to know the basename.
      basePath="/resources"
      locationSearch={location.search}
      locationPathname={location.pathname}
      onNavigate={handleNavigate}
      // Dock actions
      onOpenLogs={openLogs}
      onOpenWorkloadLogs={openWorkloadLogs}
      // Create resource
      onCreateResource={handleCreateResource}
    />
    <CreateResourceDialog
      open={createDialogOpen}
      onClose={() => setCreateDialogOpen(false)}
      initialYaml={createDialogYaml}
      title={createDialogTitle}
      onCreated={(result) => {
        onResourceClick?.({ kind: result.kind, namespace: result.namespace, name: result.name, group: '' })
      }}
    />
    </>
  )
}
