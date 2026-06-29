import type { ReactNode } from 'react'
import { Activity, Cpu } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, ResourceLink, type ConditionTone } from '../../ui/drawer-components'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import { kindToPlural } from '../../../utils/navigation'
import { formatAge } from '../resource-utils'
import { hpaStateLabel, hpaStateLevel } from '../resource-utils-hpa'
import type { HPADiagnosis, HPADiagnosisState } from '../../../types'

interface HPARendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  hpaDiagnosis?: HPADiagnosis
  /** Optional host-provided section rendered after Conditions — used to inject Prometheus-backed charts. */
  extraSections?: ReactNode
}

function hpaBadgeSeverity(state: HPADiagnosisState): BadgeSeverity {
  switch (hpaStateLevel(state)) {
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

function hpaConditionTone(condition: any): ConditionTone | undefined {
  if (condition?.type !== 'ScalingLimited') return undefined
  if (condition.status === 'False') return 'ok'
  if (condition.status !== 'True') return 'unknown'

  const reason = String(condition.reason ?? '').toLowerCase()
  const message = String(condition.message ?? '').toLowerCase()
  if (reason.includes('toomany') || message.includes('maximum')) return 'warning'
  if (reason.includes('toofew') || message.includes('minimum')) return 'ok'
  return 'warning'
}

function formatReasonID(id: string): string {
  return id.replace(/_/g, ' ')
}

function isReasonMessageRedundant(state: HPADiagnosisState, reason: NonNullable<HPADiagnosis['reasons']>[number]): boolean {
  return state === 'limited_max' && reason.id === 'limited_max'
}

export function HPARenderer({ data, onNavigate, hpaDiagnosis, extraSections }: HPARendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const metrics = status.currentMetrics || []

  return (
    <>
      {hpaDiagnosis && (
        <Section title="Diagnosis" icon={Activity}>
          <div className="card-inner space-y-3">
            <div className="flex flex-wrap items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="text-sm font-medium text-theme-text-primary">{hpaDiagnosis.summary}</div>
                <div className="mt-1 text-xs text-theme-text-secondary">
                  {hpaDiagnosis.bounds.current}/{hpaDiagnosis.bounds.desired} replicas, bounds {hpaDiagnosis.bounds.min}-{hpaDiagnosis.bounds.max}
                </div>
              </div>
              <Badge severity={hpaBadgeSeverity(hpaDiagnosis.state)}>{hpaStateLabel(hpaDiagnosis.state)}</Badge>
            </div>
            {hpaDiagnosis.reasons && hpaDiagnosis.reasons.length > 0 && (
              <div className="space-y-2">
                {hpaDiagnosis.reasons.map((reason) => (
                  <div key={`${reason.id}-${reason.message}`} className="rounded border border-theme-border bg-theme-surface p-2">
                    <div className="flex flex-wrap items-center gap-2 text-xs">
                      <span className="font-medium text-theme-text-primary">Evidence</span>
                      <span className="text-theme-text-tertiary">{formatReasonID(reason.id)}</span>
                      {reason.conditionType && <span className="text-theme-text-tertiary">{reason.conditionType}</span>}
                      {reason.conditionReason && <span className="text-theme-text-tertiary">{reason.conditionReason}</span>}
                      {reason.detail && <span className="text-theme-text-tertiary">{reason.detail}</span>}
                    </div>
                    {!isReasonMessageRedundant(hpaDiagnosis.state, reason) && (
                      <div className="mt-1 text-xs text-theme-text-secondary">{reason.message}</div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        </Section>
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

      {hpaDiagnosis?.metrics && hpaDiagnosis.metrics.length > 0 && (
        <Section title="Metrics" defaultExpanded>
          <div className="space-y-2">
            {hpaDiagnosis.metrics.map((metric) => (
              <div key={`${metric.type}-${metric.name}`} className="card-inner">
                <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
                  <div>
                    <div className="font-medium text-theme-text-primary">{metric.name}</div>
                    <div className="text-xs text-theme-text-tertiary">{metric.type}</div>
                  </div>
                  <div className="flex flex-wrap items-center gap-2 text-xs text-theme-text-secondary">
                    {metric.current && <span>Current {metric.current}</span>}
                    {metric.target && <span>Target {metric.target}</span>}
                    <Badge severity={metric.status === 'ok' ? 'success' : metric.status === 'missing' ? 'warning' : 'neutral'} size="sm">
                      {metric.status.replace(/_/g, ' ')}
                    </Badge>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {!hpaDiagnosis?.metrics?.length && metrics.length > 0 && (
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

      <ConditionsSection conditions={status.conditions} getConditionTone={hpaConditionTone} />

      {extraSections}
    </>
  )
}
