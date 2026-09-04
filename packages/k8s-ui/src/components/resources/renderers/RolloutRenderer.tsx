import { useState } from 'react'
import { Server, GitBranch, Network, Activity, Play, FastForward, SkipForward, RotateCcw, Undo2 } from 'lucide-react'
import { clsx } from 'clsx'
import { AlertBanner, Section, PropertyList, Property, ConditionsSection, PodTemplateSection, type ConditionTone } from '../../ui/drawer-components'
import { Tooltip } from '../../ui/Tooltip'
import { ConfirmDialog } from '../../ui/ConfirmDialog'
import { formatAge, getRolloutStep } from '../resource-utils'
import { CanaryStepTimeline, BlueGreenTimeline } from './rollout/CanaryStepTimeline'
import { ReplicaSetProgression } from './rollout/ReplicaSetProgression'
import type { WorkloadRevision, WorkloadPodInfo } from '../../../types'

export type RolloutAction = 'abort' | 'retry' | 'promote' | 'promote-full' | 'skip-step'

export interface RolloutCapabilities {
  abort: boolean
  retry: boolean
  promote: boolean
  promoteFull: boolean
  skipStep: boolean
  rollback: boolean
  restart: boolean
  strategy: string
  terminating: boolean
}

export interface RolloutActionConfirm {
  title: string
  message: string
}

export interface RolloutActionSpec {
  action: RolloutAction
  label: string
  pendingLabel: string
  icon: typeof Play
  hint: string
  blocked?: string
  destructive?: boolean
  // Set on the verbs that shift production traffic in one click.
  confirm?: RolloutActionConfirm
}

export interface AnalysisRunHistoryEntry {
  name: string
  phase: string
  message?: string
  trigger?: string
  stepIndex?: number
  createdAt: string
  metricsTotal: number
  metricsPassing: number
  metricsNotPassing: number
}

interface RolloutRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  capabilities?: RolloutCapabilities
  onAction?: (action: RolloutAction) => void
  pendingAction?: RolloutAction | null
  // Host-fetched — full AnalysisRun history (not just the 4 "active" slots
  // rolloutAnalysisRuns already covers). Omitted entirely, the History
  // subsection just doesn't render; a library consumer that skips the fetch
  // loses nothing else.
  analysisRunHistory?: AnalysisRunHistoryEntry[]
  // Host-fetched — the Rollout's ReplicaSet revision history + its pods, via
  // the generic workload hooks (useWorkloadRevisions/useWorkloadPods) shared
  // with every other workload kind, joined client-side by the host. Read-only
  // here; the rollback action stays exclusively in the existing history dialog.
  revisions?: WorkloadRevision[]
  pods?: WorkloadPodInfo[]
}

// Has the controller reconciled the template as it stands? A rollback changes the
// template before the controller writes status, and until it catches up a Healthy phase
// still describes the previous revision.
//
// A workloadRef Rollout keeps its template on another object, and a rollback patches
// THAT object — this payload carries neither its generation nor a way to reach it, so
// the honest answer is "unknown", not "yes".
function controllerCaughtUp(data: any): boolean {
  if (data?.spec?.workloadRef) return false
  const observed = data?.status?.observedGeneration
  if (observed === undefined || observed === null) return true
  const seen = Number(observed)
  if (!Number.isFinite(seen)) return true
  return seen >= Number(data?.metadata?.generation ?? 0)
}

