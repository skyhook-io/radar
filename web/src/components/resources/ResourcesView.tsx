import { useState, useMemo, useCallback, useEffect } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ApiError, debugNamespaceLog, fetchJSON, isForbiddenError, useCapabilities, useNamespaceCapabilities, useSecretCertExpiry, useTopPodMetrics, useTopNodeMetrics, useBulkDeleteResources, useBulkRestartWorkloads, useBulkScaleWorkloads, useAudit } from '../../api/client'
import { isBadgeWorthy } from '../../utils/auditBadges'
import type { AuditBadgeMessage } from '@skyhook-io/k8s-ui'
import { apiUrl, getAuthHeaders, getCredentialsMode, getBasename } from '../../api/config'
import { useAPIResources } from '../../api/apiResources'
import { initNavigationMap } from '@skyhook-io/k8s-ui'
import { usePinnedKinds } from '../../hooks/useFavorites'
import { useOpenLogs, useOpenWorkloadLogs } from '../dock'
import {
  canBulkRestartKind,
  canBulkScaleKind,
  ResourcesView as BaseResourcesView,
  CORE_RESOURCES,
  intersectWorkloadWrites,
} from '@skyhook-io/k8s-ui'
import type { Capabilities, ResourceQueryResult, WorkloadWritePermissions } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { kindToPlural, type NavigateToResource } from '../../utils/navigation'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { getSkeletonYaml } from '../../utils/skeleton-yaml'

interface ResourceCountsResponse {
  counts: Record<string, number>
  forbidden?: string[]
  reasons?: Record<string, string>
  unavailable?: string[]
}

interface ResourcesViewProps {
  namespaces: string[]
  selectedResource?: SelectedResource | null
  onResourceClick?: (resource: SelectedResource | null) => void
  onResourceClickYaml?: NavigateToResource
  onKindChange?: () => void
  onClearNamespaces?: () => void
}

type SelectedKindInfo = { name: string; kind: string; group: string } | null

const LARGE_RESOURCE_LIST_LIMIT = 25000
const LARGE_RESOURCE_LIST_GUARD_KEYS = new Set([
  'Pod',
  'Event',
  'apps/ReplicaSet',
  'discovery.k8s.io/EndpointSlice',
])

const deniedWorkloadWrites: WorkloadWritePermissions = {
  deployments: false,
  daemonSets: false,
  statefulSets: false,
  rollouts: false,
}

function resourceCountKey(kind: NonNullable<SelectedKindInfo>): string {
  return kind.group ? `${kind.group}/${kind.kind}` : kind.kind
}

