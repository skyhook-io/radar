import { useMemo, useEffect, useCallback, useState } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useNavigate, useLocation, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { Terminal } from 'lucide-react'
import {
  WorkloadView as BaseWorkloadView,
  type RendererOverrides,
  type GitOpsOwnerRef,
  gitOpsRouteForOwner,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource, ResourceRef, ResolvedEnvFrom } from '../../types'
import { kindToPlural, buildWorkloadPath, type NavigateToResource } from '../../utils/navigation'
import {
  useChanges, useResourceWithRelationships, usePodLogs, useTopology, useUpdateResource,
  useDeleteResource, useTriggerCronJob, useSuspendCronJob, useResumeCronJob,
  useRestartWorkload, useWorkloadRevisions, useRollbackWorkload,
  useFluxReconcile, useFluxSyncWithSource, useFluxSuspend, useFluxResume,
  useArgoSync, useArgoRefresh, useArgoSuspend, useArgoResume,
  useCordonNode, useUncordonNode, useDrainNode,
  useCascadeDeletePreview,
  useResourceEvents,
  fetchJSON,
} from '../../api/client'
import { PrometheusCharts, isPrometheusSupported } from '../resource/PrometheusCharts'
import { useResourceAudit, useResources } from '../../api/client'
import { AuditAlerts } from '@skyhook-io/k8s-ui'
import { WorkloadLogsViewer } from '../logs/WorkloadLogsViewer'
import { LogsViewer } from '../logs/LogsViewer'
import { useCanUpdateSecrets, useCanNodeWrite, useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { useOpenTerminal, useOpenLogs, useOpenWorkloadLogs, useOpenNodeTerminal } from '../dock'
import { PortForwardButton } from '../portforward/PortForwardButton'
import { useToast } from '../ui/Toast'
import { PodRenderer } from '../resources/renderers/PodRenderer'
import { NodeRenderer } from '../resources/renderers/NodeRenderer'
import { ServiceRenderer } from '../resources/renderers/ServiceRenderer'
import { WorkloadRenderer } from '../resources/renderers/WorkloadRenderer'
import { CompositeRenderer } from '../resources/CompositeRenderer'
import { ServiceAccountRenderer } from '../resources/renderers/ServiceAccountRenderer'
import { RoleRenderer } from '../resources/renderers/RoleRenderer'
import { RoleBindingRenderer } from '../resources/renderers/RoleBindingRenderer'
import { NamespaceRenderer } from '../resources/renderers/NamespaceRenderer'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { cleanYamlForDuplicate } from '../../utils/skeleton-yaml'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useCompareLauncher } from '../compare/useCompareLauncher'
import { apiVersionToGroup } from '../../utils/navigation'

type TabType = 'overview' | 'timeline' | 'logs' | 'metrics' | 'yaml'

// Stable reference — web renderer wrappers inject platform hooks internally
const rendererOverrides: RendererOverrides = {
  PodRenderer, NodeRenderer, ServiceRenderer, WorkloadRenderer, CompositeRenderer,
  ServiceAccountRenderer,
  RoleRenderer,
  RoleBindingRenderer,
  NamespaceRenderer,
}

// ============================================================================
// ROUTE WRAPPER — parses kind/ns/name from URL
// ============================================================================

interface WorkloadViewRouteProps {
  onNavigateToResource?: NavigateToResource
}

