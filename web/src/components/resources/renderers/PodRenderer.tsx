import { PodRenderer as BasePodRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/PodRenderer'
import type { CopyHandler } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import type { PodEnvironmentRevealResponse, ResolvedEnvFrom } from '@skyhook-io/k8s-ui'
import { useNavigate } from 'react-router-dom'
import { useOpenTerminal, useOpenLogs } from '../../dock'
import { useCapabilitiesContext, useNamespacedCapabilities, useIsLocalDeployment } from '../../../contexts/CapabilitiesContext'
import { getVisibleLiveMetrics, isLiveMetricsUnavailable, shouldFetchLiveMetrics, usePodEnvironment, usePodMetrics, usePodMetricsHistory, usePrometheusResourceMetrics, usePrometheusStatus, useResourceEvents, useResources, useRevealPodEnvironment } from '../../../api/client'
import { toSecretsStoreRead } from '../../../api/secretsStore'
import {
  SECRETS_STORE_GROUP, SECRET_PROVIDER_CLASSES, SECRET_PROVIDER_CLASS_POD_STATUSES, getSecretsStoreVolumes,
} from '@skyhook-io/k8s-ui/components/resources/resource-utils-secrets-store'
import { useRBACSubject } from '../../../api/rbac'
import { usePolicyResource } from '../../../api/policy'
import { podLimitRangeNames, useNamespaceLimitRanges } from '../../../api/quotas'
import { podAwaitsScheduling } from '../../capacity/podDemandGate'
import { PortForwardInlineButton } from '../../portforward/PortForwardButton'
import { ImageFilesystemModal } from '../ImageFilesystemModal'
import { PodFilesystemModal } from '../PodFilesystemModal'

interface PodRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  onOpenLogs?: (podName: string, containerName: string) => void
  resolvedEnvFrom?: ResolvedEnvFrom
}

