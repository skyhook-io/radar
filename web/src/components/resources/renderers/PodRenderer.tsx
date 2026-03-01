import { useState } from 'react'
import { Server, HardDrive, Terminal as TerminalIcon, FileText, Activity, CirclePlay, FolderOpen } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, CopyHandler, AlertBanner, ResourceLink } from '../drawer-components'
import { formatResources, formatDuration } from '../resource-utils'
import { PortForwardInlineButton } from '../../portforward/PortForwardButton'
import { useOpenTerminal, useOpenLogs } from '../../dock'
import { Tooltip } from '../../ui/Tooltip'
import { useCanExec, useCanViewLogs, useCanPortForward } from '../../../contexts/CapabilitiesContext'
import { usePodMetrics, usePodMetricsHistory, usePrometheusResourceMetrics, usePrometheusStatus } from '../../../api/client'
import { MetricsChart } from '../../ui/MetricsChart'
import { ImageFilesystemModal } from '../ImageFilesystemModal'
import { PodFilesystemModal } from '../PodFilesystemModal'

function parseValidDate(dateStr: string): Date | null {
  const d = new Date(dateStr)
  return isNaN(d.getTime()) ? null : d
}

function formatMsAgo(dateStr: string): string | null {
  const d = parseValidDate(dateStr)
  if (!d) return null
  const ms = Date.now() - d.getTime()
  if (ms < 60000) return 'just now'
  if (ms < 3600000) return `${Math.floor(ms / 60000)}m ago`
  if (ms < 86400000) return `${Math.floor(ms / 3600000)}h ago`
  return `${Math.floor(ms / 86400000)}d ago`
}

function getRestartRecency(finishedAt: string | undefined, restarts: number): { color: string; label: string | null } {
  const d = finishedAt ? parseValidDate(finishedAt) : null
  const ms = d ? Date.now() - d.getTime() : null

  if (ms !== null) {
    let color = 'text-theme-text-tertiary'
    if (ms < 10 * 60 * 1000) color = 'text-red-400'
    else if (ms < 60 * 60 * 1000) color = 'text-yellow-400'
    const label = formatMsAgo(finishedAt!)
    return { color, label }
  }

  return { color: restarts > 5 ? 'text-red-400' : 'text-yellow-400', label: null }
}

function getRunDuration(startedAt: string, finishedAt: string): string | null {
  const start = parseValidDate(startedAt)
  const end = parseValidDate(finishedAt)
  if (!start || !end) return null
  const dur = end.getTime() - start.getTime()
  if (dur <= 0) return null
  return formatDuration(dur, true)
}

interface PodRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

// Extract problems from pod status and conditions
function getPodProblems(data: any): string[] {
  const problems: string[] = []
  const phase = data.status?.phase
  const conditions = data.status?.conditions || []
  const containerStatuses = data.status?.containerStatuses || []
  const initContainerStatuses = data.status?.initContainerStatuses || []

  // Check phase
  if (phase === 'Failed' || phase === 'Unknown') {
    problems.push(`Pod is in ${phase} state`)
  }

  // Check conditions for scheduling/init issues
  for (const cond of conditions) {
    if (cond.status === 'False') {
      if (cond.type === 'PodScheduled' && cond.reason) {
        problems.push(`Scheduling: ${cond.reason}${cond.message ? ' - ' + cond.message : ''}`)
      } else if (cond.type === 'Initialized' && cond.message) {
        problems.push(`Init failed: ${cond.message}`)
      } else if (cond.type === 'ContainersReady' && cond.reason === 'ContainersNotReady') {
        // Will be covered by container status details
      } else if (cond.message) {
        problems.push(`${cond.type}: ${cond.message}`)
      }
    }
  }

  // Check init container failures
  for (const initStatus of initContainerStatuses) {
    if (initStatus.state?.waiting?.reason && initStatus.state.waiting.reason !== 'PodInitializing') {
      problems.push(`Init container "${initStatus.name}": ${initStatus.state.waiting.reason}`)
    }
    if (initStatus.state?.terminated?.exitCode && initStatus.state.terminated.exitCode !== 0) {
      problems.push(`Init container "${initStatus.name}" failed with exit code ${initStatus.state.terminated.exitCode}`)
    }
  }

  // Check container issues (most important)
  for (const status of containerStatuses) {
    const waiting = status.state?.waiting
    const terminated = status.state?.terminated

    if (waiting?.reason && waiting.reason !== 'ContainerCreating') {
      const msg = `Container "${status.name}": ${waiting.reason}`
      if (waiting.reason === 'ImagePullBackOff' || waiting.reason === 'ErrImagePull') {
        problems.push(msg + (waiting.message ? ` - ${waiting.message.slice(0, 100)}` : ''))
      } else if (waiting.reason === 'CrashLoopBackOff') {
        problems.push(msg + ' (container keeps crashing)')
      } else {
        problems.push(msg)
      }
    }

    if (terminated?.reason === 'OOMKilled') {
      problems.push(`Container "${status.name}" was OOMKilled - increase memory limit`)
    } else if (terminated?.exitCode && terminated.exitCode !== 0) {
      problems.push(`Container "${status.name}" exited with code ${terminated.exitCode}`)
    }
  }

  return problems
}

