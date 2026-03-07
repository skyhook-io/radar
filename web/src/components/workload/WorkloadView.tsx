import { useMemo, useEffect, useCallback } from 'react'
import { useNavigate, useLocation, useSearchParams } from 'react-router-dom'
import { clsx } from 'clsx'
import { Terminal } from 'lucide-react'
import {
  WorkloadView as BaseWorkloadView,
} from '@skyhook-io/k8s-ui'
import type { SelectedResource, ResourceRef } from '../../types'
import type { NavigateToResource } from '../../utils/navigation'
import {
  useChanges, useResourceWithRelationships, usePodLogs, useTopology, useUpdateResource,
  useDeleteResource, useTriggerCronJob, useSuspendCronJob, useResumeCronJob,
  useRestartWorkload, useWorkloadRevisions, useRollbackWorkload,
  useFluxReconcile, useFluxSyncWithSource, useFluxSuspend, useFluxResume,
  useArgoSync, useArgoRefresh, useArgoSuspend, useArgoResume,
} from '../../api/client'
import { PrometheusCharts, isPrometheusSupported } from '../resource/PrometheusCharts'
import { WorkloadLogsViewer } from '../logs/WorkloadLogsViewer'
import { LogsViewer } from '../logs/LogsViewer'
import { useCanUpdateSecrets, useCanExec, useCanViewLogs, useCanPortForward } from '../../contexts/CapabilitiesContext'
import { useOpenTerminal, useOpenLogs, useOpenWorkloadLogs } from '../dock'
import { PortForwardButton } from '../portforward/PortForwardButton'
import { useToast } from '../ui/Toast'

type TabType = 'overview' | 'timeline' | 'logs' | 'metrics' | 'yaml'

// ============================================================================
// ROUTE WRAPPER — parses kind/ns/name from URL
// ============================================================================

interface WorkloadViewRouteProps {
  onNavigateToResource?: NavigateToResource
}

export function WorkloadViewRoute({ onNavigateToResource }: WorkloadViewRouteProps) {
  const location = useLocation()
  const navigate = useNavigate()

  // Parse /workload/:kind/:ns/:name from pathname
  const parts = location.pathname.replace(/^\//, '').split('/')
  // parts[0] = 'workload', parts[1] = kind, parts[2] = ns, parts[3+] = name (may contain slashes)
  const kind = parts[1] || ''
  const namespace = parts[2] || ''
  const name = parts.slice(3).join('/') || ''

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
    // Navigate to another workload view
    navigate(`/workload/${resource.kind}/${resource.namespace}/${resource.name}`)
  }, [navigate])

  return (
    <WorkloadView
      kind={kind}
      namespace={namespace}
      name={name}
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
  const canExec = useCanExec()
  const canViewLogs = useCanViewLogs()
  const canPortForward = useCanPortForward()

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

  return {
    canExec,
    canViewLogs,
    canPortForward,
    onOpenTerminal: openTerminal,
    onOpenLogs: openLogs,
    onOpenWorkloadLogs: openWorkloadLogs,
    onCopyCommand: (text: string, message: string, event: React.MouseEvent) => showCopied(text, message, event),
    renderPortForward: ({ type, namespace: ns, name: n, className }: { type: 'pod' | 'service'; namespace: string; name: string; className?: string }) => (
      <PortForwardButton type={type} namespace={ns} name={n} className={className} />
    ),
    onDelete: (params: any, callbacks?: any) => deleteMutation.mutate(params, { onSuccess: callbacks?.onSuccess }),
    isDeleting: deleteMutation.isPending,
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

  // Fetch topology for hierarchy building (only when expanded)
  const { data: topology } = useTopology([namespace], 'resources', { enabled: expanded })

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
  const actionsBarProps = useActionsBarProps(kindProp, namespace, name)

  const handleUpdateResource = useCallback(async (params: { kind: string; namespace: string; name: string; yaml: string }) => {
    await updateResource.mutateAsync(params)
  }, [updateResource])

  return (
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
      // Capabilities
      canUpdateSecrets={canUpdateSecrets}
      // Mutations
      onUpdateResource={handleUpdateResource}
      isUpdatingResource={updateResource.isPending}
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
      actionsBarProps={actionsBarProps}
    />
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
