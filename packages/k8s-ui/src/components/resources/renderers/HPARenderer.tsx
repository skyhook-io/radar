import { Cpu, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { formatAge } from '../resource-utils'

interface HPARendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

// Extract problems from HPA conditions
function getHPAProblems(data: any): string[] {
  const problems: string[] = []
  const conditions = data.status?.conditions || []
  const status = data.status || {}
  const spec = data.spec || {}

  // Check conditions for issues
  for (const cond of conditions) {
    // AbleToScale = False means target not found or can't scale
    if (cond.type === 'AbleToScale' && cond.status === 'False') {
      problems.push(`Cannot scale: ${cond.reason}${cond.message ? ' - ' + cond.message : ''}`)
    }

    // ScalingActive = False means metrics unavailable
    if (cond.type === 'ScalingActive' && cond.status === 'False') {
      if (cond.reason === 'FailedGetResourceMetric') {
        problems.push('Metrics unavailable — is metrics-server running?')
      } else {
        problems.push(`Scaling inactive: ${cond.reason}${cond.message ? ' - ' + cond.message : ''}`)
      }
    }

    // ScalingLimited = True means at min/max bound
    if (cond.type === 'ScalingLimited' && cond.status === 'True') {
      if (cond.reason === 'TooFewReplicas') {
        problems.push(`At minimum replicas (${spec.minReplicas || 1}) — cannot scale down further`)
      } else if (cond.reason === 'TooManyReplicas') {
        problems.push(`At maximum replicas (${spec.maxReplicas}) — cannot scale up further`)
      }
    }
  }

  // Check for desired != current (scaling in progress or stuck)
  if (status.currentReplicas !== undefined && status.desiredReplicas !== undefined) {
    if (status.currentReplicas !== status.desiredReplicas) {
      const direction = status.desiredReplicas > status.currentReplicas ? 'up' : 'down'
      problems.push(`Scaling ${direction}: ${status.currentReplicas} → ${status.desiredReplicas} replicas`)
    }
  }

  return problems
}

export function HPARenderer({ data, onNavigate }: HPARendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const metrics = status.currentMetrics || []

  // Check for problems
  const problems = getHPAProblems(data)
  const hasProblems = problems.length > 0

  // Determine if these are errors (red) or warnings (yellow)
  const hasErrors = problems.some(p =>
    p.includes('Cannot scale') || p.includes('unavailable') || p.includes('inactive')
  )

  return (
    <>
      {/* Problems/warnings alert */}
      {hasProblems && (
        <div className={clsx(
          'mb-4 p-3 rounded-lg border',
          hasErrors
            ? 'bg-red-500/10 border-red-500/30'
            : 'bg-yellow-500/10 border-yellow-500/30'
        )}>
          <div className="flex items-start gap-2">
            <AlertTriangle className={clsx(
              'w-4 h-4 mt-0.5 shrink-0',
              hasErrors ? 'text-red-400' : 'text-yellow-400'
            )} />
            <div className="flex-1 min-w-0">
              <div className={clsx(
                'text-sm font-medium mb-1',
                hasErrors ? 'text-red-400' : 'text-yellow-400'
              )}>
                {hasErrors ? 'Scaling Issues' : 'Scaling Status'}
              </div>
              <ul className={clsx(
                'text-xs space-y-1',
                hasErrors ? 'text-red-300' : 'text-yellow-300'
              )}>
                {problems.map((problem, i) => (
                  <li key={i} className="flex items-start gap-1.5">
                    <span className={clsx(
                      'mt-0.5',
                      hasErrors ? 'text-red-400/60' : 'text-yellow-400/60'
                    )}>•</span>
                    <span>{problem}</span>
                  </li>
                ))}
              </ul>
            </div>
          </div>
        </div>
      )}

      <Section title="Scaling" icon={Cpu}>
        <PropertyList>
          <Property label="Target" value={
            spec.scaleTargetRef?.name ? (
              <ResourceLink
                name={spec.scaleTargetRef.name}
                kind={kindToPlural(spec.scaleTargetRef.kind || 'Deployment')}
                namespace={data.metadata?.namespace || ''}
                label={`${spec.scaleTargetRef.kind}/${spec.scaleTargetRef.name}`}
                onNavigate={onNavigate}
              />
            ) : undefined
          } />
          <Property label="Current" value={status.currentReplicas} />
          <Property label="Desired" value={status.desiredReplicas} />
          <Property label="Min" value={spec.minReplicas || 1} />
          <Property label="Max" value={spec.maxReplicas} />
          {status.lastScaleTime && <Property label="Last Scale" value={formatAge(status.lastScaleTime)} />}
        </PropertyList>
      </Section>

      {metrics.length > 0 && (
        <Section title="Metrics" defaultExpanded>
          <div className="space-y-3">
            {metrics.map((metric: any, i: number) => {
              const current = metric.resource?.current?.averageUtilization || metric.resource?.current?.averageValue
              const target = spec.metrics?.[i]?.resource?.target?.averageUtilization || spec.metrics?.[i]?.resource?.target?.averageValue
              return (
                <div key={i} className="card-inner">
                  <div className="flex items-center justify-between text-sm">
                    <span className="text-theme-text-primary">{metric.resource?.name || metric.type}</span>
                    <span className="text-theme-text-secondary">{current}{typeof current === 'number' ? '%' : ''} / {target}{typeof target === 'number' ? '%' : ''}</span>
                  </div>
                  {typeof current === 'number' && typeof target === 'number' && (
                    <div className="mt-2 h-2 bg-theme-hover rounded overflow-hidden">
                      <div
                        className={clsx(
                          'h-full transition-all',
                          current > target ? 'bg-red-500' : current > target * 0.8 ? 'bg-yellow-500' : 'bg-green-500'
                        )}
                        style={{ width: `${Math.min(100, (current / target) * 100)}%` }}
                      />
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
