import { useMemo, useEffect, useCallback, useRef, useState } from 'react'
import { useQueries, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useLocation, useSearchParams } from 'react-router-dom'
import { workloadPodAwaitsScheduling } from '../capacity/podDemandGate'
import { clsx } from 'clsx'
import { Terminal, Stethoscope } from 'lucide-react'
import {
  WorkloadView as BaseWorkloadView,
  EditableYamlView,
  FetchResult,
  Section,
  type WorkloadTabType,
  type RendererOverrides,
  type GitOpsOwnerRef,
  type GitOpsStatus,
  type HelmOwnerRef,
  type AppRow,
  type ResourceOwnershipContext,
  type ServingResourceDetail,
  type AuditFinding,
  gitOpsRouteForOwner,
  gitOpsOwnerFromRelationships,
  getGitOpsResourceStatus,
  isDiagnoseKind,
  isRolloutKind,
  canSetWorkloadImages,
  isCoreBatchJob,
  type ManagedImageSource,
  type WorkloadImageTarget,
} from '@skyhook-io/k8s-ui'
import type { ServicePortRenderProps } from '@skyhook-io/k8s-ui/components/resources/renderers/ServiceRenderer'
import type { SelectedResource, ResourceRef, Relationships, ResourceWithRelationships } from '../../types'
import {
  kindToPlural,
  kindToPluralWithGroup,
  apiVersionToGroup,
  pluralToKind,
  relatedResourcePath,
  buildWorkloadPath,
  type NavigateToResource,
} from '../../utils/navigation'
import {
  useChanges,
  useResourceWithRelationships,
  usePodLogs,
  useTopology,
  useUpdateResource,
  usePreviewResources,
  useDeleteResource,
  useTriggerCronJob,
  useSuspendCronJob,
  useResumeCronJob,
  useRestartWorkload,
  fetchWorkloadImages,
  fetchResourceWithRelationships,
  useSetWorkloadImages,
  useWorkloadRevisions,
  useRollbackWorkload,
  useRolloutAction,
  useRolloutCapabilities,
  useWorkloadPods,
  useFluxReconcile,
  useFluxSyncWithSource,
  useFluxSuspend,
  useFluxResume,
  useArgoSync,
  useArgoRefresh,
  useArgoSuspend,
  useArgoResume,
  useCordonNode,
  useUncordonNode,
  useDrainNode,
  useCascadeDeletePreview,
  useResourceEvents,
  useResource,
  useWorkloadRuns,
  useApplications,
  fetchJSON,
  fetchYamlSchemas,
} from '../../api/client'
import { PrometheusCharts, isPrometheusSupported } from '../resource/PrometheusCharts'
import { PrometheusChartsGrid } from '../resource/PrometheusChartsGrid'
import { RestartEventLane } from '../resource/RestartChart'
import { RightsizingPanel, RightsizingStrip } from '../resource/RightsizingStrip'
import { WorkloadCostTab } from '../cost/WorkloadCostTab'
import { isOpenCostWorkloadKind } from '../cost/kinds'
import { useResourceAudit, useResourceIssues, useResources, useTrace, fetchTraceWithProbes, fetchInClusterCapability, runInClusterMerged } from '../../api/client'
import { AuditAlerts, ResourceIssuesSection, ReachabilityView, TraceSummary, InClusterConsentDialog, traceFingerprint, staticPollUnreliable, summarizeInClusterTests, type Trace as NetworkTrace, type InClusterCapability, inClusterConsentGiven, consentRequestRows } from '@skyhook-io/k8s-ui'
import { WorkloadLogsViewer } from '../logs/WorkloadLogsViewer'
import { ScheduledWorkloadLogsViewer } from '../logs/ScheduledWorkloadLogsViewer'
import { LogsViewer } from '../logs/LogsViewer'
import { BatchExecutionFullscreen } from '../execution/BatchExecutionView'
import { workloadRunTimelineEvents } from '../execution/batch-timeline'
import {
  useCanUpdateSecrets,
  useCanNodeWrite,
  useNamespacedCapabilities,
  useIsLocalDeployment,
  useCapabilitiesContext,
} from '../../contexts/CapabilitiesContext'
import { useOpenTerminal, useOpenLogs, useOpenWorkloadLogs, useOpenNodeTerminal } from '../dock'
import { PortForwardButton, PortForwardInlineButton } from '../portforward/PortForwardButton'
import {
  CurlButton,
  CurlPanel,
  isHttpishPort,
  defaultScheme,
  defaultPathForPort,
} from '../curl/ServiceCurlButton'
import { useToast } from '../ui/Toast'
import { Tooltip } from '../ui/Tooltip'
import { PodRenderer } from '../resources/renderers/PodRenderer'
import { KarpenterNodePoolRenderer } from '../resources/renderers/KarpenterNodePoolRenderer'
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
import { RolloutRenderer } from '../resources/renderers/RolloutRenderer'
import { KyvernoPolicyCoverage } from '../resources/renderers/KyvernoPolicyCoverage'
import { KyvernoPolicyQueued } from '../resources/renderers/KyvernoPolicyQueued'
import { CNPGObjectStoreRenderer } from '../resources/renderers/CNPGObjectStoreRenderer'
import { VeleroBSLRenderer } from '../resources/renderers/VeleroBSLRenderer'
import { VeleroBackupRenderer } from '../resources/renderers/VeleroBackupRenderer'
import { VeleroRestoreRenderer } from '../resources/renderers/VeleroRestoreRenderer'
import { CNPGClusterRenderer } from '../resources/renderers/CNPGClusterRenderer'
import { CNPGImageCatalogRenderer } from '../resources/renderers/CNPGImageCatalogRenderer'
import {
  CNPGDatabaseRenderer,
  CNPGPublicationRenderer,
  CNPGSubscriptionRenderer,
} from '../resources/renderers/CNPGDeclarativeRenderer'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { cleanYamlForDuplicate } from '../../utils/skeleton-yaml'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useCompareLauncher } from '../compare/useCompareLauncher'
import { useDiagnoseCustomization } from '../../context/DiagnoseCustomization'

type TabType = WorkloadTabType
const BATCH_EXECUTION_KINDS = new Set([
  'Job',
  'CronJob',
  'Workflow',
  'CronWorkflow',
  'WorkflowTemplate',
  'ClusterWorkflowTemplate',
  'ScaledJob',
])