// A denied capability omits the verb entirely; `blocked` covers wrong-state only.
export function rolloutActions(data: any, capabilities?: RolloutCapabilities): RolloutActionSpec[] {
  if (!capabilities) return []

  const status = data?.status || {}
  const phase = status.phase || 'Unknown'
  const steps = data?.spec?.strategy?.canary?.steps || []
  const isAborted = status.abort === true
  const isSettled = phase === 'Healthy' && !isAborted
  // Promote full is the one verb needed DURING the window where status is stale, so it
  // only claims "nothing to promote" when the controller is known to have caught up.
  const isFullyPromoted = isSettled && controllerCaughtUp(data)
  const stepsRemaining = steps.length > 0 && (status.currentStepIndex ?? 0) < steps.length
  // Mid-analysis a canary is Progressing with nothing paused, and promote advances
  // the step rather than clearing a pause — so gating on Paused alone hides it.
  const stepAnalysis = status.canary?.currentStepAnalysisRunStatus?.status
  const analysisInFlight =
    stepAnalysis === 'Running' || stepAnalysis === 'Pending' || stepAnalysis === 'Inconclusive'
  const promotable = phase === 'Paused' || analysisInFlight

  const actions: RolloutActionSpec[] = []
  if (capabilities.promote) {
    actions.push({
      action: 'promote',
      label: 'Promote',
      pendingLabel: 'Promoting…',
      icon: Play,
      hint: 'Clear the current pause, or advance past the running analysis, and continue',
      // An aborted Rollout keeps an Inconclusive analysis status, so promotable stays
      // true; Argo's own promote reports success there and advances nothing.
      blocked: isAborted
        ? 'Retry the rollout first'
        : !promotable
          ? 'Rollout is not paused and no analysis is running'
          : undefined,
    })
  }
  if (capabilities.promoteFull) {
    actions.push({
      action: 'promote-full',
      label: 'Promote full',
      pendingLabel: 'Promoting…',
      icon: FastForward,
      hint: 'Skip every remaining step, pause, and analysis — emergency hotfix path',
      blocked: isFullyPromoted ? 'Nothing left to promote' : isAborted ? 'Retry the rollout first' : undefined,
      confirm: {
        title: 'Promote fully?',
        message:
          'Every remaining step, pause, and analysis is skipped and the new version goes to 100% of production traffic immediately.',
      },
    })
  }
  if (capabilities.skipStep) {
    actions.push({
      action: 'skip-step',
      label: 'Skip step',
      pendingLabel: 'Skipping…',
      icon: SkipForward,
      hint: 'Advance one canary step without waiting for it',
      blocked: !stepsRemaining ? 'No canary step left to skip' : isAborted ? 'Retry the rollout first' : undefined,
    })
  }
  if (capabilities.retry && isAborted) {
    actions.push({
      action: 'retry',
      label: 'Retry',
      pendingLabel: 'Retrying…',
      icon: RotateCcw,
      hint: 'Clear the abort and resume the rollout from its current step',
    })
  }
  if (capabilities.abort && !isAborted) {
    actions.push({
      action: 'abort',
      label: 'Abort',
      pendingLabel: 'Aborting…',
      icon: Undo2,
      hint: 'Shift all traffic back to the stable revision and stop progressing',
      blocked: isSettled ? 'Rollout is fully promoted — roll back instead' : undefined,
      destructive: true,
      confirm: {
        title: 'Abort rollout?',
        message:
          'All production traffic shifts back to the stable revision and the rollout stops progressing.',
      },
    })
  }
  return actions
}

export interface RolloutProblem {
  color: 'red' | 'yellow'
  message: string
}

export function rolloutProblems(data: any): RolloutProblem[] {
  const status = data?.status || {}
  const phase = status.phase || 'Unknown'
  const conditions: any[] = status.conditions || []
  const problems: RolloutProblem[] = []

  if (status.abort === true) {
    problems.push({ color: 'red', message: status.message || 'Rollout was aborted' })
  }
  if (phase === 'Degraded') {
    problems.push({ color: 'red', message: status.message || 'Rollout is degraded' })
  }

  const progressDeadline = conditions.find(
    (c) => c.type === 'Progressing' && c.status === 'False' && c.reason === 'ProgressDeadlineExceeded'
  )
  if (progressDeadline) {
    problems.push({ color: 'red', message: progressDeadline.message || 'Progress deadline exceeded' })
  }

  const invalidSpec = conditions.find((c) => c.type === 'InvalidSpec' && c.status === 'True')
  if (invalidSpec) {
    problems.push({ color: 'red', message: invalidSpec.message || 'Invalid rollout spec' })
  }

  if (phase === 'Paused' && !status.abort) {
    const reasons = (status.pauseConditions || []).map((pc: any) => {
      const since = pc.startTime ? ` (since ${formatAge(pc.startTime)})` : ''
      return `${pc.reason || 'Unknown'}${since}`
    })
    problems.push({
      color: 'yellow',
      message: reasons.length > 0 ? `Rollout is paused: ${reasons.join('; ')}` : 'Rollout is paused',
    })
  }

  for (const run of rolloutAnalysisRuns(status)) {
    if (run.status === 'Inconclusive' || run.status === 'Failed' || run.status === 'Error') {
      problems.push({
        color: run.status === 'Inconclusive' ? 'yellow' : 'red',
        message: `${run.label} ${run.status.toLowerCase()} (${run.name})${run.message ? `: ${run.message}` : ''}`,
      })
    }
  }

  // An aborted Rollout is also Degraded, and both read status.message — so the
  // same sentence arrives twice. Keep the first, which carries the abort framing.
  const seen = new Set<string>()
  return problems.filter((p) => {
    if (seen.has(p.message)) return false
    seen.add(p.message)
    return true
  })
}