export function WorkloadViewRoute({ onNavigateToResource }: WorkloadViewRouteProps) {
  const location = useLocation()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()

  // Parse /workload/:kind/:ns/:name from pathname. Segments are URL-encoded by
  // buildWorkloadPath; names can also contain literal slashes (e.g. some CRD names),
  // which survive encoding as %2F and reassemble correctly here.
  const parts = location.pathname.replace(/^\//, '').split('/')
  const decode = (s: string): string => {
    try { return decodeURIComponent(s) } catch { return s }
  }
  const kind = decode(parts[1] ?? '')
  const namespace = decode(parts[2] ?? '')
  const name = parts.slice(3).map(decode).join('/')
  const group = searchParams.get('apiGroup') || ''

  if (!kind || !namespace || !name) {
    return (
      <div className="flex items-center justify-center h-full text-theme-text-tertiary">
        Invalid workload URL
      </div>
    )
  }

  const handleBack = useCallback(() => {
    if (window.history.length > 1) {
      navigate(-1)
    } else {
      navigate('/')
    }
  }, [navigate])

  const handleNavigate = useCallback((resource: SelectedResource) => {
    navigate(buildWorkloadPath(resource))
  }, [navigate])

  return (
    <WorkloadView
      kind={kind}
      namespace={namespace}
      name={name}
      group={group}
      onBack={handleBack}
      onNavigateToResource={onNavigateToResource || handleNavigate}
    />
  )
}

// ============================================================================
// WORKLOAD VIEW WRAPPER — injects data fetching hooks
// ============================================================================

interface WorkloadViewProps {
  kind: string
  namespace: string
  name: string
  onBack: () => void
  onNavigateToResource?: NavigateToResource
  onCollapseToDrawer?: () => void
  expanded?: boolean
  onClose?: () => void
  onExpand?: () => void
  initialTab?: 'detail' | 'yaml'
  group?: string
}

function useActionsBarProps(kind: string, namespace: string, name: string) {
  const { showCopied } = useToast()
  const openTerminal = useOpenTerminal()
  const openLogs = useOpenLogs()
  const openWorkloadLogs = useOpenWorkloadLogs()
  const openNodeTerminal = useOpenNodeTerminal()
  const { canExec, canViewLogs, canPortForward } = useNamespacedCapabilities(namespace)

  const deleteMutation = useDeleteResource()
  const restartWorkloadMutation = useRestartWorkload()
  const rollbackMutation = useRollbackWorkload()
  const triggerCronJobMutation = useTriggerCronJob()
  const suspendCronJobMutation = useSuspendCronJob()
  const resumeCronJobMutation = useResumeCronJob()

  const isRollbackKind = ['deployments', 'statefulsets', 'daemonsets'].includes(kind.toLowerCase())
  const { data: revisionsList, isLoading: revisionsLoading, error: revisionsError } = useWorkloadRevisions(kind.toLowerCase(), namespace, name, isRollbackKind)

  const fluxReconcileMutation = useFluxReconcile()
  const fluxSyncWithSourceMutation = useFluxSyncWithSource()
  const fluxSuspendMutation = useFluxSuspend()
  const fluxResumeMutation = useFluxResume()

  const argoSyncMutation = useArgoSync()
  const argoRefreshMutation = useArgoRefresh()
  const argoSuspendMutation = useArgoSuspend()
  const argoResumeMutation = useArgoResume()

  const { data: cascadePreview, isLoading: cascadeLoading } = useCascadeDeletePreview(kind, namespace, name, true)

  const canNodeWrite = useCanNodeWrite()
  const cordonMutation = useCordonNode()
  const uncordonMutation = useUncordonNode()
  const drainMutation = useDrainNode()

  return {
    canExec,
    canViewLogs,
    canPortForward,
    onOpenTerminal: openTerminal,
    onOpenLogs: openLogs,
    onOpenWorkloadLogs: openWorkloadLogs,
    onOpenNodeTerminal: openNodeTerminal,
    onCopyCommand: (text: string, message: string, event: React.MouseEvent) => showCopied(text, message, event),
    renderPortForward: ({ type, namespace: ns, name: n, className }: { type: 'pod' | 'service'; namespace: string; name: string; className?: string }) => (
      <PortForwardButton type={type} namespace={ns} name={n} className={className} />
    ),
    onDelete: (params: any, callbacks?: any) => deleteMutation.mutate(params, { onSuccess: callbacks?.onSuccess }),
    isDeleting: deleteMutation.isPending,
    cascadeDependents: cascadePreview?.dependents,
    cascadeLoading,
    onRestart: (params: any) => restartWorkloadMutation.mutate(params),
    isRestarting: restartWorkloadMutation.isPending,
    revisions: revisionsList,
    revisionsLoading,
    revisionsError: revisionsError ?? null,
    onRollback: (params: any, callbacks?: any) => rollbackMutation.mutate(params, { onSuccess: callbacks?.onSuccess }),
    isRollingBack: rollbackMutation.isPending,
    onTriggerCronJob: (params: any) => triggerCronJobMutation.mutate(params),
    isTriggeringCronJob: triggerCronJobMutation.isPending,
    onSuspendCronJob: (params: any) => suspendCronJobMutation.mutate(params),
    isSuspendingCronJob: suspendCronJobMutation.isPending,
    onResumeCronJob: (params: any) => resumeCronJobMutation.mutate(params),
    isResumingCronJob: resumeCronJobMutation.isPending,
    onFluxReconcile: (params: any) => fluxReconcileMutation.mutate(params),
    isFluxReconciling: fluxReconcileMutation.isPending,
    onFluxSyncWithSource: (params: any) => fluxSyncWithSourceMutation.mutate(params),
    isFluxSyncing: fluxSyncWithSourceMutation.isPending,
    onFluxSuspend: (params: any) => fluxSuspendMutation.mutate(params),
    isFluxSuspending: fluxSuspendMutation.isPending,
    onFluxResume: (params: any) => fluxResumeMutation.mutate(params),
    isFluxResuming: fluxResumeMutation.isPending,
    onArgoSync: (params: any) => argoSyncMutation.mutate(params),
    isArgoSyncing: argoSyncMutation.isPending,
    onArgoRefresh: (params: any) => argoRefreshMutation.mutate(params),
    isArgoRefreshing: argoRefreshMutation.isPending,
    onArgoSuspend: (params: any) => argoSuspendMutation.mutate(params),
    isArgoSuspending: argoSuspendMutation.isPending,
    onArgoResume: (params: any) => argoResumeMutation.mutate(params),
    isArgoResuming: argoResumeMutation.isPending,
    canNodeWrite,
    onCordonNode: (params: any) => cordonMutation.mutate(params),
    isCordoningNode: cordonMutation.isPending,
    onUncordonNode: (params: any) => uncordonMutation.mutate(params),
    isUncordoningNode: uncordonMutation.isPending,
    onDrainNode: (params: any) => drainMutation.mutate(params),
    isDrainingNode: drainMutation.isPending,
  }
}

export function WorkloadView({
  kind: kindProp,
  namespace,
  name,
  expanded = true,
  ...rest
}: WorkloadViewProps) {
  const [searchParams, setSearchParams] = useSearchParams()

  // Tab state from URL query param — migrate legacy tab names
  const rawTab = searchParams.get('tab')
  const migratedTab: TabType = rawTab === 'info' ? 'overview'
    : rawTab === 'events' ? 'timeline'
    : (rawTab as TabType) || 'overview'

  const handleTabChange = useCallback((tab: TabType) => {
    const params = new URLSearchParams(searchParams)
    if (tab === 'overview') {
      params.delete('tab')
    } else {
      params.set('tab', tab)
    }
    setSearchParams(params, { replace: true })
  }, [searchParams, setSearchParams])

  // Fetch resource with relationships
  const { data: resourceResponse, isLoading: resourceLoading, refetch: refetchResource } = useResourceWithRelationships<any>(kindProp, namespace, name, rest.group)
  const resource = resourceResponse?.resource
  const relationships = resourceResponse?.relationships
  const certificateInfo = resourceResponse?.certificateInfo

  // For pods: extract envFrom ConfigMap/Secret names and resolve their keys
  const isPod = kindProp.toLowerCase() === 'pods'
  const { envFromConfigMapNames, envFromSecretNames } = useMemo(() => {
    if (!isPod || !resource) return { envFromConfigMapNames: [] as string[], envFromSecretNames: [] as string[] }
    const cmNames = new Set<string>()
    const secretNames = new Set<string>()
    const containers = [...(resource.spec?.containers || []), ...(resource.spec?.initContainers || [])]
    for (const c of containers) {
      for (const ef of (c.envFrom || [])) {
        if (ef.configMapRef?.name) cmNames.add(ef.configMapRef.name)
        if (ef.secretRef?.name) secretNames.add(ef.secretRef.name)
      }
    }
    return { envFromConfigMapNames: Array.from(cmNames), envFromSecretNames: Array.from(secretNames) }
  }, [isPod, resource])

  const configMapQueries = useQueries({
    queries: envFromConfigMapNames.map((cmName) => ({
      queryKey: ['resources', 'configmaps', namespace, cmName],
      queryFn: () => fetchJSON<any>(`/resources/configmaps/${namespace}/${cmName}`),
      enabled: isPod,
      staleTime: 30000,
    })),
  })

  const secretQueries = useQueries({
    queries: envFromSecretNames.map((secretName) => ({
      queryKey: ['resources', 'secrets', namespace, secretName],
      queryFn: () => fetchJSON<any>(`/resources/secrets/${namespace}/${secretName}`),
      enabled: isPod,
      staleTime: 30000,
    })),
  })

  const resolvedEnvFrom = useMemo(() => {
    if (!isPod || (envFromConfigMapNames.length === 0 && envFromSecretNames.length === 0)) return undefined
    const result: ResolvedEnvFrom = {}
    envFromConfigMapNames.forEach((n, i) => {
      // Single-resource endpoint returns { resource, relationships } wrapper
      const cm = configMapQueries[i]?.data?.resource ?? configMapQueries[i]?.data
      if (cm) result[n] = { keys: Object.keys(cm.data || {}), values: cm.data || {}, isSecret: false }
    })
    envFromSecretNames.forEach((n, i) => {
      const secret = secretQueries[i]?.data?.resource ?? secretQueries[i]?.data
      if (secret) {
        const decodedValues: Record<string, string> = {}
        for (const [k, v] of Object.entries(secret.data || {})) {
          try { decodedValues[k] = atob(v as string) } catch { decodedValues[k] = v as string }
        }
        result[n] = { keys: Object.keys(decodedValues), values: decodedValues, isSecret: true }
      }
    })
    return Object.keys(result).length > 0 ? result : undefined
  }, [isPod, envFromConfigMapNames, envFromSecretNames, configMapQueries, secretQueries])

  // Fetch topology for hierarchy building (only when expanded)
  const { data: topology } = useTopology([namespace], 'resources', { enabled: expanded })

  // Always fetched so Recent Events populates on drawer open; allEvents below is
  // gated on expanded because it's namespace-wide and expensive.
  const {
    k8sEvents: resourceFocusedK8sEvents,
    updates: resourceFocusedUpdates,
    isLoading: resourceFocusedEventsLoading,
    k8sError: resourceFocusedK8sError,
    updatesError: resourceFocusedUpdatesError,
  } = useResourceEvents(kindProp, namespace, name)

  // Fetch all events for this resource's namespace (only when expanded)
  const { data: allEvents, isLoading: eventsLoading } = useChanges({
    namespaces: [namespace],
    timeRange: 'all',
    includeK8sEvents: true,
    includeManaged: true,
    limit: 10000,
    enabled: expanded,
  })

  // RBAC
  const canUpdateSecrets = useCanUpdateSecrets()
  const updateResource = useUpdateResource()
  const baseActionsBarProps = useActionsBarProps(kindProp, namespace, name)
  const desktopDownload = useDesktopDownload()

  const resourceGroup = useMemo(
    () => (resource?.apiVersion ? apiVersionToGroup(resource.apiVersion) : undefined),
    [resource?.apiVersion],
  )
  const { onCompareTo, picker: comparePicker } = useCompareLauncher({
    kind: kindProp,
    namespace,
    name,
    // Prefer the URL-supplied group so Compare works even before the resource
    // fetch completes; fall back to the derived group for callers that don't
    // pass one.
    group: rest.group || resourceGroup || undefined,
  })
  const actionsBarProps = useMemo(
    () => ({ ...baseActionsBarProps, onCompareTo }),
    [baseActionsBarProps, onCompareTo],
  )

  const handleUpdateResource = useCallback(async (params: { kind: string; namespace: string; name: string; yaml: string }) => {
    await updateResource.mutateAsync(params)
  }, [updateResource])

  const navigateRouter = useNavigate()
  const handleOpenGitOpsResource = useCallback(
    (ref: GitOpsOwnerRef) => navigateRouter(gitOpsRouteForOwner(ref)),
    [navigateRouter],
  )
  const handleNavigateGitOpsPath = useCallback(
    (path: string) => navigateRouter(path),
    [navigateRouter],
  )

  // Duplicate dialog
  const [duplicateDialogOpen, setDuplicateDialogOpen] = useState(false)
  const [duplicateYaml, setDuplicateYaml] = useState('')

  const handleDuplicate = useCallback((params: { kind: string; namespace: string; name: string; yaml: string }) => {
    setDuplicateYaml(cleanYamlForDuplicate(params.yaml))
    setDuplicateDialogOpen(true)
  }, [])

  return (
    <>
    <BaseWorkloadView
      kind={kindProp}
      namespace={namespace}
      name={name}
      expanded={expanded}
      {...rest}
      // Data
      resource={resource}
      relationships={relationships}
      certificateInfo={certificateInfo}
      isLoading={resourceLoading}
      refetch={refetchResource}
      // Timeline
      allEvents={allEvents}
      eventsLoading={eventsLoading}
      topology={topology}
      resourceFocusedK8sEvents={resourceFocusedK8sEvents}
      resourceFocusedUpdates={resourceFocusedUpdates}
      resourceFocusedEventsLoading={resourceFocusedEventsLoading}
      resourceFocusedK8sError={resourceFocusedK8sError}
      resourceFocusedUpdatesError={resourceFocusedUpdatesError}
      // Capabilities
      canUpdateSecrets={canUpdateSecrets}
      // Mutations
      onUpdateResource={handleUpdateResource}
      isUpdatingResource={updateResource.isPending}
      updateResourceError={updateResource.error?.message ?? null}
      // Tab state (URL-synced)
      activeTab={migratedTab}
      onTabChange={handleTabChange}
      // Render props
      renderLogsTab={(props) => <LogsTabContent {...props} />}
      renderMetricsTab={({ kind, namespace: ns, name: n }) => (
        <PrometheusCharts kind={kind} namespace={ns} name={n} showEmptyState />
      )}
      isMetricsAvailable={(kind, res) =>
        isPrometheusSupported(kind) && !(kind === 'Pod' && res?.status?.phase === 'Pending')
      }
      onDuplicate={handleDuplicate}
      onDownload={desktopDownload}
      actionsBarProps={actionsBarProps}
      rendererOverrides={rendererOverrides}
      resolvedEnvFrom={resolvedEnvFrom}
      renderOverviewExtra={({ kind: k, namespace: ns, name: n }) => (
        <>
          <AuditSection kind={k} namespace={ns} name={n} />
          <FluxSourceConsumersSection kind={k} namespace={ns} name={n} />
        </>
      )}
      onOpenGitOpsResource={handleOpenGitOpsResource}
      onNavigateGitOpsPath={handleNavigateGitOpsPath}
    />
    <CreateResourceDialog
      open={duplicateDialogOpen}
      onClose={() => setDuplicateDialogOpen(false)}
      initialYaml={duplicateYaml}
      title="Duplicate Resource"
      onCreated={(result) => {
        rest.onNavigateToResource?.({ kind: kindToPlural(result.kind), namespace: result.namespace, name: result.name, group: '' })
      }}
    />
    {comparePicker}
    </>
  )
}

// ============================================================================
// LOGS TAB — platform-specific (uses data-fetching hooks)
// ============================================================================

const WORKLOAD_LOG_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet'])

function LogsTabContent({
  kind,
  apiKind,
  namespace,
  name,
  resource,
  pods,
  selectedPod,
  onSelectPod,
  initialContainer,
  onConsumeInitialContainer,
}: {
  kind: string
  apiKind: string
  namespace: string
  name: string
  resource: any
  pods: ResourceRef[]
  selectedPod: string | null
  onSelectPod: (name: string | null) => void
  initialContainer: string | null
  onConsumeInitialContainer: () => void
}) {
  // Workload kinds (Deployment, StatefulSet, DaemonSet) use the aggregated workload logs viewer
  if (WORKLOAD_LOG_KINDS.has(kind)) {
    return (
      <div className="h-full">
        <WorkloadLogsViewer kind={apiKind} namespace={namespace} name={name} />
      </div>
    )
  }

  // Individual Pod — use LogsViewer with container list from resource data
  if (kind === 'Pod') {
    return <PodLogsTab namespace={namespace} name={name} resource={resource} initialContainer={initialContainer} onConsumeInitialContainer={onConsumeInitialContainer} />
  }

  // Other kinds with associated pods (Jobs, CronJobs, ReplicaSets, etc.) — pod selector + LogsViewer
  return (
    <MultiPodLogsTab
      pods={pods}
      namespace={namespace}
      selectedPod={selectedPod}
      onSelectPod={onSelectPod}
      initialContainer={initialContainer}
    />
  )
}

function PodLogsTab({ namespace, name, resource, initialContainer, onConsumeInitialContainer }: {
  namespace: string
  name: string
  resource: any
  initialContainer?: string | null
  onConsumeInitialContainer?: () => void
}) {
  const containers = useMemo(() => {
    const names: string[] = []
    for (const c of resource?.spec?.initContainers || []) if (c.name) names.push(c.name)
    for (const c of resource?.spec?.containers || []) if (c.name) names.push(c.name)
    return names
  }, [resource])

  useEffect(() => {
    if (initialContainer && containers.includes(initialContainer)) {
      onConsumeInitialContainer?.()
    }
  }, [initialContainer, containers, onConsumeInitialContainer])

  return (
    <div className="h-full">
      <LogsViewer
        namespace={namespace}
        podName={name}
        containers={containers}
        initialContainer={initialContainer || undefined}
      />
    </div>
  )
}

function MultiPodLogsTab({ pods, namespace, selectedPod, onSelectPod, initialContainer }: {
  pods: ResourceRef[]
  namespace: string
  selectedPod: string | null
  onSelectPod: (name: string | null) => void
  initialContainer?: string | null
}) {
  useEffect(() => {
    if (pods.length > 0 && !selectedPod) {
      onSelectPod(pods[0].name)
    }
  }, [pods, selectedPod, onSelectPod])

  const podNamespace = pods.find(p => p.name === selectedPod)?.namespace || namespace

  // Fetch container list for the selected pod
  const { data: logsData } = usePodLogs(podNamespace, selectedPod || '', { tailLines: 1 })
  const containers = logsData?.containers || []

  if (pods.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
        <Terminal className="w-12 h-12 mb-4 opacity-50" />
        <p>No pods available</p>
      </div>
    )
  }

  return (
    <div className="h-full flex flex-col">
      {pods.length > 1 && (
        <div className="shrink-0 border-b border-theme-border bg-theme-surface/50 px-4 py-2 flex gap-2 overflow-x-auto">
          {pods.map(pod => (
            <button
              key={pod.name}
              onClick={() => onSelectPod(pod.name)}
              className={clsx(
                'px-3 py-1.5 text-sm rounded-lg whitespace-nowrap transition-colors',
                selectedPod === pod.name
                  ? 'bg-blue-500 text-theme-text-primary'
                  : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover'
              )}
            >
              {pod.name.length > 40 ? '...' + pod.name.slice(-37) : pod.name}
            </button>
          ))}
        </div>
      )}
      {selectedPod && containers.length > 0 && (
        <div className="flex-1 min-h-0">
          <LogsViewer
            key={selectedPod}
            namespace={podNamespace}
            podName={selectedPod}
            containers={containers}
            initialContainer={initialContainer || undefined}
          />
        </div>
      )}
    </div>
  )
}