// Stable reference — web renderer wrappers inject platform hooks internally
const rendererOverrides: RendererOverrides = {
  PodRenderer,
  KarpenterNodePoolRenderer,
  NodeRenderer,
  ServiceRenderer,
  WorkloadRenderer,
  CompositeRenderer,
  ServiceAccountRenderer,
  RoleRenderer,
  RoleBindingRenderer,
  NamespaceRenderer,
  HPARenderer,
  PVCRenderer,
  RolloutRenderer,
  KyvernoPolicyCoverage,
  KyvernoPolicyQueued,
  CNPGObjectStoreRenderer,
  VeleroBSLRenderer,
  VeleroBackupRenderer,
  VeleroRestoreRenderer,
  CNPGClusterRenderer,
  CNPGDatabaseRenderer,
  CNPGPublicationRenderer,
  CNPGSubscriptionRenderer,
  CNPGImageCatalogRenderer,
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
  //
  // Cluster-scoped resources (Node, PersistentVolume, Namespace, …) have no
  // namespace: buildWorkloadPath encodes the namespace segment as '_'. Decode
  // that back to '' here, and tolerate a legacy empty segment ('//') and the
  // collapsed three-segment form (/workload/:kind/:name) for older links.
  const parts = location.pathname.replace(/^\//, '').split('/')
  const decode = (s: string): string => {
    try {
      return decodeURIComponent(s)
    } catch {
      return s
    }
  }
  const kind = decode(parts[1] ?? '')
  let namespace: string
  let name: string
  if (parts.length <= 3) {
    // /workload/:kind/:name — cluster-scoped link with no namespace segment.
    namespace = ''
    name = decode(parts[2] ?? '')
  } else {
    const nsSegment = parts[2] ?? ''
    namespace = nsSegment === '_' || nsSegment === '' ? '' : decode(nsSegment)
    name = parts.slice(3).map(decode).join('/')
  }
  const group = searchParams.get('apiGroup') || ''

  const handleBack = useCallback(() => {
    if (window.history.length > 1) {
      navigate(-1)
    } else {
      navigate('/')
    }
  }, [navigate])

  const handleNavigate = useCallback(
    (resource: SelectedResource) => {
      navigate(relatedResourcePath(resource))
    },
    [navigate],
  )

  // Hooks must run unconditionally — the invalid-URL guard comes after them.
  // Namespace is empty for cluster-scoped resources, so only kind + name are required.
  if (!kind || !name) {
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
  /** false on the outgoing layer during an expand/collapse crossfade (default true) */
  active?: boolean
  onClose?: () => void
  onExpand?: (opts?: { yaml?: boolean }) => void
  onExpandIntent?: () => void
  onCancelExpandIntent?: () => void
  initialTab?: 'detail' | 'yaml'
  group?: string
  pushTabHistory?: boolean
}

interface ImageTargetOwnershipContext {
  root: {
    resource: string
    namespace: string
    name: string
  }
  target: WorkloadImageTarget
  response: ResourceWithRelationships<Record<string, unknown>>
  inheritedResponse?: ResourceWithRelationships<Record<string, unknown>>
}

function useActionsBarProps(
  kind: string,
  namespace: string,
  name: string,
  group: string | undefined,
  cascadeEnabled: boolean,
) {
  const { showCopied } = useToast()
  const { features } = useCapabilitiesContext()
  const openTerminal = useOpenTerminal()
  const openLogs = useOpenLogs()
  const openWorkloadLogs = useOpenWorkloadLogs()
  const openNodeTerminal = useOpenNodeTerminal()
  const { canExec, canViewLogs, canPortForward, workloadWrites, workloadWritesPending } = useNamespacedCapabilities(namespace)
  const queryClient = useQueryClient()
  // Live forward when local+RBAC; otherwise (in-cluster/Cloud) still surface the
  // copy-paste kubectl command. The button picks live vs. copy by deployment mode.
  const isLocal = useIsLocalDeployment()
  const showPortForward = canPortForward || !isLocal

  const deleteMutation = useDeleteResource()
  const restartWorkloadMutation = useRestartWorkload()
  const setWorkloadImagesMutation = useSetWorkloadImages()
  const rollbackMutation = useRollbackWorkload()
  // Only the revision dialog drives this one, and it explains a failed promote in place
  // with the retry — the generic action toast would be a second, vaguer voice.
  const rolloutDialogPromote = useRolloutAction({ reportErrors: false })
  const triggerCronJobMutation = useTriggerCronJob()
  const suspendCronJobMutation = useSuspendCronJob()
  const resumeCronJobMutation = useResumeCronJob()

  const isRolloutShape = isRolloutKind(kind)
  const isRollout = isRolloutShape && group === 'argoproj.io'
  const isRollbackKind = ['deployments', 'statefulsets', 'daemonsets'].includes(kind.toLowerCase()) || isRollout
  const { data: rolloutCapabilities, isPending: rolloutCapabilitiesPending } = useRolloutCapabilities(namespace, name, isRollout)
  // Restart and Rollback are the generic workload buttons, so a Rollout has to
  // withhold the callback the way promote-full does. Permissive until the probe
  // answers — withholding while it loads would flicker the shared buttons.
  const rolloutAllows = (verb: 'restart' | 'rollback') =>
    !isRollout || !rolloutCapabilities || rolloutCapabilities[verb]
  const imageUpdatesSupported = features?.workloadImages === true
  const canSetImages = isRolloutShape
    ? imageUpdatesSupported && isRollout && !rolloutCapabilitiesPending && rolloutCapabilities?.setImage === true
    : !workloadWritesPending && canSetWorkloadImages({ name: kind, group }, workloadWrites, imageUpdatesSupported)
  const loadImages = useCallback(
    (params: { kind: string; namespace: string; name: string }) =>
      queryClient.fetchQuery({
        queryKey: ['workload-images', params.kind, params.namespace, params.name],
        queryFn: () => fetchWorkloadImages(params.kind, params.namespace, params.name),
        staleTime: 0,
      }),
    [queryClient],
  )
  const {
    data: revisionsList,
    isLoading: revisionsLoading,
    error: revisionsError,
  } = useWorkloadRevisions(kind.toLowerCase(), namespace, name, isRollbackKind)

  const fluxReconcileMutation = useFluxReconcile()
  const fluxSyncWithSourceMutation = useFluxSyncWithSource()
  const fluxSuspendMutation = useFluxSuspend()
  const fluxResumeMutation = useFluxResume()

  const argoSyncMutation = useArgoSync()
  const argoRefreshMutation = useArgoRefresh()
  const argoSuspendMutation = useArgoSuspend()
  const argoResumeMutation = useArgoResume()

  const {
    data: cascadePreview,
    isLoading: cascadeLoading,
    isError: cascadeError,
  } = useCascadeDeletePreview(
    kind,
    namespace,
    name,
    group,
    cascadeEnabled,
  )

  const canNodeWrite = useCanNodeWrite()
  const cordonMutation = useCordonNode()
  const uncordonMutation = useUncordonNode()
  const drainMutation = useDrainNode()

  const { renderAction: renderDiagnose } = useDiagnoseCustomization()

  return {
    canExec,
    canViewLogs,
    canPortForward: showPortForward,
    onOpenTerminal: openTerminal,
    onOpenLogs: openLogs,
    onOpenWorkloadLogs: openWorkloadLogs,
    onOpenNodeTerminal: openNodeTerminal,
    onCopyCommand: (text: string, message: string, event: React.MouseEvent) =>
      showCopied(text, message, event),
    renderPortForward: ({
      type,
      namespace: ns,
      name: n,
      className,
    }: {
      type: 'pod' | 'service'
      namespace: string
      name: string
      className?: string
    }) => <PortForwardButton type={type} namespace={ns} name={n} className={className} />,
    renderDiagnose,
    onDelete: (
      params: Parameters<typeof deleteMutation.mutate>[0],
      callbacks?: { onSuccess?: () => void },
    ) => deleteMutation.mutate(params, { onSuccess: callbacks?.onSuccess }),
    isDeleting: deleteMutation.isPending,
    cascadeDependents: cascadePreview?.dependents,
    cascadeLoading,
    cascadeRootResolved: cascadeError ? false : cascadePreview?.rootResolved,
    onRestart: rolloutAllows('restart')
      ? (params: Parameters<typeof restartWorkloadMutation.mutate>[0]) =>
          restartWorkloadMutation.mutate(params)
      : undefined,
    isRestarting: restartWorkloadMutation.isPending,
    onLoadImages: canSetImages ? loadImages : undefined,
    onSetImages: canSetImages
      ? (params: Parameters<typeof setWorkloadImagesMutation.mutateAsync>[0]) =>
          setWorkloadImagesMutation.mutateAsync(params)
      : undefined,
    isSettingImages: setWorkloadImagesMutation.isPending,
    revisions: revisionsList,
    revisionsLoading,
    revisionsError: revisionsError ?? null,
    onRollback: rolloutAllows('rollback')
      ? (
          params: Parameters<typeof rollbackMutation.mutate>[0],
          callbacks?: { onSuccess?: () => void },
        ) => rollbackMutation.mutate(params, { onSuccess: callbacks?.onSuccess })
      : undefined,
    isRollingBack: rollbackMutation.isPending,
    // Absent when promote-full is denied — the dialog reads the callback's
    // presence as the permission signal and hides the option.
    // mutateAsync, not mutate: the revision dialog awaits this before closing.
    onRolloutPromoteFull: rolloutCapabilities?.promoteFull
      ? (params: { namespace: string; name: string }) =>
          rolloutDialogPromote.mutateAsync({ action: 'promote-full', ...params })
      : undefined,
    onTriggerCronJob: (params: Parameters<typeof triggerCronJobMutation.mutate>[0]) =>
      triggerCronJobMutation.mutate(params),
    isTriggeringCronJob: triggerCronJobMutation.isPending,
    onSuspendCronJob: (params: Parameters<typeof suspendCronJobMutation.mutate>[0]) =>
      suspendCronJobMutation.mutate(params),
    isSuspendingCronJob: suspendCronJobMutation.isPending,
    onResumeCronJob: (params: Parameters<typeof resumeCronJobMutation.mutate>[0]) =>
      resumeCronJobMutation.mutate(params),
    isResumingCronJob: resumeCronJobMutation.isPending,
    onFluxReconcile: (params: Parameters<typeof fluxReconcileMutation.mutate>[0]) =>
      fluxReconcileMutation.mutate(params),
    isFluxReconciling: fluxReconcileMutation.isPending,
    onFluxSyncWithSource: (params: Parameters<typeof fluxSyncWithSourceMutation.mutate>[0]) =>
      fluxSyncWithSourceMutation.mutate(params),
    isFluxSyncing: fluxSyncWithSourceMutation.isPending,
    onFluxSuspend: (params: Parameters<typeof fluxSuspendMutation.mutate>[0]) =>
      fluxSuspendMutation.mutate(params),
    isFluxSuspending: fluxSuspendMutation.isPending,
    onFluxResume: (params: Parameters<typeof fluxResumeMutation.mutate>[0]) =>
      fluxResumeMutation.mutate(params),
    isFluxResuming: fluxResumeMutation.isPending,
    onArgoSync: (params: Parameters<typeof argoSyncMutation.mutate>[0]) =>
      argoSyncMutation.mutate(params),
    isArgoSyncing: argoSyncMutation.isPending,
    onArgoRefresh: (params: Parameters<typeof argoRefreshMutation.mutate>[0]) =>
      argoRefreshMutation.mutate(params),
    isArgoRefreshing: argoRefreshMutation.isPending,
    onArgoSuspend: (params: Parameters<typeof argoSuspendMutation.mutate>[0]) =>
      argoSuspendMutation.mutate(params),
    isArgoSuspending: argoSuspendMutation.isPending,
    onArgoResume: (params: Parameters<typeof argoResumeMutation.mutate>[0]) =>
      argoResumeMutation.mutate(params),
    isArgoResuming: argoResumeMutation.isPending,
    canNodeWrite,
    onCordonNode: (params: Parameters<typeof cordonMutation.mutate>[0]) =>
      cordonMutation.mutate(params),
    isCordoningNode: cordonMutation.isPending,
    onUncordonNode: (params: Parameters<typeof uncordonMutation.mutate>[0]) =>
      uncordonMutation.mutate(params),
    isUncordoningNode: uncordonMutation.isPending,
    onDrainNode: (params: Parameters<typeof drainMutation.mutate>[0]) =>
      drainMutation.mutate(params),
    isDrainingNode: drainMutation.isPending,
  }
}

export function WorkloadView({
  kind: kindProp,
  namespace,
  name,
  expanded = true,
  pushTabHistory = false,
  ...rest
}: WorkloadViewProps) {
  const [searchParams, setSearchParams] = useSearchParams()
  const apiKind = kindToPluralWithGroup(kindProp, rest.group ?? '')
  const queryClient = useQueryClient()
  const [imageTargetOwnership, setImageTargetOwnership] =
    useState<ImageTargetOwnershipContext | null>(null)
  const imageOwnershipRequestRef = useRef(0)

  // Tab state from URL query param — migrate legacy tab names
  const rawTab = searchParams.get('tab')
  const migratedTab: TabType =
    rawTab === 'info'
      ? 'overview'
      : rawTab === 'events'
        ? 'timeline'
        : rawTab === 'diagnose'
          ? 'reachability' // the network tab was renamed Reachability
          : (rawTab as TabType) || 'overview'

  const handleTabChange = useCallback(
    (tab: TabType, opts?: { replace?: boolean }) => {
      const params = new URLSearchParams(searchParams)
      if (tab === 'overview') {
        params.delete('tab')
      } else {
        params.set('tab', tab)
      }
      setSearchParams(params, { replace: opts?.replace ?? !pushTabHistory })
    },
    [pushTabHistory, searchParams, setSearchParams],
  )

  const selectedRunKey = searchParams.get('run') ?? ''
  const handleSelectedRunChange = useCallback(
    (runKey: string) => {
      const params = new URLSearchParams(searchParams)
      if (runKey) params.set('run', runKey)
      else params.delete('run')
      setSearchParams(params, { replace: true })
    },
    [searchParams, setSearchParams],
  )

  const batchKind = pluralToKind(apiKind)
  const batchExecution = BATCH_EXECUTION_KINDS.has(batchKind) &&
    (batchKind !== 'Job' || isCoreBatchJob(apiKind, rest.group))
  const batchRunsQuery = useWorkloadRuns(apiKind, namespace, name, expanded && batchExecution, {
    refetchActive: true,
    clusterScoped: batchKind === 'ClusterWorkflowTemplate',
  })
  const relatedTimelineEvents = useMemo(
    () => workloadRunTimelineEvents(batchRunsQuery.data?.runs ?? []),
    [batchRunsQuery.data?.runs],
  )

  // Fetch resource with relationships
  const {
    data: resourceResponse,
    isLoading: resourceLoading,
    error: resourceError,
    refetch: refetchResource,
  } = useResourceWithRelationships<any>(apiKind, namespace, name, rest.group)
  const resource = resourceResponse?.resource
  const relationships = resourceResponse?.relationships
  const resourceGroup = useMemo(
    () => (resource?.apiVersion ? apiVersionToGroup(resource.apiVersion) : undefined),
    [resource?.apiVersion],
  )
  // The URL group arrives as '' when ?apiGroup is absent, which fails
  // isDiagnoseKind's group check for Gateway-API kinds (HTTPRoute/GRPCRoute/
  // Gateway) - an HTTPRoute page would then trace its serving Service instead
  // of itself. Fall back to the group derived from the fetched resource, then
  // to undefined (which the gate treats as "group unknown → allow").
  const effectiveGroup = rest.group || resourceGroup || undefined
  // Reachability for a workload IS the reachability of the Services in front of
  // it - a Deployment has no address of its own. Empty for a workload nothing
  // selects, which correctly leaves the tab hidden: there is no path to trace.
  // Entry kinds trace themselves and ignore this.
  const servingServices = useMemo(
    () => (isDiagnoseKind(apiKind, effectiveGroup) ? [] : (relationships?.services ?? [])),
    [apiKind, effectiveGroup, relationships],
  )
  const refetchResourceAndRuns = useCallback(async () => {
    await Promise.all([
      refetchResource(),
      queryClient.refetchQueries({
        queryKey: ['workload-runs', apiKind, namespace, name],
      }),
      queryClient.refetchQueries({
        queryKey: ['workload-pods', apiKind, namespace, name],
      }),
    ])
  }, [apiKind, name, namespace, queryClient, refetchResource])
  const podWorkloadOwner = useMemo(
    () => podWorkloadOwnerFromRelationships(apiKind, namespace, relationships, resource),
    [apiKind, namespace, relationships, resource],
  )
  const podOwnerAppsQuery = useApplications(
    podWorkloadOwner?.namespace ? [podWorkloadOwner.namespace] : [],
    { enabled: Boolean(podWorkloadOwner?.namespace) },
  )
  const ownershipContext = useMemo(
    () => buildPodOwnershipContext(podWorkloadOwner, podOwnerAppsQuery.data?.applications),
    [podWorkloadOwner, podOwnerAppsQuery.data?.applications],
  )
  const certificateInfo = resourceResponse?.certificateInfo
  const hpaDiagnosis = resourceResponse?.hpaDiagnosis
  const activeImageTargetOwnership = useMemo(
    () =>
      imageTargetOwnership &&
      imageTargetOwnership.root.resource.toLowerCase() === apiKind.toLowerCase() &&
      imageTargetOwnership.root.namespace === namespace &&
      imageTargetOwnership.root.name === name
        ? imageTargetOwnership
        : null,
    [apiKind, imageTargetOwnership, name, namespace],
  )
  const relationshipGitopsOwner = useMemo(
    () => gitOpsOwnerFromRelationships(relationships),
    [relationships],
  )
  const inheritedGitOpsLookupRef = useMemo(
    () =>
      findInheritedGitOpsLookupRef(relationships, relationshipGitopsOwner, {
        kind: apiKind,
        namespace,
        name,
        group: rest.group,
      }),
    [relationships, relationshipGitopsOwner, apiKind, namespace, name, rest.group],
  )
  const inheritedGitOpsResponse = useResourceWithRelationships<any>(
    inheritedGitOpsLookupRef ? kindToPluralWithGroup(inheritedGitOpsLookupRef.kind, inheritedGitOpsLookupRef.group ?? '') : '',
    inheritedGitOpsLookupRef?.namespace ?? '',
    inheritedGitOpsLookupRef?.name ?? '',
    inheritedGitOpsLookupRef?.group,
  )
  const inheritedGitopsOwner = useMemo(
    () => gitOpsOwnerFromRelationships(inheritedGitOpsResponse.data?.relationships),
    [inheritedGitOpsResponse.data?.relationships],
  )
  const relationshipHelmOwner = useMemo(
    () =>
      nativeHelmOwnerFromRelationships(relationships, resource?.metadata?.namespace ?? namespace),
    [relationships, resource?.metadata?.namespace, namespace],
  )
  const inheritedHelmOwner = useMemo(
    () =>
      nativeHelmOwnerFromRelationships(
        inheritedGitOpsResponse.data?.relationships,
        inheritedGitOpsResponse.data?.resource?.metadata?.namespace ?? namespace,
      ),
    [
      inheritedGitOpsResponse.data?.relationships,
      inheritedGitOpsResponse.data?.resource?.metadata?.namespace,
      namespace,
    ],
  )
  const rawGitopsOwner = relationshipGitopsOwner ?? inheritedGitopsOwner
  const gitOpsSourceResource = relationshipGitopsOwner
    ? resource
    : inheritedGitOpsResponse.data?.resource
  const helmOwner = relationshipHelmOwner ?? inheritedHelmOwner
  const helmSourceResource = relationshipHelmOwner
    ? resource
    : inheritedGitOpsResponse.data?.resource
  const targetRelationshipGitopsOwner = useMemo(
    () => gitOpsOwnerFromRelationships(activeImageTargetOwnership?.response.relationships),
    [activeImageTargetOwnership?.response.relationships],
  )
  const targetInheritedGitopsOwner = useMemo(
    () =>
      gitOpsOwnerFromRelationships(
        activeImageTargetOwnership?.inheritedResponse?.relationships,
      ),
    [activeImageTargetOwnership?.inheritedResponse?.relationships],
  )
  const targetRawGitopsOwner = targetRelationshipGitopsOwner ?? targetInheritedGitopsOwner
  const targetRelationshipHelmOwner = nativeHelmOwnerFromRelationships(
    activeImageTargetOwnership?.response.relationships,
    activeImageTargetOwnership?.target.namespace ?? namespace,
  )
  const targetInheritedHelmOwner = nativeHelmOwnerFromRelationships(
    activeImageTargetOwnership?.inheritedResponse?.relationships,
    activeImageTargetOwnership?.target.namespace ?? namespace,
  )
  const targetHelmOwner = targetRelationshipHelmOwner ?? targetInheritedHelmOwner
  const shouldResolveArgoOwner =
    (rawGitopsOwner?.tool === 'argocd' && !rawGitopsOwner.namespace) ||
    (targetRawGitopsOwner?.tool === 'argocd' && !targetRawGitopsOwner.namespace)
  const { data: argoApplications } = useResources<any>('applications', undefined, 'argoproj.io', {
    enabled: shouldResolveArgoOwner,
  })
  const gitopsOwner = useMemo(
    () => resolveGitOpsOwner(rawGitopsOwner, argoApplications),
    [rawGitopsOwner, argoApplications],
  )
  const targetGitopsOwner = useMemo(
    () => resolveGitOpsOwner(targetRawGitopsOwner, argoApplications),
    [argoApplications, targetRawGitopsOwner],
  )
  const gitopsOwnerGroup = gitopsOwner ? gitOpsOwnerGroup(gitopsOwner) : ''
  const shouldFetchGitOpsOwner = Boolean(gitopsOwner?.namespace)
  const gitopsOwnerQuery = useResource<any>(
    shouldFetchGitOpsOwner ? gitopsOwner!.kind : '',
    gitopsOwner?.namespace ?? '',
    gitopsOwner?.name ?? '',
    gitopsOwnerGroup,
  )
  const targetGitopsOwnerGroup = targetGitopsOwner ? gitOpsOwnerGroup(targetGitopsOwner) : ''
  const shouldFetchTargetGitOpsOwner = Boolean(
    activeImageTargetOwnership && targetGitopsOwner?.namespace,
  )
  const targetGitopsOwnerQuery = useResource<Record<string, unknown>>(
    shouldFetchTargetGitOpsOwner ? targetGitopsOwner!.kind : '',
    targetGitopsOwner?.namespace ?? '',
    targetGitopsOwner?.name ?? '',
    targetGitopsOwnerGroup,
    { enabled: shouldFetchTargetGitOpsOwner },
  )
  const gitOpsOwnerStatus = useMemo(
    () => deriveGitOpsOwnerStatus(gitopsOwner, gitopsOwnerQuery.data),
    [gitopsOwner, gitopsOwnerQuery.data],
  )
  const gitOpsOwnerVerified = Boolean(gitopsOwner?.namespace && gitopsOwnerQuery.data)
  const gitOpsOwnerPending = Boolean(
    gitopsOwner?.namespace && gitopsOwnerQuery.isLoading && !gitopsOwnerQuery.data,
  )
  const gitOpsOwnerSource = useMemo(
    () => describeGitOpsOwnerSource(rawGitopsOwner, gitOpsSourceResource),
    [rawGitopsOwner, gitOpsSourceResource],
  )
  const helmOwnerSource = useMemo(
    () => describeHelmOwnerSource(helmOwner, helmSourceResource),
    [helmOwner, helmSourceResource],
  )

  // Fetch topology for hierarchy building (only when expanded). Polled like
  // useTrace's "drawer feeling live" pattern — without this, a resource
  // whose status/labels/edges change without its node/edge identity changing
  // (a canary weight step, a Pod's traffic role flipping to stable, an
  // AnalysisRun finishing) never refreshes until something else forces a
  // remount; a genuinely stuck-looking Topology tab, not just a stale one.
  const { data: topology } = useTopology([namespace], 'resources', {
    enabled: expanded,
    refetchInterval: expanded ? 5000 : false,
  })

  // Always fetched so Recent Events populates on drawer open; allEvents below is
  // gated on expanded because it's namespace-wide and expensive.
  const {
    k8sEvents: resourceFocusedK8sEvents,
    updates: resourceFocusedUpdates,
    isLoading: resourceFocusedEventsLoading,
    k8sError: resourceFocusedK8sError,
    updatesError: resourceFocusedUpdatesError,
  } = useResourceEvents(apiKind, namespace, name)

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
  const { features, karpenter } = useCapabilitiesContext()
  const { canPortForward } = useNamespacedCapabilities(namespace)
  const isLocalDeployment = useIsLocalDeployment()
  const showServingPortForward = canPortForward || !isLocalDeployment
  const showServingCurl = !isLocalDeployment
  const [servingCurl, setServingCurl] = useState<{
    namespace: string
    serviceName: string
    port: number
    closing: boolean
  } | null>(null)
  const closeServingCurl = useCallback(() => {
    setServingCurl((p) => (p ? { ...p, closing: true } : null))
    window.setTimeout(() => setServingCurl((p) => (p?.closing ? null : p)), 220)
  }, [])
  const renderServicePortAction = useCallback(
    (props: ServicePortRenderProps) => {
      const active =
        servingCurl?.namespace === props.namespace &&
        servingCurl?.serviceName === props.serviceName &&
        servingCurl?.port === props.port &&
        !servingCurl.closing
      return (
        <>
          {showServingCurl &&
            isHttpishPort(props.port, props.name, props.appProtocol, props.protocol) && (
              <CurlButton
                active={active}
                onClick={() => {
                  if (active) closeServingCurl()
                  else
                    setServingCurl({
                      namespace: props.namespace,
                      serviceName: props.serviceName,
                      port: props.port,
                      closing: false,
                    })
                }}
              />
            )}
          {showServingPortForward && (
            <PortForwardInlineButton
              namespace={props.namespace}
              serviceName={props.serviceName}
              port={props.port}
              protocol={props.protocol}
            />
          )}
        </>
      )
    },
    [closeServingCurl, servingCurl, showServingCurl, showServingPortForward],
  )
  const renderServicePortPanel = useCallback(
    (props: ServicePortRenderProps) => {
      const active =
        servingCurl?.namespace === props.namespace &&
        servingCurl?.serviceName === props.serviceName &&
        servingCurl?.port === props.port
      return active ? (
        <CurlPanel
          namespace={props.namespace}
          serviceName={props.serviceName}
          port={props.port}
          initialScheme={defaultScheme(props.port, props.name, props.appProtocol)}
          initialPath={defaultPathForPort(props.port, props.name, props.appProtocol)}
          open={!servingCurl.closing}
          onClose={closeServingCurl}
        />
      ) : null
    },
    [closeServingCurl, servingCurl],
  )
  const updateResource = useUpdateResource()
  const previewResources = usePreviewResources()
  const baseActionsBarProps = useActionsBarProps(
    apiKind,
    namespace,
    name,
    effectiveGroup,
    !resourceLoading && Boolean(resource),
  )
  const desktopDownload = useDesktopDownload()

  // Live Operational Issues for this resource. Fetched here (not inside the lead
  // render-prop) so the count also gates `hasOperationalIssues` — which tells the
  // renderers to suppress their own status-derived problems and avoid duplicates.
  // Keyed on the stable API kind+group (same inputs as the resource fetch above),
  // NOT the manifest-derived ones: deriving kind/group from the loaded resource
  // would flip the query key when the manifest arrives, drop liveIssues, and flash
  // the renderer banners. The backend canonicalizes a plural kind via discovery,
  // so using the normalized API kind resolves direct links and app navigation alike.
  const { data: liveIssues, isPending: issuesPending } = useResourceIssues(apiKind, rest.group, namespace, name)
  const { data: auditFindings } = useResourceAudit(apiKind, namespace, name)
  const hasOperationalIssues = Boolean(liveIssues?.length)
  const {
    onCompareTo,
    onCompareAcrossClusters,
    picker: comparePicker,
  } = useCompareLauncher({
    kind: apiKind,
    namespace,
    name,
    // Prefer the URL-supplied group so Compare works even before the resource
    // fetch completes; fall back to the derived group for callers that don't
    // pass one.
    group: effectiveGroup,
  })
  const handleUpdateResource = useCallback(
    async (params: Parameters<typeof updateResource.mutateAsync>[0]) => {
      await updateResource.mutateAsync(params)
    },
    [updateResource],
  )
  const handlePreviewResource = useCallback(
    async (params: Parameters<typeof previewResources.mutateAsync>[0]) =>
      previewResources.mutateAsync(params),
    [previewResources],
  )

  const navigateRouter = useNavigate()
  const handleOpenGitOpsResource = useCallback(
    (ref: GitOpsOwnerRef) => {
      const params = new URLSearchParams()
      const namespaces = searchParams.get('namespaces')
      if (namespaces) params.set('namespaces', namespaces)
      navigateRouter({
        pathname: gitOpsRouteForOwner(ref),
        search: params.toString(),
      })
    },
    [navigateRouter, searchParams],
  )
  const handleNavigateGitOpsPath = useCallback(
    (path: string) => navigateRouter(path),
    [navigateRouter],
  )
  // Drawer TraceSummary CTA → open the full resource view ON the Reachability tab.
  // The generic onExpand navigates to the workload path but drops the query, so we
  // navigate directly to that path WITH ?tab=reachability - the deeplink the
  // expanded view reads to land on the right tab.
  const openReachability = useCallback(() => {
    const base = buildWorkloadPath({ kind: kindProp, namespace, name, group: rest.group })
    // autorun=1 tells the expanded view to immediately run the (proxy) reachability
    // test - the operator clicked "Open Reachability" to SEE results, not to land on
    // a static page and click Run again.
    navigateRouter(`${base}${base.includes('?') ? '&' : '?'}tab=reachability&autorun=1`)
  }, [navigateRouter, kindProp, namespace, name, rest.group])
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
  const loadImagesWithTargetOwnership = useCallback(
    async (params: { kind: string; namespace: string; name: string }) => {
      const request = ++imageOwnershipRequestRef.current
      const inventory = await baseActionsBarProps.onLoadImages!(params)
      const targetDiffers =
        inventory.target.resource.toLowerCase() !== params.kind.toLowerCase() ||
        inventory.target.namespace !== params.namespace ||
        inventory.target.name !== params.name
      if (!targetDiffers) {
        if (request === imageOwnershipRequestRef.current) {
          setImageTargetOwnership(null)
        }
        return inventory
      }

      const fetchRelationships = (
        resource: string,
        targetNamespace: string,
        targetName: string,
        group?: string,
      ) =>
        queryClient.fetchQuery({
          queryKey: ['resource', resource, targetNamespace, targetName, group],
          queryFn: () =>
            fetchResourceWithRelationships<Record<string, unknown>>(
              resource,
              targetNamespace,
              targetName,
              group,
            ),
          staleTime: 0,
        })
      const response = await fetchRelationships(
        inventory.target.resource,
        inventory.target.namespace,
        inventory.target.name,
        inventory.target.group,
      )
      const inheritedRef = findInheritedGitOpsLookupRef(
        response.relationships,
        gitOpsOwnerFromRelationships(response.relationships),
        {
          kind: inventory.target.kind,
          namespace: inventory.target.namespace,
          name: inventory.target.name,
          group: inventory.target.group,
        },
      )
      const inheritedResponse = inheritedRef
        ? await fetchRelationships(
            kindToPluralWithGroup(inheritedRef.kind, inheritedRef.group ?? ''),
            inheritedRef.namespace,
            inheritedRef.name,
            inheritedRef.group,
          )
        : undefined
      if (request === imageOwnershipRequestRef.current) {
        setImageTargetOwnership({
          root: {
            resource: params.kind,
            namespace: params.namespace,
            name: params.name,
          },
          target: inventory.target,
          response,
          inheritedResponse,
        })
      }
      return inventory
    },
    [baseActionsBarProps.onLoadImages, queryClient, setImageTargetOwnership],
  )
  const imageGitopsOwner = activeImageTargetOwnership ? targetGitopsOwner : gitopsOwner
  const imageHelmOwner = activeImageTargetOwnership ? targetHelmOwner : helmOwner
  const imageGitopsOwnerData = activeImageTargetOwnership
    ? targetGitopsOwnerQuery.data
    : gitopsOwnerQuery.data
  const managedImageSources = useMemo<ManagedImageSource[] | undefined>(() => {
    if (!activeImageTargetOwnership && inheritedGitOpsLookupRef && (inheritedGitOpsResponse.isPending || inheritedGitOpsResponse.isError)) {
      return undefined
    }
    const sources: ManagedImageSource[] = []
    if (imageGitopsOwner) {
      sources.push({
        type: 'GitOps',
        label: imageGitopsOwner.namespace
          ? `${imageGitopsOwner.namespace}/${imageGitopsOwner.name}`
          : imageGitopsOwner.name,
        onOpen: imageGitopsOwnerData
          ? () => handleOpenGitOpsResource(imageGitopsOwner)
          : undefined,
      })
    }
    if (imageHelmOwner) {
      sources.push({
        type: 'Helm',
        label: `${imageHelmOwner.namespace}/${imageHelmOwner.name}`,
        onOpen: () => handleOpenHelmRelease(imageHelmOwner),
      })
    }
    return sources
  }, [
    handleOpenGitOpsResource,
    handleOpenHelmRelease,
    activeImageTargetOwnership,
    imageGitopsOwner,
    imageGitopsOwnerData,
    imageHelmOwner,
    inheritedGitOpsLookupRef,
    inheritedGitOpsResponse.isError,
    inheritedGitOpsResponse.isPending,
  ])
  const actionsBarProps = useMemo(
    () => ({
      ...baseActionsBarProps,
      onLoadImages: baseActionsBarProps.onLoadImages
        ? loadImagesWithTargetOwnership
        : undefined,
      onCompareTo,
      onCompareAcrossClusters,
      managedImageSources,
    }),
    [
      baseActionsBarProps,
      loadImagesWithTargetOwnership,
      managedImageSources,
      onCompareTo,
      onCompareAcrossClusters,
    ],
  )
  const handleOpenApplication = useCallback(
    (appKey: string) => {
      const params = new URLSearchParams()
      const namespaces = new Set(
        (searchParams.get('namespaces') ?? '')
          .split(',')
          .map((ns) => ns.trim())
          .filter(Boolean),
      )
      if (ownershipContext?.application?.key === appKey && ownershipContext.workload.namespace) {
        namespaces.add(ownershipContext.workload.namespace)
      }
      if (namespaces.size > 0) params.set('namespaces', Array.from(namespaces).join(','))
      params.set('app', appKey)
      navigateRouter({ pathname: '/applications', search: params.toString() })
    },
    [navigateRouter, ownershipContext, searchParams],
  )

  // Duplicate dialog
  const [duplicateDialogOpen, setDuplicateDialogOpen] = useState(false)
  const [duplicateYaml, setDuplicateYaml] = useState('')

  const handleDuplicate = useCallback(
    (params: { kind: string; namespace: string; name: string; yaml: string }) => {
      setDuplicateYaml(cleanYamlForDuplicate(params.yaml))
      setDuplicateDialogOpen(true)
    },
    [],
  )

  const supportsWorkloadPods = ['deployments', 'statefulsets', 'daemonsets', 'rollouts'].includes(apiKind)
  const workloadPodsQuery = useWorkloadPods(supportsWorkloadPods ? apiKind : '', namespace, name)
  const workloadAwaitsCapacity =
    karpenter?.state === 'available' &&
    (workloadPodsQuery.data?.pods ?? []).some(workloadPodAwaitsScheduling)
  const servingRefs = useMemo(() => collectServingRefs(relationships), [relationships])
  const servingQueries = useQueries({
    queries: servingRefs.map((ref) => {
      const pluralKind = kindToPluralWithGroup(ref.kind, ref.group ?? '')
      const ns = ref.namespace || '_'
      const params = new URLSearchParams()
      if (ref.group) params.set('group', ref.group)
      const queryString = params.toString()
      return {
        queryKey: ['resource', pluralKind, ref.namespace, ref.name, ref.group],
        queryFn: () =>
          fetchJSON<any>(
            `/resources/${pluralKind}/${ns}/${ref.name}${queryString ? `?${queryString}` : ''}`,
          ),
        enabled: expanded && Boolean(ref.kind && ref.name),
        staleTime: 30000,
      }
    }),
  })
  const servingResources = useMemo<ServingResourceDetail[]>(
    () =>
      servingRefs.map((ref, index) => {
        const query = servingQueries[index]
        const data = query?.data?.resource ?? query?.data
        return {
          ref,
          resource: data,
          loading: query?.isLoading ?? false,
          error: (query?.error as Error | null) ?? null,
        }
      }),
    [servingRefs, servingQueries],
  )

  return (
    <>
      <BaseWorkloadView
        kind={apiKind}
        namespace={namespace}
        name={name}
        expanded={expanded}
        {...rest}
        group={effectiveGroup}
        // Data
        resource={resource}
        relationships={relationships}
        ownershipContext={ownershipContext}
        onOpenApplication={handleOpenApplication}
        certificateInfo={certificateInfo}
        hpaDiagnosis={hpaDiagnosis}
        workloadPods={supportsWorkloadPods ? workloadPodsQuery.data?.pods : undefined}
        onEvaluateCapacity={
          workloadAwaitsCapacity
            ? () =>
                navigateRouter(
                  `/capacity/demand?owner=${encodeURIComponent(`${namespace}/${pluralToKind(apiKind)}/${name}`)}`,
                )
            : undefined
        }
        workloadPodsLoading={supportsWorkloadPods ? workloadPodsQuery.isLoading : false}
        workloadPodsError={supportsWorkloadPods ? (workloadPodsQuery.error as Error | null) : null}
        servingResources={servingResources}
        renderServicePortAction={renderServicePortAction}
        renderServicePortPanel={renderServicePortPanel}
        isLoading={resourceLoading}
        resourceError={resourceError}
        refetch={refetchResourceAndRuns}
        // Timeline
        allEvents={allEvents}
        relatedTimelineEvents={relatedTimelineEvents}
        eventsLoading={eventsLoading || (batchExecution && batchRunsQuery.isLoading)}
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
        onPreviewResource={features?.yamlReview ? handlePreviewResource : undefined}
        isPreviewingResource={previewResources.isPending}
        previewResourceError={previewResources.error?.message ?? null}
        yamlSchemaLoader={features?.yamlSchemas ? fetchYamlSchemas : undefined}
        // Tab state (URL-synced)
        activeTab={migratedTab}
        onTabChange={handleTabChange}
        // Render props
        renderLogsTab={(props) => (
          <LogsTabContent
            {...props}
            group={effectiveGroup}
            selectedRunKey={selectedRunKey}
            onSelectRun={handleSelectedRunChange}
          />
        )}
        renderExpandedOverview={({ kind: k, apiKind, namespace: ns, name: n, resource: res }) =>
          BATCH_EXECUTION_KINDS.has(k) &&
          (k !== 'Job' || isCoreBatchJob(apiKind, effectiveGroup)) &&
          res ? (
            <BatchExecutionFullscreen
              kind={k}
              apiKind={apiKind}
              namespace={ns}
              name={n}
              resource={res}
              selectedRunKey={selectedRunKey}
              canViewLogs={baseActionsBarProps.canViewLogs}
              onSelectRun={handleSelectedRunChange}
              onSwitchToLogs={() => handleTabChange('logs')}
              onSwitchToTimeline={() => handleTabChange('timeline')}
              onNavigateToResource={rest.onNavigateToResource}
            />
          ) : null
        }
        renderRelatedYaml={(ref) => (
          <RelatedResourceYaml key={`${ref.kind}/${ref.namespace}/${ref.name}`} target={ref} />
        )}
        renderMetricsTab={({ kind, namespace: ns, name: n }) => (
          <MetricsTabContent
            kind={kind}
            namespace={ns}
            name={n}
            resource={resource}
            expanded={expanded}
          />
        )}
        renderCostTab={({ kind, namespace: ns, name: n }) => (
          <div className="space-y-4">
            <RightsizingPanel kind={kind} namespace={ns} name={n} />
            <WorkloadCostTab kind={kind} namespace={ns} name={n} />
          </div>
        )}
        reachableVia={servingServices}
        renderDiagnoseTab={({ namespace: ns, name: n }) => (
          // Key by resource identity so the tab REMOUNTS on A→B navigation - without
          // this, the first render with B's identity but A's still-set probeTrace
          // paints A's verdict under B for one commit before the reset effect runs.
          //
          // The kind comes from the loaded resource, else the URL's own singular
          // PascalCase kind - NEVER the base view's re-derived one: before the
          // resource (or CRD discovery) loads, de-pluralizing "httproutes" guessed
          // "Httproute", which fired the auto-probes once under the guessed kind
          // and again under the real one - two live probe runs per tab open.
          <WorkloadReachabilityTab
            key={`${resource?.kind ?? kindProp}/${ns}/${n}`}
            kind={resource?.kind ?? kindProp}
            namespace={ns}
            name={n}
            group={effectiveGroup}
            servingServices={servingServices}
            onNavigate={rest.onNavigateToResource}
          />
        )}
        isMetricsAvailable={(kind, res) =>
          isPrometheusSupported(kind) && !(kind === 'Pod' && res?.status?.phase === 'Pending')
        }
        isCostAvailable={(kind) => isOpenCostWorkloadKind(kind)}
        onDuplicate={handleDuplicate}
        onDownload={desktopDownload}
        actionsBarProps={actionsBarProps}
        rendererOverrides={rendererOverrides}
        renderOverviewExtra={({ kind: k, namespace: ns, name: n, group: g, context }) => {
          // Network entry kinds (Service/Ingress/Route/Gateway) ARE the diagnosis
          // target: DiagnoseInlineSection renders in the drawer, no hint. Workload
          // kinds lead with DiagnoseFromWorkloadHint so a developer who opened a
          // failing workload finds the diagnose entry. The group disambiguates a CRD
          // sharing a core kind name (Knative Service, Istio Gateway).
          const isNetworkKind = isDiagnoseKind(k, g)
          const diagnoseInline = context === 'drawer' && isNetworkKind ? (
            <DiagnoseInlineSection kind={k} namespace={ns} name={n} group={g} onOpenReachability={openReachability} />
          ) : null
          const diagnoseHint = context === 'drawer' && !isNetworkKind ? (
            <DiagnoseFromWorkloadHint services={servingServices} onOpenReachability={openReachability} />
          ) : null
          return (
          <>
            {diagnoseInline}
            {diagnoseHint}
            <FluxSourceConsumersSection kind={k} namespace={ns} name={n} />
            <AuditOverviewSection
              findings={auditFindings ?? []}
              onViewAll={() => navigateRouter('/checks')}
            />
          </>
          )
        }}
        renderOverviewLead={() => (
          <ResourceIssuesSection
            issues={liveIssues}
            subjectResource={{ kind: apiKind, namespace, name, group: rest.group }}
            onResourceClick={
              rest.onNavigateToResource
                ? (ref) =>
                    rest.onNavigateToResource?.({
                      kind: kindToPluralWithGroup(ref.kind, ref.group ?? ''),
                      namespace: ref.namespace ?? '',
                      name: ref.name,
                      group: ref.group ?? '',
                    })
                : undefined
            }
          />
        )}
        hasOperationalIssues={hasOperationalIssues}
        operationalIssuesPending={issuesPending}
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
          const group = apiVersionToGroup(result.apiVersion)
          rest.onNavigateToResource?.({
            kind: kindToPluralWithGroup(result.kind, group),
            namespace: result.namespace,
            name: result.name,
            group,
          })
        }}
      />
      {comparePicker}
    </>
  )
}

