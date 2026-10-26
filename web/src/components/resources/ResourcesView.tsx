import { useState, useMemo, useCallback } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { fetchJSON, isForbiddenError, isKindSyncFailed, isStillLoadingError, useCapabilities, useNamespaceCapabilities, useResources, useSecretCertExpiry, useTopPodMetrics, useTopNodeMetrics, useBulkDeleteResources, useBulkRestartWorkloads, useBulkScaleWorkloads, useAudit, useIssues, fetchResourceList, isListTooLargeError, isUnretryableReadError } from '../../api/client'
import { isBadgeWorthy } from '../../utils/auditBadges'
import { ISSUE_SEVERITY_RANK } from '@skyhook-io/k8s-ui'
import type { Issue, AuditBadgeMessage } from '@skyhook-io/k8s-ui'
import { stripBasename } from '../../api/config'
import { createListRefetchInterval, type ResourceListResult } from '../../api/resourceLists'
import { useAPIResources } from '../../api/apiResources'
import { useConnection } from '../../context/ConnectionContext'
import { getSecretStoreProviderType } from '@skyhook-io/k8s-ui'
import { usePinnedKinds } from '../../hooks/useFavorites'
import { useResourceCounts } from '../../hooks/useResourceCounts'
import { useCNPGSidebarWorkspace } from '../cnpg/useCNPGSidebarWorkspace'
import { renderResourceKindList, resourceKindListMode, useResourceHostFeatures } from '../../integrations/resourceHosts'
import { useOpenLogs, useOpenWorkloadLogs } from '../dock'
import {
  canBulkRestartKind,
  canBulkScaleKind,
  ResourcesView as BaseResourcesView,
  CORE_RESOURCES,
  intersectWorkloadWrites,
  hasCuratedColumns,
} from '@skyhook-io/k8s-ui'
import type { Capabilities, ResourceQueryResult, WorkloadWritePermissions } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { apiVersionToGroup, kindToPluralWithGroup, type NavigateToResource } from '../../utils/navigation'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { getSkeletonYaml } from '../../utils/skeleton-yaml'

interface ResourcesViewProps {
  namespaces: string[]
  selectedResource?: SelectedResource | null
  onResourceClick?: (resource: SelectedResource | null) => void
  onResourceClickYaml?: NavigateToResource
  onKindChange?: () => void
  onClearNamespaces?: () => void
  rememberedKindSearch?: (kind: { name: string; group: string }) => string | undefined
  onKindFiltersChange?: (kind: { name: string; group: string }, search: string) => void
  onApplyNamespacesToAllSections?: (namespaces: string[]) => Promise<unknown> | void
}

type SelectedKindInfo = { name: string; kind: string; group: string } | null

const EMPTY_RESOURCE_COUNTS: Record<string, number> = {}
const LARGE_RESOURCE_LIST_LIMIT = 25000
const LARGE_RESOURCE_LIST_GUARD_KEYS = new Set([
  'Pod',
  'Event',
  'apps/ReplicaSet',
  'discovery.k8s.io/EndpointSlice',
])
// Kinds the server slims with ?include=summary: rows carry only the fields the
// table reads (5–8x smaller for pods — a production pod is ~9–13KB raw,
// ~1–2.5KB slimmed), so the browser holds far more rows before the guard must
// block. The detail drawer is unaffected — row clicks always refetch the full
// object.
const SUMMARY_LIST_KINDS = new Set(['Pod', 'apps/ReplicaSet'])
const SUMMARY_LIST_LIMIT = 50000

const deniedWorkloadWrites: WorkloadWritePermissions = {
  deployments: false,
  daemonSets: false,
  statefulSets: false,
  rollouts: false,
}

function resourceCountKey(kind: NonNullable<SelectedKindInfo>): string {
  return kind.group ? `${kind.group}/${kind.kind}` : kind.kind
}

function hasResourceCount(counts: Record<string, number> | undefined, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(counts ?? {}, key)
}

