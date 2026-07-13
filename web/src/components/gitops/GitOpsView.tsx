import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import yaml from 'yaml'
import {
  GitOpsActivityInsightView,
  GitOpsChangesView,
  GitOpsDetailLayout,
  GitOpsGraphFilterRail,
  GitOpsTableView as SharedGitOpsTableView,
  FreshnessControl,
  GitOpsTreeGraph,
  RollbackDialog,
  SyncOptionsDialog,
  buildFluxSourceUrlMap,
  buildTreeFacets,
  describeGitOpsTerminating,
  formatGitOpsDestination,
  formatGitOpsSourceUrl,
  getGitOpsResourceStatus,
  getGitOpsTool,
  isArgoOperationInProgress,
  isArgoSuspendedByRadar,
  gitOpsInsightChangeKey,
  initNavigationMap,
  kindToPlural,
  normalizeArgoApplication,
  normalizeFluxHelmRelease,
  normalizeFluxKustomization,
  parseArgoRollbackID,
  toggleSet,
  type APIResource,
  type ArgoActionHandlers,
  type FluxActionHandlers,
  type GitOpsDetailMetadata,
  type GitOpsDetailTab,
  type GitOpsResourceTree,
  type GitOpsInsightRef,
  type GitOpsRow,
  type GitOpsRowAction,
  type GitOpsTreeFilters,
  type GitOpsTreeRef,
  type GitOpsTreePreset,
  type SelectedResource,
} from '@skyhook-io/k8s-ui'
import { useToast } from '../ui/Toast'

import {
  fetchJSON,
  buildArgoResourceSyncVars,
  useApplyResource,
  useArgoRefresh,
  useArgoResourceValidation,
  useArgoResume,
  useArgoRollback,
  useArgoSuspend,
  useArgoSync,
  useArgoTerminate,
  useFluxReconcile,
  useFluxResume,
  useFluxSuspend,
  useFluxSyncWithSource,
  useGitOpsInsights,
  useGitOpsTree,
  useResource,
} from '../../api/client'
import { useAPIResources } from '../../api/apiResources'
import { useConnection } from '../../context/ConnectionContext'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { useRegisterShortcut } from '../../hooks/useKeyboardShortcuts'
import { CodeViewer } from '../ui/CodeViewer'
import type { GitOpsHistoryItem } from '@skyhook-io/k8s-ui'

