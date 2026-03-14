import { useState, type ReactNode, type JSX } from 'react'
import { Server, HardDrive, Terminal as TerminalIcon, FileText, Activity, CirclePlay, FolderOpen, List, Eye, EyeOff } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, CopyHandler, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { formatResources, formatDuration } from '../resource-utils'
import { getResourceStatusColor } from '../../../utils/badge-colors'
import type { ResolvedEnvFrom } from '../../../types'
import { Tooltip } from '../../ui/Tooltip'
import { MetricsChart } from '../../ui/MetricsChart'

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
  /** When provided, container-level Logs buttons call this instead of onOpenLogsPanel */
  onOpenLogs?: (podName: string, containerName: string) => void
  // Platform capabilities
  canExec?: boolean
  canViewLogs?: boolean
  canPortForward?: boolean
  // Dock actions
  onOpenTerminal?: (params: { namespace: string; podName: string; containerName: string; containers: string[] }) => void
  onOpenLogsPanel?: (params: { namespace: string; podName: string; containers: string[]; containerName?: string }) => void
  // Port forward render prop
  renderPortAction?: (props: { namespace: string; podName: string; port: number; protocol: string; disabled?: boolean }) => ReactNode
  // Metrics data injection
  metrics?: { containers?: any[]; timestamp?: string }
  metricsHistory?: { containers?: any[]; collectionError?: string }
  hideMetricsServer?: boolean
  // Filesystem browser render props
  renderImageBrowser?: (props: { image: string; namespace: string; podName: string; pullSecrets: string[]; onClose: () => void; onSwitchToPodFiles?: () => void }) => ReactNode
  renderPodBrowser?: (props: { namespace: string; podName: string; containers: string[]; initialContainer: string; onClose: () => void; onSwitchToImageFiles: () => void }) => ReactNode
  /**
   * Resolved content for envFrom references.
   * When provided, expands ConfigMap/Secret keys inline instead of showing "(all keys)".
   */
  resolvedEnvFrom?: ResolvedEnvFrom
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

// ── Env vars section — extracted to use hooks (useState for reveal) ──────────

function SecretValueCell({ value }: { value: string }) {
  const [revealed, setRevealed] = useState(false)
  return (
    <span className="flex items-center gap-1 min-w-0">
      <span className={clsx('break-all text-amber-700 dark:text-yellow-400/80', !revealed && 'blur-[3px] select-none')}>
        {value || '•••'}
      </span>
      <button
        onClick={() => setRevealed(r => !r)}
        className="shrink-0 text-theme-text-tertiary hover:text-amber-700 dark:hover:text-yellow-400 transition-colors"
        title={revealed ? 'Hide value' : 'Reveal value'}
      >
        {revealed ? <EyeOff className="w-3 h-3" /> : <Eye className="w-3 h-3" />}
      </button>
    </span>
  )
}

function EnvRowShell({ name, children }: { name: string; children: ReactNode }) {
  return (
    <div className="flex items-start gap-1 text-xs font-mono py-0.5 border-b border-theme-border/30 last:border-0">
      <span className="text-theme-text-secondary shrink-0 break-all">{name}</span>
      <span className="text-theme-text-tertiary shrink-0">=</span>
      {children}
    </div>
  )
}

function EnvRow({ name, value, isSecret }: { name: string; value: string; isSecret: boolean }) {
  return (
    <EnvRowShell name={name}>
      {isSecret
        ? <SecretValueCell value={value} />
        : <span className="text-theme-text-primary break-all min-w-0">{value}</span>
      }
    </EnvRowShell>
  )
}