export function ResourcesView({ namespaces, selectedResource, onResourceClick, onResourceClickYaml, onKindChange, onClearNamespaces }: ResourcesViewProps) {
  const location = useLocation()
  const navigate = useNavigate()

  const { data: capabilities } = useCapabilities()
  const namespaceForCapabilities = namespaces.length === 1 ? namespaces[0] : undefined
  const { data: namespaceCapabilities } = useNamespaceCapabilities(namespaceForCapabilities, capabilities)
  const namespaceCapabilityNames = useMemo(() => namespaces.length > 1 ? [...namespaces].sort() : [], [namespaces])
  const { data: namespaceCapabilitiesList } = useQuery<Array<Pick<Capabilities, 'workloadWrites'>>>({
    queryKey: ['capabilities', 'namespaces', namespaceCapabilityNames],
    queryFn: async () => {
      const results = await Promise.allSettled(
        namespaceCapabilityNames.map(async ns => ({
          namespace: ns,
          capabilities: await fetchJSON<Capabilities>(`/capabilities?namespace=${encodeURIComponent(ns)}`),
        }))
      )
      return results.map((result, index) => {
        if (result.status === 'fulfilled') {
          return { workloadWrites: result.value.capabilities.workloadWrites }
        }
        console.warn(`Failed to fetch namespace capabilities for ${namespaceCapabilityNames[index]}, withholding workload writes:`, result.reason)
        return { workloadWrites: deniedWorkloadWrites }
      })
    },
    enabled: namespaceCapabilityNames.length > 1 && capabilities != null,
    staleTime: 60000,
  })
  const multiNamespaceWorkloadWrites = useMemo(() => intersectWorkloadWrites(namespaceCapabilitiesList), [namespaceCapabilitiesList])

  // API resources discovery
  const { data: apiResources } = useAPIResources()

  // Initialize navigation kind↔plural maps from discovered API resources
  useEffect(() => {
    if (apiResources) initNavigationMap(apiResources)
  }, [apiResources])

  // Track the selected kind from the k8s-ui component
  const [selectedKind, setSelectedKind] = useState<SelectedKindInfo>(null)
  const workloadWrites = namespaces.length === 0
    ? capabilities?.workloadWrites
    : namespaces.length === 1
      ? namespaceCapabilities?.workloadWrites
      : multiNamespaceWorkloadWrites
  const canBulkRestartSelectedKind = useMemo(() => canBulkRestartKind(selectedKind, workloadWrites), [selectedKind, workloadWrites])
  const canBulkScaleSelectedKind = useMemo(() => canBulkScaleKind(selectedKind, workloadWrites), [selectedKind, workloadWrites])

  // Lightweight resource counts for sidebar badges (~2KB instead of ~608MB)
  const namespacesParam = namespaces.join(',')
  const { data: countsData, isError: countsIsError } = useQuery({
    queryKey: ['resource-counts', namespacesParam],
    queryFn: async () => {
      const params = new URLSearchParams()
      if (namespaces.length > 0) params.set('namespaces', namespacesParam)
      const startedAt = performance.now()
      debugNamespaceLog('resources:counts-fetch-start', { namespaces, params: params.toString() })
      try {
        return await fetchJSON<ResourceCountsResponse>(`/resource-counts?${params}`)
      } finally {
        debugNamespaceLog('resources:counts-fetch-end', {
          namespaces,
          params: params.toString(),
          durationMs: Math.round(performance.now() - startedAt),
        })
      }
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

  // The canonical Kind for the selected resource. selectedKind.kind is the plural
  // URL segment for CRDs/grouped kinds (e.g. "ingressroutes", "ingresses") — only
  // core no-group kinds resolve to the real Kind there — so resolve it via
  // discovery to match audit findings, which key by the real Kind ("IngressRoute").
  const selectedKindCanonical = useMemo(() => {
    if (!selectedKind) return undefined
    const match = apiResources?.find(r => r.name === selectedKind.name && r.group === selectedKind.group)
      ?? CORE_RESOURCES.find(r => r.name === selectedKind.name && r.group === selectedKind.group)
    return match?.kind ?? selectedKind.kind
  }, [selectedKind, apiResources])

  // Cluster Audit findings for the selected kind, keyed by "namespace/name" for
  // the resource list. The list shows ONE kind at a time, so ns/name is enough;
  // we still match the finding's group (built-ins → real group, CRDs → "") so a
  // kind shared across groups doesn't bleed findings across the two lists. Only
  // "badge-worthy" findings count (reference-integrity / lifecycle) — posture
  // and best-practice nags fire near-universally and would just be noise.
  const audit = useAudit(namespaces)
  const auditBadges = useMemo(() => {
    if (!selectedKind || !audit.data?.findings) return undefined
    const wantGroup = isSelectedCrd ? '' : selectedKind.group
    const map: Record<string, { danger: number; warning: number; messages: AuditBadgeMessage[] }> = {}
    for (const f of audit.data.findings) {
      if (f.kind !== selectedKindCanonical || (f.group ?? '') !== wantGroup) continue
      if (!isBadgeWorthy(f, audit.data.checks)) continue
      const k = `${f.namespace || ''}/${f.name}`
      const cur = map[k] ?? { danger: 0, warning: 0, messages: [] }
      if (f.severity === 'danger') cur.danger++
      else if (f.severity === 'warning') cur.warning++
      cur.messages.push({ severity: f.severity, message: f.message })
      map[k] = cur
    }
    for (const cur of Object.values(map)) {
      cur.messages.sort((a, b) => (a.severity === 'danger' ? 0 : 1) - (b.severity === 'danger' ? 0 : 1))
    }
    return map
  }, [audit.data?.findings, audit.data?.checks, selectedKind, selectedKindCanonical, isSelectedCrd])

  const selectedCountKey = selectedKind ? resourceCountKey(selectedKind) : ''
  const selectedCount = selectedCountKey ? countsData?.counts[selectedCountKey] : undefined
  const selectedCountUnavailable = selectedCountKey ? countsData?.unavailable?.includes(selectedCountKey) ?? false : false
  const isSelectedKindGuarded = selectedCountKey !== '' && LARGE_RESOURCE_LIST_GUARD_KEYS.has(selectedCountKey)
  const waitingForGuardCount = isSelectedKindGuarded && !countsData && !countsIsError
  const largeListBlocked = isSelectedKindGuarded && countsData != null && (selectedCountUnavailable || (selectedCount ?? 0) > LARGE_RESOURCE_LIST_LIMIT)
  const selectedKindQueryBlocked = waitingForGuardCount || largeListBlocked
  const podCount = countsData?.counts.Pod
  const podCountUnavailable = countsData?.unavailable?.includes('Pod') ?? false
  const podCountAllowsBulkMetrics = countsData != null && !podCountUnavailable && (podCount ?? 0) <= LARGE_RESOURCE_LIST_LIMIT
  const selectedKindName = selectedKind?.name.toLowerCase() ?? ''
  const topPodMetricsEnabled = selectedKindName === 'pods' && podCountAllowsBulkMetrics
  const topNodeMetricsEnabled = selectedKindName === 'nodes' && namespaces.length === 0 && podCountAllowsBulkMetrics
  const largeListGuard = selectedKind && largeListBlocked
    ? {
        kind: selectedKind.name,
        count: selectedCountUnavailable ? undefined : selectedCount,
        reason: selectedCountUnavailable ? 'count-unavailable' as const : 'too-many' as const,
        limit: LARGE_RESOURCE_LIST_LIMIT,
        namespaces,
      }
    : null

  // Fetch full data only for the selected kind
  const selectedKindQuery = useQuery({
    queryKey: ['resources', selectedKind?.name, isSelectedCrd ? selectedKind?.group : '', namespaces],
    queryFn: async () => {
      if (!selectedKind) return []
      const params = new URLSearchParams()
      if (namespaces.length > 0) params.set('namespaces', namespacesParam)
      if (isSelectedCrd && selectedKind.group) params.set('group', selectedKind.group)
      const startedAt = performance.now()
      debugNamespaceLog('resources:selected-kind-fetch-start', {
        kind: selectedKind.name,
        group: isSelectedCrd ? selectedKind.group : '',
        namespaces,
        params: params.toString(),
      })
      const res = await fetch(apiUrl(`/resources/${selectedKind.name}?${params}`), {
        credentials: getCredentialsMode(),
        headers: getAuthHeaders(),
      })
      debugNamespaceLog('resources:selected-kind-fetch-response', {
        kind: selectedKind.name,
        group: isSelectedCrd ? selectedKind.group : '',
        namespaces,
        params: params.toString(),
        status: res.status,
        durationMs: Math.round(performance.now() - startedAt),
      })
      if (!res.ok) {
        const errorData = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
        throw new ApiError(errorData.error || `Failed to fetch ${selectedKind.name}`, res.status, errorData)
      }
      return res.json()
    },
    enabled: !!selectedKind && !selectedKindQueryBlocked,
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
      data: selectedKindQueryBlocked ? [] : selectedKindQuery.data as any[] | undefined,
      isLoading: waitingForGuardCount || selectedKindQuery.isLoading,
      error: selectedKindQueryBlocked ? undefined : selectedKindQuery.error,
      refetch: selectedKindQuery.refetch,
      dataUpdatedAt: selectedKindQuery.dataUpdatedAt,
    }
  }, [selectedKind, selectedKindQueryBlocked, waitingForGuardCount, selectedKindQuery.data, selectedKindQuery.isLoading, selectedKindQuery.error, selectedKindQuery.refetch, selectedKindQuery.dataUpdatedAt])

  // Metrics
  const { data: topPodMetrics } = useTopPodMetrics({ enabled: topPodMetricsEnabled, namespaces })
  const { data: topNodeMetrics } = useTopNodeMetrics({ enabled: topNodeMetricsEnabled })

  // Certificate expiry
  const { data: certExpiry, isError: certExpiryError } = useSecretCertExpiry()

  // Pinned kinds
  const { pinned, togglePin, isPinned } = usePinnedKinds()

  // Dock actions
  const openLogs = useOpenLogs()
  const openWorkloadLogs = useOpenWorkloadLogs()

  // Bulk delete
  const bulkDeleteMutation = useBulkDeleteResources()
  const bulkRestartMutation = useBulkRestartWorkloads()
  const bulkScaleMutation = useBulkScaleWorkloads()

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
      onClearNamespaces={onClearNamespaces}
      // Injected data
      apiResources={apiResources}
      // Lightweight counts for sidebar (replaces 233 parallel queries)
      resourceCounts={countsData?.counts}
      resourceForbidden={countsData?.forbidden}
      resourceReasons={countsData?.reasons}
      resourceUnavailable={countsData?.unavailable}
      selectedKindQuery={selectedKindQueryResult}
      largeListGuard={largeListGuard}
      onSelectedKindChange={setSelectedKind}
      topPodMetrics={topPodMetrics}
      topNodeMetrics={topNodeMetrics}
      certExpiry={certExpiry}
      certExpiryError={certExpiryError}
      auditBadges={auditBadges}
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
      // Bulk operations
      onBulkDelete={(items, options) => bulkDeleteMutation.mutate({ items, force: options?.force }, { onSuccess: options?.onSuccess })}
      isBulkDeleting={bulkDeleteMutation.isPending}
      onBulkRestart={canBulkRestartSelectedKind ? (items, options) => bulkRestartMutation.mutate({ items }, { onSuccess: options?.onSuccess }) : undefined}
      isBulkRestarting={canBulkRestartSelectedKind && bulkRestartMutation.isPending}
      onBulkScale={canBulkScaleSelectedKind ? (items, replicas, options) => bulkScaleMutation.mutate({ items, replicas }, { onSuccess: options?.onSuccess }) : undefined}
      isBulkScaling={canBulkScaleSelectedKind && bulkScaleMutation.isPending}
    />
    <CreateResourceDialog
      open={createDialogOpen}
      onClose={() => setCreateDialogOpen(false)}
      initialYaml={createDialogYaml}
      title={createDialogTitle}
      onCreated={(result) => {
        onResourceClick?.({ kind: kindToPlural(result.kind), namespace: result.namespace, name: result.name, group: '' })
      }}
    />
    </>
  )
}