const GITOPS_KINDS: APIResource[] = [
  { name: 'applications', kind: 'Application', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'applicationsets', kind: 'ApplicationSet', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'appprojects', kind: 'AppProject', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'kustomizations', kind: 'Kustomization', group: 'kustomize.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'helmreleases', kind: 'HelmRelease', group: 'helm.toolkit.fluxcd.io', version: 'v2', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'gitrepositories', kind: 'GitRepository', group: 'source.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'ocirepositories', kind: 'OCIRepository', group: 'source.toolkit.fluxcd.io', version: 'v1beta2', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'helmrepositories', kind: 'HelmRepository', group: 'source.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'alerts', kind: 'Alert', group: 'notification.toolkit.fluxcd.io', version: 'v1beta3', namespaced: true, verbs: ['list', 'get'], isCrd: true },
]

type ArgoSyncDialogTarget =
  | { scope: 'application' }
  | { scope: 'resource'; resource: GitOpsInsightRef }

const KIND_BY_NAME = new Map(GITOPS_KINDS.map((k) => [k.name, k]))

// Rows are the table's primary content; their poll cadence is what the toolbar
// freshness signal advertises ("Auto-refreshes every 2m"). Single source of
// truth so the signal can't drift from the actual refetchInterval below.
const GITOPS_ROWS_REFRESH_INTERVAL_MS = 120_000

interface ResourceCountsResponse {
  counts: Record<string, number>
  forbidden?: string[]
  unavailable?: string[]
}

interface GitOpsViewProps {
  namespaces: string[]
  onOpenResource: (resource: SelectedResource) => void
  onClearNamespaces?: () => void
}

export function GitOpsView({ namespaces, onOpenResource, onClearNamespaces }: GitOpsViewProps) {
  const location = useLocation()
  if (location.pathname.startsWith('/gitops/detail/')) {
    return <GitOpsDetailView namespaces={namespaces} onOpenResource={onOpenResource} />
  }
  return <GitOpsTableView namespaces={namespaces} onClearNamespaces={onClearNamespaces} />
}

function GitOpsTableView({ namespaces, onClearNamespaces }: { namespaces: string[]; onClearNamespaces?: () => void }) {
  const navigate = useNavigate()
  const { connection } = useConnection()
  const namespacesParam = namespaces.join(',')
  const { data: apiResources, isLoading: apiResourcesLoading } = useAPIResources()

  const argoSync = useArgoSync()
  const argoRefresh = useArgoRefresh()
  const argoTerminate = useArgoTerminate()
  const argoSuspend = useArgoSuspend()
  const argoResume = useArgoResume()
  const fluxReconcile = useFluxReconcile()
  const fluxSyncWithSource = useFluxSyncWithSource()
  const fluxSuspend = useFluxSuspend()
  const fluxResume = useFluxResume()

  const [syncDialogRow, setSyncDialogRow] = useState<GitOpsRow | null>(null)
  const [pendingActions, setPendingActions] = useState<Map<string, Set<GitOpsRowAction>>>(new Map())

  // Mark an action as in-flight (or done) for a given row. Cloning the
  // outer Map + inner Set keeps the state immutable so React rerenders
  // and the per-item spinner flips at the right moment.
  function markAction(rowId: string, action: GitOpsRowAction, on: boolean) {
    setPendingActions((prev) => {
      const next = new Map(prev)
      const current = new Set(next.get(rowId) ?? [])
      if (on) current.add(action)
      else current.delete(action)
      if (current.size === 0) next.delete(rowId)
      else next.set(rowId, current)
      return next
    })
  }

  useEffect(() => {
    initNavigationMap([...(apiResources ?? []), ...GITOPS_KINDS])
  }, [apiResources])

  const hasGitOpsRowResource = useMemo(() => (
    hasAPIResource(apiResources, 'applications', 'argoproj.io') ||
    hasAPIResource(apiResources, 'kustomizations', 'kustomize.toolkit.fluxcd.io') ||
    hasAPIResource(apiResources, 'helmreleases', 'helm.toolkit.fluxcd.io')
  ), [apiResources])

  // Counts come from radar's /api/resource-counts. The extracted
  // GitOpsTableView reads only the GitOps keys for mode tabs + empty-state
  // checks.
  const countsQuery = useQuery({
    queryKey: ['gitops-resource-counts', namespacesParam],
    queryFn: async () => {
      const params = new URLSearchParams()
      if (namespaces.length > 0) params.set('namespaces', namespacesParam)
      return fetchJSON<ResourceCountsResponse>(`/resource-counts?${params}`)
    },
    staleTime: 10_000,
    refetchInterval: 60_000,
  })

  // Row-producing fetch: Applications + Kustomizations + HelmReleases,
  // plus the Flux source CRs (GitRepository / HelmRepository /
  // OCIRepository / Bucket) for sourceRef→URL resolution. We skip per-
  // kind requests when the cluster doesn't have the CRD installed; the
  // capability map comes from useAPIResources.
  const rowsQuery = useQuery({
    queryKey: ['gitops-rows-main', namespaces, apiResources?.length ?? 0],
    queryFn: async () => {
      const hasApplications = hasAPIResource(apiResources, 'applications', 'argoproj.io')
      const hasKustomizations = hasAPIResource(apiResources, 'kustomizations', 'kustomize.toolkit.fluxcd.io')
      const hasHelmReleases = hasAPIResource(apiResources, 'helmreleases', 'helm.toolkit.fluxcd.io')
      const hasFluxSources = hasKustomizations || hasHelmReleases
      const hasGitRepos = hasFluxSources && hasAPIResource(apiResources, 'gitrepositories', 'source.toolkit.fluxcd.io')
      const hasHelmRepos = hasFluxSources && hasAPIResource(apiResources, 'helmrepositories', 'source.toolkit.fluxcd.io')
      const hasOCIRepos = hasFluxSources && hasAPIResource(apiResources, 'ocirepositories', 'source.toolkit.fluxcd.io')
      const hasBuckets = hasFluxSources && hasAPIResource(apiResources, 'buckets', 'source.toolkit.fluxcd.io')
      const [applications, kustomizations, helmReleases, gitRepos, helmRepos, ociRepos, buckets] = await Promise.all([
        hasApplications ? fetchResourceList('applications', 'argoproj.io', namespacesParam) : Promise.resolve([]),
        hasKustomizations ? fetchResourceList('kustomizations', 'kustomize.toolkit.fluxcd.io', namespacesParam) : Promise.resolve([]),
        hasHelmReleases ? fetchResourceList('helmreleases', 'helm.toolkit.fluxcd.io', namespacesParam) : Promise.resolve([]),
        hasGitRepos ? fetchResourceList('gitrepositories', 'source.toolkit.fluxcd.io', '') : Promise.resolve([]),
        hasHelmRepos ? fetchResourceList('helmrepositories', 'source.toolkit.fluxcd.io', '') : Promise.resolve([]),
        hasOCIRepos ? fetchResourceList('ocirepositories', 'source.toolkit.fluxcd.io', '') : Promise.resolve([]),
        hasBuckets ? fetchResourceList('buckets', 'source.toolkit.fluxcd.io', '') : Promise.resolve([]),
      ])
      const fluxSourceUrls = buildFluxSourceUrlMap([...gitRepos, ...helmRepos, ...ociRepos, ...buckets])
      return [
        ...applications.map((r) => normalizeArgoApplication(r)),
        ...kustomizations.map((r) => normalizeFluxKustomization(r, fluxSourceUrls)),
        ...helmReleases.map((r) => normalizeFluxHelmRelease(r, fluxSourceUrls)),
      ]
    },
    enabled: !apiResourcesLoading,
    staleTime: 30_000,
    refetchInterval: GITOPS_ROWS_REFRESH_INTERVAL_MS,
  })

  // Row mutations invalidate granular keys (['resource', …], ['gitops-tree', …])
  // that don't match the table's aggregate gitops-rows-main / counts queries,
  // so refetch those explicitly — otherwise a row keeps showing the pre-action
  // state (e.g. "Suspend" after a successful suspend) until the 120s poll,
  // inviting a duplicate request. Radar serves reads from an informer cache that
  // lags the write by the watch-propagation delay, so refetch once now (covers
  // an already-current cache) and once shortly after to catch the propagated
  // update; refetch() forces a fetch regardless of staleTime. The toolbar's
  // manual refresh reuses refetchTable so rows + counts stay in sync.
  // Return the combined promise so the toolbar's refresh animation waits for the
  // real fetches to settle before showing its success checkmark.
  const refetchTable = () => Promise.all([rowsQuery.refetch(), countsQuery.refetch()])
  const refetchTableAfterMutation = () => {
    refetchTable()
    window.setTimeout(refetchTable, 1200)
  }

  // Cold-cache catch-up: right after a cluster/namespace switch (or first open)
  // the GitOps CRD informers can still be warming, so the first list resolves
  // EMPTY even though apps exist — stranding the user on "No applications found"
  // until the 120s poll (which is why a manual Refresh "fixes" it). When the
  // fetch settles empty, briefly retry (bounded) to catch the cache as it syncs.
  // Reset the budget whenever the cluster (apiResources identity) or namespace
  // scope changes so each switch gets a fresh set of retries.
  const coldRetriesRef = useRef(0)
  // While we're still retrying a cold cache, the view shows a spinner (not the
  // false "No applications found") — so the user sees "loading", not "empty".
  const [coldRetrying, setColdRetrying] = useState(false)
  useEffect(() => { coldRetriesRef.current = 0; setColdRetrying(false) }, [apiResources, namespacesParam])
  useEffect(() => {
    if (!hasGitOpsRowResource || rowsQuery.error) { setColdRetrying(false); return }
    if (apiResourcesLoading || rowsQuery.isFetching) return
    if ((rowsQuery.data?.length ?? 0) > 0) { setColdRetrying(false); return }
    if (coldRetriesRef.current >= 4) { setColdRetrying(false); return }
    setColdRetrying(true)
    const t = window.setTimeout(() => { coldRetriesRef.current += 1; refetchTable() }, 2000)
    return () => window.clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rowsQuery.data, rowsQuery.isFetching, rowsQuery.error, apiResourcesLoading, hasGitOpsRowResource])

  const handleRowAction = (row: GitOpsRow, action: GitOpsRowAction) => {
    const { kindName: kind, namespace, name, id } = row
    const settle = { onSuccess: refetchTableAfterMutation, onSettled: () => markAction(id, action, false) }
    markAction(id, action, true)
    switch (action) {
      case 'sync':
        // Argo Sync is the one action that confirms — same dialog the
        // detail page uses. The mutation fires from onConfirm; clear the
        // in-flight flag here since the dialog now owns the lifecycle.
        markAction(id, action, false)
        setSyncDialogRow(row)
        return
      case 'refresh':
        argoRefresh.mutate({ namespace, name, hard: false }, settle)
        return
      case 'hard-refresh':
        argoRefresh.mutate({ namespace, name, hard: true }, settle)
        return
      case 'terminate':
        argoTerminate.mutate({ namespace, name }, settle)
        return
      case 'suspend':
        if (row.tool === 'argo') argoSuspend.mutate({ namespace, name }, settle)
        else fluxSuspend.mutate({ kind, namespace, name }, settle)
        return
      case 'resume':
        if (row.tool === 'argo') argoResume.mutate({ namespace, name }, settle)
        else fluxResume.mutate({ kind, namespace, name }, settle)
        return
      case 'reconcile':
        fluxReconcile.mutate({ kind, namespace, name }, settle)
        return
      case 'sync-with-source':
        fluxSyncWithSource.mutate({ kind, namespace, name }, settle)
        return
    }
  }

  return (
    <>
      <SharedGitOpsTableView
        rows={rowsQuery.data ?? []}
        loading={apiResourcesLoading || countsQuery.isLoading || rowsQuery.isLoading || coldRetrying}
        error={(rowsQuery.error as Error | null) ?? null}
        counts={countsQuery.data?.counts ?? {}}
        countsUnavailable={countsQuery.data?.unavailable}
        freshnessSlot={
          <FreshnessControl
            mode="auto"
            dataUpdatedAt={rowsQuery.dataUpdatedAt}
            onRefresh={refetchTable}
            connectionState={connection.state}
          />
        }
        onRowClick={(row) => {
          const ns = row.namespace || '_'
          const params = new URLSearchParams()
          params.set('apiGroup', row.group)
          navigate({ pathname: gitOpsDetailPath(row.kindName, ns, row.name), search: params.toString() })
        }}
        onRowAction={handleRowAction}
        pendingRowActions={pendingActions}
        searchHotkey
        globalNamespaces={namespaces}
        onClearNamespaces={onClearNamespaces}
      />
      <SyncOptionsDialog
        open={!!syncDialogRow}
        appLabel={syncDialogRow ? `${syncDialogRow.namespace}/${syncDialogRow.name}` : ''}
        pending={argoSync.isPending}
        onCancel={() => setSyncDialogRow(null)}
        onConfirm={(opts) => {
          if (!syncDialogRow) return
          const { namespace, name } = syncDialogRow
          argoSync.mutate(
            { namespace, name, ...opts },
            // onSettled so the dialog closes on both success and error —
            // otherwise the error toast surfaces behind the still-open
            // modal and the user can't read it.
            { onSuccess: refetchTableAfterMutation, onSettled: () => setSyncDialogRow(null) },
          )
        }}
      />
    </>
  )
}

function GitOpsDetailView({ namespaces, onOpenResource }: GitOpsViewProps) {
  const location = useLocation()
  const navigate = useNavigate()
  const { showError, showSuccess } = useToast()
  const parts = location.pathname.split('/').filter(Boolean)
  const kind = parts[2] || 'applications'
  const namespace = parts[3] === '_' ? '' : decodePathPart(parts[3] || '')
  const name = decodePathPart(parts[4] || '')
  const group = new URLSearchParams(location.search).get('apiGroup') || (KIND_BY_NAME.get(kind)?.group ?? '')
  const apiKind = KIND_BY_NAME.get(kind)
  // Parent lineage from the ?from=kind|namespace|name query param. Set by
  // openResourceFromTree when the user clicks a child GitOps node from a
  // parent's graph. Renders an extra breadcrumb segment + "↑ Open parent"
  // button so the user always knows where they came from. Falls back to
  // null (no breadcrumb) for direct/deep links.
  const parent = useMemo<{ kind: string; namespace: string; name: string; group: string } | null>(() => {
    const raw = new URLSearchParams(location.search).get('from')
    if (!raw) return null
    const [pKind = '', pNs = '', pName = ''] = raw.split('|')
    if (!pKind || !pName) return null
    return {
      kind: pKind,
      namespace: pNs,
      name: pName,
      group: KIND_BY_NAME.get(pKind)?.group ?? '',
    }
  }, [location.search])

  const resourceQ = useResource<any>(kind, namespace, name, group)
  const treeQ = useGitOpsTree(kind, namespace, name, group, namespaces)
  const insightsQ = useGitOpsInsights(kind, namespace, name, group, namespaces)
  const status = resourceQ.data ? getGitOpsResourceStatus(kind, resourceQ.data) : null
  const tool = getGitOpsTool(kind, group)
  // Argo "auto-sync ON" is determined by spec.syncPolicy.automated being set,
  // not by health.status === Suspended (which is Argo's CronJob-style suspend).
  // The toggle button reads from this so the label flips correctly when an
  // app is in Manual mode or suspended via Radar's annotations.
  const argoAutoSyncEnabled = kind === 'applications' && Boolean(resourceQ.data?.spec?.syncPolicy?.automated)
  // Radar-driven Argo suspension is signaled by annotations that record the
  // pre-suspend prune/selfHeal state for restoration on resume. When present,
  // the app is in a deliberately-paused state (vs. Manual mode, which is a
  // normal operational choice) and should surface a Suspended chip alongside
  // the other status indicators. Shared with the fleet table's row normalizer
  // (isArgoSuspendedByRadar) so both surfaces agree on what "suspended" means.
  const argoSuspendedByRadar = kind === 'applications' && isArgoSuspendedByRadar(resourceQ.data)
  const effectiveSuspended = (status?.suspended ?? false) || argoSuspendedByRadar
  // Lifecycle gate: when the resource is pending deletion, mutating
  // actions are futile (the controller is processing finalizers and
  // ignores reconcile/sync triggers). Surface it visually + disable
  // the affected buttons. Read-style verbs (Refresh, Hard refresh,
  // Terminate) intentionally remain enabled — see the corresponding
  // carve-out in pkg/gitops/operations.go.
  const terminating = !!insightsQ.data?.summary?.terminating
  const terminatingDescriptions = describeGitOpsTerminating(insightsQ.data?.summary)
  const terminatingChipTooltip = terminatingDescriptions.chipTooltip
  const terminatingActionTooltip = terminatingDescriptions.actionDisabledTooltip
  const [appView, setAppView] = useState<GitOpsDetailTab>('topology')
  // When the user clicks an actionable issue alert ("OutOfSync — NodePool
  // default is out of sync · View →"), we navigate to Changes and focus
  // that resource. The ref is stringified to a stable key so GitOpsChangesView
  // can find and scroll it; cleared after a few seconds so the highlight
  // doesn't persist past its purpose.
  const [changesFocusKey, setChangesFocusKey] = useState<string | null>(null)
  const [graphPreset, setGraphPreset] = useState<GitOpsTreePreset>('compact')
  const [graphSearch, setGraphSearch] = useState('')
  const [graphKinds, setGraphKinds] = useState<Set<string>>(new Set())
  const [graphSync, setGraphSync] = useState<Set<string>>(new Set())
  const [graphHealth, setGraphHealth] = useState<Set<string>>(new Set())
  const [graphNamespaces, setGraphNamespaces] = useState<Set<string>>(new Set())
  const [graphRoles, setGraphRoles] = useState<Set<string>>(new Set())
  const [graphFullscreen, setGraphFullscreen] = useState(false)
  const [helmValuesOpen, setHelmValuesOpen] = useState(false)

  const argoSync = useArgoSync()
  const argoResourceValidation = useArgoResourceValidation()
  const argoRefresh = useArgoRefresh()
  const argoTerminate = useArgoTerminate()
  const argoSuspend = useArgoSuspend()
  const argoResume = useArgoResume()
  const argoRollback = useArgoRollback()
  const applyResource = useApplyResource()
  const fluxReconcile = useFluxReconcile()
  const fluxSyncWithSource = useFluxSyncWithSource()
  const fluxSuspend = useFluxSuspend()
  const fluxResume = useFluxResume()

  const [syncDialogTarget, setSyncDialogTarget] = useState<ArgoSyncDialogTarget | null>(null)
  // Doubles as the "open" flag (truthy = dialog open) and the data carrier
  // for which history entry to roll back to.
  const [rollbackTarget, setRollbackTarget] = useState<GitOpsHistoryItem | null>(null)
  // Disambiguates which refresh button is in flight (both share argoRefresh).
  const [refreshKind, setRefreshKind] = useState<'normal' | 'hard'>('normal')

  function openArgoSyncDialog(target: ArgoSyncDialogTarget) {
    argoResourceValidation.reset()
    setSyncDialogTarget(target)
  }

  function closeArgoSyncDialog() {
    argoResourceValidation.reset()
    setSyncDialogTarget(null)
  }

  const detailRow = resourceQ.data ? normalizeDetailResource(kind, group, resourceQ.data) : null
  const tree = treeQ.data ?? null
  const helmValues = useMemo(() => extractHelmValues(kind, resourceQ.data), [kind, resourceQ.data])
  const graphFilters = useMemo<GitOpsTreeFilters>(() => ({
    kinds: graphKinds,
    sync: graphSync,
    health: graphHealth,
    namespaces: graphNamespaces,
    roles: graphRoles,
  }), [graphHealth, graphKinds, graphNamespaces, graphRoles, graphSync])
  const graphFacets = useMemo(() => buildTreeFacets(tree), [tree])

  function openResourceFromTree(ref: GitOpsTreeRef | GitOpsInsightRef) {
    if (isGitOpsDetailRef(ref) && isValidKubernetesName(ref.name)) {
      const detailKind = kindToPlural(ref.kind)
      const params = new URLSearchParams()
      if (ref.group) params.set('apiGroup', ref.group)
      // Lineage breadcrumb support: when the user opens a child GitOps CR
      // from inside a parent's tree, encode the parent into the URL so
      // the child page can render "GitOps / parent / child" + "↑ Open
      // parent" affordance. Encoded as kind|namespace|name (a single
      // "from" param keeps the URL short; multi-level lineage isn't
      // supported here yet — the deepest valid breadcrumb is parent →
      // child. Going further would need either a chain encoding or
      // history-state walking, both deferred until the use case shows up).
      const fromKind = apiKind?.name ?? kind
      if (fromKind && name) {
        params.set('from', `${fromKind}|${namespace || ''}|${name}`)
      }
      navigate({ pathname: gitOpsDetailPath(detailKind, ref.namespace || '_', ref.name), search: params.toString() })
      return
    }
    onOpenResource({ kind: kindToPlural(ref.kind), namespace: ref.namespace || '', name: ref.name, group: ref.group })
  }

  const isRunning = resourceQ.data?.status?.operationState?.phase === 'Running'
  const operationInProgress = isArgoOperationInProgress(resourceQ.data)
  const isFluxWorkload = kind === 'kustomizations' || kind === 'helmreleases'
  const isFlux = tool === 'flux'
  const isArgoApp = kind === 'applications'

  // Detail-page shortcuts. Skip when a modal is already open so a stray "s"
  // in an input field doesn't pop another sync dialog.
  const shortcutsEnabled = !syncDialogTarget && !rollbackTarget
  useRegisterShortcut({
    id: 'gitops-detail-sync',
    keys: 's',
    description: isArgoApp ? 'Open sync options' : 'Reconcile',
    category: 'GitOps',
    scope: 'gitops',
    handler: () => {
      if (effectiveSuspended || terminating || operationInProgress) return
      if (isArgoApp) openArgoSyncDialog({ scope: 'application' })
      else if (isFlux) fluxReconcile.mutate({ kind, namespace, name })
    },
    enabled: shortcutsEnabled && (isArgoApp || isFlux) && !effectiveSuspended && !terminating && !(isArgoApp && operationInProgress),
  })
  useRegisterShortcut({
    id: 'gitops-detail-refresh',
    keys: 'r',
    description: 'Refresh application',
    category: 'GitOps',
    scope: 'gitops',
    handler: () => {
      if (!isArgoApp) return
      setRefreshKind('normal')
      argoRefresh.mutate({ namespace, name, hard: false })
    },
    enabled: shortcutsEnabled && isArgoApp,
  })
  useRegisterShortcut({
    id: 'gitops-detail-hard-refresh',
    keys: 'Shift+R',
    description: 'Hard refresh (re-resolve source from Git)',
    category: 'GitOps',
    scope: 'gitops',
    handler: () => {
      if (!isArgoApp) return
      setRefreshKind('hard')
      argoRefresh.mutate({ namespace, name, hard: true })
    },
    enabled: shortcutsEnabled && isArgoApp,
  })
  useRegisterShortcut({
    id: 'gitops-detail-terminate',
    keys: 't',
    description: 'Terminate running sync',
    category: 'GitOps',
    scope: 'gitops',
    handler: () => {
      if (isArgoApp && isRunning) argoTerminate.mutate({ namespace, name })
    },
    enabled: shortcutsEnabled && isArgoApp && isRunning,
  })

  // Adapt the OSS-internal row + insights data into the layout's props.
  // The bulk of the JSX is now in <GitOpsDetailLayout>; this wrapper does
  // the OSS-specific things the layout can't (call OSS-side data hooks,
  // open OSS dialogs, talk to OSS Toast, hit OSS keyboard registry).
  const detail: GitOpsDetailMetadata = {
    project: detailRow?.project,
    repository: detailRow?.repository ? formatGitOpsSourceUrl(detailRow.repository) : undefined,
    path: detailRow?.path || undefined,
    chart: detailRow?.chart || undefined,
    destination: formatGitOpsDestination(detailRow?.destination, detailRow?.destinationNamespace),
    autoSyncMode: insightsQ.data?.summary?.autoSyncMode,
  }

  const argoHandlers: ArgoActionHandlers | undefined = isArgoApp ? {
    onSyncRequested: () => openArgoSyncDialog({ scope: 'application' }),
    onRefresh: (refreshType) => {
      setRefreshKind(refreshType)
      argoRefresh.mutate({ namespace, name, hard: refreshType === 'hard' })
    },
    onTerminate: () => argoTerminate.mutate({ namespace, name }),
    onSuspend: () => argoSuspend.mutate({ namespace, name }),
    onResume: () => argoResume.mutate({ namespace, name }),
    syncing: argoSync.isPending,
    refreshing: argoRefresh.isPending,
    refreshingKind: refreshKind,
    terminating: argoTerminate.isPending,
    suspending: argoSuspend.isPending,
    resuming: argoResume.isPending,
    autoSyncEnabled: argoAutoSyncEnabled,
    isRunning,
    operationInProgress,
  } : undefined

  const fluxHandlers: FluxActionHandlers | undefined = isFlux ? {
    onReconcile: () => fluxReconcile.mutate({ kind, namespace, name }),
    onSyncWithSource: () => fluxSyncWithSource.mutate({ kind, namespace, name }),
    onSuspend: () => fluxSuspend.mutate({ kind, namespace, name }),
    onResume: () => fluxResume.mutate({ kind, namespace, name }),
    reconciling: fluxReconcile.isPending,
    syncingWithSource: fluxSyncWithSource.isPending,
    suspending: fluxSuspend.isPending,
    resuming: fluxResume.isPending,
  } : undefined

  return (
    <GitOpsDetailLayout
      identity={{
        kind,
        group,
        namespace,
        name,
        toolLabel: tool === 'argo' ? 'ArgoCD' : 'FluxCD',
        kindLabel: apiKind?.kind ?? kind,
      }}
      parent={parent}
      status={status ? { sync: status.sync, health: status.health, suspended: effectiveSuspended } : null}
      terminating={terminating}
      terminatingChipTooltip={terminatingChipTooltip}
      terminatingActionTooltip={terminatingActionTooltip}
      detail={detail}
      insight={insightsQ.data ?? null}
      insightLoading={insightsQ.isLoading}
      onSelectIssue={(issue) => {
        const ref = issue.refs?.[0]
        if (!ref) return
        setAppView('changes')
        setChangesFocusKey(gitOpsInsightChangeKey(ref))
        // Window the highlight: 4s is long enough to find the row visually
        // but short enough that it doesn't linger if the user navigates
        // away and back.
        window.setTimeout(() => setChangesFocusKey(null), 4000)
      }}
      remediationPending={applyResource.isPending || argoSync.isPending}
      onRemediate={(remediation) => {
        if (remediation.kind === 'create-namespace' && remediation.target) {
          const nsName = remediation.target
          const yamlManifest = `apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ${nsName}\n`
          applyResource.mutate(
            { yaml: yamlManifest, mode: 'apply' },
            {
              onSuccess: () => {
                // Defer the success toast until we know whether the follow-on
                // sync was triggered, so a successful create-namespace + sync
                // failure doesn't yield a misleading "sync triggered" toast.
                if (kind === 'applications') {
                  argoSync.mutate(
                    { namespace, name },
                    {
                      onSuccess: () => {
                        showSuccess(`Created namespace ${nsName}`, 'Sync triggered to retry the apply.')
                      },
                      onError: () => {
                        showSuccess(`Created namespace ${nsName}`, "Couldn't trigger sync automatically — click Sync to retry.")
                      },
                    },
                  )
                } else {
                  showSuccess(`Created namespace ${nsName}`)
                }
              },
              onError: (err: unknown) => {
                const msg = err instanceof Error ? err.message : 'Unknown error'
                showError(
                  `Couldn't create namespace ${nsName}`,
                  msg.includes('forbidden')
                    ? 'Radar lacks RBAC to create namespaces in this cluster. Create it manually or have a cluster-admin do it.'
                    : msg,
                )
              },
            },
          )
        }
      }}
      helmValues={helmValues}
      helmValuesOpen={helmValuesOpen}
      onToggleHelmValues={() => setHelmValuesOpen((v) => !v)}
      helmValuesContent={helmValues ? <CodeViewer code={helmValues.yaml} language="yaml" showLineNumbers maxHeight="320px" /> : null}
      isArgoApp={isArgoApp}
      isFlux={isFlux}
      isFluxWorkload={isFluxWorkload}
      argo={argoHandlers}
      flux={fluxHandlers}
      activeTab={appView}
      onTabChange={(tab) => setAppView(tab)}
      fullscreen={graphFullscreen}
      onToggleFullscreen={() => setGraphFullscreen(!graphFullscreen)}
      resourceLoading={resourceQ.isLoading}
      resourceError={(resourceQ.error as Error | null) ?? null}
      onNavigateRoot={() => navigate('/gitops')}
      onNavigateParent={parent ? () => {
        const params = new URLSearchParams()
        if (parent.group) params.set('apiGroup', parent.group)
        navigate({
          pathname: gitOpsDetailPath(parent.kind, parent.namespace || '_', parent.name),
          search: params.toString(),
        })
      } : undefined}
      manageDocumentTitle={false /* title handled centrally in App's radarPageTitle */}
      renderTabBarCounts={({ tab }) => (
        tab === 'topology' && tree ? <TopologyCounts tree={tree} /> : null
      )}
      renderTabBarAccessory={({ tab }) => (
        tab === 'topology' ? (
          <button
            type="button"
            onClick={() => {
              setGraphSearch('')
              setGraphKinds(new Set())
              setGraphSync(new Set())
              setGraphHealth(new Set())
              setGraphNamespaces(new Set())
              setGraphRoles(new Set())
            }}
            className="rounded px-2 py-1 text-xs text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
          >
            Clear filters
          </button>
        ) : null
      )}
      renderTabBody={({ tab }) => {
        if (tab === 'activity') {
          return (
            <GitOpsActivityInsightView
              insight={insightsQ.data}
              error={insightsQ.error as Error | null}
              onRollback={isArgoApp && !operationInProgress ? (item) => {
                if (parseArgoRollbackID(item.id) == null) return
                setRollbackTarget(item)
              } : undefined}
            />
          )
        }
        if (tab === 'changes') {
          return (
            <GitOpsChangesView
              insight={insightsQ.data}
              error={insightsQ.error as Error | null}
              onOpenResource={openResourceFromTree}
              onSyncResource={isArgoApp ? (resource) => openArgoSyncDialog({ scope: 'resource', resource }) : undefined}
              syncResourceDisabledReason={isArgoApp ? (
                terminating
                  ? terminatingActionTooltip
                  : effectiveSuspended
                    ? 'Resume the Application before syncing a resource.'
                    : operationInProgress || argoSync.isPending
                      ? 'Wait for the current sync operation to finish.'
                      : undefined
              ) : undefined}
              focusKey={changesFocusKey}
              tree={tree}
            />
          )
        }
        // topology
        return (
          <div className="grid min-h-0 min-w-0 flex-1 grid-cols-[280px_minmax(0,1fr)] max-lg:grid-cols-1">
            <GitOpsGraphFilterRail
              facets={graphFacets}
              preset={graphPreset}
              onPresetChange={setGraphPreset}
              search={graphSearch}
              onSearchChange={setGraphSearch}
              kinds={graphKinds}
              onToggleKind={(value) => toggleSet(graphKinds, setGraphKinds, value)}
              sync={graphSync}
              onToggleSync={(value) => toggleSet(graphSync, setGraphSync, value)}
              health={graphHealth}
              onToggleHealth={(value) => toggleSet(graphHealth, setGraphHealth, value)}
              namespaces={graphNamespaces}
              onToggleNamespace={(value) => toggleSet(graphNamespaces, setGraphNamespaces, value)}
              roles={graphRoles}
              onToggleRole={(value) => toggleSet(graphRoles, setGraphRoles, value)}
            />
            <div className="min-h-0 min-w-0 border-l border-theme-border max-lg:border-l-0 max-lg:border-t">
              <GitOpsTreeGraph
                tree={tree}
                loading={treeQ.isLoading}
                error={treeQ.error as Error | null}
                onNodeClick={openResourceFromTree}
                preset={graphPreset}
                onPresetChange={setGraphPreset}
                query={graphSearch}
                onQueryChange={setGraphSearch}
                filters={graphFilters}
                showToolbar={false}
              />
            </div>
          </div>
        )
      }}
    >
      {/* Modals — portaled to body, only render the ones for the current tool. */}
      {isArgoApp && (
        <>
          <SyncOptionsDialog
            open={!!syncDialogTarget}
            appLabel={`${namespace}/${name}`}
            resource={syncDialogTarget?.scope === 'resource' ? syncDialogTarget.resource : undefined}
            pending={argoSync.isPending}
            autoSyncEnabled={argoAutoSyncEnabled}
            validationPending={argoResourceValidation.isPending}
            operationInProgress={operationInProgress}
            validationResult={argoResourceValidation.data}
            validationError={argoResourceValidation.error?.message}
            onCancel={closeArgoSyncDialog}
            onValidationReset={() => argoResourceValidation.reset()}
            onValidate={syncDialogTarget?.scope === 'resource' ? (opts) => {
              argoResourceValidation.mutate(buildArgoResourceSyncVars(namespace, name, syncDialogTarget.resource, opts))
            } : undefined}
            onConfirm={(opts) => {
              if (!syncDialogTarget) return
              const variables = syncDialogTarget.scope === 'resource'
                ? buildArgoResourceSyncVars(namespace, name, syncDialogTarget.resource, opts)
                : { namespace, name, ...opts }
              argoSync.mutate(variables, {
                onSettled: closeArgoSyncDialog,
              })
            }}
          />
          <RollbackDialog
            open={!!rollbackTarget}
            appLabel={`${namespace}/${name}`}
            revision={rollbackTarget?.revision || ''}
            historyId={rollbackTarget?.id}
            pending={argoRollback.isPending}
            onCancel={() => setRollbackTarget(null)}
            onConfirm={(opts) => {
              const id = parseArgoRollbackID(rollbackTarget?.id)
              if (id == null) {
                showError('Rollback target became invalid', 'The history entry changed while the dialog was open. Reselect a target and try again.')
                setRollbackTarget(null)
                return
              }
              argoRollback.mutate({ namespace, name, id, ...opts }, {
                onSettled: () => setRollbackTarget(null),
              })
            }}
          />
        </>
      )}
    </GitOpsDetailLayout>
  )
}
type HelmValuesSource = 'flux' | 'argo-object' | 'argo-string' | 'argo-parameters'
interface HelmValuesData {
  yaml: string
  keyCount: number
  source: HelmValuesSource
}

// Both Flux HelmRelease and Argo CD Application-with-Helm-source carry user
// overrides for chart values, but spell them differently. We surface them via
// a single disclosure on the GitOps detail page; this helper normalizes the
// four flavors we may encounter into one renderable shape.
function extractHelmValues(kind: string, resource: any): HelmValuesData | null {
  if (!resource) return null
  if (kind === 'helmreleases') {
    const values = resource?.spec?.values
    if (values && typeof values === 'object' && Object.keys(values).length > 0) {
      return { yaml: safeStringifyYaml(values), keyCount: Object.keys(values).length, source: 'flux' }
    }
    return null
  }
  if (kind === 'applications') {
    const helm = resource?.spec?.source?.helm
    if (helm?.valuesObject && typeof helm.valuesObject === 'object' && Object.keys(helm.valuesObject).length > 0) {
      return {
        yaml: safeStringifyYaml(helm.valuesObject),
        keyCount: Object.keys(helm.valuesObject).length,
        source: 'argo-object',
      }
    }
    if (typeof helm?.values === 'string' && helm.values.trim() !== '') {
      const parsed = tryParseYaml(helm.values)
      const keyCount = parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? Object.keys(parsed).length : 0
      return { yaml: helm.values, keyCount, source: 'argo-string' }
    }
    if (Array.isArray(helm?.parameters) && helm.parameters.length > 0) {
      const obj: Record<string, unknown> = {}
      for (const param of helm.parameters) {
        if (param?.name) obj[param.name] = param.value
      }
      if (Object.keys(obj).length === 0) return null
      return {
        yaml: safeStringifyYaml(obj),
        keyCount: Object.keys(obj).length,
        source: 'argo-parameters',
      }
    }
  }
  return null
}

function safeStringifyYaml(value: unknown): string {
  try {
    return yaml.stringify(value, { lineWidth: 0 })
  } catch {
    return JSON.stringify(value, null, 2)
  }
}

function tryParseYaml(value: string): unknown {
  try {
    return yaml.parse(value)
  } catch {
    return null
  }
}

// AppFact + ViewButton + ActionButton moved into
// @skyhook-io/k8s-ui's GitOpsDetailLayout (shared with hub-web's fleet
// detail page). The OSS wrapper above mounts the layout instead of
// rendering its own header chrome.



function normalizeDetailResource(kind: string, group: string, resource: any): GitOpsRow | null {
  if (kind === 'applications') return normalizeArgoApplication(resource)
  if (kind === 'kustomizations') return normalizeFluxKustomization(resource)
  if (kind === 'helmreleases') return normalizeFluxHelmRelease(resource)
  const status = getGitOpsResourceStatus(kind, resource)
  return {
    id: `${group}/${kind}/${resource.metadata?.namespace ?? ''}/${resource.metadata?.name ?? ''}`,
    mode: 'applications',
    tool: getGitOpsTool(kind, group),
    kindName: kind,
    kind: resource.kind ?? kind,
    group,
    name: resource.metadata?.name ?? '',
    namespace: resource.metadata?.namespace ?? '',
    project: resource.metadata?.namespace ?? '',
    labels: resource.metadata?.labels ?? {},
    sync: status?.sync ?? 'Unknown',
    health: status?.health ?? 'Unknown',
    suspended: status?.suspended ?? resource.spec?.suspend === true,
    repository: resource.spec?.url ?? resource.spec?.sourceRef?.name ?? '',
    targetRevision: resource.status?.artifact?.revision ?? resource.status?.lastAppliedRevision ?? resource.status?.lastAttemptedRevision ?? '',
    path: resource.spec?.path ?? '',
    chart: resource.spec?.chart?.spec?.chart ?? '',
    destination: 'in-cluster',
    destinationNamespace: resource.spec?.targetNamespace ?? resource.metadata?.namespace ?? '',
    createdAt: resource.metadata?.creationTimestamp ?? '',
    lastSync: newestConditionTime(resource),
    autoSync: !resource.spec?.suspend,
    terminating: isTerminating(resource),
    terminationStartedAt: terminationStartedAt(resource),
    raw: resource,
  }
}


function gitOpsDetailPath(kind: string, namespace: string, name: string): string {
  return `/gitops/detail/${encodeURIComponent(kind)}/${encodeURIComponent(namespace || '_')}/${encodeURIComponent(name)}`
}

function decodePathPart(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

function isGitOpsDetailRef(ref: GitOpsTreeRef | GitOpsInsightRef): boolean {
  const kind = ref.kind.toLowerCase()
  if (ref.group === 'argoproj.io') {
    return kind === 'application' || kind === 'applicationset' || kind === 'appproject'
  }
  if (ref.group === 'kustomize.toolkit.fluxcd.io') return kind === 'kustomization'
  if (ref.group === 'helm.toolkit.fluxcd.io') return kind === 'helmrelease'
  // Flux source CRs (GitRepository/HelmRepository/OCIRepository/Bucket/HelmChart)
  // are NOT GitOps detail-page CRs — they're config objects with spec/status
  // but no managed-resource tree. The standard resource drawer renders them
  // cleanly. Keep this in sync with pkg/gitops/tree/graph.go classifyGitOpsKind.
  return false
}

function isValidKubernetesName(name: string): boolean {
  return /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)
}

function hasAPIResource(resources: APIResource[] | undefined, name: string, group: string): boolean {
  return (resources ?? []).some((resource) => resource.name === name && resource.group === group)
}

async function fetchResourceList(kind: string, group: string, namespacesParam: string): Promise<any[]> {
  const params = new URLSearchParams()
  if (namespacesParam) params.set('namespaces', namespacesParam)
  if (group) params.set('group', group)
  const res = await fetch(apiUrl(`/resources/${kind}?${params}`), {
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  })
  if (res.status === 400 || res.status === 403 || res.status === 404) return []
  if (!res.ok) throw new Error(`Failed to fetch ${kind}: HTTP ${res.status}`)
  return res.json()
}

function isTerminating(resource: any): boolean {
  return Boolean(resource?.metadata?.deletionTimestamp)
}

// terminationStartedAt extracts the RFC3339 deletion timestamp, or
// undefined when the resource isn't being deleted. Centralized so all
// three normalizers (Argo, Flux Kustomization, Flux HelmRelease) agree
// on the field path.
function terminationStartedAt(resource: any): string | undefined {
  return resource?.metadata?.deletionTimestamp || undefined
}

function newestConditionTime(resource: any): string {
  const times = (resource.status?.conditions ?? [])
    .map((condition: any) => condition.lastTransitionTime)
    .filter(Boolean)
    .sort()
  return times[times.length - 1] ?? ''
}


// Inline counts for the topology toolbar — answers "how many resources, how
// many of them are healthy / drifted" at a glance, without making the user
// count facets in the filter rail.
function TopologyCounts({ tree }: { tree: GitOpsResourceTree }) {
  const nodes = (tree.nodes ?? []).filter((n) => n.role !== 'group' && n.role !== 'root')
  const total = nodes.length
  if (total === 0) return null
  const healthy = nodes.filter((n) => (n.health || '').toLowerCase() === 'healthy').length
  const degraded = nodes.filter((n) => {
    const h = (n.health || '').toLowerCase()
    return h === 'degraded' || h === 'missing' || h === 'unhealthy'
  }).length
  const outOfSync = nodes.filter((n) => (n.sync || '').toLowerCase() === 'outofsync').length
  return (
    <div className="hidden min-w-0 flex-1 items-center gap-3 truncate text-[11px] text-theme-text-tertiary sm:flex">
      <span><span className="text-theme-text-primary">{total}</span> resources</span>
      {healthy > 0 && <span className="flex items-center gap-1"><span className="h-1.5 w-1.5 rounded-full bg-emerald-500" /> {healthy} healthy</span>}
      {/* Bad-news counts use status colors on the number itself so the worst
          fact in the row visually pops, not just the dot next to it. */}
      {degraded > 0 && <span className="flex items-center gap-1 font-medium text-red-600 dark:text-red-400"><span className="h-1.5 w-1.5 rounded-full bg-red-500" /> {degraded} degraded</span>}
      {outOfSync > 0 && <span className="flex items-center gap-1 font-medium text-amber-700 dark:text-amber-400"><span className="h-1.5 w-1.5 rounded-full bg-amber-500" /> {outOfSync} out of sync</span>}
    </div>
  )
}