function resolveEnvValueNode(env: any): JSX.Element {
  if (env.valueFrom?.secretKeyRef) {
    const { name, key } = env.valueFrom.secretKeyRef
    return <span className="text-amber-700 dark:text-yellow-400/60 text-[10px] shrink-0 self-center px-1 py-0.5 bg-amber-500/10 dark:bg-yellow-500/10 rounded">secret:{name}[{key}]</span>
  }
  if (env.valueFrom?.configMapKeyRef) {
    const { name, key } = env.valueFrom.configMapKeyRef
    return <span className="text-blue-400/70 text-[10px] shrink-0 self-center px-1 py-0.5 bg-blue-500/10 rounded">configmap:{name}[{key}]</span>
  }
  if (env.valueFrom?.fieldRef) {
    return <span className="text-purple-400/70 break-all">field:{env.valueFrom.fieldRef.fieldPath}</span>
  }
  if (env.valueFrom?.resourceFieldRef) {
    return <span className="text-purple-400/70 break-all">resource:{env.valueFrom.resourceFieldRef.resource}</span>
  }
  return <span className="text-theme-text-primary break-all min-w-0">{env.value ?? ''}</span>
}

function EnvVarRow({ env }: { env: any }) {
  return (
    <EnvRowShell name={env.name}>
      {resolveEnvValueNode(env)}
    </EnvRowShell>
  )
}

function EnvVarsSection({
  initContainers,
  containers,
  resolvedEnvFrom,
}: {
  initContainers: any[]
  containers: any[]
  resolvedEnvFrom?: ResolvedEnvFrom
}) {
  const allContainers = [...initContainers, ...containers]
  const multiContainer = allContainers.filter((c: any) => c.env?.length > 0 || c.envFrom?.length > 0).length > 1

  return (
    <Section title="Environment Variables" icon={List}>
      <div className="space-y-4">
        {allContainers.map((container: any) => {
          const envVars: any[] = container.env || []
          const envFrom: any[] = container.envFrom || []
          if (!envVars.length && !envFrom.length) return null
          return (
            <div key={container.name}>
              {/* Only show container name label when there are multiple containers with env vars */}
              {multiContainer && (
                <div className="text-xs font-medium text-theme-text-tertiary mb-2 uppercase tracking-wide">
                  {container.name}
                </div>
              )}
              <div className="space-y-1">
                {/* envFrom — ConfigMap / Secret bulk injections */}
                {envFrom.map((ef: any, i: number) => {
                  const isSecret = !!ef.secretRef
                  const sourceName = ef.configMapRef?.name ?? ef.secretRef?.name ?? 'unknown'
                  const prefix = ef.configMapRef ? 'ConfigMap' : ef.secretRef ? 'Secret' : 'Source'
                  const resolved = resolvedEnvFrom?.[sourceName]
                  return (
                    <div key={i} className="mb-1">
                      <div className="flex items-center gap-1.5 text-xs font-mono py-0.5">
                        <span className={clsx(
                          'shrink-0 px-1 py-0.5 rounded text-[10px]',
                          isSecret ? 'bg-amber-500/10 dark:bg-yellow-500/10 text-amber-700 dark:text-yellow-400' : 'bg-blue-500/10 text-blue-400'
                        )}>
                          {prefix}
                        </span>
                        <span className="text-theme-text-secondary">{sourceName}</span>
                        {!resolved && <span className="text-theme-text-tertiary">(all keys)</span>}
                      </div>
                      {resolved && resolved.keys.length > 0 && (
                        <div className="ml-2 mt-0.5">
                          {resolved.keys.map((key) => (
                            <EnvRow
                              key={key}
                              name={key}
                              value={resolved.values[key] ?? ''}
                              isSecret={isSecret}
                            />
                          ))}
                        </div>
                      )}
                    </div>
                  )
                })}

                {/* Individual env vars */}
                {envVars.map((env: any) => (
                  <EnvVarRow key={env.name} env={env} />
                ))}
              </div>
            </div>
          )
        })}
      </div>
    </Section>
  )
}