// Argo inverts polarity on two condition types: Paused=False and InvalidSpec=False
// are the good states, which the generic True-is-healthy rule scores as failing.
export function rolloutConditionTone(cond: { type?: string; status?: string }): ConditionTone | undefined {
  if (cond.status !== 'True' && cond.status !== 'False') return undefined
  const isTrue = cond.status === 'True'
  switch (cond.type) {
    case 'Paused':
      return isTrue ? 'warning' : 'ok'
    case 'InvalidSpec':
      return isTrue ? 'fail' : 'ok'
    default:
      return undefined
  }
}

/** Every CanaryStep variant Argo defines; raw JSON is unreadable in a step list. */
export function canaryStepLabel(step: any): string {
  if (!step || typeof step !== 'object') return 'Unknown step'

  if (step.setWeight !== undefined) return `Set weight: ${step.setWeight}%`

  if (step.pause !== undefined) {
    return step.pause?.duration ? `Pause: ${step.pause.duration}` : 'Pause: until promoted'
  }

  if (step.analysis) {
    const templates = (step.analysis.templates || [])
      .map((t: any) => t.templateName || t.clusterTemplateName)
      .filter(Boolean)
    return templates.length > 0 ? `Analysis: ${templates.join(', ')}` : 'Analysis'
  }

  if (step.experiment) {
    const templates = (step.experiment.templates || []).map((t: any) => t.name).filter(Boolean)
    const duration = step.experiment.duration ? ` for ${step.experiment.duration}` : ''
    return templates.length > 0
      ? `Experiment: ${templates.join(', ')}${duration}`
      : `Experiment${duration}`
  }

  if (step.setCanaryScale) {
    const { weight, replicas, matchTrafficWeight } = step.setCanaryScale
    if (matchTrafficWeight) return 'Set canary scale: match traffic weight'
    if (replicas !== undefined) return `Set canary scale: ${replicas} replicas`
    if (weight !== undefined) return `Set canary scale: ${weight}%`
    return 'Set canary scale'
  }

  if (step.setHeaderRoute) {
    const { name, match } = step.setHeaderRoute
    // An empty match list is how a header route is torn down again.
    if (!match || match.length === 0) return `Remove header route${name ? `: ${name}` : ''}`
    const headers = match.map((m: any) => m.headerName).filter(Boolean)
    return `Header route${name ? ` ${name}` : ''}${headers.length ? `: ${headers.join(', ')}` : ''}`
  }

  if (step.setMirrorRoute) {
    const { name, match, percentage } = step.setMirrorRoute
    if (!match || match.length === 0) return `Remove mirror route${name ? `: ${name}` : ''}`
    const pct = percentage !== undefined ? ` (${percentage}%)` : ''
    return `Mirror route${name ? ` ${name}` : ''}${pct}`
  }

  if (step.plugin) return `Plugin: ${step.plugin.name || 'unnamed'}`

  const key = Object.keys(step)[0]
  return key ? `Unrecognized step: ${key}` : 'Unknown step'
}

/** AnalysisTemplate/ClusterAnalysisTemplate references on either a canary
 *  step's analysis.templates[] array, or a single Experiment spec.analyses[]
 *  entry — the Experiment entry carries templateName/clusterScope flat on
 *  itself rather than nested under its own templates array, so it's wrapped
 *  into a one-element list to share the same mapping below. Both shapes use
 *  the SAME field name for the ref (templateName) regardless of scope —
 *  clusterScope is a separate boolean on the entry, not a distinct field
 *  name, so it's read directly rather than inferred from which field is set. */
