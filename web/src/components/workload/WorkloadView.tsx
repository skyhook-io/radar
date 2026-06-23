import { useMemo, useEffect, useCallback, useState } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useNavigate, useLocation, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { Terminal } from 'lucide-react'
import {
  WorkloadView as BaseWorkloadView,
  EditableYamlView,
  FetchResult,
  type WorkloadTabType,
  type RendererOverrides,
  type GitOpsOwnerRef,
  type GitOpsStatus,
  type HelmOwnerRef,
  gitOpsRouteForOwner,
  gitOpsOwnerFromRelationships,
  getGitOpsResourceStatus,
  resolvedEnvFromKey,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource, ResourceRef, Relationships, ResolvedEnvFrom } from '../../types'
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
  useResource,
  fetchJSON,
} from '../../api/client'
import { PrometheusCharts, isPrometheusSupported } from '../resource/PrometheusCharts'
import { PrometheusChartsGrid } from '../resource/PrometheusChartsGrid'
import { RestartEventLane } from '../resource/RestartChart'
import { RightsizingStrip } from '../resource/RightsizingStrip'
import { useResourceAudit, useResources } from '../../api/client'
import { AuditAlerts } from '@skyhook-io/k8s-ui'
import { WorkloadLogsViewer } from '../logs/WorkloadLogsViewer'
import { LogsViewer } from '../logs/LogsViewer'
import { useCanUpdateSecrets, useCanNodeWrite, useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { useOpenTerminal, useOpenLogs, useOpenWorkloadLogs, useOpenNodeTerminal } from '../dock'
import { PortForwardButton } from '../portforward/PortForwardButton'
import { useToast } from '../ui/Toast'
import { Tooltip } from '../ui/Tooltip'
import { PodRenderer } from '../resources/renderers/PodRenderer'
import { NodeRenderer } from '../resources/renderers/NodeRenderer'
import { ServiceRenderer } from '../resources/renderers/ServiceRenderer'
import { WorkloadRenderer } from '../resources/renderers/WorkloadRenderer'
import { CompositeRenderer } from '../resources/CompositeRenderer'
import { ServiceAccountRenderer } from '../resources/renderers/ServiceAccountRenderer'
import { RoleRenderer } from '../resources/renderers/RoleRenderer'
import { RoleBindingRenderer } from '../resources/renderers/RoleBindingRenderer'
import { NamespaceRenderer } from '../resources/renderers/NamespaceRenderer'
import { HPARenderer } from '../resources/renderers/HPARenderer'
import { PVCRenderer } from '../resources/renderers/PVCRenderer'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { cleanYamlForDuplicate } from '../../utils/skeleton-yaml'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useCompareLauncher } from '../compare/useCompareLauncher'
import { apiVersionToGroup } from '../../utils/navigation'

type TabType = WorkloadTabType

