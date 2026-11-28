import { Activity, BarChart3, Server } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner } from '../../ui/drawer-components'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import { TimeValue } from '../../ui/ScheduleValue'
import { KargoLink } from './KargoShared'

interface AnalysisRunRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

const PHASE_SEVERITY: Record<string, BadgeSeverity> = {
  Successful: 'success',
  Running: 'info',
  Pending: 'neutral',
  Inconclusive: 'warning',
  Failed: 'error',
  Error: 'error',
}

function phaseSeverity(phase: string): BadgeSeverity {
  return PHASE_SEVERITY[phase] ?? 'neutral'
}

// Kargo names the Stage whose verification created the run in this label, or
// in the annotation of the same key when the name is too long for a label.
const KARGO_STAGE_KEY = 'kargo.akuity.io/stage'

/** The Kargo Stage whose verification this AnalysisRun is, if Kargo created it. */
export function getAnalysisRunKargoStage(data: any): string | undefined {
  return data?.metadata?.annotations?.[KARGO_STAGE_KEY] || data?.metadata?.labels?.[KARGO_STAGE_KEY] || undefined
}

function verdictTitle(phase: string, kargoStage: string | undefined, ownedByRollout: boolean): string {
  if (kargoStage) {
    if (phase === 'Error') return `Verification couldn't run for Kargo stage ${kargoStage}`
    if (phase === 'Inconclusive') return `Verification was inconclusive for Kargo stage ${kargoStage}`
    return `Verification failed for Kargo stage ${kargoStage}`
  }
  if (phase === 'Error') return 'Analysis could not run'
  if (phase === 'Inconclusive') {
    return ownedByRollout ? 'Analysis was inconclusive - the rollout is paused for a human decision' : 'Analysis was inconclusive'
  }
  return ownedByRollout ? 'Analysis failed - the rollout will be aborted' : 'Analysis failed'
}

