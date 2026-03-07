import { PodRenderer as BasePodRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/PodRenderer'
import type { CopyHandler } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { useOpenTerminal, useOpenLogs } from '../../dock'
import { useCanExec, useCanViewLogs, useCanPortForward } from '../../../contexts/CapabilitiesContext'
import { usePodMetrics, usePodMetricsHistory, usePrometheusResourceMetrics, usePrometheusStatus } from '../../../api/client'
import { PortForwardInlineButton } from '../../portforward/PortForwardButton'
import { ImageFilesystemModal } from '../ImageFilesystemModal'
import { PodFilesystemModal } from '../PodFilesystemModal'

interface PodRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  onOpenLogs?: (podName: string, containerName: string) => void
}

export function PodRenderer({ data, onCopy, copied, onNavigate, onOpenLogs }: PodRendererProps) {
  const namespace = data.metadata?.namespace
  const podName = data.metadata?.name

  const openTerminal = useOpenTerminal()
  const openLogsPanel = useOpenLogs()

  // Capabilities
  const canExec = useCanExec()
  const canViewLogs = useCanViewLogs()
  const canPortForward = useCanPortForward()

  // Metrics
  const { data: metrics } = usePodMetrics(namespace, podName)
  const { data: metricsHistory } = usePodMetricsHistory(namespace, podName)

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

  return (
    <BasePodRenderer
      data={data}
      onCopy={onCopy}
      copied={copied}
      onNavigate={onNavigate}
      onOpenLogs={onOpenLogs}
      canExec={canExec}
      canViewLogs={canViewLogs}
      canPortForward={canPortForward}
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
      metrics={metrics}
      metricsHistory={metricsHistory}
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