export function PodRenderer({
  data,
  onCopy,
  copied,
  onNavigate,
  onOpenLogs: onOpenLogsOverride,
  canExec,
  canViewLogs,
  canPortForward,
  onOpenTerminal,
  onOpenLogsPanel,
  renderPortAction,
  metrics,
  metricsHistory,
  hideMetricsServer,
  renderImageBrowser,
  renderPodBrowser,
  resolvedEnvFrom,
}: PodRendererProps) {
  const containerStatuses = data.status?.containerStatuses || []
  const containers = data.spec?.containers || []
  const initContainers = data.spec?.initContainers || []
  const initContainerStatuses = data.status?.initContainerStatuses || []

  const namespace = data.metadata?.namespace
  const podName = data.metadata?.name
  const isRunning = data.status?.phase === 'Running'

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
      onOpenTerminal?.({
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
    if (onOpenLogsOverride && podName && containerName) {
      onOpenLogsOverride(podName, containerName)
      return
    }
    if (namespace && podName) {
      onOpenLogsPanel?.({
        namespace,
        podName,
        containers: allContainerNames,
        containerName,
      })
    }
  }

  // Render image name — clickable when renderImageBrowser is provided
  const renderImageName = (image: string) => {
    if (renderImageBrowser) {
      return (
        <Tooltip content="Browse image filesystem from registry" delay={150} position="bottom">
          <button
            className="truncate text-blue-400 hover:text-blue-300 hover:underline text-left w-full"
            onClick={() => setSelectedImage(image)}
          >
            Image: {image}
          </button>
        </Tooltip>
      )
    }
    return (
      <div className="truncate text-theme-text-secondary">
        Image: {image}
      </div>
    )
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
              if (isCompleted) {
                statusLabel = 'Completed'
              } else if (isFailed) {
                statusLabel = `Exit ${exitCode}`
              } else if (isInitRunning) {
                statusLabel = 'Running'
              } else if (isWaiting) {
                statusLabel = state?.waiting?.reason || 'Waiting'
              } else {
                statusLabel = 'Pending'
              }
              const statusColor = getResourceStatusColor(
                isCompleted ? 'succeeded' : isFailed ? 'failed' : isInitRunning ? 'running' : isWaiting ? 'waiting' : 'pending'
              )

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
                    {renderImageName(container.image)}
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
                    {stateKey === 'running' && canExec && onOpenTerminal && (
                      <Tooltip content="Terminal" delay={150}>
                        <button
                          onClick={() => handleOpenTerminal(container.name)}
                          className="p-1 text-slate-400 hover:text-blue-400 hover:bg-slate-600/50 rounded transition-colors"
                        >
                          <TerminalIcon className="w-4 h-4" />
                        </button>
                      </Tooltip>
                    )}
                    {stateKey === 'running' && canExec && renderPodBrowser && (
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
                  {renderImageName(container.image)}
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
                        canPortForward && renderPortAction ? (
                          <span key={`${p.containerPort}-${p.protocol || 'TCP'}`}>
                            {renderPortAction({
                              namespace,
                              podName,
                              port: p.containerPort,
                              protocol: p.protocol || 'TCP',
                              disabled: !isRunning,
                            })}
                          </span>
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

      {/* Environment Variables */}
      {[...initContainers, ...containers].some((c: any) => c.env?.length > 0 || c.envFrom?.length > 0) && (
        <EnvVarsSection
          initContainers={initContainers}
          containers={containers}
          resolvedEnvFrom={resolvedEnvFrom}
        />
      )}

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

      {/* Image Filesystem Modal (via render prop) */}
      {selectedImage && renderImageBrowser && renderImageBrowser({
        image: selectedImage,
        namespace: namespace || '',
        podName: podName || '',
        pullSecrets: imagePullSecrets,
        onClose: () => setSelectedImage(null),
        onSwitchToPodFiles: isRunning && canExec && renderPodBrowser ? () => {
          // Find which container uses this image and open pod files for it
          const match = containers.find((c: any) => c.image === selectedImage)
          setPodFilesContainer(match?.name || containers[0]?.name)
        } : undefined,
      })}

      {/* Pod Filesystem Modal (via render prop) */}
      {podFilesContainer && renderPodBrowser && renderPodBrowser({
        namespace: namespace || '',
        podName: podName || '',
        containers: containers.map((c: { name: string }) => c.name),
        initialContainer: podFilesContainer,
        onClose: () => setPodFilesContainer(null),
        onSwitchToImageFiles: () => {
          // Find which image this container uses and open image filesystem
          const match = containers.find((c: any) => c.name === podFilesContainer)
          if (match?.image) setSelectedImage(match.image)
        },
      })}
    </>
  )
}