export function PodRenderer({ data, onCopy, copied, onNavigate, onOpenLogs, resolvedEnvFrom }: PodRendererProps) {
  const namespace = data.metadata?.namespace
  const podName = data.metadata?.name
  const isTerminal = data.status?.phase === 'Succeeded' || data.status?.phase === 'Failed'
  const environmentEnabled = [...(data.spec?.initContainers ?? []), ...(data.spec?.containers ?? [])]
    .some((container: any) => container.env?.length > 0 || container.envFrom?.length > 0)
  const environmentQuery = usePodEnvironment(namespace ?? '', podName ?? '', environmentEnabled)
  const revealEnvironment = useRevealPodEnvironment()

  const openTerminal = useOpenTerminal()
  const openLogsPanel = useOpenLogs()
  const navigate = useNavigate()

  // Unscheduled pod on a Karpenter cluster -> bridge into the Capacity Demand
  // view (the purpose-built surface for "why is this pod pending").
  const karpenterAvailable = useCapabilitiesContext().karpenter?.state === 'available'
  const awaitsScheduling = podAwaitsScheduling(data)

  // Capabilities (namespace-scoped: re-checks RBAC if globally denied)
  const { canExec, canViewLogs, canPortForward } = useNamespacedCapabilities(namespace)
  // Show the port-forward affordance for a live forward (local + RBAC) OR when
  // not local — in-cluster/Cloud surfaces a copy-paste kubectl command instead.
  // The button itself picks live vs. copy-command based on deployment mode.
  const isLocal = useIsLocalDeployment()
  const showPortForward = canPortForward || !isLocal

  // Metrics
  const metricsHistoryQuery = usePodMetricsHistory(namespace, podName)
  const { data: metricsHistory } = metricsHistoryQuery
  const historyMetricsUnavailable = metricsHistory?.metricsUnavailable === true
  const liveMetricsEnabled = !isTerminal && shouldFetchLiveMetrics(metricsHistoryQuery.isFetched || metricsHistoryQuery.isError, historyMetricsUnavailable)
  const { data: metrics } = usePodMetrics(namespace, podName, { enabled: liveMetricsEnabled })
  const metricsUnavailable = historyMetricsUnavailable || isLiveMetricsUnavailable(liveMetricsEnabled, metrics)
  const visibleMetrics = getVisibleLiveMetrics(liveMetricsEnabled, metricsUnavailable, metrics)

  // Hide metrics-server section when Prometheus has CPU data
  const { data: prometheusStatus } = usePrometheusStatus()
  const prometheusConnected = prometheusStatus?.connected === true
  const { data: prometheusCPU, isLoading: prometheusCPULoading, error: prometheusCPUError } = usePrometheusResourceMetrics(
    'Pod', namespace ?? '', podName ?? '', 'cpu', '1h', prometheusConnected,
  )
  const prometheusHasCPU = !prometheusCPUError && (prometheusCPU?.result?.series?.some(
    s => s.dataPoints?.length > 0,
  ) ?? false)
  const hideMetricsServer = prometheusHasCPU || (prometheusConnected && prometheusCPULoading)

  // RBAC reverse-lookup for the Pod's ServiceAccount. Defaults to "default" —
  // that's the SA every Pod uses when spec.serviceAccountName is unset, which
  // is itself a useful signal (operators often don't realize "default" still
  // has whatever permissions the namespace's defaults grant).
  const saName = data.spec?.serviceAccountName || 'default'
  const { data: rbacData, isLoading: rbacLoading, error: rbacError } = useRBACSubject(
    'ServiceAccount', namespace ?? '', saName, !!namespace,
  )

  const { data: policyData, isLoading: policyLoading, error: policyError } = usePolicyResource(
    'pods', namespace ?? '', podName, !!namespace && !!podName,
  )

  const { data: limitRanges, isSuccess: limitRangesReady } = useNamespaceLimitRanges(namespace ?? '', !!namespace)

  // Secrets Store CSI: read only for a Pod that mounts such a volume.
  const usesSecretsStore = !!namespace && getSecretsStoreVolumes(data).length > 0
  const providerClasses = useResources<any>(SECRET_PROVIDER_CLASSES, namespace, SECRETS_STORE_GROUP, { enabled: usesSecretsStore })
  const providerStatuses = useResources<any>(SECRET_PROVIDER_CLASS_POD_STATUSES, namespace, SECRETS_STORE_GROUP, { enabled: usesSecretsStore })
  // The query the detail view's Events section already runs, so it shares that cache.
  const podEvents = useResourceEvents(usesSecretsStore ? 'pods' : '', namespace ?? '', podName ?? '', '')

  return (
    <BasePodRenderer
      data={data}
      onCopy={onCopy}
      copied={copied}
      onNavigate={onNavigate}
      onOpenLogs={onOpenLogs}
      onEvaluateCapacity={
        karpenterAvailable && awaitsScheduling
          ? () =>
              navigate(
                `/capacity/demand?pod=${encodeURIComponent(`${data.metadata?.namespace ?? ''}/${data.metadata?.name ?? ''}`)}`,
              )
          : undefined
      }
      resolvedEnvFrom={resolvedEnvFrom}
      environment={environmentQuery.data}
      environmentLoading={environmentQuery.isLoading}
      environmentError={environmentQuery.error as Error | null}
      onRevealEnvironment={(container, variable): Promise<PodEnvironmentRevealResponse> => revealEnvironment.mutateAsync({
        namespace: namespace ?? '',
        podName: podName ?? '',
        container,
        variable,
      })}
      rbacData={rbacData ?? null}
      rbacLoading={rbacLoading}
      rbacError={rbacError as Error | null}
      policyData={policyData ?? null}
      policyLoading={policyLoading}
      policyError={policyError as Error | null}
      namespaceLimitRangeNames={limitRangesReady ? podLimitRangeNames(limitRanges) : undefined}
      secretsStore={usesSecretsStore ? {
        classes: toSecretsStoreRead({ data: providerClasses.data, coverage: providerClasses.coverage, error: providerClasses.error, isLoading: providerClasses.isLoading }),
        statuses: toSecretsStoreRead({ data: providerStatuses.data, coverage: providerStatuses.coverage, error: providerStatuses.error, isLoading: providerStatuses.isLoading }),
        events: podEvents.k8sError ? undefined : podEvents.k8sEvents,
      } : undefined}
      canExec={canExec}
      canViewLogs={canViewLogs}
      canPortForward={showPortForward}
      onOpenTerminal={(params) => openTerminal(params)}
      onOpenLogsPanel={(params) => openLogsPanel(params)}
      renderPortAction={({ namespace: ns, podName: pod, port, protocol, disabled }) => (
        <PortForwardInlineButton
          namespace={ns}
          podName={pod}
          port={port}
          protocol={protocol}
          disabled={disabled}
        />
      )}
      metrics={visibleMetrics}
      metricsHistory={metricsHistory}
      metricsHistoryLoading={metricsHistoryQuery.isLoading}
      metricsHistoryError={metricsHistoryQuery.error}
      metricsUnavailable={metricsUnavailable}
      hideMetricsServer={hideMetricsServer}
      renderImageBrowser={({ image, namespace: ns, podName: pod, pullSecrets, onClose, onSwitchToPodFiles }) => (
        <ImageFilesystemModal
          open={true}
          onClose={onClose}
          image={image}
          namespace={ns}
          podName={pod}
          pullSecrets={pullSecrets}
          onSwitchToPodFiles={onSwitchToPodFiles}
        />
      )}
      renderPodBrowser={({ namespace: ns, podName: pod, containers, initialContainer, onClose, onSwitchToImageFiles }) => (
        <PodFilesystemModal
          open={true}
          onClose={onClose}
          namespace={ns}
          podName={pod}
          containers={containers}
          initialContainer={initialContainer}
          onSwitchToImageFiles={onSwitchToImageFiles}
        />
      )}
    />
  )
}