// The Rollout's own pause reason is only "InconclusiveAnalysisRun" — the metric
// that actually decided it lives here, in metricResults[].
export function AnalysisRunRenderer({ data, onNavigate }: AnalysisRunRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const phase = status.phase || 'Unknown'
  const summary = status.runSummary || {}
  const metricResults: any[] = status.metricResults || []
  const labels = data.metadata?.labels || {}
  const ownerRollout = (data.metadata?.ownerReferences || []).find((ref: any) => ref.kind === 'Rollout')
  const kargoStage = getAnalysisRunKargoStage(data)
  const namespace = data.metadata?.namespace ?? ''
  const stageLink = kargoStage && onNavigate
    ? <KargoLink kind="Stage" name={kargoStage} namespace={namespace} label="Open stage" onNavigate={onNavigate} />
    : undefined

  const failing = metricResults.filter((m) => m.phase === 'Failed' || m.phase === 'Error')
  const inconclusive = metricResults.filter((m) => m.phase === 'Inconclusive')

  const verdictMessage = status.message || failing[0]?.message || inconclusive[0]?.message

  return (
    <>
      {(phase === 'Failed' || phase === 'Error') && (
        <AlertBanner
          variant="error"
          title={verdictTitle(phase, kargoStage, !!ownerRollout)}
          message={verdictMessage || 'One or more metrics breached their failure condition.'}
          action={stageLink}
        />
      )}

      {phase === 'Inconclusive' && (
        <AlertBanner
          variant="warning"
          title={verdictTitle(phase, kargoStage, !!ownerRollout)}
          message={verdictMessage || 'No metric failed outright, but none met its success condition either.'}
          action={stageLink}
        />
      )}

      <Section title="Status" icon={Activity}>
        <PropertyList>
          <Property label="Phase" value={<Badge severity={phaseSeverity(phase)}>{phase}</Badge>} />
          {labels['rollout-type'] && <Property label="Triggered as" value={labels['rollout-type']} />}
          {labels['step-index'] && <Property label="Canary step" value={labels['step-index']} />}
          <Property label="Started" value={status.startedAt ? <TimeValue timestamp={status.startedAt} /> : undefined} />
          <Property label="Completed" value={status.completedAt ? <TimeValue timestamp={status.completedAt} /> : undefined} />
          <Property label="Message" value={status.message} />
          {kargoStage && (
            <Property
              label="Kargo stage"
              value={<KargoLink kind="Stage" name={kargoStage} namespace={namespace} onNavigate={onNavigate} />}
            />
          )}
          {ownerRollout && (
            <Property
              label="Rollout"
              value={
                onNavigate ? (
                  <button
                    onClick={() =>
                      onNavigate({ kind: 'Rollout', namespace: data.metadata?.namespace, name: ownerRollout.name })
                    }
                    className="text-brand hover:underline"
                  >
                    {ownerRollout.name}
                  </button>
                ) : (
                  ownerRollout.name
                )
              }
            />
          )}
        </PropertyList>
      </Section>

      {Object.keys(summary).length > 0 && (
        <Section title="Measurement Tally" icon={BarChart3}>
          <PropertyList>
            <Property label="Measurements" value={summary.count} />
            <Property label="Successful" value={summary.successful} />
            <Property label="Failed" value={summary.failed} />
            <Property label="Inconclusive" value={summary.inconclusive} />
            <Property label="Errored" value={summary.error} />
          </PropertyList>
        </Section>
      )}

      {metricResults.length > 0 && (
        <Section title={`Metrics (${metricResults.length})`} defaultExpanded>
          <div className="space-y-2">
            {metricResults.map((metric: any) => {
              const definition = (spec.metrics || []).find((m: any) => m.name === metric.name)
              const measurements: any[] = metric.measurements || []
              const latest = measurements[measurements.length - 1]
              return (
                <div key={metric.name} className="card-inner space-y-1.5">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-sm text-theme-text-primary">{metric.name}</span>
                    <Badge severity={phaseSeverity(metric.phase)} size="sm">
                      {metric.phase || 'Unknown'}
                      {metric.dryRun ? ' (dry-run)' : ''}
                    </Badge>
                  </div>
                  {latest?.value && (
                    <div className="font-mono text-xs text-theme-text-secondary">latest: {latest.value}</div>
                  )}
                  {definition?.successCondition && (
                    <div className="font-mono text-xs text-theme-text-tertiary">
                      success if: {definition.successCondition}
                    </div>
                  )}
                  {definition?.failureCondition && (
                    <div className="font-mono text-xs text-theme-text-tertiary">
                      fail if: {definition.failureCondition}
                    </div>
                  )}
                  <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-theme-text-secondary">
                    {metric.count != null && <span>{metric.count} measured</span>}
                    {metric.successful ? <span>{metric.successful} ok</span> : null}
                    {metric.failed ? <span>{metric.failed} failed</span> : null}
                    {metric.inconclusive ? <span>{metric.inconclusive} inconclusive</span> : null}
                    {metric.error ? <span>{metric.error} errored</span> : null}
                    {metric.consecutiveError ? <span>{metric.consecutiveError} consecutive errors</span> : null}
                    {definition?.failureLimit != null && <span>failure limit {definition.failureLimit}</span>}
                  </div>
                  {metric.message && (
                    <div className="text-xs text-theme-text-secondary">{metric.message}</div>
                  )}
                  {latest?.message && latest.message !== metric.message && (
                    <div className="text-xs text-theme-text-secondary">{latest.message}</div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {(spec.args || []).length > 0 && (
        <Section title={`Arguments (${spec.args.length})`} icon={Server} defaultExpanded={false}>
          <PropertyList>
            {spec.args.map((arg: any) => (
              <Property
                key={arg.name}
                label={arg.name}
                value={arg.value ?? (arg.valueFrom ? 'from reference' : undefined)}
              />
            ))}
          </PropertyList>
        </Section>
      )}
    </>
  )
}
