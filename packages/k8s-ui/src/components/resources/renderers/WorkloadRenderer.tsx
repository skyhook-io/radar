import { useState, useEffect } from 'react'
import { Server, ExternalLink, Scale, Minus, Plus, Loader2 } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, PodTemplateSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { DialogPortal } from '../../ui/DialogPortal'

interface WorkloadRendererProps {
  kind: string
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  onViewPods?: () => void
  onScale?: (replicas: number) => Promise<void>
  isScalePending?: boolean
  onRequestRefresh?: () => void
}

// Check if the workload is actively progressing (scaling, rolling update)
function isWorkloadProgressing(status: any): boolean {
  const conditions = status.conditions || []
  const progressing = conditions.find((c: any) => c.type === 'Progressing')
  return progressing?.status === 'True' && progressing?.reason !== 'ProgressDeadlineExceeded'
}

// Extract real problems from workload status (excludes normal rollout progress)
function getWorkloadProblems(status: any, spec: any, kind: string): string[] {
  const problems: string[] = []
  const progressing = isWorkloadProgressing(status)
  const isDaemonSet = kind === 'daemonsets'

  // Check replica/pod counts — only flag as problem if NOT actively progressing
  if (!progressing) {
    if (isDaemonSet) {
      const ready = status.numberReady || 0
      const desired = status.desiredNumberScheduled || 0
      if (desired > 0 && ready < desired) {
        problems.push(`${desired - ready} of ${desired} pods are not ready`)
      }
      if (status.numberUnavailable > 0) {
        problems.push(`${status.numberUnavailable} pods are unavailable`)
      }
    } else {
      const ready = status.readyReplicas || 0
      const desired = spec.replicas || 0
      if (desired > 0 && ready < desired) {
        problems.push(`${desired - ready} of ${desired} replicas are not ready`)
      }
      if (status.unavailableReplicas > 0) {
        problems.push(`${status.unavailableReplicas} replicas are unavailable`)
      }
    }
  }

  // Check conditions — real failures always shown
  const conditions = status.conditions || []
  for (const cond of conditions) {
    if (cond.status === 'True' && cond.type === 'ReplicaFailure' && cond.message) {
      problems.push(cond.message)
    }
    // Show condition failures, but skip Available=False during active rollout (that's expected)
    if (cond.status === 'False' && cond.message) {
      if (progressing && cond.type === 'Available') continue
      problems.push(`${cond.type}: ${cond.message}`)
    }
  }

  return problems
}

// Get progress info for active rollouts
function getWorkloadProgress(status: any, spec: any, kind: string): string | null {
  if (!isWorkloadProgressing(status)) return null

  const isDaemonSet = kind === 'daemonsets'
  if (isDaemonSet) {
    const ready = status.numberReady || 0
    const desired = status.desiredNumberScheduled || 0
    if (desired > 0 && ready < desired) {
      return `${ready} of ${desired} pods ready`
    }
  } else {
    const ready = status.readyReplicas || 0
    const desired = spec.replicas || 0
    if (desired > 0 && ready < desired) {
      return `${ready} of ${desired} replicas ready`
    }
  }
  return null
}

