import { Check, X, AlertTriangle, Minus, Clock } from 'lucide-react'
import { clsx } from 'clsx'
import { canaryStepLabel, canaryStepTemplateRefs } from '../RolloutRenderer'
import { analysisPhaseLevel, healthColors, type HealthLevel } from '../../resource-utils'

export interface StepAnalysisStatus {
  name?: string
  status?: string
  message?: string
}

interface CanaryStepTimelineProps {
  steps: any[]
  currentStepIndex?: number
  // The live "Step analysis" slot from rolloutAnalysisRuns(status) — shown
  // inline on the current step when it's an analysis step, instead of only
  // in the separate Analysis section above.
  stepAnalysisStatus?: StepAnalysisStatus
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  namespace?: string
}

type StepState = 'completed' | 'current' | 'pending'

// Only 'unhealthy' (Failed/Error) and 'alert' (Inconclusive) colour the dot.
// A Successful or still-running analysis deliberately gets no special colour:
// the step itself may still be paused or in progress regardless, so it keeps
// the same "current" tone as any other in-progress step rather than reading
// as done.
function stepDotTone(state: StepState, analysisLevel?: HealthLevel) {
  if (state === 'current' && (analysisLevel === 'unhealthy' || analysisLevel === 'alert')) {
    return healthColors[analysisLevel]
  }
  switch (state) {
    case 'completed':
      return healthColors.healthy
    case 'current':
      return healthColors.neutral
    default:
      return 'bg-theme-hover text-theme-text-tertiary'
  }
}

function StepDot({ state, analysisLevel }: { state: StepState; analysisLevel?: HealthLevel }) {
  const tone = stepDotTone(state, analysisLevel)
  let Icon = Minus
  if (state === 'current' && analysisLevel === 'unhealthy') Icon = X
  else if (state === 'current' && analysisLevel === 'alert') Icon = AlertTriangle
  else if (state === 'completed') Icon = Check
  else if (state === 'current') Icon = Clock
  return (
    <span className={clsx('z-10 mt-1 flex h-3 w-3 shrink-0 items-center justify-center rounded-full ring-2 ring-theme-surface', tone)}>
      <Icon className="h-2 w-2" strokeWidth={4} />
    </span>
  )
}

// Vertical connected stepper for a Rollout's canary steps — a linear
// sequence (no branching), so a simple top-to-bottom timeline reads more
// naturally than a graph. Same connecting-line visual as ConditionsSection.
export function CanaryStepTimeline({ steps, currentStepIndex, stepAnalysisStatus, onNavigate, namespace }: CanaryStepTimelineProps) {
  if (steps.length === 0) return null

  return (
    <div className="relative">
      <div className="absolute bottom-2 left-[9px] top-2 w-px bg-theme-border" />
      <div className="space-y-0.5">
        {steps.map((step, index) => {
          const state: StepState =
            currentStepIndex === undefined ? 'pending' : index < currentStepIndex ? 'completed' : index === currentStepIndex ? 'current' : 'pending'
          const isCurrent = state === 'current'
          const label = canaryStepLabel(step)
          const templateRefs = step.analysis ? canaryStepTemplateRefs(step) : []
          const showAnalysisStatus = isCurrent && step.analysis && stepAnalysisStatus?.status
          const analysisLevel: HealthLevel | undefined = showAnalysisStatus
            ? analysisPhaseLevel(stepAnalysisStatus!.status)
            : undefined

          return (
            <div key={index} className="relative flex items-start gap-2 py-1.5 pr-1 text-sm">
              <StepDot state={state} analysisLevel={analysisLevel} />
              <div className="min-w-0 flex-1 pl-1">
                <div className="flex items-center gap-2">
                  <span className="text-[11px] text-theme-text-tertiary">#{index}</span>
                  <span className={clsx(isCurrent ? 'font-medium text-theme-text-primary' : state === 'completed' ? 'text-theme-text-secondary' : 'text-theme-text-tertiary')}>
                    {label}
                  </span>
                  {showAnalysisStatus && analysisLevel && (
                    onNavigate && stepAnalysisStatus!.name ? (
                      <button
                        onClick={() =>
                          onNavigate({ kind: 'AnalysisRun', namespace: namespace ?? '', name: stepAnalysisStatus!.name! })
                        }
                        className={clsx('badge-sm hover:underline', healthColors[analysisLevel])}
                        title={stepAnalysisStatus!.name}
                      >
                        {stepAnalysisStatus!.status}
                      </button>
                    ) : (
                      <span className={clsx('badge-sm', healthColors[analysisLevel])}>
                        {stepAnalysisStatus!.status}
                      </span>
                    )
                  )}
                </div>
                {templateRefs.length > 0 && (
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5">
                    {templateRefs.map((ref) => (
                      <button
                        key={ref.name}
                        onClick={() =>
                          onNavigate?.({
                            kind: ref.clusterScoped ? 'ClusterAnalysisTemplate' : 'AnalysisTemplate',
                            namespace: ref.clusterScoped ? '' : namespace ?? '',
                            name: ref.name,
                          })
                        }
                        disabled={!onNavigate}
                        className={clsx(
                          'font-mono text-[11px]',
                          onNavigate ? 'text-brand hover:underline' : 'text-theme-text-tertiary'
                        )}
                      >
                        {ref.name}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

export interface BlueGreenPhase {
  label: string
  state: 'completed' | 'current' | 'pending'
  analysis?: { name?: string; status?: string }
}

// Same connected-dot visual as CanaryStepTimeline, for blueGreenPhases()'s
// derived phase list — blueGreen has no steps[] array to iterate, so there's
// nothing to reuse structurally, but the "linear sequence, top to bottom"
// visual language should match.
export function BlueGreenTimeline({
  phases,
  onNavigate,
  namespace,
}: {
  phases: BlueGreenPhase[]
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  namespace?: string
}) {
  if (phases.length === 0) return null
  return (
    <div className="relative">
      <div className="absolute bottom-2 left-[9px] top-2 w-px bg-theme-border" />
      <div className="space-y-0.5">
        {phases.map((phase, index) => {
          const level = phase.analysis?.status ? analysisPhaseLevel(phase.analysis.status) : undefined
          return (
            <div key={index} className="relative flex items-start gap-2 py-1.5 pr-1 text-sm">
              <StepDot state={phase.state} analysisLevel={level} />
              <span
                className={clsx(
                  'pl-1',
                  phase.state === 'current'
                    ? 'font-medium text-theme-text-primary'
                    : phase.state === 'completed'
                      ? 'text-theme-text-secondary'
                      : 'text-theme-text-tertiary'
                )}
              >
                {phase.label}
              </span>
              {level &&
                (onNavigate && phase.analysis?.name ? (
                  <button
                    onClick={() =>
                      onNavigate({ kind: 'AnalysisRun', namespace: namespace ?? '', name: phase.analysis!.name! })
                    }
                    className={clsx('badge-sm hover:underline', healthColors[level])}
                    title={phase.analysis.name}
                  >
                    {phase.analysis.status}
                  </button>
                ) : (
                  <span className={clsx('badge-sm', healthColors[level])}>{phase.analysis!.status}</span>
                ))}
            </div>
          )
        })}
      </div>
    </div>
  )
}