function AuditSection({ kind, namespace, name }: { kind: string; namespace: string; name: string }) {
  const navigate = useNavigate()
  const { data: findings } = useResourceAudit(kind, namespace, name)
  if (!findings || findings.length === 0) return null
  return <AuditAlerts findings={findings} onViewAll={() => navigate('/audit')} />
}

// FluxSourceConsumersSection lists the reconcilers (Kustomization, HelmRelease)
// that reference this Flux source CR — the inverse of `spec.sourceRef`. Renders
// only when the focused resource is a Flux source kind; otherwise null. Sources
// can have many consumers (one repo feeding multiple apps), so this answers
// "if I edit this source, what gets affected on the next reconcile?".
//
// Filtering happens client-side off the namespaced reconciler lists — these
// are typically small (tens, not thousands) and the dynamic informer cache
// makes the request cheap. If a cluster ever has thousands of HelmReleases,
// a dedicated /api/gitops/consumers endpoint would be the right move; today
// it'd be premature.
// Outer component is cheap — it does only the kind check and decides whether
// to mount the data-fetching child. Without this split, useResources would
// fire two API calls on EVERY workload drawer open (Pod, Deployment, Service,
// …), since the hook has no `enabled` flag and can't be conditionally called
// (Rules of Hooks). The hooks only need to run when the focused resource is
// actually a Flux source CR.
function FluxSourceConsumersSection({ kind, namespace, name }: { kind: string; namespace: string; name: string }) {
  // The inner WorkloadView de-pluralizes the URL's plural form, which gives
  // "Gitrepository" (single-uppercase) rather than the wire-correct
  // "GitRepository" — so we match lowercase. spec.sourceRef.kind on consumers
  // is always wire-correct, so we look that up separately.
  const sourceKind = FLUX_SOURCE_KIND_BY_LOWER.get(kind.toLowerCase()) ?? null
  if (!sourceKind) return null
  return <FluxSourceConsumersInner sourceKind={sourceKind} namespace={namespace} name={name} />
}