export function canaryStepTemplateRefs(step: any): Array<{ name: string; clusterScoped: boolean }> {
  const templates = step?.analysis?.templates || step?.templates || (step?.templateName ? [step] : [])
  return templates
    .map((t: any) => (t.templateName ? { name: t.templateName, clusterScoped: !!t.clusterScope } : null))
    .filter(Boolean)
}

/** Ordered phase list for a blueGreen Rollout — there's no steps[] array to
 *  iterate, so this derives an equivalent sequence from strategy config
 *  crossed with live status. */
export function blueGreenPhases(data: any): Array<{ label: string; state: 'completed' | 'current' | 'pending' }> {
  const spec = data?.spec?.strategy?.blueGreen || {}
  const status = data?.status || {}
  const bg = status.blueGreen || {}
  // previewSelector is transient — Argo Rollouts clears it once there's no
  // active preview to track (a settled, fully-promoted blueGreen commonly
  // has none), so activeSelector === previewSelector alone under-detects
  // completion for anything but the narrow just-promoted window. currentPodHash
  // is a generic, always-populated status field (used by every strategy, not
  // just blueGreen) naming the newest ReplicaSet's hash — activeSelector
  // matching IT is the more durable "the latest revision is live" signal,
  // true both right after cutover and indefinitely after.
  const promoted = !!bg.activeSelector && (bg.activeSelector === status.currentPodHash || bg.activeSelector === bg.previewSelector)

  // scaleUpPreviewCheckPoint is only set in a narrow preview-replica-count
  // configuration and is absent on most real blueGreen Rollouts (confirmed
  // live) — pre-promotion analysis existing at all, or the Rollout having
  // reached ANY blueGreen pause, are both proof the preview already scaled
  // up (neither can happen before it does), so either is equally valid
  // "done" evidence even when the checkpoint field itself is never set.
  const previewScaledUp =
    !!bg.scaleUpPreviewCheckPoint || !!bg.prePromotionAnalysisRunStatus || status.phase === 'Paused' || promoted

  const steps: Array<{ label: string; done: boolean }> = [
    { label: 'Preview scaled up', done: previewScaledUp },
  ]

  if (spec.prePromotionAnalysis) {
    steps.push({ label: 'Pre-promotion analysis', done: bg.prePromotionAnalysisRunStatus?.status === 'Successful' || promoted })
  }

  steps.push({
    label:
      spec.autoPromotionEnabled === false
        ? 'Awaiting manual promotion'
        : spec.autoPromotionSeconds !== undefined
          ? `Auto-promote after ${spec.autoPromotionSeconds}s`
          : 'Awaiting promotion',
    done: promoted,
  })

  steps.push({ label: 'Active cutover', done: promoted })

  if (spec.postPromotionAnalysis) {
    steps.push({ label: 'Post-promotion analysis', done: bg.postPromotionAnalysisRunStatus?.status === 'Successful' })
  }

  let currentAssigned = false
  return steps.map((s) => {
    if (s.done) return { label: s.label, state: 'completed' as const }
    if (!currentAssigned) {
      currentAssigned = true
      return { label: s.label, state: 'current' as const }
    }
    return { label: s.label, state: 'pending' as const }
  })
}

/** Populated analysis slots, in the order the controller runs them. */
export function rolloutAnalysisRuns(
  status: any
): Array<{ label: string; name?: string; status?: string; message?: string }> {
  return [
    { label: 'Step analysis', ...(status?.canary?.currentStepAnalysisRunStatus || {}) },
    { label: 'Background analysis', ...(status?.canary?.currentBackgroundAnalysisRunStatus || {}) },
    { label: 'Pre-promotion analysis', ...(status?.blueGreen?.prePromotionAnalysisRunStatus || {}) },
    { label: 'Post-promotion analysis', ...(status?.blueGreen?.postPromotionAnalysisRunStatus || {}) },
  ].filter((run) => run.name)
}

function analysisStatusClass(status?: string): string {
  switch (status) {
    case 'Successful':
      return 'status-healthy'
    case 'Running':
    case 'Pending':
      return 'status-degraded'
    case 'Inconclusive':
      return 'status-alert'
    case 'Failed':
    case 'Error':
      return 'status-unhealthy'
    default:
      return 'status-unknown'
  }
}