export function PodRenderer({ data, onCopy, copied, onNavigate }: PodRendererProps) {
  const containerStatuses = data.status?.containerStatuses || []
  const containers = data.spec?.containers || []
  const initContainers = data.spec?.initContainers || []
  const initContainerStatuses = data.status?.initContainerStatuses || []

  const namespace = data.metadata?.namespace
  const podName = data.metadata?.name
  const isRunning = data.status?.phase === 'Running'

  const openTerminal = useOpenTerminal()
  const openLogs = useOpenLogs()

  // Check capabilities
  const canExec = useCanExec()
  const canViewLogs = useCanViewLogs()
  const canPortForward = useCanPortForward()

  // Fetch pod metrics (current and historical)
  const { data: metrics } = usePodMetrics(namespace, podName)
  const { data: metricsHistory } = usePodMetricsHistory(namespace, podName)

  // Hide metrics-server section only when Prometheus actually has CPU data for this pod.
  // Also hide while the CPU probe is loading (when Prometheus is connected) to avoid a brief flash.
  const { data: prometheusStatus } = usePrometheusStatus()
  const prometheusConnected = prometheusStatus?.connected === true
  const { data: prometheusCPU, isLoading: prometheusCPULoading, error: prometheusCPUError } = usePrometheusResourceMetrics(
    'Pod', namespace ?? '', podName ?? '', 'cpu', '1h', prometheusConnected,
  )
  const prometheusHasCPU = !prometheusCPUError && (prometheusCPU?.result?.series?.some(
    s => s.dataPoints?.length > 0,
  ) ?? false)
  const hideMetricsServer = prometheusHasCPU || (prometheusConnected && prometheusCPULoading)

  // Check for problems
  const problems = getPodProblems(data)
  const hasProblems = problems.length > 0

  // Image filesystem modal state
  const [selectedImage, setSelectedImage] = useState<string | null>(null)
  const imagePullSecrets = data.spec?.imagePullSecrets?.map((s: { name: string }) => s.name) || []

  // Pod filesystem modal state
  const [podFilesContainer, setPodFilesContainer] = useState<string | null>(null)

  const handleOpenTerminal = (containerName?: string) => {
    const container = containerName || containers[0]?.name
    if (namespace && podName && container) {
      openTerminal({
        namespace,
        podName,
        containerName: container,
        containers: containers.map((c: { name: string }) => c.name),
      })
    }
  }

  const allContainerNames = [
    ...initContainers.map((c: { name: string }) => c.name),
    ...containers.map((c: { name: string }) => c.name),
  ]

  const handleOpenLogs = (containerName?: string) => {
    if (namespace && podName) {
      openLogs({
        namespace,
        podName,
        containers: allContainerNames,
        containerName,
      })
    }
  }

  return (
    <>
      {/* Problems alert - shown at top when there are issues */}
      {hasProblems && (
        <AlertBanner variant="error" title="Issues Detected" items={problems} />
      )}

      {/* Status section */}
      <Section title="Status" icon={Server}>
        <PropertyList>
          <Property label="Phase" value={data.status?.phase} />
          <Property label="Node" value={
            data.spec?.nodeName ? <ResourceLink name={data.spec.nodeName} kind="nodes" onNavigate={onNavigate} /> : undefined
          } copyable onCopy={onCopy} copied={copied} />
          <Property label="Pod IP" value={data.status?.podIP} copyable onCopy={onCopy} copied={copied} />
          <Property label="Host IP" value={data.status?.hostIP} />
          <Property
            label={
              <Tooltip
                content={
                  data.status?.qosClass === 'Guaranteed'
                    ? 'Guaranteed: Pod has exact resource requests=limits. Least likely to be evicted.'
                    : data.status?.qosClass === 'Burstable'
                    ? 'Burstable: Pod has some resource requests/limits. May be evicted if node is under pressure.'
                    : 'BestEffort: No resource requests/limits. First to be evicted under memory pressure.'
                }
                position="right"
              >
                <span className="border-b border-dotted border-theme-text-tertiary cursor-help">QoS Class</span>
              </Tooltip>
            }
            value={data.status?.qosClass}
          />
          <Property label="Service Account" value={
            data.spec?.serviceAccountName ? <ResourceLink name={data.spec.serviceAccountName} kind="serviceaccounts" namespace={data.metadata?.namespace || ''} onNavigate={onNavigate} /> : undefined
          } />
        </PropertyList>
      </Section>

      {/* Init Containers - shown only when present */}
      {initContainers.length > 0 && (
        <Section title={`Init Containers (${initContainers.length})`} icon={CirclePlay} defaultExpanded>
          <div className="space-y-3">
            {initContainers.map((container: any, index: number) => {
              const status = initContainerStatuses.find((s: any) => s.name === container.name)
              const state = status?.state
              const stateKey = state ? Object.keys(state)[0] : 'unknown'
              const restarts = status?.restartCount || 0

              // Determine completion status
              const exitCode = state?.terminated?.exitCode
              const isCompleted = stateKey === 'terminated' && exitCode === 0
              const isFailed = stateKey === 'terminated' && exitCode != null && exitCode !== 0
              const isWaiting = stateKey === 'waiting'
              const isInitRunning = stateKey === 'running'

              // Status label and color
              let statusLabel: string
              let statusColor: string
              if (isCompleted) {
                statusLabel = 'Completed'
                statusColor = 'bg-green-500/20 text-green-400'
              } else if (isFailed) {
                statusLabel = `Exit ${exitCode}`
                statusColor = 'bg-red-500/20 text-red-400'
              } else if (isInitRunning) {
                statusLabel = 'Running'
                statusColor = 'bg-blue-500/20 text-blue-400'
              } else if (isWaiting) {
                statusLabel = state?.waiting?.reason || 'Waiting'
                statusColor = 'bg-yellow-500/20 text-yellow-400'
              } else {
                statusLabel = 'Pending'
                statusColor = 'bg-gray-500/20 text-gray-400'
              }

              // Build command string
              const command = container.command || container.args
                ? [...(container.command || []), ...(container.args || [])].join(' ')
                : null

              return (
                <div key={container.name} className={clsx(
                  'rounded-lg p-3 border-l-2',
                  isCompleted ? 'bg-theme-elevated/20 border-green-500/40' :
                  isFailed ? 'bg-theme-elevated/30 border-red-500/50' :
                  isInitRunning ? 'bg-theme-elevated/30 border-blue-500/50' :
                  'bg-theme-elevated/30 border-yellow-500/40'
                )}>
                  <div className="flex items-center justify-between mb-2">
                    <div className="flex items-center gap-2">
                      <span className="text-[10px] font-mono text-theme-text-tertiary bg-theme-elevated rounded px-1.5 py-0.5">
                        {index + 1}/{initContainers.length}
                      </span>
                      <span className="text-sm font-medium text-theme-text-primary">{container.name}</span>
                    </div>
                    <div className="flex items-center gap-2">
                      {canViewLogs && (
                        <Tooltip content="Logs" delay={150}>
                          <button
                            onClick={() => handleOpenLogs(container.name)}
                            className="p-1 text-slate-400 hover:text-blue-400 hover:bg-slate-600/50 rounded transition-colors"
                          >
                            <FileText className="w-4 h-4" />
                          </button>
                        </Tooltip>
                      )}
                      <span className={clsx('px-2 py-0.5 text-xs rounded', statusColor)}>
                        {statusLabel}
                      </span>
                    </div>
                  </div>
                  <div className="text-xs text-theme-text-secondary space-y-1">
                    <Tooltip content="Browse image filesystem from registry" delay={150} position="bottom">
                      <button
                        className="truncate text-blue-400 hover:text-blue-300 hover:underline text-left w-full"
                        onClick={() => setSelectedImage(container.image)}
                      >
                        Image: {container.image}
                      </button>
                    </Tooltip>
                    {command && (
                      <div className="text-theme-text-tertiary font-mono break-all">
                        $ {command}
                      </div>
                    )}
                    {restarts > 0 && (
                      <div className={restarts > 5 ? 'text-red-400' : 'text-yellow-400'}>
                        Restarts: {restarts}
                      </div>
                    )}
                    {/* Waiting reason detail */}
                    {isWaiting && state?.waiting?.reason && state.waiting.reason !== 'PodInitializing' && (
                      <div className="text-red-400 flex items-center gap-1">
                        <span className="font-medium">{state.waiting.reason}</span>
                        {state.waiting.message && (
                          <span className="text-theme-text-tertiary truncate" title={state.waiting.message}>
                            — {state.waiting.message.slice(0, 60)}{state.waiting.message.length > 60 ? '...' : ''}
                          </span>
                        )}
                      </div>
                    )}
                    {/* Failed termination detail */}
                    {isFailed && (
                      <div className="text-red-400 flex items-center gap-1">
                        <span className="font-medium">
                          {state?.terminated?.reason || 'Failed'}
                        </span>
                        {state?.terminated?.message && (
                          <span className="text-theme-text-tertiary truncate" title={state.terminated.message}>
                            — {state.terminated.message.slice(0, 80)}{state.terminated.message.length > 80 ? '...' : ''}
                          </span>
                        )}
                      </div>
                    )}
                    {(container.resources?.requests || container.resources?.limits) && (
                      <div className="flex gap-4 mt-1">
                        {container.resources?.requests && (
                          <span>Requests: {formatResources(container.resources.requests)}</span>
                        )}
                        {container.resources?.limits && (
                          <span>Limits: {formatResources(container.resources.limits)}</span>
                        )}
                      </div>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {/* Container Status */}
      <Section title="Containers" icon={HardDrive} defaultExpanded>
        <div className="space-y-3">
          {containers.map((container: any) => {
            const status = containerStatuses.find((s: any) => s.name === container.name)
            const state = status?.state
            const stateKey = state ? Object.keys(state)[0] : 'unknown'
            const isReady = status?.ready
            const restarts = status?.restartCount || 0

            // Get last termination info for troubleshooting
            const lastTermination = status?.lastState?.terminated
            const currentWaiting = status?.state?.waiting
            const currentTerminated = status?.state?.terminated

            return (
              <div key={container.name} className="bg-theme-elevated/30 rounded-lg p-3">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-theme-text-primary">{container.name}</span>
                  <div className="flex items-center gap-2">
                    {stateKey === 'running' && canExec && (
                      <Tooltip content="Terminal" delay={150}>
                        <button
                          onClick={() => handleOpenTerminal(container.name)}
                          className="p-1 text-slate-400 hover:text-blue-400 hover:bg-slate-600/50 rounded transition-colors"
                        >
                          <TerminalIcon className="w-4 h-4" />
                        </button>
                      </Tooltip>
                    )}
                    {stateKey === 'running' && canExec && (
                      <Tooltip content="Browse files" delay={150}>
                        <button
                          onClick={() => setPodFilesContainer(container.name)}
                          className="p-1 text-slate-400 hover:text-blue-400 hover:bg-slate-600/50 rounded transition-colors"
                        >
                          <FolderOpen className="w-4 h-4" />
                        </button>
                      </Tooltip>
                    )}
                    {canViewLogs && (
                      <Tooltip content="Logs" delay={150}>
                        <button
                          onClick={() => handleOpenLogs(container.name)}
                          className="p-1 text-slate-400 hover:text-blue-400 hover:bg-slate-600/50 rounded transition-colors"
                        >
                          <FileText className="w-4 h-4" />
                        </button>
                      </Tooltip>
                    )}
                    <span className={clsx(
                      'px-2 py-0.5 text-xs rounded',
                      isReady ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'
                    )}>
                      {isReady ? 'Ready' : 'Not Ready'}
                    </span>
                    <span className={clsx(
                      'px-2 py-0.5 text-xs rounded',
                      stateKey === 'running' ? 'bg-green-500/20 text-green-400' :
                      stateKey === 'waiting' ? 'bg-yellow-500/20 text-yellow-400' :
                      'bg-red-500/20 text-red-400'
                    )}>
                      {stateKey}
                    </span>
                  </div>
                </div>
                <div className="text-xs text-theme-text-secondary space-y-1">
                  <button
                    className="truncate text-blue-400 hover:text-blue-300 hover:underline text-left w-full"
                    title="Browse static image filesystem from registry"
                    onClick={() => setSelectedImage(container.image)}
                  >
                    Image: {container.image}
                  </button>
                  {restarts > 0 && (() => {
                    const { color, label } = getRestartRecency(lastTermination?.finishedAt, restarts)
                    return (
                      <div className={color}>
                        Restarts: {restarts}{label ? ` (last: ${label})` : ''}
                      </div>
                    )
                  })()}
                  {/* Show current waiting reason (e.g., CrashLoopBackOff) */}
                  {currentWaiting?.reason && currentWaiting.reason !== 'ContainerCreating' && (
                    <div className="text-red-400 flex items-center gap-1">
                      <span className="font-medium">{currentWaiting.reason}</span>
                      {currentWaiting.message && (
                        <span className="text-theme-text-tertiary truncate" title={currentWaiting.message}>
                          — {currentWaiting.message.slice(0, 60)}{currentWaiting.message.length > 60 ? '...' : ''}
                        </span>
                      )}
                    </div>
                  )}
                  {/* Show current terminated reason */}
                  {currentTerminated?.reason && (
                    <div className="text-red-400 flex items-center gap-1">
                      <span className="font-medium">Terminated: {currentTerminated.reason}</span>
                      {currentTerminated.exitCode !== undefined && currentTerminated.exitCode !== 0 && (
                        <span className="text-theme-text-tertiary">(exit code {currentTerminated.exitCode})</span>
                      )}
                    </div>
                  )}
                  {/* Show last termination info if container restarted */}
                  {lastTermination && restarts > 0 && !currentTerminated && (
                    <div className="text-amber-400/80 space-y-0.5">
                      <div className="flex items-center gap-1">
                        <span className="font-medium">Last exit: {lastTermination.reason || 'Error'}</span>
                        {lastTermination.exitCode !== undefined && lastTermination.exitCode !== 0 && (
                          <span className="text-theme-text-tertiary">(code {lastTermination.exitCode})</span>
                        )}
                        {lastTermination.reason === 'OOMKilled' && container.resources?.limits?.memory && (
                          <span className="text-theme-text-tertiary">— limit: {container.resources.limits.memory}</span>
                        )}
                      </div>
                      {lastTermination.finishedAt && (() => {
                        const agoLabel = formatMsAgo(lastTermination.finishedAt)
                        if (!agoLabel) return null
                        const runDur = lastTermination.startedAt
                          ? getRunDuration(lastTermination.startedAt, lastTermination.finishedAt)
                          : null
                        return (
                          <div className="text-theme-text-tertiary flex items-center gap-1">
                            <span>Terminated {agoLabel}</span>
                            {runDur && <span>(ran {runDur})</span>}
                          </div>
                        )
                      })()}
                    </div>
                  )}
                  {container.ports && container.ports.length > 0 && (
                    <div className="flex items-center gap-2 flex-wrap">
                      <span>Ports:</span>
                      {container.ports.map((p: any) => (
                        canPortForward ? (
                          <PortForwardInlineButton
                            key={`${p.containerPort}-${p.protocol || 'TCP'}`}
                            namespace={namespace}
                            podName={podName}
                            port={p.containerPort}
                            protocol={p.protocol || 'TCP'}
                            disabled={!isRunning}
                          />
                        ) : (
                          <span key={`${p.containerPort}-${p.protocol || 'TCP'}`} className="text-theme-text-tertiary">
                            {p.containerPort}/{p.protocol || 'TCP'}
                          </span>
                        )
                      ))}
                    </div>
                  )}
                  {(container.resources?.requests || container.resources?.limits) && (
                    <div className="flex gap-4 mt-1">
                      {container.resources?.requests && (
                        <span>Requests: {formatResources(container.resources.requests)}</span>
                      )}
                      {container.resources?.limits && (
                        <span>Limits: {formatResources(container.resources.limits)}</span>
                      )}
                    </div>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </Section>

      {/* Resource Usage (from metrics-server) — hidden when Prometheus has CPU/memory data */}
      {!hideMetricsServer && !!(metrics?.containers?.length || metricsHistory?.containers?.length || metricsHistory?.collectionError) && (
        <Section title="Resource Usage" icon={Activity} defaultExpanded>
          {metricsHistory?.collectionError && !metricsHistory?.containers?.length && (
            <div className="mb-3 rounded-lg border border-yellow-500/30 bg-yellow-500/5 px-3 py-2 text-xs text-yellow-400">
              <span className="font-medium">Metrics collection error:</span>{' '}
              <span className="break-all">{metricsHistory.collectionError}</span>
            </div>
          )}
          <div className="space-y-4">
            {(metricsHistory?.containers || metrics?.containers || []).map((historyContainer) => {
              // Find current metrics for this container
              const currentMetrics = metrics?.containers?.find(c => c.name === historyContainer.name)
              // Find the container spec to compare against limits
              const containerSpec = containers.find((c: any) => c.name === historyContainer.name)
              const limits = containerSpec?.resources?.limits
              const requests = containerSpec?.resources?.requests

              // Get historical data points (from history or empty)
              const dataPoints = 'dataPoints' in historyContainer ? historyContainer.dataPoints : []

              return (
                <div key={historyContainer.name} className="bg-theme-elevated/30 rounded-lg p-3">
                  <div className="flex items-center justify-between mb-3">
                    <span className="text-sm font-medium text-theme-text-primary">{historyContainer.name}</span>
                  </div>

                  {dataPoints && dataPoints.length > 0 ? (
                    <div className="grid grid-cols-2 gap-6">
                      <div>
                        <div className="text-xs text-theme-text-tertiary mb-2">CPU</div>
                        <MetricsChart
                          dataPoints={dataPoints}
                          type="cpu"
                          height={80}
                          showAxis={true}
                          limit={limits?.cpu}
                          request={requests?.cpu}
                        />
                      </div>
                      <div>
                        <div className="text-xs text-theme-text-tertiary mb-2">Memory</div>
                        <MetricsChart
                          dataPoints={dataPoints}
                          type="memory"
                          height={80}
                          showAxis={true}
                          limit={limits?.memory}
                          request={requests?.memory}
                        />
                      </div>
                    </div>
                  ) : currentMetrics ? (
                    /* Fallback to simple display if no history yet */
                    <div className="grid grid-cols-2 gap-4 text-xs">
                      <div>
                        <div className="text-theme-text-tertiary mb-1">CPU</div>
                        <div className="flex items-baseline gap-1">
                          <span className="text-sm font-medium text-blue-400">{currentMetrics.usage.cpu}</span>
                          {limits?.cpu && (
                            <span className="text-theme-text-tertiary">/ {limits.cpu} limit</span>
                          )}
                        </div>
                      </div>
                      <div>
                        <div className="text-theme-text-tertiary mb-1">Memory</div>
                        <div className="flex items-baseline gap-1">
                          <span className="text-sm font-medium text-purple-400">{currentMetrics.usage.memory}</span>
                          {limits?.memory && (
                            <span className="text-theme-text-tertiary">/ {limits.memory} limit</span>
                          )}
                        </div>
                      </div>
                    </div>
                  ) : (
                    <div className="text-xs text-theme-text-tertiary">Collecting metrics data...</div>
                  )}
                </div>
              )
            })}
          </div>
          {metrics?.timestamp && (
            <div className="mt-2 text-xs text-theme-text-tertiary">
              Last updated: {new Date(metrics.timestamp).toLocaleTimeString()}
            </div>
          )}
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={data.status?.conditions} />

      {/* Image Filesystem Modal */}
      {selectedImage && (
        <ImageFilesystemModal
          open={!!selectedImage}
          onClose={() => setSelectedImage(null)}
          image={selectedImage}
          namespace={namespace || ''}
          podName={podName || ''}
          pullSecrets={imagePullSecrets}
          onSwitchToPodFiles={isRunning && canExec ? () => {
            // Find which container uses this image and open pod files for it
            const match = containers.find((c: any) => c.image === selectedImage)
            setPodFilesContainer(match?.name || containers[0]?.name)
          } : undefined}
        />
      )}

      {/* Pod Filesystem Modal */}
      {podFilesContainer && (
        <PodFilesystemModal
          open={!!podFilesContainer}
          onClose={() => setPodFilesContainer(null)}
          namespace={namespace || ''}
          podName={podName || ''}
          containers={containers.map((c: { name: string }) => c.name)}
          initialContainer={podFilesContainer}
          onSwitchToImageFiles={() => {
            // Find which image this container uses and open image filesystem
            const match = containers.find((c: any) => c.name === podFilesContainer)
            if (match?.image) setSelectedImage(match.image)
          }}
        />
      )}
    </>
  )
}
