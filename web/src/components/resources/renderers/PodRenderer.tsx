import { PodRenderer as BasePodRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/PodRenderer'
import type { CopyHandler } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import type { ResolvedEnvFrom } from '@skyhook-io/k8s-ui'
import { useOpenTerminal, useOpenLogs } from '../../dock'
import { useNamespacedCapabilities, useIsLocalDeployment } from '../../../contexts/CapabilitiesContext'
import { getVisibleLiveMetrics, isLiveMetricsUnavailable, shouldFetchLiveMetrics, usePodMetrics, usePodMetricsHistory, usePrometheusResourceMetrics, usePrometheusStatus } from '../../../api/client'
import { useRBACSubject } from '../../../api/rbac'
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

  const openTerminal = useOpenTerminal()
  const openLogsPanel = useOpenLogs()

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
  const liveMetricsEnabled = shouldFetchLiveMetrics(metricsHistoryQuery.isFetched || metricsHistoryQuery.isError, historyMetricsUnavailable)
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

  return (
    <BasePodRenderer
      data={data}
      onCopy={onCopy}
      copied={copied}
      onNavigate={onNavigate}
      onOpenLogs={onOpenLogs}
      resolvedEnvFrom={resolvedEnvFrom}
      rbacData={rbacData ?? null}
      rbacLoading={rbacLoading}
      rbacError={rbacError as Error | null}
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