export function RolloutRenderer({ data, onNavigate, capabilities, onAction, pendingAction, analysisRunHistory, revisions, pods }: RolloutRendererProps) {
  const [confirming, setConfirming] = useState<RolloutActionSpec | null>(null)
  const status = data.status || {}
  const spec = data.spec || {}
  const phase = status.phase || 'Unknown'

  const canaryStrategy = spec.strategy?.canary
  const blueGreenStrategy = spec.strategy?.blueGreen
  const isCanary = !!canaryStrategy
  const steps = canaryStrategy?.steps || []
  const currentStepIndex = status.currentStepIndex
  const trafficRouting = canaryStrategy?.trafficRouting

  // Detect traffic routing provider
  const trafficProvider = trafficRouting
    ? (() => {
        if (trafficRouting.istio) return { name: 'Istio', details: trafficRouting.istio }
        if (trafficRouting.nginx) return { name: 'Nginx', details: trafficRouting.nginx }
        if (trafficRouting.alb) return { name: 'ALB', details: trafficRouting.alb }
        if (trafficRouting.smi) return { name: 'SMI', details: trafficRouting.smi }
        if (trafficRouting.traefik) return { name: 'Traefik', details: trafficRouting.traefik }
        if (trafficRouting.ambassador) return { name: 'Ambassador', details: trafficRouting.ambassador }
        return null
      })()
    : null

  const analysisRuns = rolloutAnalysisRuns(status)
  const problems = rolloutProblems(data)
  const actions = onAction ? rolloutActions(data, capabilities) : []
  const blueGreenPhaseList = blueGreenStrategy ? blueGreenPhases(data) : []

  // Phase badge color
  const phaseColor = (() => {
    switch (phase) {
      case 'Healthy': return 'status-healthy'
      case 'Paused': return 'status-degraded'
      case 'Progressing': return 'status-degraded'
      case 'Degraded': return 'status-unhealthy'
      default: return 'status-unknown'
    }
  })()

  return (
    <>
      {problems.map((problem, i) => (
        <AlertBanner
          key={i}
          variant={problem.color === 'red' ? 'error' : 'warning'}
          title={problem.color === 'red' ? 'Issue Detected' : 'Warning'}
          message={problem.message}
        />
      ))}

      {actions.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          {actions.map((spec) => {
            const { action, label, pendingLabel, icon: Icon, hint, blocked, destructive, confirm } = spec
            const pending = pendingAction === action
            const disabled = Boolean(blocked) || pendingAction != null || capabilities?.terminating
            return (
              <Tooltip key={action} content={capabilities?.terminating ? 'Rollout is being deleted' : blocked || hint} delay={150}>
                <button
                  onClick={() => (confirm ? setConfirming(spec) : onAction?.(action))}
                  disabled={disabled}
                  className={clsx(
                    'flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-40',
                    destructive
                      ? 'border border-red-400/50 text-red-400 enabled:hover:bg-red-500/10'
                      : 'btn-brand-muted'
                  )}
                >
                  <Icon className={clsx('h-3.5 w-3.5', pending && 'animate-pulse')} />
                  {pending ? pendingLabel : label}
                </button>
              </Tooltip>
            )
          })}
        </div>
      )}

      {confirming?.confirm && (
        <ConfirmDialog
          open
          onClose={() => setConfirming(null)}
          onConfirm={() => {
            const action = confirming.action
            setConfirming(null)
            onAction?.(action)
          }}
          title={confirming.confirm.title}
          message={confirming.confirm.message}
          details={`${data.metadata?.namespace}/${data.metadata?.name}`}
          confirmLabel={confirming.label}
          variant={confirming.destructive ? 'danger' : 'warning'}
        />
      )}

      {/* Status section */}
      <Section title="Status" icon={Server}>
        <PropertyList>
          <Property
            label="Phase"
            value={
              <span className={clsx('badge', phaseColor)}>
                {phase}
              </span>
            }
          />
          <Property label="Replicas" value={`${status.availableReplicas || 0}/${spec.replicas || 0}`} />
          <Property label="Updated" value={status.updatedReplicas} />
          <Property label="Current Pod Hash" value={status.currentPodHash} />
          <Property label="Stable RS" value={status.stableRS} />
          <Property label="Message" value={status.message} />
        </PropertyList>
      </Section>

      {/* Strategy section */}
      <Section title="Strategy" icon={GitBranch}>
        <PropertyList>
          {isCanary ? (
            <>
              <Property label="Strategy" value="Canary" />
              {steps.length > 0 && currentStepIndex !== undefined && (
                <Property label="Current Step" value={getRolloutStep(data)} />
              )}
              {status.canary?.weights?.canary?.weight !== undefined && (
                <Property label="Canary Weight" value={`${status.canary.weights.canary.weight}%`} />
              )}
              {status.canary?.weights?.stable?.weight !== undefined && (
                <Property label="Stable Weight" value={`${status.canary.weights.stable.weight}%`} />
              )}
              <Property label="Canary Service" value={canaryStrategy.canaryService} />
              <Property label="Stable Service" value={canaryStrategy.stableService} />
              {status.canary?.currentExperiment && (
                <Property
                  label="Running Experiment"
                  value={
                    onNavigate ? (
                      <button
                        onClick={() =>
                          onNavigate({
                            kind: 'Experiment',
                            namespace: data?.metadata?.namespace ?? '',
                            name: status.canary.currentExperiment,
                          })
                        }
                        className="font-mono text-xs text-brand hover:underline"
                      >
                        {status.canary.currentExperiment}
                      </button>
                    ) : (
                      status.canary.currentExperiment
                    )
                  }
                />
              )}
            </>
          ) : blueGreenStrategy ? (
            <>
              <Property label="Strategy" value="Blue-Green" />
              <Property label="Active Service" value={blueGreenStrategy.activeService} />
              <Property label="Preview Service" value={blueGreenStrategy.previewService} />
              {/* The selectors are what each Service actually serves right now — a
                  rolled-back revision sits in preview until it is promoted. */}
              <Property label="Active Selector" value={status.blueGreen?.activeSelector} />
              <Property label="Preview Selector" value={status.blueGreen?.previewSelector} />
              <Property
                label="Auto Promote"
                value={
                  blueGreenStrategy.autoPromotionEnabled !== undefined
                    ? String(blueGreenStrategy.autoPromotionEnabled)
                    : undefined
                }
              />
              {blueGreenStrategy.autoPromotionSeconds !== undefined && (
                <Property
                  label="Auto Promote After"
                  value={`${blueGreenStrategy.autoPromotionSeconds}s`}
                />
              )}
              {status.blueGreen?.scaleUpPreviewCheckPoint && (
                <Property label="Preview" value="Scaled up, awaiting promotion" />
              )}
            </>
          ) : (
            <Property label="Strategy" value="Unknown" />
          )}
        </PropertyList>
      </Section>

      {/* Traffic Routing */}
      {trafficProvider && (
        <Section title="Traffic Routing" icon={Network}>
          <PropertyList>
            <Property
              label="Provider"
              value={
                <span className="badge-sm status-neutral">{trafficProvider.name}</span>
              }
            />
            {trafficProvider.name === 'Istio' && (
              <>
                {trafficProvider.details.virtualService?.name && (
                  <Property label="VirtualService" value={trafficProvider.details.virtualService.name} />
                )}
                {trafficProvider.details.destinationRule?.name && (
                  <Property label="DestinationRule" value={trafficProvider.details.destinationRule.name} />
                )}
              </>
            )}
            {trafficProvider.name === 'ALB' && (
              <>
                {trafficProvider.details.ingress && (
                  <Property label="Ingress" value={trafficProvider.details.ingress} />
                )}
                {trafficProvider.details.servicePort != null && (
                  <Property label="Service Port" value={trafficProvider.details.servicePort} />
                )}
              </>
            )}
            {trafficProvider.name === 'Nginx' && (
              <>
                {trafficProvider.details.stableIngress && (
                  <Property label="Stable Ingress" value={trafficProvider.details.stableIngress} />
                )}
                {trafficProvider.details.additionalIngressAnnotations && (
                  <Property
                    label="Annotations"
                    value={Object.keys(trafficProvider.details.additionalIngressAnnotations).length + ' annotations'}
                  />
                )}
              </>
            )}
            {trafficProvider.name === 'SMI' && (
              <>
                {trafficProvider.details.rootService && (
                  <Property label="Root Service" value={trafficProvider.details.rootService} />
                )}
                {trafficProvider.details.trafficSplitName && (
                  <Property label="TrafficSplit" value={trafficProvider.details.trafficSplitName} />
                )}
              </>
            )}
            {trafficProvider.name === 'Traefik' && (
              <>
                {trafficProvider.details.weightedTraefikServiceName && (
                  <Property label="Weighted Service" value={trafficProvider.details.weightedTraefikServiceName} />
                )}
              </>
            )}
            {trafficProvider.name === 'Ambassador' && (
              <>
                {trafficProvider.details.mappings && (
                  <Property label="Mappings" value={trafficProvider.details.mappings.join(', ')} />
                )}
              </>
            )}
          </PropertyList>
        </Section>
      )}

      {analysisRuns.length > 0 && (
        <Section title="Analysis" icon={Activity}>
          <PropertyList>
            {analysisRuns.map((run) => (
              <Property
                key={run.label}
                label={run.label}
                value={
                  <span className="flex items-center gap-2">
                    <span className={clsx('badge', analysisStatusClass(run.status))}>{run.status || 'Unknown'}</span>
                    {onNavigate ? (
                      <button
                        onClick={() =>
                          onNavigate({
                            kind: 'AnalysisRun',
                            namespace: data?.metadata?.namespace ?? '',
                            name: run.name!,
                          })
                        }
                        className="font-mono text-xs text-brand hover:underline"
                      >
                        {run.name}
                      </button>
                    ) : (
                      <span className="font-mono text-xs text-theme-text-secondary">{run.name}</span>
                    )}
                  </span>
                }
              />
            ))}
          </PropertyList>
        </Section>
      )}

      {analysisRunHistory && analysisRunHistory.length > 0 && (
        <Section title={`AnalysisRun History (${analysisRunHistory.length})`} icon={Activity}>
          <div className="space-y-1">
            {analysisRunHistory.map((run) => (
              <div key={run.name} className="flex items-center gap-2 rounded px-2 py-1.5 text-sm">
                <span className={clsx('badge', analysisStatusClass(run.phase))}>{run.phase || 'Unknown'}</span>
                {onNavigate ? (
                  <button
                    onClick={() =>
                      onNavigate({ kind: 'AnalysisRun', namespace: data?.metadata?.namespace ?? '', name: run.name })
                    }
                    className="font-mono text-xs text-brand hover:underline"
                  >
                    {run.name}
                  </button>
                ) : (
                  <span className="font-mono text-xs text-theme-text-secondary">{run.name}</span>
                )}
                {run.trigger && <span className="text-xs text-theme-text-tertiary">{run.trigger}</span>}
                {run.metricsTotal > 0 && (
                  <span
                    className={clsx(
                      'ml-auto text-xs',
                      run.metricsNotPassing > 0 ? 'text-amber-400' : 'text-theme-text-tertiary'
                    )}
                  >
                    {run.metricsPassing}/{run.metricsTotal} passing
                  </span>
                )}
                <span className={clsx('text-xs text-theme-text-tertiary', run.metricsTotal === 0 && 'ml-auto')}>
                  {formatAge(run.createdAt)}
                </span>
              </div>
            ))}
          </div>
        </Section>
      )}

      {isCanary && steps.length > 0 && (
        <Section title={`Canary Steps (${steps.length})`} defaultExpanded>
          <CanaryStepTimeline
            steps={steps}
            currentStepIndex={currentStepIndex}
            stepAnalysisStatus={analysisRuns.find((r) => r.label === 'Step analysis')}
            onNavigate={onNavigate}
            namespace={data?.metadata?.namespace}
          />
        </Section>
      )}

      {!isCanary && blueGreenStrategy && blueGreenPhaseList.length > 0 && (
        <Section title="Progression" defaultExpanded>
          <BlueGreenTimeline phases={blueGreenPhaseList} />
        </Section>
      )}

      {(revisions?.length ?? 0) > 0 && (
        <Section title={`ReplicaSets (${revisions!.length})`}>
          <ReplicaSetProgression
            revisions={revisions!}
            pods={pods}
            isRollout
            namespace={data?.metadata?.namespace ?? ''}
            onNavigate={onNavigate}
          />
        </Section>
      )}

      {/* Pod Template section (collapsed by default) */}
      <Section title="Pod Template" defaultExpanded={false}>
        <PodTemplateSection template={spec.template} />
      </Section>

      {/* Conditions section */}
      <ConditionsSection conditions={status.conditions} getConditionTone={rolloutConditionTone} />
    </>
  )
}