// Stable reference — web renderer wrappers inject platform hooks internally
const rendererOverrides: RendererOverrides = {
  PodRenderer, NodeRenderer, ServiceRenderer, WorkloadRenderer, CompositeRenderer,
  ServiceAccountRenderer,
  RoleRenderer,
  RoleBindingRenderer,
  NamespaceRenderer,
  HPARenderer,
  PVCRenderer,
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

  // Hooks must run unconditionally — the invalid-URL guard comes after them.
  if (!kind || !namespace || !name) {
    return (
      <div className="flex items-center justify-center h-full text-theme-text-tertiary">
        Invalid workload URL
      </div>
    )
  }

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
  hideBackButton?: boolean
  compactHeader?: boolean
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
    onDelete: (params: Parameters<typeof deleteMutation.mutate>[0], callbacks?: { onSuccess?: () => void }) => deleteMutation.mutate(params, { onSuccess: callbacks?.onSuccess }),
    isDeleting: deleteMutation.isPending,
    cascadeDependents: cascadePreview?.dependents,
    cascadeLoading,
    onRestart: (params: Parameters<typeof restartWorkloadMutation.mutate>[0]) => restartWorkloadMutation.mutate(params),
    isRestarting: restartWorkloadMutation.isPending,
    revisions: revisionsList,
    revisionsLoading,
    revisionsError: revisionsError ?? null,
    onRollback: (params: Parameters<typeof rollbackMutation.mutate>[0], callbacks?: { onSuccess?: () => void }) => rollbackMutation.mutate(params, { onSuccess: callbacks?.onSuccess }),
    isRollingBack: rollbackMutation.isPending,
    onTriggerCronJob: (params: Parameters<typeof triggerCronJobMutation.mutate>[0]) => triggerCronJobMutation.mutate(params),
    isTriggeringCronJob: triggerCronJobMutation.isPending,
    onSuspendCronJob: (params: Parameters<typeof suspendCronJobMutation.mutate>[0]) => suspendCronJobMutation.mutate(params),
    isSuspendingCronJob: suspendCronJobMutation.isPending,
    onResumeCronJob: (params: Parameters<typeof resumeCronJobMutation.mutate>[0]) => resumeCronJobMutation.mutate(params),
    isResumingCronJob: resumeCronJobMutation.isPending,
    onFluxReconcile: (params: Parameters<typeof fluxReconcileMutation.mutate>[0]) => fluxReconcileMutation.mutate(params),
    isFluxReconciling: fluxReconcileMutation.isPending,
    onFluxSyncWithSource: (params: Parameters<typeof fluxSyncWithSourceMutation.mutate>[0]) => fluxSyncWithSourceMutation.mutate(params),
    isFluxSyncing: fluxSyncWithSourceMutation.isPending,
    onFluxSuspend: (params: Parameters<typeof fluxSuspendMutation.mutate>[0]) => fluxSuspendMutation.mutate(params),
    isFluxSuspending: fluxSuspendMutation.isPending,
    onFluxResume: (params: Parameters<typeof fluxResumeMutation.mutate>[0]) => fluxResumeMutation.mutate(params),
    isFluxResuming: fluxResumeMutation.isPending,
    onArgoSync: (params: Parameters<typeof argoSyncMutation.mutate>[0]) => argoSyncMutation.mutate(params),
    isArgoSyncing: argoSyncMutation.isPending,
    onArgoRefresh: (params: Parameters<typeof argoRefreshMutation.mutate>[0]) => argoRefreshMutation.mutate(params),
    isArgoRefreshing: argoRefreshMutation.isPending,
    onArgoSuspend: (params: Parameters<typeof argoSuspendMutation.mutate>[0]) => argoSuspendMutation.mutate(params),
    isArgoSuspending: argoSuspendMutation.isPending,
    onArgoResume: (params: Parameters<typeof argoResumeMutation.mutate>[0]) => argoResumeMutation.mutate(params),
    isArgoResuming: argoResumeMutation.isPending,
    canNodeWrite,
    onCordonNode: (params: Parameters<typeof cordonMutation.mutate>[0]) => cordonMutation.mutate(params),
    isCordoningNode: cordonMutation.isPending,
    onUncordonNode: (params: Parameters<typeof uncordonMutation.mutate>[0]) => uncordonMutation.mutate(params),
    isUncordoningNode: uncordonMutation.isPending,
    onDrainNode: (params: Parameters<typeof drainMutation.mutate>[0]) => drainMutation.mutate(params),
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
  const { data: resourceResponse, isLoading: resourceLoading, error: resourceError, refetch: refetchResource } = useResourceWithRelationships<any>(kindProp, namespace, name, rest.group)
  const resource = resourceResponse?.resource
  const relationships = resourceResponse?.relationships
  const certificateInfo = resourceResponse?.certificateInfo
  const hpaDiagnosis = resourceResponse?.hpaDiagnosis
  const relationshipGitopsOwner = useMemo(() => gitOpsOwnerFromRelationships(relationships), [relationships])
  const inheritedGitOpsLookupRef = useMemo(
    () => findInheritedGitOpsLookupRef(relationships, relationshipGitopsOwner, { kind: kindProp, namespace, name, group: rest.group }),
    [relationships, relationshipGitopsOwner, kindProp, namespace, name, rest.group],
  )
  const inheritedGitOpsResponse = useResourceWithRelationships<any>(
    inheritedGitOpsLookupRef ? kindToPlural(inheritedGitOpsLookupRef.kind) : '',
    inheritedGitOpsLookupRef?.namespace ?? '',
    inheritedGitOpsLookupRef?.name ?? '',
    inheritedGitOpsLookupRef?.group,
  )
  const inheritedGitopsOwner = useMemo(
    () => gitOpsOwnerFromRelationships(inheritedGitOpsResponse.data?.relationships),
    [inheritedGitOpsResponse.data?.relationships],
  )
  const relationshipHelmOwner = useMemo(
    () => nativeHelmOwnerFromRelationships(relationships, resource?.metadata?.namespace ?? namespace),
    [relationships, resource?.metadata?.namespace, namespace],
  )
  const inheritedHelmOwner = useMemo(
    () => nativeHelmOwnerFromRelationships(inheritedGitOpsResponse.data?.relationships, inheritedGitOpsResponse.data?.resource?.metadata?.namespace ?? namespace),
    [inheritedGitOpsResponse.data?.relationships, inheritedGitOpsResponse.data?.resource?.metadata?.namespace, namespace],
  )
  const rawGitopsOwner = relationshipGitopsOwner ?? inheritedGitopsOwner
  const gitOpsSourceResource = relationshipGitopsOwner ? resource : inheritedGitOpsResponse.data?.resource
  const helmOwner = relationshipHelmOwner ?? inheritedHelmOwner
  const helmSourceResource = relationshipHelmOwner ? resource : inheritedGitOpsResponse.data?.resource
  const shouldResolveArgoOwner = rawGitopsOwner?.tool === 'argocd' && !rawGitopsOwner.namespace
  const { data: argoApplications } = useResources<any>('applications', undefined, 'argoproj.io', { enabled: shouldResolveArgoOwner })
  const gitopsOwner = useMemo(
    () => resolveGitOpsOwner(rawGitopsOwner, argoApplications),
    [rawGitopsOwner, argoApplications],
  )
  const gitopsOwnerGroup = gitopsOwner ? gitOpsOwnerGroup(gitopsOwner) : ''
  const shouldFetchGitOpsOwner = Boolean(gitopsOwner?.namespace)
  const gitopsOwnerQuery = useResource<any>(
    shouldFetchGitOpsOwner ? gitopsOwner!.kind : '',
    gitopsOwner?.namespace ?? '',
    gitopsOwner?.name ?? '',
    gitopsOwnerGroup,
  )
  const gitOpsOwnerStatus = useMemo(
    () => deriveGitOpsOwnerStatus(gitopsOwner, gitopsOwnerQuery.data),
    [gitopsOwner, gitopsOwnerQuery.data],
  )
  const gitOpsOwnerVerified = Boolean(gitopsOwner?.namespace && gitopsOwnerQuery.data)
  const gitOpsOwnerPending = Boolean(gitopsOwner?.namespace && gitopsOwnerQuery.isLoading && !gitopsOwnerQuery.data)
  const gitOpsOwnerSource = useMemo(
    () => describeGitOpsOwnerSource(rawGitopsOwner, gitOpsSourceResource),
    [rawGitopsOwner, gitOpsSourceResource],
  )
  const helmOwnerSource = useMemo(
    () => describeHelmOwnerSource(helmOwner, helmSourceResource),
    [helmOwner, helmSourceResource],
  )

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
      if (cm) result[resolvedEnvFromKey('configmap', n)] = { keys: Object.keys(cm.data || {}), values: cm.data || {}, isSecret: false }
    })
    envFromSecretNames.forEach((n, i) => {
      const secret = secretQueries[i]?.data?.resource ?? secretQueries[i]?.data
      if (secret) {
        const decodedValues: Record<string, string> = {}
        for (const [k, v] of Object.entries(secret.data || {})) {
          try { decodedValues[k] = atob(v as string) } catch { decodedValues[k] = v as string }
        }
        result[resolvedEnvFromKey('secret', n)] = { keys: Object.keys(decodedValues), values: decodedValues, isSecret: true }
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
  const { onCompareTo, onCompareAcrossClusters, picker: comparePicker } = useCompareLauncher({
    kind: kindProp,
    namespace,
    name,
    // Prefer the URL-supplied group so Compare works even before the resource
    // fetch completes; fall back to the derived group for callers that don't
    // pass one.
    group: rest.group || resourceGroup || undefined,
  })
  const actionsBarProps = useMemo(
    () => ({ ...baseActionsBarProps, onCompareTo, onCompareAcrossClusters }),
    [baseActionsBarProps, onCompareTo, onCompareAcrossClusters],
  )

  const handleUpdateResource = useCallback(async (params: { kind: string; namespace: string; name: string; yaml: string }) => {
    await updateResource.mutateAsync(params)
  }, [updateResource])

  const navigateRouter = useNavigate()
  const handleOpenGitOpsResource = useCallback(
    (ref: GitOpsOwnerRef) => {
      const params = new URLSearchParams()
      const namespaces = searchParams.get('namespaces')
      if (namespaces) params.set('namespaces', namespaces)
      navigateRouter({ pathname: gitOpsRouteForOwner(ref), search: params.toString() })
    },
    [navigateRouter, searchParams],
  )
  const handleNavigateGitOpsPath = useCallback(
    (path: string) => navigateRouter(path),
    [navigateRouter],
  )
  const handleOpenHelmRelease = useCallback(
    (ref: HelmOwnerRef) => {
      const params = new URLSearchParams()
      const namespaces = searchParams.get('namespaces')
      if (namespaces) params.set('namespaces', namespaces)
      params.set('release', `${ref.namespace}/${ref.name}`)
      navigateRouter({ pathname: '/helm', search: params.toString() })
    },
    [navigateRouter, searchParams],
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
      hpaDiagnosis={hpaDiagnosis}
      isLoading={resourceLoading}
      resourceError={resourceError}
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
      renderRelatedYaml={(ref) => <RelatedResourceYaml key={`${ref.kind}/${ref.namespace}/${ref.name}`} target={ref} />}
      renderMetricsTab={({ kind, namespace: ns, name: n }) => (
        <MetricsTabContent kind={kind} namespace={ns} name={n} resource={resource} expanded={expanded} />
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
      onOpenGitOpsResource={gitopsOwnerQuery.data ? handleOpenGitOpsResource : undefined}
      resolvedGitOpsOwner={gitopsOwner}
      gitOpsOwnerVerified={gitOpsOwnerVerified}
      gitOpsOwnerPending={gitOpsOwnerPending}
      gitOpsOwnerSource={gitOpsOwnerSource}
      gitOpsOwnerStatus={gitOpsOwnerStatus}
      helmOwner={helmOwner}
      helmOwnerSource={helmOwnerSource}
      onOpenHelmRelease={handleOpenHelmRelease}
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

function resolveGitOpsOwner(owner: GitOpsOwnerRef | null, argoApplications: any[] | undefined): GitOpsOwnerRef | null {
  if (!owner || owner.namespace || owner.tool !== 'argocd') return owner
  const matches = (argoApplications ?? []).filter((app) => app?.metadata?.name === owner.name)
  if (matches.length !== 1) return owner
  const namespace = matches[0]?.metadata?.namespace
  return namespace ? { ...owner, namespace } : owner
}

function findInheritedGitOpsLookupRef(
  relationships: Relationships | undefined,
  directOwner: GitOpsOwnerRef | null,
  current: ResourceRef,
): ResourceRef | null {
  if (directOwner) return null
  const inheritedManagerRefs = (relationships?.managedBy ?? []).filter((ref) =>
    !gitOpsOwnerFromRelationships({ managedBy: [ref] })
    && !isNativeHelmManager(ref)
  )
  const candidates = [
    relationships?.deployment,
    ...inheritedManagerRefs,
    relationships?.owner,
  ].filter(Boolean) as ResourceRef[]

  return candidates.find((ref) => !isCurrentResource(ref, current)) ?? null
}

function nativeHelmOwnerFromRelationships(relationships: Relationships | undefined, fallbackNamespace: string): HelmOwnerRef | null {
  const ref = relationships?.managedBy?.[0]
  if (!ref || !isNativeHelmManager(ref)) return null
  return {
    namespace: ref.namespace || fallbackNamespace,
    name: ref.name,
  }
}

function isCurrentResource(ref: ResourceRef, current: ResourceRef): boolean {
  return kindToPlural(ref.kind) === kindToPlural(current.kind)
    && ref.namespace === current.namespace
    && ref.name === current.name
    && (ref.group ?? '') === (current.group ?? '')
}

function isNativeHelmManager(ref: ResourceRef): boolean {
  return ref.kind === 'HelmRelease' && ref.group !== 'helm.toolkit.fluxcd.io'
}

function describeGitOpsOwnerSource(owner: GitOpsOwnerRef | null, resource: any): string | null {
  if (!owner || !resource) return null
  const labels = resource.metadata?.labels ?? {}
  const annotations = resource.metadata?.annotations ?? {}

  if (owner.tool === 'fluxcd') {
    const nameKey = owner.kind === 'helmreleases' ? 'helm.toolkit.fluxcd.io/name' : 'kustomize.toolkit.fluxcd.io/name'
    const nsKey = owner.kind === 'helmreleases' ? 'helm.toolkit.fluxcd.io/namespace' : 'kustomize.toolkit.fluxcd.io/namespace'
    if (labels[nameKey] || labels[nsKey]) {
      return `${nameKey}=${labels[nameKey] ?? ''}, ${nsKey}=${labels[nsKey] ?? ''}`
    }
  }

  const trackingID = annotations['argocd.argoproj.io/tracking-id']
  if (trackingID) return `argocd.argoproj.io/tracking-id=${trackingID}`
  const argoInstance = labels['argocd.argoproj.io/instance']
  if (argoInstance) return `argocd.argoproj.io/instance=${argoInstance}`
  return null
}

function describeHelmOwnerSource(owner: HelmOwnerRef | null, resource: any): string | null {
  if (!owner || !resource) return null
  const annotations = resource.metadata?.annotations ?? {}
  const releaseName = annotations['meta.helm.sh/release-name']
  const releaseNamespace = annotations['meta.helm.sh/release-namespace']
  if (releaseName || releaseNamespace) {
    return `meta.helm.sh/release-name=${releaseName ?? ''}, meta.helm.sh/release-namespace=${releaseNamespace ?? ''}`
  }
  return null
}

function gitOpsOwnerGroup(owner: GitOpsOwnerRef): string {
  if (owner.tool === 'argocd') return 'argoproj.io'
  if (owner.kind === 'kustomizations') return 'kustomize.toolkit.fluxcd.io'
  return 'helm.toolkit.fluxcd.io'
}

function deriveGitOpsOwnerStatus(owner: GitOpsOwnerRef | null, resource: any): GitOpsStatus | null {
  if (!owner || !resource || !hasGitOpsStatusPayload(owner, resource)) return null
  return getGitOpsResourceStatus(owner.kind, resource)
}

function hasGitOpsStatusPayload(owner: GitOpsOwnerRef, resource: any): boolean {
  if (owner.kind === 'applications') {
    const status = resource.status ?? {}
    return Boolean(status.sync?.status || status.health?.status || status.operationState?.phase)
  }
  if (resource.spec?.suspend === true) return true
  return Array.isArray(resource.status?.conditions) && resource.status.conditions.length > 0
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

  // A terminated pod has nothing to follow — only stream live ones. Wait for
  // the phase to be known so a completed pod isn't briefly streamed while the
  // resource is still loading.
  const phase = resource?.status?.phase
  const autoStream = !!phase && phase !== 'Succeeded' && phase !== 'Failed'

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
        autoStream={autoStream}
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

  // A terminated pod (common for Job/CronJob children) has nothing to follow —
  // only stream live ones. Wait for the pod to load before deciding so we don't
  // briefly auto-stream a completed pod while its phase is still unknown.
  const { data: selectedPodResource } = useResource<any>('Pod', podNamespace, selectedPod || '')
  const phase = selectedPodResource?.status?.phase
  const autoStream = !!phase && phase !== 'Succeeded' && phase !== 'Failed'

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
            autoStream={autoStream}
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
  return <AuditAlerts findings={findings} onViewAll={() => navigate('/checks')} />
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
          <Tooltip
            key={`${c.kind}/${c.namespace}/${c.name}`}
            content={`${c.kind} ${c.namespace}/${c.name}`}
          >
          <button
            onClick={() => navigate(`/gitops/detail/${c.plural}/${encodeURIComponent(c.namespace)}/${encodeURIComponent(c.name)}`)}
            className="inline-flex items-center gap-1.5 rounded border border-theme-border bg-theme-surface px-1.5 py-0.5 text-[11px] text-theme-text-secondary hover:border-skyhook-500/60 hover:text-skyhook-500 transition-colors"
          >
            <span className="text-theme-text-tertiary">{c.kind === 'HelmRelease' ? 'HR' : 'K'}</span>
            <span>{c.namespace}/{c.name}</span>
          </button>
          </Tooltip>
        ))}
      </div>
    </div>
  )
}

// Drawer mode: single chart + category tabs (compact for ~500px width).
// Full-screen mode: multi-chart grid so CPU + Memory + Network can be
// compared side-by-side without tab switching.
function MetricsTabContent({ kind, namespace, name, resource, expanded }: {
  kind: string
  namespace: string
  name: string
  resource: any
  expanded: boolean
}) {
  const showRightsizing = expanded && ['Deployment', 'StatefulSet', 'DaemonSet'].includes(kind)

  if (expanded) {
    return (
      <div className="flex flex-col h-full">
        {showRightsizing && (
          <div className="px-4 pt-4">
            <RightsizingStrip kind={kind} namespace={namespace} name={name} />
          </div>
        )}
        <div className="flex-1 min-h-0">
          <PrometheusChartsGrid
            kind={kind}
            namespace={namespace}
            name={name}
            resource={resource}
          />
        </div>
      </div>
    )
  }

  // Drawer fallback: single chart with tabs + restart lane below. The chart's
  // time-range selector is mirrored to the restart lane so they stay aligned.
  return (
    <DrawerMetricsContent
      kind={kind}
      namespace={namespace}
      name={name}
      resource={resource}
    />
  )
}

function DrawerMetricsContent({ kind, namespace, name, resource }: {
  kind: string
  namespace: string
  name: string
  resource: any
}) {
  const [chartRange, setChartRange] = useState<import('../../api/client').PrometheusTimeRange>('1h')
  const showRestartLane = isPrometheusSupported(kind) && kind !== 'Node'

  return (
    <div className="flex flex-col h-full">
      <div className="flex-1 min-h-0">
        <PrometheusCharts kind={kind} namespace={namespace} name={name} showEmptyState resource={resource} onTimeRangeChange={setChartRange} />
      </div>
      {showRestartLane && (
        <div className="px-4 pb-4">
          <RestartEventLane kind={kind} namespace={namespace} name={name} range={chartRange} />
        </div>
      )}
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

// Read-only manifest view for an object in the workload's neighborhood (the
// YAML tab's object rail). Read-only by design — editing an arbitrary related
// object belongs on that resource's own page.
function RelatedResourceYaml({ target }: { target: { kind: string; namespace: string; name: string; group?: string } }) {
  const { data, isLoading, error } = useResource<any>(kindToPlural(target.kind), target.namespace, target.name, target.group)
  const [copied, setCopied] = useState(false)
  const handleCopy = useCallback((text: string) => {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }, [])
  if (!data) return <FetchResult loading={isLoading} error={error as Error | null} className="h-32" />
  return (
    <EditableYamlView
      resource={{ kind: kindToPlural(target.kind), namespace: target.namespace, name: target.name, group: target.group }}
      data={data}
      onCopy={handleCopy}
      copied={copied}
      readOnly
    />
  )
}