export function WorkloadRenderer({ kind, data, onNavigate, onViewPods, onScale, isScalePending, onRequestRefresh }: WorkloadRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const metadata = data.metadata || {}

  const isDaemonSet = kind === 'daemonsets'
  const isStatefulSet = kind === 'statefulsets'
  const isScalable = (kind === 'deployments' || kind === 'statefulsets') && !!onScale

  // Scale dialog state
  const [showScaleDialog, setShowScaleDialog] = useState(false)
  const [targetReplicas, setTargetReplicas] = useState(spec.replicas || 0)
  const [scaledTo, setScaledTo] = useState<number | null>(null)

  // Clear scaledTo once the backend data catches up
  useEffect(() => {
    if (scaledTo !== null && spec.replicas === scaledTo) {
      setScaledTo(null)
    }
  }, [spec.replicas, scaledTo])

  // Check for problems and progress
  const problems = getWorkloadProblems(status, spec, kind)
  const hasProblems = problems.length > 0
  const progressMessage = getWorkloadProgress(status, spec, kind)

  // Request data refresh while scaling is in progress
  const isScaling = scaledTo !== null || progressMessage !== null
  useEffect(() => {
    if (!isScaling || !onRequestRefresh) return
    const interval = setInterval(() => {
      onRequestRefresh()
    }, 2000)
    return () => clearInterval(interval)
  }, [isScaling, onRequestRefresh])

  const handleScale = async () => {
    if (!onScale) return
    try {
      await onScale(targetReplicas)
      setScaledTo(targetReplicas)
      setShowScaleDialog(false)
    } catch {
      // Stay on the dialog if scale failed
    }
  }

  const openScaleDialog = () => {
    setTargetReplicas(spec.replicas || 0)
    setShowScaleDialog(true)
  }

  return (
    <>
      {/* Scaling in progress banner */}
      {(scaledTo !== null || progressMessage) && !hasProblems && (
        <div className="mb-4 p-3 bg-blue-500/10 border border-blue-500/30 rounded-lg">
          <div className="flex items-center gap-2">
            <Loader2 className="w-4 h-4 text-blue-400 animate-spin shrink-0" />
            <div className="text-sm text-blue-300">
              {progressMessage || `Scaling to ${scaledTo} replicas...`}
            </div>
          </div>
        </div>
      )}

      {/* Problems alert - shown at top when there are real issues */}
      {hasProblems && (
        <AlertBanner variant="error" title="Issues Detected" items={problems}>
          <div className="flex items-center justify-between mt-2">
            <div className="text-xs text-red-400/60">
              Check Events below for details, or view individual pods for logs.
            </div>
            {onViewPods && (
              <button
                onClick={onViewPods}
                className="flex items-center gap-1 px-2 py-1 text-xs font-medium text-red-400 hover:text-red-300 bg-red-500/10 hover:bg-red-500/20 border border-red-500/30 rounded transition-colors"
              >
                <ExternalLink className="w-3 h-3" />
                View Pods
              </button>
            )}
          </div>
        </AlertBanner>
      )}

      <Section title="Status" icon={Server}>
        <PropertyList>
          {isDaemonSet ? (
            <>
              <Property label="Desired" value={status.desiredNumberScheduled} />
              <Property label="Current" value={status.currentNumberScheduled} />
              <Property label="Ready" value={status.numberReady} />
              <Property label="Up-to-date" value={status.updatedNumberScheduled} />
              <Property label="Available" value={status.numberAvailable} />
            </>
          ) : (
            <>
              <Property label="Replicas" value={`${status.readyReplicas || 0}/${spec.replicas || 0}`} />
              <Property label="Updated" value={status.updatedReplicas} />
              <Property label="Available" value={status.availableReplicas} />
              <Property label="Unavailable" value={status.unavailableReplicas} />
            </>
          )}
        </PropertyList>
        <div className="mt-3 pt-3 border-t border-theme-border flex items-center gap-2">
          {onViewPods && (
            <button
              onClick={onViewPods}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-blue-400 hover:text-blue-300 bg-blue-500/10 hover:bg-blue-500/20 border border-blue-500/30 rounded transition-colors"
            >
              <ExternalLink className="w-3 h-3" />
              View Managed Pods
            </button>
          )}
          {isScalable && (
            <button
              onClick={openScaleDialog}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-emerald-400 hover:text-emerald-300 bg-emerald-500/10 hover:bg-emerald-500/20 border border-emerald-500/30 rounded transition-colors"
            >
              <Scale className="w-3 h-3" />
              Scale
            </button>
          )}
        </div>
      </Section>

      {/* Scale Dialog */}
      <DialogPortal open={showScaleDialog} onClose={() => setShowScaleDialog(false)} className="w-80 p-4">
        <h3 className="text-sm font-medium text-theme-text-primary mb-4">
          Scale {metadata.name}
        </h3>

        <div className="flex items-center justify-center gap-4 mb-4">
          <button
            onClick={() => setTargetReplicas(Math.max(0, targetReplicas - 1))}
            className="p-2 rounded-lg bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
            disabled={targetReplicas <= 0}
          >
            <Minus className="w-5 h-5" />
          </button>

          <input
            type="number"
            min="0"
            max="10000"
            value={targetReplicas}
            onChange={(e) => setTargetReplicas(Math.min(10000, Math.max(0, parseInt(e.target.value) || 0)))}
            className="w-20 text-center text-2xl font-semibold bg-theme-elevated border border-theme-border rounded-lg py-2 text-theme-text-primary focus:outline-none focus:border-blue-500"
            autoFocus
          />

          <button
            onClick={() => setTargetReplicas(Math.min(10000, targetReplicas + 1))}
            disabled={targetReplicas >= 10000}
            className="p-2 rounded-lg bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Plus className="w-5 h-5" />
          </button>
        </div>

        <div className="text-xs text-theme-text-tertiary text-center mb-4">
          Current: {spec.replicas || 0} replicas
          {targetReplicas !== (spec.replicas || 0) && (
            <span className="text-theme-text-secondary">
              {' '}→ {targetReplicas}
            </span>
          )}
        </div>

        <div className="flex gap-2">
          <button
            onClick={() => setShowScaleDialog(false)}
            className="flex-1 px-3 py-2 text-sm text-theme-text-secondary hover:text-theme-text-primary bg-theme-elevated hover:bg-theme-hover border border-theme-border rounded-lg transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleScale}
            disabled={isScalePending || targetReplicas === (spec.replicas || 0)}
            className="flex-1 px-3 py-2 text-sm btn-brand disabled:cursor-not-allowed rounded-lg"
          >
            {isScalePending ? 'Scaling...' : 'Apply'}
          </button>
        </div>
      </DialogPortal>

      <Section title="Strategy">
        <PropertyList>
          {isDaemonSet || isStatefulSet ? (
            <Property label="Update Strategy" value={spec.updateStrategy?.type} />
          ) : (
            <>
              <Property label="Strategy" value={spec.strategy?.type} />
              {spec.strategy?.rollingUpdate && (
                <>
                  <Property label="Max Surge" value={spec.strategy.rollingUpdate.maxSurge} />
                  <Property label="Max Unavailable" value={spec.strategy.rollingUpdate.maxUnavailable} />
                </>
              )}
            </>
          )}
          {isStatefulSet && (
            <>
              <Property label="Service Name" value={
                spec.serviceName ? <ResourceLink name={spec.serviceName} kind="services" namespace={data.metadata?.namespace || ''} onNavigate={onNavigate} /> : undefined
              } />
              <Property label="Pod Management" value={spec.podManagementPolicy || 'OrderedReady'} />
            </>
          )}
        </PropertyList>
      </Section>

      <Section title="Pod Template" defaultExpanded={false}>
        <PodTemplateSection template={spec.template} />
      </Section>

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