function collectServingRefs(relationships: Relationships | undefined): ResourceRef[] {
  if (!relationships) return []
  return dedupeRefs([
    ...(relationships.services ?? []),
    ...(relationships.ingresses ?? []),
    ...(relationships.gateways ?? []),
    ...(relationships.routes ?? []),
  ])
}

function dedupeRefs(refs: ResourceRef[]): ResourceRef[] {
  const seen = new Set<string>()
  return refs.filter((ref) => {
    const key = `${ref.kind}/${ref.namespace}/${ref.name}/${ref.group ?? ''}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function resolveGitOpsOwner(
  owner: GitOpsOwnerRef | null,
  argoApplications: any[] | undefined,
): GitOpsOwnerRef | null {
  if (!owner || owner.namespace || owner.tool !== 'argocd') return owner
  const matches = (argoApplications ?? []).filter((app) => app?.metadata?.name === owner.name)
  if (matches.length !== 1) return owner
  const namespace = matches[0]?.metadata?.namespace
  return namespace ? { ...owner, namespace } : owner
}

export function findInheritedGitOpsLookupRef(
  relationships: Relationships | undefined,
  directOwner: GitOpsOwnerRef | null,
  current: ResourceRef,
): ResourceRef | null {
  if (directOwner) return null
  const inheritedManagerRefs = (relationships?.managedBy ?? []).filter(
    (ref) => !gitOpsOwnerFromRelationships({ managedBy: [ref] }) && !isNativeHelmManager(ref),
  )
  const candidates = [
    relationships?.deployment,
    ...inheritedManagerRefs,
    relationships?.owner,
  ].filter(Boolean) as ResourceRef[]

  return candidates.find((ref) => !isCurrentResource(ref, current)) ?? null
}

const POD_OWNERSHIP_WORKLOAD_KINDS = new Set([
  'deployments',
  'statefulsets',
  'daemonsets',
  'jobs',
  'cronjobs',
  'rollouts',
])

function podWorkloadOwnerFromRelationships(
  kind: string,
  namespace: string,
  relationships: Relationships | undefined,
  resource: any,
): ResourceRef | null {
  if (kindToPlural(kind).toLowerCase() !== 'pods') return null

  if (relationships?.deployment) return relationships.deployment

  const managedWorkload = relationships?.managedBy?.find((ref) => isPodOwnershipWorkloadRef(ref))
  if (managedWorkload) return managedWorkload

  if (relationships?.owner && isPodOwnershipWorkloadRef(relationships.owner))
    return relationships.owner

  return podControllerOwnerFromMetadata(namespace, resource)
}

function isPodOwnershipWorkloadRef(ref: ResourceRef): boolean {
  return POD_OWNERSHIP_WORKLOAD_KINDS.has(kindToPlural(ref.kind).toLowerCase())
}

function podControllerOwnerFromMetadata(namespace: string, resource: any): ResourceRef | null {
  const ownerRefs = resource?.metadata?.ownerReferences
  if (!Array.isArray(ownerRefs)) return null
  const owner = ownerRefs.find((ref) => ref?.controller === true) ?? null
  if (!owner?.kind || !owner?.name) return null
  if (
    !isPodOwnershipWorkloadRef({
      kind: owner.kind,
      namespace,
      name: owner.name,
    })
  )
    return null
  return {
    kind: owner.kind,
    namespace,
    name: owner.name,
    group: apiVersionToGroup(owner.apiVersion),
  }
}

function buildPodOwnershipContext(
  workload: ResourceRef | null,
  apps: AppRow[] | undefined,
): ResourceOwnershipContext | undefined {
  if (!workload) return undefined
  const matches = (apps ?? []).filter((app) =>
    (app.workloads ?? []).some((candidate) => sameWorkload(candidate, workload)),
  )
  const app = matches.length === 1 ? matches[0] : null
  return {
    workload,
    application: app ? { key: app.key, name: app.name } : undefined,
  }
}

function sameWorkload(
  candidate: { kind: string; namespace: string; name: string },
  workload: ResourceRef,
): boolean {
  return (
    kindToPlural(candidate.kind).toLowerCase() === kindToPlural(workload.kind).toLowerCase() &&
    candidate.namespace === workload.namespace &&
    candidate.name === workload.name
  )
}

function nativeHelmOwnerFromRelationships(
  relationships: Relationships | undefined,
  fallbackNamespace: string,
): HelmOwnerRef | null {
  const ref = relationships?.managedBy?.[0]
  if (!ref || !isNativeHelmManager(ref)) return null
  return {
    namespace: ref.namespace || fallbackNamespace,
    name: ref.name,
  }
}

function isCurrentResource(ref: ResourceRef, current: ResourceRef): boolean {
  return (
    kindToPluralWithGroup(ref.kind, ref.group ?? '') ===
      kindToPluralWithGroup(current.kind, current.group ?? '') &&
    ref.namespace === current.namespace &&
    ref.name === current.name &&
    (ref.group ?? '') === (current.group ?? '')
  )
}

function isNativeHelmManager(ref: ResourceRef): boolean {
  return ref.kind === 'HelmRelease' && ref.group !== 'helm.toolkit.fluxcd.io'
}

function describeGitOpsOwnerSource(owner: GitOpsOwnerRef | null, resource: any): string | null {
  if (!owner || !resource) return null
  const labels = resource.metadata?.labels ?? {}
  const annotations = resource.metadata?.annotations ?? {}

  if (owner.tool === 'fluxcd') {
    const nameKey =
      owner.kind === 'helmreleases'
        ? 'helm.toolkit.fluxcd.io/name'
        : 'kustomize.toolkit.fluxcd.io/name'
    const nsKey =
      owner.kind === 'helmreleases'
        ? 'helm.toolkit.fluxcd.io/namespace'
        : 'kustomize.toolkit.fluxcd.io/namespace'
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

const WORKLOAD_LOG_KINDS = new Set(['Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'Workflow'])
const SCHEDULED_LOG_KINDS = new Set([
  'CronJob',
  'CronWorkflow',
  'WorkflowTemplate',
  'ClusterWorkflowTemplate',
  'ScaledJob',
])

function LogsTabContent({
  kind,
  apiKind,
  group,
  namespace,
  name,
  resource,
  pods,
  selectedPod,
  onSelectPod,
  initialContainer,
  onConsumeInitialContainer,
  selectedRunKey,
  onSelectRun,
}: {
  kind: string
  apiKind: string
  group?: string
  namespace: string
  name: string
  resource: any
  pods: ResourceRef[]
  selectedPod: string | null
  onSelectPod: (name: string | null) => void
  initialContainer: string | null
  onConsumeInitialContainer: () => void
  selectedRunKey: string
  onSelectRun: (runKey: string) => void
}) {
  if (SCHEDULED_LOG_KINDS.has(kind)) {
    return (
      <div className="h-full">
        <ScheduledWorkloadLogsViewer
          kind={apiKind}
          namespace={namespace}
          name={name}
          selectedRunKey={selectedRunKey}
          onSelectRun={onSelectRun}
        />
      </div>
    )
  }

  // Workload kinds with stable pod selectors use the aggregated workload logs viewer
  if (WORKLOAD_LOG_KINDS.has(kind) && (kind !== 'Job' || isCoreBatchJob(apiKind, group))) {
    return (
      <div className="h-full">
        <WorkloadLogsViewer
          kind={apiKind}
          namespace={namespace}
          name={name}
          autoStream={shouldAutoStreamWorkloadLogs(kind, resource)}
        />
      </div>
    )
  }

  // Individual Pod — use LogsViewer with container list from resource data
  if (kind === 'Pod') {
    return (
      <PodLogsTab
        namespace={namespace}
        name={name}
        resource={resource}
        initialContainer={initialContainer}
        onConsumeInitialContainer={onConsumeInitialContainer}
      />
    )
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

function shouldAutoStreamWorkloadLogs(kind: string, resource: any): boolean {
  if (kind === 'Job') {
    return (resource?.status?.active ?? 0) > 0
  }
  if (kind === 'Workflow') {
    const phase = resource?.status?.phase
    return phase === 'Running' || phase === 'Pending'
  }
  return true
}

function PodLogsTab({
  namespace,
  name,
  resource,
  initialContainer,
  onConsumeInitialContainer,
}: {
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

function MultiPodLogsTab({
  pods,
  namespace,
  selectedPod,
  onSelectPod,
  initialContainer,
}: {
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

  const podNamespace = pods.find((p) => p.name === selectedPod)?.namespace || namespace

  // Fetch container list for the selected pod
  const { data: logsData } = usePodLogs(podNamespace, selectedPod || '', {
    tailLines: 1,
  })
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
          {pods.map((pod) => (
            <button
              key={pod.name}
              onClick={() => onSelectPod(pod.name)}
              className={clsx(
                'px-3 py-1.5 text-sm rounded-lg whitespace-nowrap transition-colors',
                selectedPod === pod.name
                  ? 'bg-blue-500 text-theme-text-primary'
                  : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover',
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

function AuditOverviewSection({
  findings,
  onViewAll,
}: {
  findings: AuditFinding[]
  onViewAll: () => void
}) {
  if (findings.length === 0) return null
  return <AuditAlerts findings={findings} onViewAll={onViewAll} />
}

// DiagnoseFromWorkloadHint surfaces the Diagnose entry point for app
// developers who open a failing workload (Deployment, StatefulSet, Pod,
// etc.) and don't know that diagnosis lives on the fronting Service.
// Without this card the operator has to navigate the topology themselves
// to find the right Service. Renders only when the workload has at
// least one Service in its relationships; on isolated workloads (no
// Service in front) the card stays hidden because no entry-point exists
// to link to. Services are handed down from the drawer's own relationships
// fetch rather than re-fetched here: re-fetching by singular Kind missed
// both the plural and the API group, which 404s for a CRD whose plural
// collides with another CRD's (CNPG vs CAPI `clusters`).
function DiagnoseFromWorkloadHint({
  services,
  onOpenReachability,
}: {
  services: ResourceRef[]
  onOpenReachability: () => void
}) {
  if (services.length === 0) return null
  return (
    <Section title="Diagnose network path">
      <div className="flex items-start gap-2 text-xs text-theme-text-secondary">
        <Stethoscope className="w-4 h-4 mt-0.5 shrink-0 text-theme-text-tertiary" aria-hidden />
        <div className="flex-1 min-w-0">
          {services.length === 1 ? 'Exposed by Service ' : 'Exposed by Services '}
          {services.map((svc, i) => (
            <span key={`${svc.namespace ?? ''}/${svc.name}`}>
              <span className="font-medium text-theme-text-primary">{svc.name}</span>
              {i < services.length - 1 ? <span className="text-theme-text-tertiary">{', '}</span> : null}
            </span>
          ))}
          {/* Opens the workload's OWN Reachability tab, which traces that
              Service in place. Linking to the Service instead made the operator
              navigate away and restart the investigation somewhere else. */}
          <span className="text-theme-text-tertiary">. </span>
          <button type="button" onClick={onOpenReachability} className="font-medium text-accent-text hover:underline">
            Trace the traffic path →
          </button>
        </div>
      </div>
    </Section>
  )
}

// DiagnoseTabContent binds the static-trace polling hook + the one-shot probe
// fetch to the presentational ReachabilityView. Probe results are held in local
// state and keep showing until the resource or the tested path changes - or
// until a static poll reports that the underlying cluster state changed since
// the test ran, at which point the staleness mask below drops them (with a
// notice) so a frozen snapshot is never presented as current truth.
// useProbeRun owns the one-shot reachability-probe state for a focused
// resource. A per-resource token guards every async resolution: navigating to
// a different resource (props change) does NOT unmount this component, so an
// aliveRef-only guard would let an in-flight probe for resource A resolve and
// paint A's verdict onto resource B - a confident-wrong verdict on the wrong
// resource. The token is bumped on every resource change (and unmount); a late
// then/catch whose captured token no longer matches simply bails.
function useProbeRun(kind: string, namespace: string, name: string) {
  const [probeTrace, setProbeTrace] = useState<NetworkTrace | undefined>(undefined)
  const [probeError, setProbeError] = useState<Error | null>(null)
  const [running, setRunning] = useState(false)
  const tokenRef = useRef(0)
  // runningRef mirrors `running` so runProbes' in-flight guard reads LIVE state, not a
  // value captured at render. applyProbePath calls resetProbe() then runProbes()
  // synchronously; the closed-over `running` would still be true and bail the new run,
  // whereas resetProbe clears runningRef immediately so the guard sees it's free.
  const runningRef = useRef(false)
  useEffect(() => {
    tokenRef.current += 1
    setProbeTrace(undefined)
    setProbeError(null)
    runningRef.current = false
    setRunning(false)
  }, [kind, namespace, name])
  useEffect(() => () => { tokenRef.current += 1 }, [])
  const runProbes = useCallback((path?: string) => {
    if (runningRef.current) return
    const token = tokenRef.current
    runningRef.current = true
    setRunning(true)
    setProbeError(null)
    fetchTraceWithProbes(kind, namespace, name, path)
      .then((result) => { if (tokenRef.current === token) setProbeTrace(result) })
      .catch((e: unknown) => { if (tokenRef.current === token) setProbeError(e instanceof Error ? e : new Error(String(e))) })
      .finally(() => { if (tokenRef.current === token) { runningRef.current = false; setRunning(false) } })
  }, [kind, namespace, name])
  // resetProbe drops the current probe trace AND bumps the token so an in-flight
  // probe for the OLD path can't resolve and repaint. Used when the tested path
  // changes: the prior path's verdict must not linger under the new path's label.
  const resetProbe = useCallback(() => {
    tokenRef.current += 1
    setProbeTrace(undefined)
    setProbeError(null)
    runningRef.current = false
    setRunning(false)
  }, [])
  return { probeTrace, probeError, running, runProbes, resetProbe }
}

// useInClusterTest runs the WHOLE-subject in-cluster test in one click. The server
// runs every route's live probe and folds them in via the canonical
// trace.ApplyInClusterResults, returning the FINALIZED trace - so this hook just
// displays it, never reimplementing a weaker merge that could falsely confirm a
// sibling route or leave stale diagnosis/netpol beside a live-verified route. The
// result resets whenever the base trace changes (a fresh proxy run), so stale
// in-cluster data never lingers.
function useInClusterTest(base: NetworkTrace | undefined, kind: string, namespace: string, name: string) {
  const [running, setRunning] = useState(false)
  // undefined = the capability SSAR has not answered yet. Starting at `false`
  // made the in-cluster capsule claim "not permitted" for the first frames of
  // every load - a definitive denial for a check still in flight, which is the
  // one thing this view must never do. Consumers gate on `=== false`, so an
  // unknown answer now reads as unknown.
  const [allowed, setAllowed] = useState<boolean | undefined>(undefined)
  const [cap, setCap] = useState<InClusterCapability | undefined>(undefined)
  const [merged, setMerged] = useState<NetworkTrace | undefined>(undefined)
  const [error, setError] = useState<string | undefined>(undefined)
  const [fallback, setFallback] = useState<string | undefined>(undefined)
  const [partial, setPartial] = useState(false)
  const [evidenceOnly, setEvidenceOnly] = useState(false)
  const [evidence, setEvidence] = useState<string | undefined>(undefined)
  // Per-resource token: bumped whenever the base trace changes (navigation / fresh
  // proxy run) and on unmount, so an in-flight run that resolves AFTER the operator
  // navigated to another resource never paints resource A's verdict onto resource B.
  const tokenRef = useRef(0)
  useEffect(() => {
    let alive = true
    // A fetch ERROR is not a denial: `false` is reserved for the server
    // definitively saying no, and consumers render `=== false` as "not
    // permitted". A 503 while the cache warms or a network blip must leave the
    // answer unknown, not paint a permanent RBAC denial.
    fetchInClusterCapability(kind, namespace, name).then((c) => { if (alive) { setAllowed(!!c.allowed); setCap(c) } }).catch(() => {})
    return () => { alive = false }
  }, [kind, namespace, name])
  useEffect(() => {
    tokenRef.current++
    setMerged(undefined); setError(undefined); setFallback(undefined); setPartial(false); setEvidenceOnly(false); setEvidence(undefined); setRunning(false)
  }, [base])
  // Invalidate an in-flight run on unmount. Kept as a separate []-effect: bumping
  // the ref in a deps-driven cleanup trips react-hooks/exhaustive-deps (the ref
  // changes between re-runs), and the base-change case is already covered by the
  // body bump above.
  useEffect(() => () => { tokenRef.current++ }, [])
  const run = useCallback(async (path: string = '/') => {
    if (!base || running) return
    const token = tokenRef.current
    setRunning(true)
    setError(undefined)
    setFallback(undefined)
    try {
      const { trace, inClusterTests } = await runInClusterMerged(kind, namespace, name, path)
      if (tokenRef.current !== token) return // navigated away / base changed mid-run
      setMerged(trace)
      // A per-route in-cluster failure (Job couldn't start, timed out, RBAC) comes
      // back as HTTP 200 with an error status + a fallback command inside
      // inClusterTests. A row can also carry a message with NO fallback command -
      // nothing eligible to test (e.g. a Gateway subject), the per-call pod cap,
      // or an exhausted request time budget. Surface both - otherwise the run
      // vanishes as if nothing happened - and mark whether OTHER rows still
      // produced results, so the banner can say "partially completed" instead of
      // the false "couldn't run" over a merged trace that folded live results.
      const summary = summarizeInClusterTests(inClusterTests)
      setError(summary.error)
      setFallback(summary.fallback)
      setPartial(summary.partial)
      setEvidenceOnly(summary.evidenceOnly)
      setEvidence(summary.evidence)
    } catch (e: unknown) {
      if (tokenRef.current !== token) return
      setError(e instanceof Error ? e.message : String(e))
      setFallback(undefined)
      setPartial(false)
      setEvidenceOnly(false)
      setEvidence(undefined)
    } finally {
      if (tokenRef.current === token) setRunning(false)
    }
  }, [base, running, kind, namespace, name])
  return { run, running, allowed, cap, merged, error, fallback, partial, evidenceOnly, evidence }
}

/**
 * Reachability for a resource that has no address of its own.
 *
 * A Deployment cannot be dialled; what can be dialled is the Service in front of
 * it, so this traces that Service while keeping the workload as the thing the
 * operator opened. Previously a workload offered only a link to the Service,
 * which meant navigating away and restarting the investigation - the workload
 * half of "Service/workload reachability" did not exist as a journey.
 *
 * An entry kind traces itself and never reaches the picker below.
 */
function WorkloadReachabilityTab({
  kind,
  namespace,
  name,
  group,
  servingServices,
  onNavigate,
}: {
  kind: string
  namespace: string
  name: string
  group?: string
  servingServices: ResourceRef[]
  onNavigate?: NavigateToResource
}) {
  const tracesItself = isDiagnoseKind(kind, group)
  const [pick, setPick] = useState(0)
  if (tracesItself) {
    return <DiagnoseTabContent kind={kind} namespace={namespace} name={name} onNavigate={onNavigate} />
  }
  const svc = servingServices[pick] ?? servingServices[0]
  if (!svc) return null
  return (
    <div className="flex h-full min-h-0 flex-col gap-2">
      {/* Only when there is a CHOICE to make. A full-width band to say "this
          Deployment has no address" spent a row of height on a sentence, and the
          workload now names the Pods at the end of the path in the graph itself -
          which is where the reader is already looking. */}
      {servingServices.length > 1 && (
        <div className="flex flex-wrap items-center gap-2 px-1 text-[11.5px] text-theme-text-tertiary">
          <span>Reached through:</span>
          {servingServices.map((s, i) => (
            <button
              key={`${s.namespace ?? ''}/${s.name}`}
              type="button"
              onClick={() => setPick(i)}
              className={`rounded px-1.5 py-0.5 font-mono text-[11px] ${
                i === pick ? 'bg-theme-hover font-semibold text-theme-text-primary' : 'text-accent-text hover:underline'
              }`}
            >
              {s.name}
            </button>
          ))}
        </div>
      )}
      <div className="min-h-0 flex-1">
        <DiagnoseTabContent
          key={`${svc.namespace ?? namespace}/${svc.name}`}
          kind="Service"
          namespace={svc.namespace ?? namespace}
          name={svc.name}
          onNavigate={onNavigate}
        />
      </div>
    </div>
  )
}

function DiagnoseTabContent({
  kind,
  namespace,
  name,
  onNavigate,
}: {
  kind: string
  namespace: string
  name: string
  onNavigate?: NavigateToResource
}) {
  const { data: staticTrace, isLoading, error, refetch } = useTrace(kind, namespace, name)
  const { probeTrace, probeError, running, runProbes, resetProbe } = useProbeRun(kind, namespace, name)
  const baseTrace = probeTrace ?? staticTrace
  const { run: runInClusterTest, running: inClusterRunning, allowed: inClusterAllowed, cap: inClusterCap, merged: inClusterTrace, error: inClusterError, fallback: inClusterFallback, partial: inClusterPartial, evidenceOnly: inClusterEvidenceOnly, evidence: inClusterEvidenceNote } = useInClusterTest(baseTrace, kind, namespace, name)
  // Gate the merged in-cluster trace on a live probe trace: when the staleness
  // mask below clears probeTrace, useInClusterTest resets `merged` one effect
  // pass later - without the gate that pass would still paint the stale
  // in-cluster verdict for a frame.
  const displayTrace = (probeTrace !== undefined ? inClusterTrace : undefined) ?? baseTrace
  // Staleness mask for the probe snapshot: probe/in-cluster results are a
  // snapshot of the moment they ran, while the static trace keeps polling
  // underneath. The baseline is the fingerprint of THE RESULT TRACE ITSELF:
  // a ?probe=true response (and the in-cluster merged trace) embeds the same
  // static-derived content the probes actually ran against, and
  // traceFingerprint covers only probe-invariant fields - so it exists at the
  // instant of adoption (no race with the separate static query: a probe that
  // beats the static fetch, or a cluster change mid-run, can't baseline on
  // post-change state). When a later static poll fingerprints DIFFERENTLY,
  // drop the snapshot (the view falls back to the live static trace) and say
  // why - keeping the old verdict up would present stale evidence as current
  // truth. An adopted in-cluster merged trace re-baselines: it reflects the
  // (possibly newer) state its run observed.
  const staticFp = useMemo(() => (staticTrace ? traceFingerprint(staticTrace) : undefined), [staticTrace])
  // Only a FULLY-BUILT, healthy static poll is trustworthy staleness evidence. A
  // budget-timeout partial (fewer hops) or a transient pod-lister failure
  // (endpointSource=unknown) still returns HTTP 200 but fingerprints DIFFERENTLY -
  // comparing against it would drop a good snapshot and cry "cluster changed" though
  // nothing did. Skip the comparison for such polls; a poll we couldn't fully build
  // is not evidence of change.
  const staticPollDegraded = useMemo(() => (staticTrace ? staticPollUnreliable(staticTrace) : false), [staticTrace])
  const resultsTrace = inClusterTrace ?? probeTrace
  const snapshotFp = useRef<string | undefined>(undefined)
  const snapshotOf = useRef<NetworkTrace | undefined>(undefined)
  const [clusterChanged, setClusterChanged] = useState(false)
  useEffect(() => { setClusterChanged(false) }, [kind, namespace, name])
  useEffect(() => {
    if (resultsTrace === undefined) {
      snapshotOf.current = undefined
      snapshotFp.current = undefined
      return
    }
    if (resultsTrace !== snapshotOf.current) {
      // A run just adopted results: baseline on the state embedded in the
      // result itself.
      snapshotOf.current = resultsTrace
      snapshotFp.current = traceFingerprint(resultsTrace)
      setClusterChanged(false)
      return
    }
    if (staticFp !== undefined && staticFp !== snapshotFp.current) {
      // A partial/degraded poll fingerprints differently for reasons that aren't a
      // real cluster change - keep the snapshot rather than fire a false banner.
      if (staticPollDegraded) return
      resetProbe()
      setClusterChanged(true)
    }
  }, [resultsTrace, staticFp, staticPollDegraded, resetProbe])
  // Consent gate for the mutating in-cluster test: it spawns a Job/pod, so the first
  // run per cluster asks the operator to confirm - naming the cluster it lands in -
  // unless they chose "don't ask again" for that cluster. Permission is already
  // enforced upstream (the button only renders when the capability SSAR allows), so
  // this is a safety confirm, not an authz check.
  const [pendingRunPath, setPendingRunPath] = useState<string | null>(null)
  const requestInClusterRun = useCallback((path: string) => {
    if (inClusterConsentGiven(inClusterCap?.clusterKey)) runInClusterTest(path)
    else setPendingRunPath(path)
  }, [inClusterCap, runInClusterTest])
  const confirmInClusterRun = useCallback(() => {
    const path = pendingRunPath ?? '/'
    setPendingRunPath(null)
    runInClusterTest(path)
  }, [pendingRunPath, runInClusterTest])
  // The HTTP path the probes request (default "/"). Editable via the "what to
  // test" menu; the buttons re-run with the current path, the form applies a new
  // one. Applies to BOTH the reachability and in-cluster tests.
  const [probePath, setProbePath] = useState('/')
  // When the tested path actually changes, drop the prior path's probe trace
  // BEFORE re-running so displayTrace falls back to the static trace (config-only)
  // during the probe window - never the old path's verdict under the new label.
  const applyProbePath = useCallback((p: string) => {
    // Reset synchronously BEFORE runProbes - a reset inside the setProbePath updater is
    // deferred to the next render, so it would bump the token AFTER runProbes captured
    // the old one (dropping the result) and leave the stale running-guard set.
    if (p !== probePath) resetProbe()
    setProbePath(p)
    runProbes(p)
  }, [probePath, runProbes, resetProbe])
  // Bump a nonce every time a run produces a new result object (proxy or in-cluster),
  // so the view can flash "updated just now" even when the values are unchanged.
  // testedAt dates the displayed results ("tested HH:MM:SS") so even a kept
  // snapshot is honestly dated; it clears when the results do.
  const [runNonce, setRunNonce] = useState(0)
  const [testedAt, setTestedAt] = useState<Date | undefined>(undefined)
  // Bump ONLY when a NEW result is adopted (a fresh object reference), never when a
  // result is DROPPED. Without this, a mask-driven resetProbe() clears probeTrace
  // while inClusterTrace is still (transiently) truthy, so `probeTrace||inClusterTrace`
  // stays true and the effect would re-date testedAt + flash "updated just now" at the
  // exact moment results are being thrown away, beside "Cluster state changed".
  const prevResultsRef = useRef<{ probe?: NetworkTrace; inCluster?: NetworkTrace }>({})
  useEffect(() => {
    const prev = prevResultsRef.current
    const adopted = (probeTrace !== undefined && probeTrace !== prev.probe) || (inClusterTrace !== undefined && inClusterTrace !== prev.inCluster)
    prevResultsRef.current = { probe: probeTrace, inCluster: inClusterTrace }
    if (adopted) {
      setRunNonce((n) => n + 1)
      setTestedAt(new Date())
    } else if (!probeTrace && !inClusterTrace) {
      setTestedAt(undefined)
    }
  }, [probeTrace, inClusterTrace])
  // Auto-run the (proxy) reachability test once per resource when the tab loads - the
  // operator opened Reachability to SEE results, not a static page they must click Run
  // on. Only the proxy test auto-runs; the in-cluster test (which spawns a Job) stays a
  // deliberate manual action. Keyed by resource so navigating to a new one re-runs; the
  // stale ?autorun=1 deeplink flag (now redundant) is stripped to keep the URL clean.
  const [searchParams, setSearchParams] = useSearchParams()
  const autorunKey = useRef<string>('')
  useEffect(() => {
    const key = `${kind}/${namespace}/${name}`
    if (autorunKey.current === key) return
    autorunKey.current = key
    runProbes()
    if (searchParams.get('autorun')) {
      const next = new URLSearchParams(searchParams)
      next.delete('autorun')
      setSearchParams(next, { replace: true })
    }
  }, [kind, namespace, name, runProbes, searchParams, setSearchParams])
  // What the in-cluster job will ACTUALLY send, mirroring the server: a bare "/"
  // is the untouched default, so each route keeps its OWN declared path
  // (internal/server/reachability_run.go) - anything else overrides every route.
  // The consent dialog previously showed a single "GET /", which was wrong in
  // precisely the default case, and counted only `routes`, omitting the declared
  // paths that landed in `notTested`.
  const pendingPath = pendingRunPath ?? probePath
  const override = pendingPath && pendingPath !== '/' ? pendingPath : ''
  const consentRequests = useMemo(
    () => consentRequestRows(displayTrace?.routes ?? [], override),
    [displayTrace, override],
  )
  const consentUntestedCount = useMemo(() => {
    const derivable = new Set((displayTrace?.routes ?? []).filter((r) => r.inClusterRequest).map((r) => r.route))
    const declared = new Set<string>([
      ...(displayTrace?.routes ?? []).map((r) => r.route),
      ...(displayTrace?.notTested ?? []).map((s) => s.route).filter((x): x is string => !!x),
    ])
    return [...declared].filter((r) => !derivable.has(r)).length
  }, [displayTrace])
  // The full-view Reachability tab fills its pane: the shell supplies the
  // padding and this stays a full-height flex column so the board's three
  // panes can scroll independently instead of the whole page scrolling.
  return (
    <div className="flex h-full min-h-0 flex-col">
      <ReachabilityView
        trace={displayTrace}
        isLoading={isLoading || running}
        error={error as Error | null}
        probeError={probeError}
        onRefresh={() => void refetch()}
        probeRequested={running}
        probed={probeTrace !== undefined || inClusterTrace !== undefined}
        onRunProbes={() => runProbes(probePath)}
        onRunInCluster={() => requestInClusterRun(probePath)}
        inClusterRunning={inClusterRunning}
        // Permission only - never fold readiness in: `allowed && !probeTrace`
        // evaluated to false, which every consumer renders as a definitive
        // "not permitted". A missing base trace (probe failed, re-run in
        // flight, staleness mask) is not a denial; run() already no-ops
        // harmlessly until the base exists.
        inClusterAllowed={inClusterAllowed}
        inClusterDeniedReason={inClusterCap?.reason}
        inClusterError={inClusterError}
        inClusterPartial={inClusterPartial}
        inClusterFallback={inClusterFallback}
        inClusterEvidenceOnly={inClusterEvidenceOnly}
        inClusterEvidenceNote={inClusterEvidenceNote}
        probePath={probePath}
        onApplyProbePath={applyProbePath}
        runNonce={runNonce}
        testedAt={testedAt}
        clusterChangedSinceTest={clusterChanged}
        onNavigateToResource={
          onNavigate
            ? (ref) =>
                onNavigate({
                  kind: kindToPluralWithGroup(ref.kind, ref.group ?? ''),
                  namespace: ref.namespace ?? '',
                  name: ref.name,
                  group: ref.group ?? '',
                })
            : undefined
        }
      />
      <InClusterConsentDialog
        open={pendingRunPath !== null}
        cluster={inClusterCap?.cluster}
        clusterKey={inClusterCap?.clusterKey}
        namespace={inClusterCap?.namespace ?? namespace}
        requests={consentRequests}
        untestedCount={consentUntestedCount}
        maxProbes={inClusterCap?.maxProbes}
        onClose={() => setPendingRunPath(null)}
        onConfirm={confirmInClusterRun}
      />
    </div>
  )
}

// DiagnoseInlineSection is the drawer-mode glance: a passive TraceSummary, NOT the
// full panel. The full route matrix, active probes, per-route localization, path
// topology and the in-cluster test all live on the Reachability tab - reached via
// the "Open Reachability →" CTA, which deeplinks to ?tab=reachability and expands.
// The useTrace hook is gated on enabled so non-traceable kinds short-circuit.
function DiagnoseInlineSection({ kind, namespace, name, group, onOpenReachability }: { kind: string; namespace: string; name: string; group?: string; onOpenReachability: () => void }) {
  // Gate on (kind, group) so a CRD sharing a core kind name (Knative Service,
  // Istio Gateway) never enables the trace against the wrong (core) object.
  const enabled = isDiagnoseKind(kind, group)
  const { data: staticTrace } = useTrace(kind, namespace, name, enabled)
  if (!enabled || !staticTrace) return null
  // Wrap in Section so the surface matches the rest of the drawer (Ports /
  // Selector / Related Resources / Metadata) - chevron, title, divider.
  return (
    <Section title="Reachability · Network Path">
      <TraceSummary trace={staticTrace} onOpenReachability={onOpenReachability} />
    </Section>
  )
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
function FluxSourceConsumersSection({
  kind,
  namespace,
  name,
}: {
  kind: string
  namespace: string
  name: string
}) {
  // The inner WorkloadView de-pluralizes the URL's plural form, which gives
  // "Gitrepository" (single-uppercase) rather than the wire-correct
  // "GitRepository" — so we match lowercase. spec.sourceRef.kind on consumers
  // is always wire-correct, so we look that up separately.
  const sourceKind = FLUX_SOURCE_KIND_BY_LOWER.get(kind.toLowerCase()) ?? null
  if (!sourceKind) return null
  return <FluxSourceConsumersInner sourceKind={sourceKind} namespace={namespace} name={name} />
}

function FluxSourceConsumersInner({
  sourceKind,
  namespace,
  name,
}: {
  sourceKind: string
  namespace: string
  name: string
}) {
  const navigate = useNavigate()
  const { data: kustomizations } = useResources<any>(
    'kustomizations',
    undefined,
    'kustomize.toolkit.fluxcd.io',
  )
  const { data: helmReleases } = useResources<any>(
    'helmreleases',
    undefined,
    'helm.toolkit.fluxcd.io',
  )

  const consumers: Array<{
    kind: 'Kustomization' | 'HelmRelease'
    namespace: string
    name: string
    plural: string
  }> = []
  for (const k of kustomizations ?? []) {
    const ref = k?.spec?.sourceRef ?? {}
    const refNs = ref.namespace || k?.metadata?.namespace
    if (ref.kind === sourceKind && ref.name === name && refNs === namespace) {
      consumers.push({
        kind: 'Kustomization',
        namespace: k.metadata.namespace,
        name: k.metadata.name,
        plural: 'kustomizations',
      })
    }
  }
  for (const h of helmReleases ?? []) {
    const ref = h?.spec?.chart?.spec?.sourceRef ?? {}
    const refNs = ref.namespace || h?.metadata?.namespace
    if (ref.kind === sourceKind && ref.name === name && refNs === namespace) {
      consumers.push({
        kind: 'HelmRelease',
        namespace: h.metadata.namespace,
        name: h.metadata.name,
        plural: 'helmreleases',
      })
    }
  }

  if (consumers.length === 0) {
    return (
      <section className="rounded-lg border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
        <h3 className="mb-2 text-sm font-semibold text-theme-text-primary">Consumed by</h3>
        <p className="text-xs text-theme-text-tertiary">
          No Kustomization or HelmRelease references this source.
        </p>
      </section>
    )
  }

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
      <h3 className="mb-3 text-sm font-semibold text-theme-text-primary">
        Consumed by ({consumers.length})
      </h3>
      <div className="flex flex-wrap gap-1.5">
        {consumers.map((c) => (
          <Tooltip
            key={`${c.kind}/${c.namespace}/${c.name}`}
            content={`${c.kind} ${c.namespace}/${c.name}`}
          >
            <button
              onClick={() =>
                navigate(
                  `/gitops/detail/${c.plural}/${encodeURIComponent(c.namespace)}/${encodeURIComponent(c.name)}`,
                )
              }
              className="inline-flex items-center gap-1.5 rounded border border-theme-border bg-theme-surface px-1.5 py-0.5 text-[11px] text-theme-text-secondary hover:border-skyhook-500/60 hover:text-skyhook-500 transition-colors"
            >
              <span className="text-theme-text-tertiary">
                {c.kind === 'HelmRelease' ? 'HR' : 'K'}
              </span>
              <span>
                {c.namespace}/{c.name}
              </span>
            </button>
          </Tooltip>
        ))}
      </div>
    </section>
  )
}

// Drawer mode: single chart + category tabs (compact for ~500px width).
// Full-screen mode: multi-chart grid so CPU + Memory + Network can be
// compared side-by-side without tab switching.
function MetricsTabContent({
  kind,
  namespace,
  name,
  resource,
  expanded,
}: {
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
          <PrometheusChartsGrid kind={kind} namespace={namespace} name={name} resource={resource} />
        </div>
      </div>
    )
  }

  // Drawer fallback: single chart with tabs + restart lane below. The chart's
  // time-range selector is mirrored to the restart lane so they stay aligned.
  return <DrawerMetricsContent kind={kind} namespace={namespace} name={name} resource={resource} />
}

function DrawerMetricsContent({
  kind,
  namespace,
  name,
  resource,
}: {
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
        <PrometheusCharts
          kind={kind}
          namespace={namespace}
          name={name}
          showEmptyState
          resource={resource}
          onTimeRangeChange={setChartRange}
        />
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
function RelatedResourceYaml({
  target,
}: {
  target: { kind: string; namespace: string; name: string; group?: string }
}) {
  const { data, isLoading, error } = useResource<any>(
    kindToPluralWithGroup(target.kind, target.group ?? ''),
    target.namespace,
    target.name,
    target.group,
  )
  const [copied, setCopied] = useState(false)
  const handleCopy = useCallback((text: string) => {
    navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }, [])
  if (!data)
    return <FetchResult loading={isLoading} error={error as Error | null} className="h-32" />
  return (
    <EditableYamlView
      resource={{
        kind: kindToPluralWithGroup(target.kind, target.group ?? ''),
        namespace: target.namespace,
        name: target.name,
        group: target.group,
      }}
      data={data}
      onCopy={handleCopy}
      copied={copied}
      readOnly
    />
  )
}