function FluxSourceConsumersInner({ sourceKind, namespace, name }: { sourceKind: string; namespace: string; name: string }) {
  const navigate = useNavigate()
  const { data: kustomizations } = useResources<any>('kustomizations', undefined, 'kustomize.toolkit.fluxcd.io')
  const { data: helmReleases } = useResources<any>('helmreleases', undefined, 'helm.toolkit.fluxcd.io')

  const consumers: Array<{ kind: 'Kustomization' | 'HelmRelease'; namespace: string; name: string; plural: string }> = []
  for (const k of kustomizations ?? []) {
    const ref = k?.spec?.sourceRef ?? {}
    const refNs = ref.namespace || k?.metadata?.namespace
    if (ref.kind === sourceKind && ref.name === name && refNs === namespace) {
      consumers.push({ kind: 'Kustomization', namespace: k.metadata.namespace, name: k.metadata.name, plural: 'kustomizations' })
    }
  }
  for (const h of helmReleases ?? []) {
    const ref = h?.spec?.chart?.spec?.sourceRef ?? {}
    const refNs = ref.namespace || h?.metadata?.namespace
    if (ref.kind === sourceKind && ref.name === name && refNs === namespace) {
      consumers.push({ kind: 'HelmRelease', namespace: h.metadata.namespace, name: h.metadata.name, plural: 'helmreleases' })
    }
  }

  if (consumers.length === 0) {
    return (
      <div className="rounded-lg border border-theme-border bg-theme-elevated/40 p-3 text-xs text-theme-text-tertiary">
        Consumed by — no Kustomization or HelmRelease references this source.
      </div>
    )
  }

  return (
    <div className="rounded-lg border border-theme-border bg-theme-elevated/40 p-3">
      <h3 className="mb-2 text-xs font-medium text-theme-text-secondary">
        Consumed by ({consumers.length})
      </h3>
      <div className="flex flex-wrap gap-1.5">
        {consumers.map((c) => (
          <button
            key={`${c.kind}/${c.namespace}/${c.name}`}
            onClick={() => navigate(`/gitops/detail/${c.plural}/${encodeURIComponent(c.namespace)}/${encodeURIComponent(c.name)}`)}
            className="inline-flex items-center gap-1.5 rounded border border-theme-border bg-theme-surface px-1.5 py-0.5 text-[11px] text-theme-text-secondary hover:border-skyhook-500/60 hover:text-skyhook-500 transition-colors"
            title={`${c.kind} ${c.namespace}/${c.name}`}
          >
            <span className="text-theme-text-tertiary">{c.kind === 'HelmRelease' ? 'HR' : 'K'}</span>
            <span>{c.namespace}/{c.name}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

// FLUX_SOURCE_KIND_BY_LOWER maps lowercase kind (what the inner WorkloadView
// produces via its plural-to-singular fallback) to the wire-correct
// PascalCase form that consumers carry in spec.sourceRef.kind. HelmChart is
// intentionally absent — it's an auto-generated internal CR, not something
// users create or point reconcilers at directly.
const FLUX_SOURCE_KIND_BY_LOWER = new Map<string, string>([
  ['gitrepository', 'GitRepository'],
  ['helmrepository', 'HelmRepository'],
  ['ocirepository', 'OCIRepository'],
  ['bucket', 'Bucket'],
])