export function ResourcesView({ namespaces, selectedResource, onResourceClick, onResourceClickYaml, onKindChange, onClearNamespaces, rememberedKindSearch, onKindFiltersChange, onApplyNamespacesToAllSections }: ResourcesViewProps) {
  const location = useLocation()
  const navigate = useNavigate()
  const { connection } = useConnection()

  const { data: capabilities, isPending: capabilitiesPending } = useCapabilities()
  const hostFeatures = useResourceHostFeatures()
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

  // Track the selected kind from the k8s-ui component
  const [selectedKind, setSelectedKind] = useState<SelectedKindInfo>(null)
  const { data: issuesData } = useIssues(namespaces, selectedKind?.group === 'batch' && selectedKind?.name === 'cronjobs', 'CronJob')
  const operationalIssues = useMemo(() => {
    const result: Record<string, Issue> = {}
    for (const issue of issuesData?.issues ?? []) {
      if (issue.kind !== 'CronJob' || issue.group !== 'batch') continue
      const key = `${issue.namespace ?? ''}/${issue.name}`
      const previous = result[key]
      if (!previous || ISSUE_SEVERITY_RANK[issue.severity] > ISSUE_SEVERITY_RANK[previous.severity] ||
        (issue.severity === previous.severity && issue.id < previous.id)) result[key] = issue
    }
    return result
  }, [issuesData])
  const cnpgSidebarWorkspace = useCNPGSidebarWorkspace({ apiResources, namespaces, withMetrics: selectedKind?.group === 'postgresql.cnpg.io' })
  const workloadWrites = namespaces.length === 0
    ? capabilities?.workloadWrites
    : namespaces.length === 1
      ? namespaceCapabilities?.workloadWrites
      : multiNamespaceWorkloadWrites
  const canBulkRestartSelectedKind = useMemo(() => canBulkRestartKind(selectedKind, workloadWrites), [selectedKind, workloadWrites])
  const canBulkScaleSelectedKind = useMemo(() => canBulkScaleKind(selectedKind, workloadWrites), [selectedKind, workloadWrites])

  // Lightweight resource counts for sidebar badges (~2KB instead of ~608MB)
  const { data: countsData, isError: countsIsError } = useResourceCounts(namespaces)

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
  // we still match the finding's group for every resource so a
  // kind shared across groups doesn't bleed findings across the two lists. Only
  // "badge-worthy" findings count (reference-integrity / lifecycle) — posture
  // and best-practice nags fire near-universally and would just be noise.
  const audit = useAudit(namespaces)
  const auditBadges = useMemo(() => {
    if (!selectedKind || !audit.data?.findings) return undefined
    const wantGroup = selectedKind.group
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
  }, [audit.data?.findings, audit.data?.checks, selectedKind, selectedKindCanonical])

  const selectedCountKey = selectedKind ? resourceCountKey(selectedKind) : ''
  const selectedCount = selectedCountKey ? countsData?.counts[selectedCountKey] : undefined
  const selectedCountKnown = selectedCountKey ? hasResourceCount(countsData?.counts, selectedCountKey) : false
  const selectedCountUnavailable = selectedCountKey ? countsData?.unavailable?.includes(selectedCountKey) ?? false : false
  const isSelectedKindGuarded = selectedCountKey !== '' && LARGE_RESOURCE_LIST_GUARD_KEYS.has(selectedCountKey)
  const selectedKindSummaryServed = SUMMARY_LIST_KINDS.has(selectedCountKey)
  const selectedKindRowLimit = selectedKindSummaryServed ? SUMMARY_LIST_LIMIT : LARGE_RESOURCE_LIST_LIMIT
  // While still 'connecting', /resource-counts is unavailable — its error
  // must NOT unlatch the large-list guard, or a huge Pods list could fetch
  // unguarded on exactly the clusters the guard protects. Counts arrive
  // right after 'connected' and settle the guard then.
  const syncShellActive = connection.state === 'connecting'
  // A reasoned unavailable count must reach the list endpoint, which supplies
  // the collector error without materializing an unchecked large payload.
  const selectedCountReadFailure = selectedCountUnavailable &&
    Boolean(countsData?.reasons?.[selectedCountKey]) &&
    countsData?.reasons?.[selectedCountKey] !== 'kind_sync_pending'
  const selectedCountPendingSync =
    (selectedCountUnavailable && countsData?.reasons?.[selectedCountKey] === 'kind_sync_pending') ||
    (syncShellActive && selectedCountUnavailable && !selectedCountReadFailure)
  const waitingForGuardCount = isSelectedKindGuarded &&
    ((!countsData && (!countsIsError || syncShellActive)) || selectedCountPendingSync)
  const largeListBlocked = isSelectedKindGuarded && countsData != null && !selectedCountPendingSync && !selectedCountReadFailure &&
    (selectedCountUnavailable || (selectedCountKnown && (selectedCount ?? 0) > selectedKindRowLimit))
  const selectedKindQueryBlocked = waitingForGuardCount || largeListBlocked
  const podCount = countsData?.counts.Pod
  const podCountKnown = hasResourceCount(countsData?.counts, 'Pod')
  const podCountUnavailable = countsData?.unavailable?.includes('Pod') ?? false
  // Metrics rows are ~100B each (ns/name + cpu/mem), so they track the pods
  // guard rather than the raw-list limit.
  const podCountAllowsBulkMetrics = countsData != null && podCountKnown && !podCountUnavailable && (podCount ?? 0) <= SUMMARY_LIST_LIMIT
  const selectedKindName = selectedKind?.name.toLowerCase() ?? ''
  const topPodMetricsEnabled = selectedKindName === 'pods' && podCountAllowsBulkMetrics
  // Node metrics back the Nodes table and, for the Pods table, the pod-vs-node
  // context line in the CPU/Memory tooltip (a pod can be fine against its own
  // limit yet at risk from a saturated node). Nodes are cluster-wide, so the
  // pods case is not gated on the namespace filter.
  const topNodeMetricsEnabled =
    ((selectedKindName === 'nodes' && namespaces.length === 0) || selectedKindName === 'pods') &&
    podCountAllowsBulkMetrics
  const largeListGuard = selectedKind && largeListBlocked
    ? {
        kind: selectedKind.name,
        count: selectedCountUnavailable ? undefined : selectedCount,
        reason: selectedCountUnavailable ? 'count-unavailable' as const : 'too-many' as const,
        limit: selectedKindRowLimit,
        namespaces,
      }
    : null

  const loadingInterval = useMemo(() => createListRefetchInterval(120000), [])

  // Fetch full data only for the selected kind
  const selectedKindQuery = useQuery({
    queryKey: ['resources', selectedKind?.name, isSelectedCrd ? selectedKind?.group : '', namespaces],
    queryFn: async ({ signal }): Promise<ResourceListResult<any>> => {
      if (!selectedKind) return { items: [], coverage: { status: 'unknown' }, printerTable: null }
      // Only CRDs can declare printer columns, and a curated kind discards the
      // result — so table mode is requested from exactly the kinds that can use
      // it. Resolving a table costs the server a CRD read per request; doing
      // that for a kind whose columns are hand-curated is pure waste.
      const wantsTable = isSelectedCrd && !!selectedKind.group && !hasCuratedColumns(selectedKind.name, selectedKind.group)
      return fetchResourceList(selectedKind.name, {
        namespaces, group: isSelectedCrd ? selectedKind.group : undefined,
        summary: selectedKindSummaryServed, table: wantsTable, signal,
      })
    },
    enabled: !!selectedKind && !selectedKindQueryBlocked && resourceKindListMode(selectedKind, hostFeatures, capabilitiesPending) === 'table',
    staleTime: 30000,
    // Safety net — SSE k8s_event drives near-real-time invalidation. A list
    // whose namespaces are still loading is re-read until they finish.
    refetchInterval: loadingInterval,
    retry: (failureCount: number, error: Error) => {
      if (isForbiddenError(error) || isKindSyncFailed(error) || isUnretryableReadError(error)) return false
      // Cluster still connecting, or this kind's informer still completing
      // its initial sync: keep the query in its loading state and retry —
      // the header's sync-progress label explains the wait.
      if (isStillLoadingError(error)) return true
      return failureCount < 3
    },
    retryDelay: (failureCount: number, error: Error) =>
      isStillLoadingError(error) ? 2000 : Math.min(1000 * 2 ** failureCount, 30000),
  })

  // Map to ResourceQueryResult shape
  const selectedKindQueryResult: ResourceQueryResult | undefined = useMemo(() => {
    if (!selectedKind) return undefined
    return {
      resourceName: selectedKind.name,
      group: selectedKind.group,
      coverage: selectedKindQuery.data?.coverage,
      data: selectedKindQueryBlocked ? [] : selectedKindQuery.data?.items,
      isLoading: waitingForGuardCount || selectedKindQuery.isLoading,
      error: selectedKindQueryBlocked ? undefined : selectedKindQuery.error,
      refetch: selectedKindQuery.refetch,
      dataUpdatedAt: selectedKindQuery.dataUpdatedAt,
    }
  }, [selectedKind, selectedKindQueryBlocked, waitingForGuardCount, selectedKindQuery.data?.items, selectedKindQuery.data?.coverage, selectedKindQuery.isLoading, selectedKindQuery.error, selectedKindQuery.refetch, selectedKindQuery.dataUpdatedAt])

  // Metrics
  const { data: topPodMetrics } = useTopPodMetrics({ enabled: topPodMetricsEnabled, namespaces })
  const { data: topNodeMetrics } = useTopNodeMetrics({ enabled: topNodeMetricsEnabled })

  // Certificate expiry
  const { data: certExpiry, isError: certExpiryError } = useSecretCertExpiry(namespaces)

  // An ExternalSecret names a store; only the store says which backend it reads
  // from. Two list calls per page beat one lookup per row, and the column shows
  // nothing rather than guessing when a store is unreadable or absent.
  const viewingExternalSecrets = selectedKind?.name === 'externalsecrets'
  const { data: secretStores, isPending: secretStoresPending } = useResources<any>('secretstores', undefined, undefined, { enabled: viewingExternalSecrets })
  const { data: clusterSecretStores, isPending: clusterSecretStoresPending } = useResources<any>('clustersecretstores', undefined, undefined, { enabled: viewingExternalSecrets })
  const storeProviders = useMemo(() => {
    // Undefined until both lists have settled. An empty map would be
    // indistinguishable from "every store is unreadable", and the column says
    // that out loud.
    if (!viewingExternalSecrets || secretStoresPending || clusterSecretStoresPending) return undefined
    const map: Record<string, string> = {}
    for (const store of secretStores ?? []) {
      const ns = store?.metadata?.namespace
      const name = store?.metadata?.name
      if (ns && name) map[`${ns}/${name}`] = getSecretStoreProviderType(store)
    }
    for (const store of clusterSecretStores ?? []) {
      const name = store?.metadata?.name
      if (name) map[name] = getSecretStoreProviderType(store)
    }
    return map
  }, [viewingExternalSecrets, secretStoresPending, clusterSecretStoresPending, secretStores, clusterSecretStores])

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
    return (path: string, options?: { replace?: boolean }) => {
      navigate(stripBasename(path), { replace: options?.replace })
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

  const renderKindView = useCallback(
    (kind: SelectedKindInfo) => renderResourceKindList(kind, hostFeatures, capabilitiesPending, {
      namespaces,
      inspected: selectedResource ?? null,
      onInspect: (resource) => onResourceClick?.(resource),
      onClearNamespaces: onClearNamespaces ?? (() => {}),
      onCreate: handleCreateResource,
    }),
    [hostFeatures, capabilitiesPending, namespaces, selectedResource, onResourceClick, onClearNamespaces, handleCreateResource],
  )

  return (
    <>
    <BaseResourcesView
      key={connection.context || 'default'}
      namespaces={namespaces}
      selectedResource={selectedResource}
      onResourceClick={onResourceClick}
      onResourceClickYaml={onResourceClickYaml}
      onKindChange={onKindChange}
      onClearNamespaces={onClearNamespaces}
      rememberedKindSearch={rememberedKindSearch}
      onKindFiltersChange={onKindFiltersChange}
      onApplyNamespacesToAllSections={onApplyNamespacesToAllSections}
      // Injected data
      apiResources={apiResources}
      // Lightweight counts for sidebar (replaces 233 parallel queries)
      resourceCounts={countsData?.counts ?? EMPTY_RESOURCE_COUNTS}
      resourceCoverage={countsData?.coverage}
      resourceForbidden={countsData?.forbidden}
      resourceReasons={countsData?.reasons}
      resourceUnavailable={countsData?.unavailable}
      selectedKindQuery={selectedKindQueryResult}
      printerTable={selectedKindQueryBlocked ? null : selectedKindQuery.data?.printerTable ?? null}
      connectionState={connection.state === 'connecting' && connection.syncStatus ? 'syncing' : connection.state}
      largeListGuard={selectedKind && isListTooLargeError(selectedKindQuery.error)
        ? { kind: selectedKind.name, reason: 'too-many', count: typeof selectedKindQuery.error.data?.count === 'number' ? selectedKindQuery.error.data.count : undefined, limit: selectedKindRowLimit, namespaces }
        : largeListGuard}
      onSelectedKindChange={setSelectedKind}
      sidebarCategoryWorkspaces={cnpgSidebarWorkspace}
      topPodMetrics={topPodMetrics}
      topNodeMetrics={topNodeMetrics}
      certExpiry={certExpiry}
      storeProviders={storeProviders}
      certExpiryError={certExpiryError}
      operationalIssues={operationalIssues}
      operationalIssuesIncomplete={Boolean(issuesData && (issuesData.total_matched ?? issuesData.total ?? issuesData.issues.length) > issuesData.issues.length)}
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
      renderKindView={renderKindView}
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
        const group = apiVersionToGroup(result.apiVersion)
        onResourceClick?.({ kind: kindToPluralWithGroup(result.kind, group), namespace: result.namespace, name: result.name, group })
      }}
    />
    </>
  )
}
