import { useState, useEffect } from 'react'
import { Server, ExternalLink, Scale, Minus, Plus, Loader2, Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, PodTemplateSection, AlertBanner, ResourceLink, ResourceRefBadge, useOperationalIssuesShown } from '../../ui/drawer-components'
import { DialogPortal } from '../../ui/DialogPortal'
import { Tooltip } from '../../ui/Tooltip'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import type { RBACSubjectResponse, RBACPolicyRule, ResourceRef, HPADiagnosis } from '../../../types'
import { detectBlastRadius, rulePermissivenessScore } from '../../../utils/rbac-blast-radius'
import { RBACErrorSection, isRBACUnavailable } from './RBACErrorSection'
import { hpaStateLabel, hpaStateLevel } from '../resource-utils-hpa'
import {
  rbacVerbBadgeClass,
  rbacResourceBadgeClass,
  rbacApiGroupBadgeClass,
} from '../../../utils/rbac-badges'

export interface ScalerDiagnosis {
  ref: ResourceRef
  diagnosis?: HPADiagnosis
  loading?: boolean
  error?: string
}

interface WorkloadRendererProps {
  kind: string
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  onViewPods?: () => void
  onScale?: (replicas: number) => Promise<void>
  isScalePending?: boolean
  scaleBlockedBy?: ResourceRef[]
  scalerDiagnostics?: ScalerDiagnosis[]
  onRequestRefresh?: () => void
  /**
   * RBAC reverse-lookup for the workload's pod-template ServiceAccount.
   * Undefined means the host didn't wire the fetch (Permissions section is
   * omitted). Null means the fetch failed.
   */
  rbacData?: RBACSubjectResponse | null
  rbacLoading?: boolean
  rbacError?: Error | null
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

function formatScalerLabel(ref: ResourceRef): string {
  const prefix = ref.namespace ? `${ref.namespace}/` : ''
  return `${ref.kind} ${prefix}${ref.name}`
}

function compactHPASummary(diagnosis: HPADiagnosis): string {
  if (diagnosis.state === 'limited_max') {
    return `Wants more; capped at maxReplicas=${diagnosis.bounds.max}`
  }
  if (diagnosis.state === 'pinned') {
    return `Fixed at ${diagnosis.bounds.current} replicas`
  }
  if (diagnosis.state === 'metrics_unavailable' || diagnosis.state === 'metrics_incomplete') {
    const missingMetric = diagnosis.metrics?.find((metric) => metric.status !== 'ok')
    if (missingMetric?.type === 'Resource' && missingMetric.name) {
      return `Add ${missingMetric.name} requests; HPA cannot compute replicas`
    }
    if (missingMetric?.name) {
      return `${missingMetric.name} metric unavailable; HPA cannot compute replicas`
    }
    return 'Metrics unavailable; HPA cannot compute replicas'
  }
  return diagnosis.summary
}

export function WorkloadRenderer({ kind, data, onNavigate, onViewPods, onScale, isScalePending, scaleBlockedBy, scalerDiagnostics, onRequestRefresh, rbacData, rbacLoading, rbacError }: WorkloadRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const metadata = data.metadata || {}

  const isDaemonSet = kind === 'daemonsets'
  const isStatefulSet = kind === 'statefulsets'
  const isScalableKind = kind === 'deployments' || kind === 'statefulsets'
  const isScaleBlocked = !!scaleBlockedBy?.length
  const isScalable = isScalableKind && !!onScale && !isScaleBlocked
  const scaleBlockedLabel = scaleBlockedBy?.map(formatScalerLabel).join(', ')
  const scaleBlockedReason = `Manual scaling is disabled because replicas are controlled by ${scaleBlockedLabel}. Manage scaling there instead.`

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

  // Check for problems and progress. Suppressed when the dedicated Operational
  // Issues section is shown — it carries the workload's own issues plus its pods'
  // (richer, with cause/action), so the workload-status problems would duplicate.
  const operationalIssuesShown = useOperationalIssuesShown()
  const problems = operationalIssuesShown ? [] : getWorkloadProblems(status, spec, kind)
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

  useEffect(() => {
    if (isScaleBlocked) {
      setShowScaleDialog(false)
    }
  }, [isScaleBlocked])

  const handleScale = async () => {
    if (!onScale || isScaleBlocked) return
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
      {/* Scaling in progress banner — amber: replicas short of desired is an
          attention state, not an info note (it may be a stuck rollout). */}
      {(scaledTo !== null || progressMessage) && !hasProblems && (
        <div className="mb-4 p-3 bg-amber-500/10 border border-amber-500/30 rounded-lg">
          <div className="flex items-center gap-2">
            <Loader2 className="w-4 h-4 text-amber-500 animate-spin shrink-0" />
            <div className="text-sm text-amber-700 dark:text-amber-300">
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
              {scaleBlockedBy && scaleBlockedBy.length > 0 && (
                <Property
                  label="Controlled by"
                  value={
                    <div className="flex flex-wrap gap-1">
                      {scaleBlockedBy.map((ref) => (
                        <ResourceRefBadge key={`${ref.kind}/${ref.namespace}/${ref.name}`} resourceRef={ref} onClick={onNavigate} wrapAtSeparator />
                      ))}
                      {scalerDiagnostics && scalerDiagnostics.length > 0 && (
                        <div className="mt-1 w-full space-y-1">
                          {scalerDiagnostics.map((entry) => (
                            <ScalerDiagnosisRow key={`${entry.ref.kind}/${entry.ref.namespace}/${entry.ref.name}`} entry={entry} />
                          ))}
                        </div>
                      )}
                    </div>
                  }
                />
              )}
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
          {isScalable ? (
            <button
              onClick={openScaleDialog}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-emerald-400 hover:text-emerald-300 bg-emerald-500/10 hover:bg-emerald-500/20 border border-emerald-500/30 rounded transition-colors"
            >
              <Scale className="w-3 h-3" />
              Scale
            </button>
          ) : isScalableKind && !!onScale && isScaleBlocked ? (
            <Tooltip content={scaleBlockedReason}>
              <button
                type="button"
                disabled
                aria-label={scaleBlockedReason}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-theme-text-tertiary bg-theme-elevated border border-theme-border rounded cursor-not-allowed"
              >
                <Scale className="w-3 h-3" />
                Scale
              </button>
            </Tooltip>
          ) : null}
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

      {/* Permissions — same shape as PodPermissionsSection but framed for a
       *  workload (Pods this workload spawns inherit the SA). Placed below
       *  the diagnostic-signal sections because it answers an incident/audit
       *  question, not a daily-browsing one. Only renders when the host
       *  wired the RBAC fetch. */}
      {rbacData !== undefined && (
        <WorkloadPermissionsSection
          saName={spec.template?.spec?.serviceAccountName || 'default'}
          namespace={metadata.namespace || ''}
          rbacData={rbacData}
          loading={!!rbacLoading}
          error={rbacError ?? null}
          onNavigate={onNavigate}
        />
      )}
    </>
  )
}

function ScalerDiagnosisRow({ entry }: { entry: ScalerDiagnosis }) {
  if (entry.loading) {
    return <div className="text-xs text-theme-text-tertiary">Loading autoscaler diagnosis…</div>
  }
  if (entry.error) {
    return <div className="text-xs text-theme-text-tertiary">Autoscaler diagnosis unavailable</div>
  }
  if (!entry.diagnosis) return null
  return (
    <div className="rounded border border-theme-border bg-theme-surface px-2 py-1.5 text-xs leading-6 text-theme-text-secondary whitespace-normal break-normal">
      <Badge severity={badgeSeverityForHPA(entry.diagnosis)} size="sm" className="mr-1.5 align-middle">{hpaStateLabel(entry.diagnosis.state)}</Badge>
      <span className="align-middle whitespace-normal break-normal">{compactHPASummary(entry.diagnosis)}</span>
    </div>
  )
}

function badgeSeverityForHPA(diagnosis: HPADiagnosis): BadgeSeverity {
  switch (hpaStateLevel(diagnosis.state)) {
    case 'healthy':
      return 'success'
    case 'unhealthy':
      return 'error'
    case 'degraded':
      return 'warning'
    case 'alert':
      return 'alert'
    case 'neutral':
      return 'info'
    default:
      return 'neutral'
  }
}

// ============================================================================
// WORKLOAD PERMISSIONS SECTION
// ============================================================================
// Mirror of PodPermissionsSection but framed at the workload level: "Pods
// this workload spawns inherit these permissions". A compromise of any
// replica gives the attacker the same SA. Detection criteria match Pod's —
// verb wildcards, escalation verbs (escalate/bind/impersonate), cluster-
// admin, cluster-wide create pods. Resource-only wildcards do NOT trigger
// (would fire on every authenticated SA via inherited `view`).

// Blast-radius detection and scoring is shared with Pod / ServiceAccount
// renderers — see utils/rbac-blast-radius.ts.

interface WorkloadPermissionsSectionProps {
  saName: string
  namespace: string
  rbacData: RBACSubjectResponse | null
  loading: boolean
  error: Error | null
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function WorkloadPermissionsSection({
  saName,
  namespace,
  rbacData,
  loading,
  error,
  onNavigate,
}: WorkloadPermissionsSectionProps) {
  const title = `Permissions via ServiceAccount: ${saName}`
  if (loading) {
    return (
      <Section title={title} icon={Shield}>
        <div className="text-sm text-theme-text-secondary">Loading RBAC graph…</div>
      </Section>
    )
  }
  if (error) {
    // Permissions is a bonus section here; when RBAC is simply not available
    // (cluster-static) or forbidden, hide it rather than repeat a note on every
    // workload. Genuine faults still surface.
    if (isRBACUnavailable(error)) return null
    return <RBACErrorSection title={title} error={error} />
  }
  if (!rbacData) return null

  const blast = detectBlastRadius(rbacData)
  const sorted = [...(rbacData.flat ?? [])].sort(
    (a, b) => rulePermissivenessScore(b) - rulePermissivenessScore(a),
  )
  const preview = sorted.slice(0, 5)
  const more = Math.max(0, sorted.length - preview.length)
  const directCount = rbacData.direct?.length ?? 0
  const inheritedCount = (rbacData.inheritedFromGroups ?? []).reduce((n, g) => n + g.bindings.length, 0)
  const ruleCount = rbacData.flat?.length ?? 0

  // Default collapsed unless the Pod's permissions are genuinely risky —
  // workload-detail pages are most-often opened for "is this rolling out
  // OK / why is it crashing", not "audit the SA". Auto-expanding only on
  // blast-radius hits keeps the noisy case quiet without burying real
  // alarms.
  const hasBlastRadius = blast.length > 0
  return (
    <Section title={title} icon={Shield} defaultExpanded={hasBlastRadius}>
      {blast.length > 0 && (
        <AlertBanner variant="warning" title="Blast radius">
          <div className="text-xs">
            Every Pod this workload spawns inherits the ServiceAccount's
            permissions. Compromising any replica gives an attacker:
          </div>
          <ul className="mt-1.5 text-xs space-y-1">
            {blast.map((r, i) => (
              <li key={i}>
                <span className="text-theme-text-secondary">
                  {r.binding.binding.kind} <span className="font-medium">{r.binding.binding.name}</span>
                </span>{' '}
                <span className="text-theme-text-tertiary">{r.reason}</span>
              </li>
            ))}
          </ul>
        </AlertBanner>
      )}

      <div className="text-xs text-theme-text-tertiary mb-3">
        {directCount} direct binding{directCount === 1 ? '' : 's'} ·{' '}
        {inheritedCount} inherited via group{inheritedCount === 1 ? '' : 's'} ·{' '}
        {ruleCount} distinct rule{ruleCount === 1 ? '' : 's'}
        {rbacData.truncated && <span className="text-orange-400"> (truncated)</span>}
      </div>

      {preview.length === 0 ? (
        <div className="text-sm text-theme-text-secondary">
          This ServiceAccount has no effective permissions in the cluster.
        </div>
      ) : (
        <div className="space-y-1">
          {preview.map((r, i) => (
            <WorkloadRulePreviewLine key={i} rule={r} />
          ))}
          {more > 0 && (
            <div className="text-xs text-theme-text-tertiary">
              +{more} more rule{more === 1 ? '' : 's'} — open the ServiceAccount
              for full provenance.
            </div>
          )}
        </div>
      )}

      <div className="mt-3 text-xs">
        <ResourceLink
          name={saName}
          kind="serviceaccounts"
          namespace={namespace}
          label="View full permissions →"
          onNavigate={onNavigate}
        />
      </div>
    </Section>
  )
}

function WorkloadRulePreviewLine({ rule }: { rule: RBACPolicyRule }) {
  const verbs = rule.verbs ?? []
  const resources = rule.resources ?? []
  const nonResourceURLs = rule.nonResourceURLs ?? []
  const groups = rule.apiGroups ?? []
  const isNonResource = resources.length === 0 && nonResourceURLs.length > 0
  return (
    <div className="flex items-center gap-1 flex-wrap text-xs">
      {verbs.map((v) => (
        <span key={v} className={clsx('badge', rbacVerbBadgeClass(v))}>{v}</span>
      ))}
      <span className="text-theme-text-secondary">on</span>
      {isNonResource ? (
        nonResourceURLs.map((u) => (
          <span key={u} className="badge font-mono bg-theme-elevated text-theme-text-secondary">{u}</span>
        ))
      ) : (
        resources.map((r) => (
          <span key={r} className={clsx('badge', rbacResourceBadgeClass)}>{r === '*' ? '*' : r}</span>
        ))
      )}
      {!isNonResource && groups.length > 0 && groups.some((g) => g !== '') && (
        <>
          <span className="text-theme-text-secondary">in</span>
          {groups.map((g) => (
            <span key={g} className={clsx('badge', rbacApiGroupBadgeClass)}>{g === '' ? 'core' : g}</span>
          ))}
        </>
      )}
    </div>
  )
}
